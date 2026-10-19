package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	// updateEvents counts QueueItemsUpdated calls.
	updateEvents int
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

func (r *queueRecorder) QueueItemsUpdated(items []QueueItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateEvents++
	for _, item := range items {
		r.items[item.ID] = item
	}
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
	// inLibrary names files the fake library already holds.
	inLibrary map[string]bool
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
		f.mu.Lock()
		existed := f.inLibrary[name]
		f.mu.Unlock()
		return "key-" + name, false, existed, nil
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
	// existing are the albums the account already has.
	existing []LibraryAlbum
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
	m.listAlbums = func(context.Context, ApiOptions) ([]LibraryAlbum, error) { return albums.existing, nil }
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

	files, warnings, err := filterGooglePhotosFilesWithWarnings([]string{dir}, UploadOptions{Recursive: true, DisableUnsupportedFilesFilter: true}, nil, nil)
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

func TestQueueUploadsSkippedLivePhotoFilesOnRequest(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	paths := writeMedia(t, t.TempDir(), "IMG_1.JPG", "IMG_2.JPG")
	warning := PreflightWarning{Paths: paths, Code: "incomplete-live-photo-skipped", Message: "skipped"}

	// Drive one preflight by hand, as if classification had skipped both files.
	m.mu.Lock()
	m.startRunLocked()
	batch := &uploadBatch{id: 1, opts: testOpts(1).normalized(), ctx: m.runCtx, scanning: true}
	m.scans[batch] = struct{}{}
	m.scanning++
	m.tasks++
	m.mu.Unlock()
	m.addBatchItems(batch, nil, []PreflightWarning{warning}, nil)
	m.scanFinished(batch)
	m.taskDone()
	rec.waitStop(t)

	for _, name := range []string{"IMG_1.JPG", "IMG_2.JPG"} {
		if item := rec.itemByName(name); item.State != QueueItemSkipped || item.SkipCode != warning.Code {
			t.Fatalf("%s = %+v, want a skipped item with its skip code", name, item)
		}
	}

	m.UploadSkipped()
	for range 2 {
		name := <-up.started
		up.gate(name) <- nil
	}
	rec.waitStop(t)
	for _, name := range []string{"IMG_1.JPG", "IMG_2.JPG"} {
		if got := rec.itemByName(name).State; got != QueueItemDone {
			t.Fatalf("%s state = %q, want done", name, got)
		}
	}
}

func TestQueueReportsScanProgress(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	paths := writeMedia(t, t.TempDir(), "a.jpg")

	m.Upload(paths, testOpts(1))
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- nil
	rec.waitStop(t)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	sawFinding, sawFound := false, false
	for _, status := range rec.statuses {
		sawFinding = sawFinding || status.Scan.Phase == "finding"
		sawFound = sawFound || status.Scan.Found > 0
	}
	if !sawFinding || !sawFound {
		t.Fatalf("scan progress not reported: %+v", rec.statuses)
	}
	if last := rec.statuses[len(rec.statuses)-1]; last.Scan.Phase != "" {
		t.Fatalf("scan still reported after it finished: %+v", last)
	}
}

type fakeMetadataReader struct {
	photos map[string]string
	videos map[string]LivePhotoMetadata
	areas  map[string]int64
	hevc   map[string]bool
}

func (f fakeMetadataReader) MediaInfo(path string) MediaInfo {
	name := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(name))
	return MediaInfo{PixelArea: f.areas[name], HEVC: f.hevc[name] || ext == ".heic"}
}

func (f fakeMetadataReader) PhotoContentIdentifier(path string) (string, error) {
	if id, ok := f.photos[filepath.Base(path)]; ok {
		return id, nil
	}
	return "", ErrContentIdentifierMissing
}

func (f fakeMetadataReader) VideoLivePhotoMetadata(path string) (LivePhotoMetadata, error) {
	if metadata, ok := f.videos[filepath.Base(path)]; ok {
		return metadata, nil
	}
	return LivePhotoMetadata{}, ErrContentIdentifierMissing
}

