package core

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	if _, err := api.FindLivePhotoMotionByHash(hash); err != nil {
		t.Fatal(err)
	}

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
	if _, err := api.FindRemoteMediaByHash(hash); err != nil {
		t.Fatal(err)
	}
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
}

func (f *fakeLivePhotoAPI) FindRemoteMediaByHash(hash []byte) (string, error) {
	return f.remote[string(hash)], nil
}

func (f *fakeLivePhotoAPI) FindLivePhotoMotionByHash(hash []byte) (string, error) {
	return f.motion[string(hash)], nil
}

func (f *fakeLivePhotoAPI) GetUploadToken(string, int64) (string, error) { return "upload", nil }

func (f *fakeLivePhotoAPI) UploadFileWithProgress(context.Context, string, string, UploadProgressCallback) (ScottyFinalizeToken, error) {
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

	t.Run("still only", func(t *testing.T) {
		api := &fakeLivePhotoAPI{remote: map[string]string{string(photoHash): "photo"}}
		_, _, _ = uploadLivePhotoWithCallback(context.Background(), api, pair, opts, 0, NopReporter{})
		if api.uploads != 1 || api.reconciled != 1 {
			t.Fatalf("uploads=%d reconciled=%d, want the video uploaded and reconciled", api.uploads, api.reconciled)
		}
	})
}
