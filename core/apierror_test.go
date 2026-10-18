package core

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

func rpcStatusBody(code int, message string) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(code))
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, message)
	return b
}

func TestNewAPIErrorDecodesRPCStatus(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body        []byte
		wantMessage string
		storageFull bool
		credential  bool
		rateLimited bool
	}{
		{"storage full", 400, rpcStatusBody(8, "Quota exceeded"), "Quota exceeded", true, false, false},
		{"rate limit shares the code", 429, rpcStatusBody(8, "Too many requests"), "Too many requests", false, false, true},
		{"unauthenticated", 400, rpcStatusBody(16, "Bad token"), "Bad token", false, true, false},
		{"plain text", 500, []byte("backend error\n"), "backend error", false, false, false},
		{"binary", 400, []byte{0xff, 0x00, 0x01}, "3-byte response", false, false, false},
		{"forbidden without body", 403, nil, "", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(tc.body)))}
			apiErr := newAPIError(resp)
			if apiErr.Message != tc.wantMessage {
				t.Errorf("message = %q, want %q", apiErr.Message, tc.wantMessage)
			}
			if apiErr.StorageFull() != tc.storageFull || apiErr.CredentialRejected() != tc.credential || apiErr.RateLimited() != tc.rateLimited {
				t.Errorf("storageFull=%v credential=%v rateLimited=%v", apiErr.StorageFull(), apiErr.CredentialRejected(), apiErr.RateLimited())
			}
			if !strings.Contains(apiErr.Error(), fmt.Sprintf("status %d", tc.status)) {
				t.Errorf("Error() = %q lost the status code", apiErr.Error())
			}
		})
	}
}

func TestSendReauthenticatesOnceAfter401(t *testing.T) {
	var auths, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth":
			n := auths.Add(1)
			_, _ = fmt.Fprintf(w, "Auth=token%d\nExpiry=%d\n", n, time.Now().Add(time.Hour).Unix())
		default:
			calls.Add(1)
			if r.Header.Get("Authorization") != "Bearer token2" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	api := &Api{
		client:            server.Client(),
		authData:          "Email=a%40example.com",
		authEndpoint:      server.URL + "/auth",
		authResponseCache: map[string]string{"Expiry": "0", "Auth": ""},
	}
	resp, err := api.send(func() (*http.Request, error) { return newPostRequest(server.URL+"/rpc", nil, nil) })
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || auths.Load() != 2 || calls.Load() != 2 {
		t.Fatalf("status=%d auths=%d calls=%d, want 200/2/2", resp.StatusCode, auths.Load(), calls.Load())
	}
}

func TestAuthErrorLineIsCredentialRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "Error=BadAuthentication\n")
	}))
	defer server.Close()
	api := &Api{
		client: server.Client(), authData: "Email=a%40example.com", authEndpoint: server.URL,
		authResponseCache: map[string]string{"Expiry": "0", "Auth": ""},
	}

	_, err := api.BearerToken()
	apiErr, ok := asAPIError(err)
	if !ok || !apiErr.CredentialRejected() {
		t.Fatalf("BearerToken error = %v, want a credential rejection", err)
	}
}

func TestShouldRetryRequestTimeout(t *testing.T) {
	if !ShouldRetry(&http.Response{StatusCode: http.StatusRequestTimeout}, nil) {
		t.Fatal("408 should be retried")
	}
	if ShouldRetry(&http.Response{StatusCode: http.StatusBadRequest}, nil) {
		t.Fatal("400 should not be retried")
	}
}

func TestQueueStopsWhenAccountIsFull(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	paths := writeMedia(t, t.TempDir(), "a.jpg", "b.jpg")

	m.Upload(paths, testOpts(1))
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- fmt.Errorf("error uploading file: %w", &APIError{StatusCode: 400, Code: rpcCodeResourceExhausted, Message: "quota"})
	up.assertNothingStarts(t)
	status := m.Status()
	if !status.Paused || !strings.Contains(status.StoppedReason, "out of storage") {
		t.Fatalf("status = %+v, want a storage stop", status)
	}
	if got := rec.itemByName("a.jpg").State; got != QueueItemQueued {
		t.Fatalf("a.jpg state = %q, want queued for retry", got)
	}

	m.SetPaused(false)
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- nil
	up.waitStarted(t, "b.jpg")
	up.gate("b.jpg") <- nil
	rec.waitStop(t)
	for _, result := range rec.results {
		if result.IsError {
			t.Fatalf("stopped item reported as failure: %+v", result)
		}
	}
}

func TestQueuePausesAfterRateLimit(t *testing.T) {
	start, maxPause := rateLimitPauseStart, rateLimitPauseMax
	rateLimitPauseStart, rateLimitPauseMax = 50*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { rateLimitPauseStart, rateLimitPauseMax = start, maxPause })

	m, rec, up, _ := newTestQueue(t)
	paths := writeMedia(t, t.TempDir(), "a.jpg")

	m.Upload(paths, testOpts(1))
	up.waitStarted(t, "a.jpg")
	began := time.Now()
	up.gate("a.jpg") <- &APIError{StatusCode: http.StatusTooManyRequests}
	up.waitStarted(t, "a.jpg")
	if waited := time.Since(began); waited < 40*time.Millisecond {
		t.Fatalf("retried after %s, want a pause", waited)
	}
	up.gate("a.jpg") <- nil
	rec.waitStop(t)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	sawPause := false
	for _, s := range rec.statuses {
		sawPause = sawPause || s.RateLimitedUntil > 0
	}
	if !sawPause {
		t.Fatal("queue never reported a rate-limit pause")
	}
	if got := rec.items[1].State; got != QueueItemDone {
		t.Fatalf("state = %q, want done", got)
	}
}

func TestAPIErrorUnwrapsThroughWrapping(t *testing.T) {
	err := fmt.Errorf("commit failed: %w", fmt.Errorf("inner: %w", &APIError{StatusCode: 403}))
	if apiErr, ok := asAPIError(err); !ok || !apiErr.CredentialRejected() {
		t.Fatal("wrapped APIError not found")
	}
	if _, ok := asAPIError(errors.New("plain")); ok {
		t.Fatal("plain error matched")
	}
}