func TestClassifyPairsHEICWhenAnExportedCopySharesTheIdentifier(t *testing.T) {
	reader := fakeMetadataReader{
		photos: map[string]string{"IMG_0122.HEIC": "ID-1", "IMG_0122.jpeg": "ID-1"},
		videos: map[string]LivePhotoMetadata{"IMG_0122.MOV": {ContentIdentifier: "ID-1", HasStillImageTime: true}},
		hevc:   map[string]bool{"IMG_0122.MOV": true},
	}
	work, warnings := ClassifyUploadWork([]string{"/p/IMG_0122.HEIC", "/p/IMG_0122.jpeg", "/p/IMG_0122.MOV"},
		LivePhotoClassificationOptions{Enabled: true, SkipIncomplete: true}, reader)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v", warnings)
	}
	if len(work) != 2 || work[0].Kind != UploadWorkLivePhoto || work[0].LivePhoto.PhotoPath != "/p/IMG_0122.HEIC" ||
		work[1].Kind != UploadWorkSingle || work[1].Single.Path != "/p/IMG_0122.jpeg" {
		t.Fatalf("work = %+v, want the HEIC paired and the JPEG on its own", work)
	}
}

func TestClassifyPairsLivePhotoCopiesPerFolder(t *testing.T) {
	reader := fakeMetadataReader{
		photos: map[string]string{"IMG_0140.HEIC": "ID-2"},
		videos: map[string]LivePhotoMetadata{"IMG_0140.MOV": {ContentIdentifier: "ID-2", HasStillImageTime: true}},
		hevc:   map[string]bool{"IMG_0140.MOV": true, "IMG_0140(1).MOV": true},
	}
	// The same Live Photo copied into two dropped folders.
	paths := []string{"/a/IMG_0140.HEIC", "/a/IMG_0140.MOV", "/b/IMG_0140.HEIC", "/b/IMG_0140.MOV"}
	work, warnings := ClassifyUploadWork(paths, LivePhotoClassificationOptions{Enabled: true, SkipIncomplete: true}, reader)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v", warnings)
	}
	if len(work) != 2 || work[0].Kind != UploadWorkLivePhoto || work[1].Kind != UploadWorkLivePhoto ||
		filepath.Dir(work[0].LivePhoto.PhotoPath) != filepath.Dir(work[0].LivePhoto.VideoPath) ||
		filepath.Dir(work[1].LivePhoto.PhotoPath) != filepath.Dir(work[1].LivePhoto.VideoPath) {
		t.Fatalf("work = %+v, want one pair per folder", work)
	}

	// Two videos for one still in one folder are ambiguous: all are skipped,
	// not guessed.
	ambiguous := []string{"/c/IMG_0140.HEIC", "/c/IMG_0140.MOV", "/c/IMG_0140(1).MOV"}
	reader.videos["IMG_0140(1).MOV"] = LivePhotoMetadata{ContentIdentifier: "ID-2", HasStillImageTime: true}
	work, warnings = ClassifyUploadWork(ambiguous, LivePhotoClassificationOptions{Enabled: true, SkipIncomplete: true}, reader)
	if len(warnings) != 1 || warnings[0].Code != "ambiguous-identifier" || len(warnings[0].Paths) != 3 || len(work) != 0 {
		t.Fatalf("work = %+v, warnings = %+v; want all three skipped with one warning", work, warnings)
	}

	// A HEIC whose only motion is H.264 is a mismatch, not a pair.
	mismatched := []string{"/d/IMG_0140.HEIC", "/d/IMG_0140.MOV", "/e/IMG_0140.HEIC", "/e/IMG_0140(1).MOV"}
	reader.hevc["IMG_0140(1).MOV"] = false
	work, warnings = ClassifyUploadWork(mismatched, LivePhotoClassificationOptions{Enabled: true, SkipIncomplete: true}, reader)
	pairedInE := false
	for _, item := range work {
		pairedInE = pairedInE || (item.Kind == UploadWorkLivePhoto && filepath.Dir(item.LivePhoto.PhotoPath) == "/e")
	}
	if pairedInE || len(warnings) != 1 {
		t.Fatalf("work = %+v, warnings = %+v; want the mismatched folder skipped", work, warnings)
	}
}

