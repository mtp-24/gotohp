package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

type captureTransport struct{ body []byte }

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.body, _ = io.ReadAll(req.Body)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
}

// field returns the first value of field num in a protobuf message.
func field(t *testing.T, msg []byte, num protowire.Number) (protowire.Type, []byte, uint64) {
	t.Helper()
	for len(msg) > 0 {
		n, typ, l := protowire.ConsumeTag(msg)
		if l < 0 {
			t.Fatal("bad tag")
		}
		msg = msg[l:]
		switch typ {
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(msg)
			msg = msg[m:]
			if n == num {
				return typ, v, 0
			}
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(msg)
			msg = msg[m:]
			if n == num {
				return typ, nil, v
			}
		default:
			t.Fatalf("unexpected wire type %v", typ)
		}
	}
	t.Fatalf("field %d missing", num)
	return 0, nil, 0
}

func TestFindLivePhotoMotionByHashRequestLayout(t *testing.T) {
	transport := &captureTransport{}
	api := &Api{
		client:            &http.Client{Transport: transport},
		authResponseCache: map[string]string{"Expiry": "9999999999", "Auth": "token"},
	}
	hash := []byte("0123456789abcdefghij")
	// Only the request matters here; the empty reply is no answer.
	_, _ = api.FindLivePhotoMotionByHash(hash)

	// {1: {1: {1: sha1, 5: 2}, 2: {}}}
	_, lookup, _ := field(t, transport.body, 1)
	_, query, _ := field(t, lookup, 1)
	if _, got, _ := field(t, query, 1); !bytes.Equal(got, hash) {
		t.Fatalf("hash = %x", got)
	}
	if typ, _, kind := field(t, query, 5); typ != protowire.VarintType || kind != 2 {
		t.Fatalf("field 5 = %d (type %v), want varint 2", kind, typ)
	}
	if _, empty, _ := field(t, lookup, 2); len(empty) != 0 {
		t.Fatalf("field 2 = %x, want empty", empty)
	}

	// The plain lookup is the same message without field 5.
	_, _ = api.FindRemoteMediaByHash(hash)
	_, plainLookup, _ := field(t, transport.body, 1)
	_, plainQuery, _ := field(t, plainLookup, 1)
	if want := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), hash); !bytes.Equal(plainQuery, want) {
		t.Fatalf("plain query = %x, want %x", plainQuery, want)
	}
}

// fakeLivePhotoAPI answers hash lookups from maps and records uploads.
type fakeLivePhotoAPI struct {
	remote     map[string]string
	motion     map[string]string
	uploads    int
	reconciled int
	lookups    int
}

func (f *fakeLivePhotoAPI) FindRemoteMediaByHash(hash []byte) (string, error) {
	f.lookups++
	return f.remote[string(hash)], nil
}

func (f *fakeLivePhotoAPI) FindLivePhotoMotionByHash(hash []byte) (string, error) {
	return f.motion[string(hash)], nil
}

func (f *fakeLivePhotoAPI) GetUploadToken(string, int64) (string, error) { return "upload", nil }

func (f *fakeLivePhotoAPI) UploadFileWithNewSessions(context.Context, string, string, func() (string, error), UploadProgressCallback) (ScottyFinalizeToken, error) {
	f.uploads++
	return ScottyFinalizeToken{}, nil
}

func (f *fakeLivePhotoAPI) CommitLivePhoto(LivePhotoCreateRequest) (string, error) {
	return "live", nil
}

func (f *fakeLivePhotoAPI) ReconcileLivePhoto(LivePhotoReconcileRequest) (string, error) {
	f.reconciled++
	return "reconciled", nil
}

