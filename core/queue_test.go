package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// queueRecorder captures reporter events for queue tests.
type queueRecorder struct {
	NopReporter
	mu       sync.Mutex
	results  []FileUploadResult
	warnings []PreflightWarning
	items    map[int64]QueueItem
	statuses []QueueStatus
	starts   int
	stops    chan struct{}
	albumErr []AlbumError
}

func newQueueRecorder() *queueRecorder {
	return &queueRecorder{items: make(map[int64]QueueItem), stops: make(chan struct{}, 10)}
}

func (r *queueRecorder) UploadStart(UploadBatchStart) { r.mu.Lock(); r.starts++; r.mu.Unlock() }
func (r *queueRecorder) UploadStop()                  { r.stops <- struct{}{} }
func (r *queueRecorder) FileResult(res FileUploadResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, res)
}

func (r *queueRecorder) Warning(w PreflightWarning) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = append(r.warnings, w)
}

func (r *queueRecorder) AlbumError(e AlbumError) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.albumErr = append(r.albumErr, e)
}

func (r *queueRecorder) QueueItems(items []QueueItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range items {
		if _, ok := r.items[item.ID]; !ok {
			r.items[item.ID] = item
		}
	}
}

func (r *queueRecorder) QueueItemUpdate(item QueueItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[item.ID] = item
}

func (r *queueRecorder) QueueStatus(s QueueStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statuses = append(r.statuses, s)
}

func (r *queueRecorder) itemByName(name string) QueueItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.items {
		if item.Name == name {
			return item
		}
	}
	return QueueItem{}
}

func (r *queueRecorder) waitStop(t *testing.T) {
	t.Helper()
	select {
	case <-r.stops:
	case <-time.After(5 * time.Second):
		t.Fatal("upload run did not stop")
	}
}

// fakeUploads stands in for the network. Each call blocks until the test
// releases that file name, then returns the scripted outcome.
type fakeUploads struct {
	mu       sync.Mutex
	started  chan string
	release  map[string]chan error
	attempts map[string]int
}

func newFakeUploads() *fakeUploads {
	return &fakeUploads{started: make(chan string, 100), release: make(map[string]chan error), attempts: make(map[string]int)}
}

func (f *fakeUploads) gate(name string) chan error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.release[name] == nil {
		f.release[name] = make(chan error, 10)
	}
	return f.release[name]
}

func (f *fakeUploads) upload(ctx context.Context, _ *Api, item UploadWorkItem, _ UploadOptions, _ int, _ UploadReporter) (string, bool, bool, error) {
	name := filepath.Base(item.Single.Path)
	f.mu.Lock()
	f.attempts[name]++
	f.mu.Unlock()
	f.started <- name
	select {
	case err := <-f.gate(name):
		if err != nil {
			return "", false, false, err
		}
		return "key-" + name, false, false, nil
	case <-ctx.Done():
		return "", false, false, ctx.Err()
	}
}