func TestQueueCancelSendsOneUpdateForTheWholeQueue(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	names := make([]string, 500)
	for i := range names {
		names[i] = fmt.Sprintf("IMG_%04d.jpg", i)
	}
	paths := writeMedia(t, t.TempDir(), names...)

	m.Upload(paths, testOpts(1))
	up.waitStarted(t, names[0])
	waitFor(t, func() bool { return len(m.Items()) == len(names) })
	rec.mu.Lock()
	before := rec.updateEvents
	rec.mu.Unlock()

	m.Cancel()
	rec.waitStop(t)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	// One event for the queued items, one for the active item ending.
	if got := rec.updateEvents - before; got > 2 {
		t.Fatalf("cancel sent %d update events, want at most 2", got)
	}
	for _, item := range rec.items {
		if item.State != QueueItemCancelled {
			t.Fatalf("%s state = %q, want cancelled", item.Name, item.State)
		}
	}
}

func TestClassifyPairsTakeoutMP4Motion(t *testing.T) {
	// Google Takeout: the camera HEIC, an exported JPEG of the same shot, and
	// the motion as .MP4, all carrying one identifier.
	reader := fakeMetadataReader{
		photos: map[string]string{"IMG_0122.HEIC": "ID-3", "IMG_0122.jpeg": "ID-3"},
		videos: map[string]LivePhotoMetadata{"IMG_0122.MP4": {ContentIdentifier: "ID-3", HasStillImageTime: true}},
		hevc:   map[string]bool{"IMG_0122.MP4": true},
	}
	paths := []string{"/t/IMG_0122.HEIC", "/t/IMG_0122.MP4", "/t/IMG_0122.jpeg", "/t/clip.mp4"}
	for _, ignoreApple := range []bool{false, true} {
		work, warnings := ClassifyUploadWork(paths, LivePhotoClassificationOptions{Enabled: true, SkipIncomplete: true, IgnoreAppleMetadata: ignoreApple}, reader)
		if ignoreApple {
			// Filename matching cannot tell the HEIC from the JPEG copy; it
			// only must not warn about the ordinary clip.
			for _, warning := range warnings {
				if slices.Contains(warning.Paths, "/t/clip.mp4") {
					t.Fatalf("ordinary MP4 warned about: %+v", warning)
				}
			}
			continue
		}
		if len(warnings) != 0 {
			t.Fatalf("warnings = %+v", warnings)
		}
		var pairs, singles []string
		for _, item := range work {
			if item.Kind == UploadWorkLivePhoto {
				pairs = append(pairs, item.LivePhoto.PhotoPath+"+"+item.LivePhoto.VideoPath)
			} else {
				singles = append(singles, item.Single.Path)
			}
		}
		if !slices.Equal(pairs, []string{"/t/IMG_0122.HEIC+/t/IMG_0122.MP4"}) || !slices.Equal(singles, []string{"/t/IMG_0122.jpeg", "/t/clip.mp4"}) {
			t.Fatalf("pairs = %v, singles = %v", pairs, singles)
		}
	}
}

