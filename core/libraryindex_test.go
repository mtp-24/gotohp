package core

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"testing"
)

type countingFinder struct {
	calls int
	keys  map[string]string
}

func (f *countingFinder) FindRemoteMediaByHash(hash []byte) (string, error) {
	f.calls++
	return f.keys[string(hash)], nil
}

func dedupKey(hash [20]byte) string { return base64.RawURLEncoding.EncodeToString(hash[:]) }

func TestFindRemoteMediaChecksListingFirst(t *testing.T) {
	listed := sha1.Sum([]byte("listed"))
	online := sha1.Sum([]byte("online"))
	trashed := sha1.Sum([]byte("trashed"))
	locked := sha1.Sum([]byte("locked"))
	listing := newLibraryListing()
	listing.finish([]LibraryItem{
		{MediaKey: "key-listed", DedupKey: dedupKey(listed)},
		{MediaKey: "key-trashed", DedupKey: dedupKey(trashed), InTrash: true},
		{MediaKey: "key-locked", DedupKey: dedupKey(locked), IsLocked: true},
		{MediaKey: "key-no-fingerprint"},
	}, nil, 3, nil)
	ctx := withLibraryListing(context.Background(), listing)
	finder := &countingFinder{keys: map[string]string{string(online[:]): "key-online"}}

	for _, tc := range []struct {
		name  string
		hash  [20]byte
		want  string
		calls int
	}{
		{"in the listing", listed, "key-listed", 0},
		{"not in the listing", online, "key-online", 1},
		// Trashed and Locked Folder items are asked about, not trusted.
		{"trashed", trashed, "", 2},
		{"locked", locked, "", 3},
	} {
		got, err := findRemoteMedia(ctx, finder, tc.hash[:])
		if err != nil || got != tc.want || finder.calls != tc.calls {
			t.Fatalf("%s: key=%q err=%v calls=%d, want %q with %d calls", tc.name, got, err, finder.calls, tc.want, tc.calls)
		}
	}
	if check := listing.check(); check.Matched != 1 || check.Lookups != 3 || check.Items != 4 || check.Requests != 3 {
		t.Fatalf("check = %+v", check)
	}
}

func TestFindRemoteMediaAsksGoogleWithoutUsableListing(t *testing.T) {
	hash := sha1.Sum([]byte("a"))
	running := newLibraryListing()
	failed := newLibraryListing()
	failed.finish(nil, nil, 1, errors.New("offline"))
	for name, listing := range map[string]*libraryListing{"running": running, "failed": failed, "none": nil} {
		finder := &countingFinder{keys: map[string]string{string(hash[:]): "key-a"}}
		got, err := findRemoteMedia(withLibraryListing(context.Background(), listing), finder, hash[:])
		if err != nil || got != "key-a" || finder.calls != 1 {
			t.Fatalf("%s: key=%q err=%v calls=%d, want a lookup", name, got, err, finder.calls)
		}
	}
}

func TestQueueWaitsForLibraryListing(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	release := make(chan struct{})
	m.listLibrary = func(context.Context, ApiOptions) ([]LibraryItem, []LibraryAlbum, int, error) {
		<-release
		return nil, nil, 2, nil
	}
	var sawListing bool
	m.uploadItem = func(ctx context.Context, api *Api, item UploadWorkItem, opts UploadOptions, workerID int, reporter UploadReporter) (string, bool, bool, error) {
		listing := libraryListingFrom(ctx)
		sawListing = listing != nil && listing.ready()
		return up.upload(ctx, api, item, opts, workerID, reporter)
	}
	opts := testOpts(1)
	opts.AlbumName = "Trip"
	m.Upload(writeMedia(t, t.TempDir(), "a.jpg"), opts)
	up.assertNothingStarts(t)
	close(release)
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- nil
	rec.waitStop(t)
	if !sawListing {
		t.Fatal("the upload did not get the finished library listing")
	}
	if check := m.Status().Library; check.Phase != "ready" || check.Requests != 2 {
		t.Fatalf("library check = %+v", check)
	}
}

func TestLibraryListingAnswersWithTheOldestCopy(t *testing.T) {
	hash := sha1.Sum([]byte("copied"))
	listing := newLibraryListing()
	listing.finish([]LibraryItem{
		{MediaKey: "key-second", DedupKey: dedupKey(hash), AddedAt: 200},
		{MediaKey: "key-first", DedupKey: dedupKey(hash), AddedAt: 100},
		{MediaKey: "key-third", DedupKey: dedupKey(hash), AddedAt: 300},
	}, nil, 1, nil)
	if got := listing.find(hash[:]); got != "key-first" {
		t.Fatalf("find = %q, want the oldest copy, as Google's lookup answers", got)
	}
}
