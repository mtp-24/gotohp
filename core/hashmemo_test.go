package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHashMemoUploadsIdenticalFilesOnce(t *testing.T) {
	memo := newHashMemo()
	ctx := context.Background()

	key, claimed, err := memo.begin(ctx, "same")
	if err != nil || !claimed || key != "" {
		t.Fatalf("first begin = %q %v %v, want a claim", key, claimed, err)
	}
	got := make(chan string)
	go func() {
		key, claimed, _ := memo.begin(ctx, "same")
		if claimed {
			key = "claimed"
		}
		got <- key
	}()
	select {
	case k := <-got:
		t.Fatalf("second begin returned %q while the first was uploading", k)
	case <-time.After(50 * time.Millisecond):
	}
	memo.end("same", "AF1Qip-one")
	if k := <-got; k != "AF1Qip-one" {
		t.Fatalf("second begin = %q, want the first upload's key", k)
	}
}

func TestHashMemoHandsOverAfterAFailure(t *testing.T) {
	memo := newHashMemo()
	ctx := context.Background()
	_, _, _ = memo.begin(ctx, "h")
	done := make(chan bool)
	go func() {
		_, claimed, _ := memo.begin(ctx, "h")
		done <- claimed
	}()
	time.Sleep(20 * time.Millisecond)
	memo.end("h", "") // the first upload failed
	if !<-done {
		t.Fatal("after a failed upload the waiting file should try itself")
	}
}

// statusTransport answers every request with one status and records URLs.
type statusTransport struct {
	status int
	urls   []string
}

func (s *statusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.urls = append(s.urls, req.URL.String())
	return &http.Response{StatusCode: s.status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
}

func TestRateLimitedLookupNeverUploads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jpg")
	if err := os.WriteFile(path, []byte("photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport := &statusTransport{status: http.StatusTooManyRequests}
	api := testLibraryApi(transport)

	_, _, err := uploadSingleFile(context.Background(), api, path, UploadOptions{}, 0, NopReporter{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.RateLimited() {
		t.Fatalf("err = %v, want the rate limit passed on so the queue retries later", err)
	}
	if len(transport.urls) != 1 || transport.urls[0] != hashCheckEndpoint {
		t.Fatalf("requests = %v, want only the library lookup and no upload", transport.urls)
	}
}