func TestClassifyMatchesTakeoutCopiesByEncoding(t *testing.T) {
	// Two Live Photos of one shot, as Google Takeout exports them: the camera
	// original (HEIC + HEVC motion) and a "Most Compatible" export (JPEG +
	// H.264). The plain MP4 name belongs to the JPEG, not to the HEIC.
	newReader := func() fakeMetadataReader {
		return fakeMetadataReader{
			photos: map[string]string{"IMG_0122.HEIC": "ID-4", "IMG_0122.jpeg": "ID-4"},
			videos: map[string]LivePhotoMetadata{
				"IMG_0122.MP4":    {ContentIdentifier: "ID-4", HasStillImageTime: true},
				"IMG_0122(1).MP4": {ContentIdentifier: "ID-4", HasStillImageTime: true},
			},
			areas: map[string]int64{"IMG_0122.HEIC": 3024 * 4032, "IMG_0122.jpeg": 480 * 640, "IMG_0122.MP4": 720 * 960, "IMG_0122(1).MP4": 1440 * 1920},
			hevc:  map[string]bool{"IMG_0122(1).MP4": true},
		}
	}
	paths := []string{"/t/IMG_0122(1).MP4", "/t/IMG_0122.HEIC", "/t/IMG_0122.MP4", "/t/IMG_0122.jpeg"}
	classify := func(reader fakeMetadataReader) (map[string]string, []PreflightWarning, int) {
		work, warnings := ClassifyUploadWork(paths, LivePhotoClassificationOptions{Enabled: true, SkipIncomplete: true}, reader)
		pairs := map[string]string{}
		for _, item := range work {
			if item.Kind == UploadWorkLivePhoto {
				pairs[filepath.Base(item.LivePhoto.PhotoPath)] = filepath.Base(item.LivePhoto.VideoPath)
			}
		}
		return pairs, warnings, len(work)
	}
	want := map[string]string{"IMG_0122.HEIC": "IMG_0122(1).MP4", "IMG_0122.jpeg": "IMG_0122.MP4"}

	pairs, warnings, _ := classify(newReader())
	if len(warnings) != 0 || !maps.Equal(pairs, want) {
		t.Fatalf("pairs = %v, warnings = %+v; want %v", pairs, warnings, want)
	}

	// A full-resolution JPEG export: encoding still decides, resolution does
	// not contradict.
	reader := newReader()
	reader.areas["IMG_0122.jpeg"] = 3024 * 4032
	if pairs, warnings, _ = classify(reader); len(warnings) != 0 || !maps.Equal(pairs, want) {
		t.Fatalf("full-resolution export: pairs = %v, warnings = %+v", pairs, warnings)
	}

	// Encoding and resolution disagree (the larger still would get the
	// smaller video): skip everything rather than guess.
	reader = newReader()
	reader.areas["IMG_0122.MP4"] = 2160 * 2880
	if pairs, warnings, n := classify(reader); len(pairs) != 0 || n != 0 || len(warnings) != 1 || warnings[0].Code != "ambiguous-identifier" {
		t.Fatalf("contradiction: pairs = %v, items = %d, warnings = %+v; want all skipped", pairs, n, warnings)
	}

	// Both videos H.264: nothing tells them apart, so skip everything.
	reader = newReader()
	reader.hevc = nil
	if pairs, warnings, n := classify(reader); len(pairs) != 0 || n != 0 || len(warnings) != 1 {
		t.Fatalf("same encoding: pairs = %v, items = %d, warnings = %+v; want all skipped", pairs, n, warnings)
	}
}

func TestHasItemPathTracksQueuedFilesOnly(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	paths := writeMedia(t, t.TempDir(), "a.jpg")
	if m.HasItemPath(paths[0]) {
		t.Fatal("path known before it was queued")
	}
	m.Upload(paths, testOpts(1))
	up.waitStarted(t, "a.jpg")
	if !m.HasItemPath(paths[0]) || m.HasItemPath("/etc/passwd") {
		t.Fatal("HasItemPath must accept queued files and nothing else")
	}
	up.gate("a.jpg") <- nil
	rec.waitStop(t)
	m.ClearFinished()
	if m.HasItemPath(paths[0]) {
		t.Fatal("path still known after the list was cleared")
	}
}