func (f *fakeUploads) waitStarted(t *testing.T, name string) {
	t.Helper()
	select {
	case got := <-f.started:
		if got != name {
			t.Fatalf("started %q, want %q", got, name)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not start", name)
	}
}

func (f *fakeUploads) assertNothingStarts(t *testing.T) {
	t.Helper()
	select {
	case got := <-f.started:
		t.Fatalf("%s started unexpectedly", got)
	case <-time.After(150 * time.Millisecond):
	}
}

type fakeAlbums struct {
	mu      sync.Mutex
	created map[string][]string
	added   map[string][]string
}

func (a *fakeAlbums) CreateAlbum(name string, keys []string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.created[name] = append(a.created[name], keys...)
	return "AF1Qip-" + name, nil
}

func (a *fakeAlbums) AddMediaToAlbum(album string, keys []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.added[album] = append(a.added[album], keys...)
	return nil
}

func newTestQueue(t *testing.T) (*UploadManager, *queueRecorder, *fakeUploads, *fakeAlbums) {
	t.Helper()
	recorder := newQueueRecorder()
	uploads := newFakeUploads()
	albums := &fakeAlbums{created: map[string][]string{}, added: map[string][]string{}}
	m := NewUploadManager(recorder, nil)
	m.newApi = func(ApiOptions) (*Api, error) { return &Api{}, nil }
	m.uploadItem = uploads.upload
	m.newAlbumAPI = func(ApiOptions) (albumAPI, error) { return albums, nil }
	m.probe = func(context.Context, string) error { return nil }
	m.probeInterval = 10 * time.Millisecond
	return m, recorder, uploads, albums
}

func writeMedia(t *testing.T, dir string, names ...string) []string {
	t.Helper()
	paths := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("\xff\xd8\xff\xe0"+name), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return paths
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testOpts(threads int) UploadOptions {
	return UploadOptions{Api: ApiOptions{Account: "test@example.com"}, Threads: threads}
}

func TestQueueAddsFilesWhileRunning(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	dir := t.TempDir()
	first := writeMedia(t, dir, "a.jpg")
	second := writeMedia(t, dir, "b.jpg", "a.jpg")

	m.Upload(first, testOpts(1))
	up.waitStarted(t, "a.jpg")
	m.Upload(second, testOpts(1)) // a.jpg is still uploading, so only b.jpg is added
	waitFor(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return slices.ContainsFunc(rec.warnings, func(w PreflightWarning) bool { return w.Code == "already-queued" })
	})
	up.gate("a.jpg") <- nil
	up.waitStarted(t, "b.jpg")
	up.gate("b.jpg") <- nil
	rec.waitStop(t)

	if got := rec.itemByName("b.jpg").State; got != QueueItemDone {
		t.Fatalf("b.jpg state = %q, want done", got)
	}
	if len(rec.results) != 2 {
		t.Fatalf("results = %d, want 2", len(rec.results))
	}
	if rec.starts == 0 {
		t.Fatal("no UploadStart")
	}
	select {
	case <-rec.stops:
		t.Fatal("run stopped twice")
	default:
	}
}

func TestQueuePauseHoldsQueuedItems(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	paths := writeMedia(t, t.TempDir(), "a.jpg", "b.jpg")

	m.Upload(paths, testOpts(1))
	up.waitStarted(t, "a.jpg")
	m.SetPaused(true)
	up.gate("a.jpg") <- nil
	up.assertNothingStarts(t)
	if !m.Status().Paused {
		t.Fatal("queue not paused")
	}
	m.SetPaused(false)
	up.waitStarted(t, "b.jpg")
	up.gate("b.jpg") <- nil
	rec.waitStop(t)
}

func TestQueueCancelItemAndSkipQueued(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	paths := writeMedia(t, t.TempDir(), "a.jpg", "b.jpg", "c.jpg")

	m.Upload(paths, testOpts(1))
	up.waitStarted(t, "a.jpg")
	m.CancelItem(rec.itemByName("b.jpg").ID) // queued: never starts
	m.CancelItem(rec.itemByName("a.jpg").ID) // active: its upload is interrupted
	up.waitStarted(t, "c.jpg")
	up.gate("c.jpg") <- nil
	rec.waitStop(t)

	for name, want := range map[string]QueueItemState{"a.jpg": QueueItemCancelled, "b.jpg": QueueItemCancelled, "c.jpg": QueueItemDone} {
		if got := rec.itemByName(name).State; got != want {
			t.Errorf("%s state = %q, want %q", name, got, want)
		}
	}
	for _, result := range rec.results {
		if result.IsError {
			t.Errorf("cancelled item reported as a failure: %+v", result)
		}
	}
	if up.attempts["b.jpg"] != 0 {
		t.Error("removed item was uploaded")
	}
}

func TestQueueCancelAllStillAddsUploadedItemsToAlbum(t *testing.T) {
	m, rec, up, albums := newTestQueue(t)
	paths := writeMedia(t, t.TempDir(), "a.jpg", "b.jpg", "c.jpg")
	opts := testOpts(1)
	opts.AlbumName = "Trip"

	m.Upload(paths, opts)
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- nil
	up.waitStarted(t, "b.jpg")
	m.Cancel()
	rec.waitStop(t)

	if got := albums.created["Trip"]; !slices.Equal(got, []string{"key-a.jpg"}) {
		t.Fatalf("album items = %v, want [key-a.jpg]", got)
	}
	if got := rec.itemByName("c.jpg").State; got != QueueItemCancelled {
		t.Fatalf("c.jpg state = %q, want cancelled", got)
	}
}

func TestQueueReusesSessionAlbumAcrossDrops(t *testing.T) {
	m, rec, up, albums := newTestQueue(t)
	dir := t.TempDir()
	opts := testOpts(1)
	opts.AlbumName = "Trip"

	for _, name := range []string{"a.jpg", "b.jpg"} {
		m.Upload(writeMedia(t, dir, name), opts)
		up.waitStarted(t, name)
		up.gate(name) <- nil
		rec.waitStop(t)
	}

	if len(albums.created) != 1 || !slices.Equal(albums.added["AF1Qip-Trip"], []string{"key-b.jpg"}) {
		t.Fatalf("created = %v, added = %v; want one album reused", albums.created, albums.added)
	}
}

func TestQueueRequeuesAfterConnectionLoss(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	paths := writeMedia(t, t.TempDir(), "a.jpg")

	m.Upload(paths, testOpts(1))
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- fmt.Errorf("request failed: %w", &net.OpError{Op: "write", Err: errors.New("broken pipe")})
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- nil
	rec.waitStop(t)

	if got := rec.itemByName("a.jpg").State; got != QueueItemDone {
		t.Fatalf("state = %q, want done", got)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !slices.ContainsFunc(rec.statuses, func(s QueueStatus) bool { return s.WaitingForNetwork }) {
		t.Fatal("queue never reported waiting for network")
	}
	for _, result := range rec.results {
		if result.IsError {
			t.Fatalf("connection loss reported as failure: %+v", result)
		}
	}
}

func TestQueueRetryFailedItem(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	paths := writeMedia(t, t.TempDir(), "a.jpg")

	m.Upload(paths, testOpts(1))
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- errors.New("request failed with status 400")
	rec.waitStop(t)
	failed := rec.itemByName("a.jpg")
	if failed.State != QueueItemFailed {
		t.Fatalf("state = %q, want failed", failed.State)
	}

	m.RetryItem(failed.ID)
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- nil
	rec.waitStop(t)
	if got := rec.itemByName("a.jpg").State; got != QueueItemDone {
		t.Fatalf("state after retry = %q, want done", got)
	}
}

func TestGroupUploadsByFolderNamesCollidingFolders(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "photos")
	groups := groupUploadsByFolder([]albumUpload{
		{path: filepath.Join(root, "2023", "Jan", "a.jpg"), mediaKey: "a"},
		{path: filepath.Join(root, "2024", "Jan", "b.jpg"), mediaKey: "b"},
		{path: filepath.Join(root, "Trip", "c.jpg"), mediaKey: "c"},
	})
	var names []string
	for _, group := range groups {
		names = append(names, group.name)
	}
	want := []string{"Jan (2023)", "Jan (2024)", "Trip"}
	if !slices.Equal(names, want) {
		t.Fatalf("album names = %v, want %v", names, want)
	}
}

func TestIsNetworkError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("upload: %w", &net.OpError{Op: "dial", Err: errors.New("no route")}), true},
		{fmt.Errorf("x: %w", errUploadStalled), true},
		{errors.New("request failed with status 400"), false},
		{context.Canceled, false},
	} {
		if got := isNetworkError(tc.err); got != tc.want {
			t.Errorf("isNetworkError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestWatchUploadProgressDetectsStall(t *testing.T) {
	reader := NewProgressReader(nil, 100, nil)
	stalled := make(chan struct{})
	stop := watchUploadProgress(reader, 100, 30*time.Millisecond, func() { close(stalled) })
	defer stop()
	select {
	case <-stalled:
	case <-time.After(2 * time.Second):
		t.Fatal("stall not detected")
	}
}

func TestScanWarnsAboutUnreadableFoldersAndSkipsJunk(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read every folder")
	}
	dir := t.TempDir()
	writeMedia(t, dir, "a.jpg", ".DS_Store", "locked/b.jpg")
	locked := filepath.Join(dir, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	files, warnings, err := filterGooglePhotosFilesWithWarnings([]string{dir}, UploadOptions{Recursive: true, DisableUnsupportedFilesFilter: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(files, []string{filepath.Join(dir, "a.jpg")}) {
		t.Fatalf("files = %v", files)
	}
	if len(warnings) != 1 || warnings[0].Code != "folder-unreadable" {
		t.Fatalf("warnings = %+v", warnings)
	}
}