func TestUpdateExistingPhotoToLiveSkipsWhenMotionAttached(t *testing.T) {
	dir := t.TempDir()
	pair := LivePhotoPair{PhotoPath: filepath.Join(dir, "IMG_1.HEIC"), VideoPath: filepath.Join(dir, "IMG_1.MOV")}
	for _, path := range []string{pair.PhotoPath, pair.VideoPath} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	photoHash, err := CalculateSHA1(context.Background(), pair.PhotoPath)
	if err != nil {
		t.Fatal(err)
	}
	videoHash, err := CalculateSHA1(context.Background(), pair.VideoPath)
	if err != nil {
		t.Fatal(err)
	}
	opts := LivePhotoUploadOptions{UpdateExistingPhotosToLive: true}

	t.Run("already live", func(t *testing.T) {
		api := &fakeLivePhotoAPI{
			remote: map[string]string{string(photoHash): "photo"},
			motion: map[string]string{string(videoHash): "live-item"},
		}
		key, skipped, err := uploadLivePhotoWithCallback(context.Background(), api, pair, opts, 0, NopReporter{})
		if err != nil || !skipped || key != "live-item" {
			t.Fatalf("key=%q skipped=%v err=%v, want live-item/true/nil", key, skipped, err)
		}
		if api.uploads != 0 || api.reconciled != 0 {
			t.Fatalf("uploads=%d reconciled=%d, want none", api.uploads, api.reconciled)
		}
	})

	t.Run("still exists, no update", func(t *testing.T) {
		api := &fakeLivePhotoAPI{remote: map[string]string{string(photoHash): "photo"}}
		key, skipped, err := uploadLivePhotoWithCallback(context.Background(), api, pair, LivePhotoUploadOptions{}, 0, NopReporter{})
		if err != nil || !skipped || key != "photo" {
			t.Fatalf("key=%q skipped=%v err=%v; want the existing still's key so albums still get it", key, skipped, err)
		}
	})

	t.Run("still in the library listing", func(t *testing.T) {
		listing := newLibraryListing()
		listing.finish([]LibraryItem{{MediaKey: "photo", DedupKey: base64.RawURLEncoding.EncodeToString(photoHash)}}, nil, 1, nil)
		ctx := withLibraryListing(context.Background(), listing)
		api := &fakeLivePhotoAPI{}
		key, skipped, err := uploadLivePhotoWithCallback(ctx, api, pair, LivePhotoUploadOptions{}, 0, NopReporter{})
		if err != nil || !skipped || key != "photo" || api.lookups != 0 {
			t.Fatalf("key=%q skipped=%v err=%v lookups=%d; want the listing's key and no lookups", key, skipped, err, api.lookups)
		}
	})

	t.Run("still only", func(t *testing.T) {
		api := &fakeLivePhotoAPI{remote: map[string]string{string(photoHash): "photo"}}
		_, _, _ = uploadLivePhotoWithCallback(context.Background(), api, pair, opts, 0, NopReporter{})
		if api.uploads != 1 || api.reconciled != 1 {
			t.Fatalf("uploads=%d reconciled=%d, want the video uploaded and reconciled", api.uploads, api.reconciled)
		}
	})
}

// scriptedTransport answers requests in order and records their URLs.
type scriptedTransport struct {
	statuses []int
	urls     []string
}

func (s *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	s.urls = append(s.urls, req.URL.String())
	status := s.statuses[0]
	s.statuses = s.statuses[1:]
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
}

func TestUploadRetryStartsNewSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jpg")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport := &scriptedTransport{statuses: []int{http.StatusServiceUnavailable, http.StatusBadRequest}}
	api := &Api{
		client:            &http.Client{Transport: transport},
		authResponseCache: map[string]string{"Expiry": "9999999999", "Auth": "token"},
	}
	_, err := api.UploadFileWithNewSessions(context.Background(), path, "first", func() (string, error) { return "second", nil }, nil)
	if err == nil {
		t.Fatal("expected the 400 to end the upload")
	}
	if len(transport.urls) != 2 ||
		!strings.HasSuffix(transport.urls[0], "upload_id=first") || !strings.HasSuffix(transport.urls[1], "upload_id=second") {
		t.Fatalf("urls = %v, want a new session on the retry", transport.urls)
	}
}

// replyTransport answers every request with body.
type replyTransport struct{ body []byte }

func (r replyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	_, _ = io.Copy(io.Discard, req.Body)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(r.body))}, nil
}

// notFoundReply is Google's "not found" lookup reply, which repeats the
// queried hash at 1.2.1.1.
func notFoundReply(hash []byte) []byte {
	inner := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), hash)
	query := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), inner)
	match := protowire.AppendBytes(protowire.AppendTag(nil, 2, protowire.BytesType), query)
	return protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), match)
}

func TestLookupWithoutAnswerIsAnError(t *testing.T) {
	hash := []byte("0123456789abcdefghij")
	for _, tc := range []struct {
		name  string
		reply []byte
		want  error
	}{
		{"not found", notFoundReply(hash), nil},
		{"empty reply", nil, errLookupNoAnswer},
		{"another file's reply", notFoundReply([]byte("jihgfedcba9876543210")), errLookupNoAnswer},
	} {
		api := &Api{
			client:            &http.Client{Transport: replyTransport{tc.reply}},
			authResponseCache: map[string]string{"Expiry": "9999999999", "Auth": "token"},
		}
		for kind, lookup := range map[string]func([]byte) (string, error){"plain": api.FindRemoteMediaByHash, "motion": api.FindLivePhotoMotionByHash} {
			key, err := lookup(hash)
			if key != "" || !errors.Is(err, tc.want) {
				t.Errorf("%s, %s lookup: key=%q err=%v, want %v", tc.name, kind, key, err, tc.want)
			}
		}
	}
}