func TestQueueReusesAnExistingAlbumWithTheSameName(t *testing.T) {
	m, rec, up, albums := newTestQueue(t)
	albums.existing = []LibraryAlbum{
		{MediaKey: "AF1Qip-small", Title: "Trip", ItemCount: 3},
		{MediaKey: "AF1Qip-big", Title: "Trip", ItemCount: 40},
		{MediaKey: "AF1Qip-other", Title: "Other", ItemCount: 99},
	}
	opts := testOpts(1)
	opts.AlbumName = "Trip"
	m.Upload(writeMedia(t, t.TempDir(), "a.jpg"), opts)
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- nil
	rec.waitStop(t)

	if len(albums.created) != 0 {
		t.Fatalf("created %v, want the existing album reused", albums.created)
	}
	if !slices.Equal(albums.added["AF1Qip-big"], []string{"key-a.jpg"}) {
		t.Fatalf("added = %v, want the larger of the two albums named Trip", albums.added)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !slices.ContainsFunc(rec.warnings, func(w PreflightWarning) bool { return w.Code == "album-name-repeated" }) {
		t.Fatal("no warning that two albums share the name")
	}
}

func TestQueueAutoAlbumReusesExistingFolderAlbum(t *testing.T) {
	m, rec, up, albums := newTestQueue(t)
	albums.existing = []LibraryAlbum{{MediaKey: "AF1Qip-jan", Title: "Jan", ItemCount: 5}}
	opts := testOpts(1)
	opts.AlbumAutoMode = true
	m.Upload(writeMedia(t, t.TempDir(), "Jan/a.jpg", "Feb/b.jpg"), opts)
	for range 2 {
		name := <-up.started
		up.gate(name) <- nil
	}
	rec.waitStop(t)

	if !slices.Equal(albums.added["AF1Qip-jan"], []string{"key-a.jpg"}) {
		t.Fatalf("added = %v, want a.jpg in the existing Jan album", albums.added)
	}
	if !slices.Equal(albums.created["Feb"], []string{"key-b.jpg"}) || len(albums.created) != 1 {
		t.Fatalf("created = %v, want only a new Feb album", albums.created)
	}
}

func TestQueueCreatesAlbumWhenListingFails(t *testing.T) {
	m, rec, up, albums := newTestQueue(t)
	m.listAlbums = func(context.Context, ApiOptions) ([]LibraryAlbum, error) { return nil, errors.New("offline") }
	opts := testOpts(1)
	opts.AlbumName = "Trip"
	m.Upload(writeMedia(t, t.TempDir(), "a.jpg"), opts)
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- nil
	rec.waitStop(t)

	if !slices.Equal(albums.created["Trip"], []string{"key-a.jpg"}) {
		t.Fatalf("created = %v, want a new album after the lookup failed", albums.created)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !slices.ContainsFunc(rec.warnings, func(w PreflightWarning) bool { return w.Code == "album-lookup-failed" }) {
		t.Fatal("no warning that existing albums could not be checked")
	}
}

func TestQueueThreadChangesApplyToTheRunningQueue(t *testing.T) {
	m, rec, up, _ := newTestQueue(t)
	names := []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg", "e.jpg"}
	m.Upload(writeMedia(t, t.TempDir(), names...), testOpts(1))
	first := <-up.started

	// Raising the limit starts two more workers on queued files at once.
	m.SetThreads(3)
	second, third := <-up.started, <-up.started
	up.assertNothingStarts(t)

	// Lowering it lets the three running files finish, then only one worker
	// continues.
	m.SetThreads(1)
	for _, name := range []string{first, second, third} {
		up.gate(name) <- nil
	}
	fourth := <-up.started
	up.assertNothingStarts(t)
	up.gate(fourth) <- nil
	fifth := <-up.started
	up.gate(fifth) <- nil
	rec.waitStop(t)

	for _, name := range names {
		if got := rec.itemByName(name).State; got != QueueItemDone {
			t.Errorf("%s state = %q, want done", name, got)
		}
	}
}

func TestQueueRedropAddsAlreadyUploadedFilesToTheAlbum(t *testing.T) {
	// The first run uploaded the files but quit before the album step; the
	// files are now in the library and the album exists empty.
	m, rec, up, albums := newTestQueue(t)
	up.inLibrary = map[string]bool{"a.jpg": true, "b.jpg": true}
	albums.existing = []LibraryAlbum{{MediaKey: "AF1Qip-trip", Title: "Trip"}}
	opts := testOpts(1)
	opts.AlbumName = "Trip"
	m.Upload(writeMedia(t, t.TempDir(), "a.jpg", "b.jpg"), opts)
	for range 2 {
		name := <-up.started
		up.gate(name) <- nil
	}
	rec.waitStop(t)

	if got := albums.added["AF1Qip-trip"]; len(got) != 2 {
		t.Fatalf("album got %v, want both already-uploaded files", got)
	}
	if got := rec.itemByName("a.jpg").State; got != QueueItemSkipped {
		t.Fatalf("a.jpg state = %q, want skipped as already in the library", got)
	}
}
