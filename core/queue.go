package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// QueueItemState is the lifecycle state of one queued upload item.
type QueueItemState string

const (
	QueueItemQueued    QueueItemState = "queued"
	QueueItemActive    QueueItemState = "active"
	QueueItemDone      QueueItemState = "done"
	QueueItemFailed    QueueItemState = "failed"
	QueueItemSkipped   QueueItemState = "skipped"
	QueueItemCancelled QueueItemState = "cancelled"
)

func (s QueueItemState) finished() bool {
	return s == QueueItemDone || s == QueueItemFailed || s == QueueItemSkipped || s == QueueItemCancelled
}

// QueueItem describes one upload item (a file or a Live Photo pair) in the queue.
type QueueItem struct {
	ID          int64          `json:"ID"`
	BatchID     int64          `json:"BatchID"`
	Name        string         `json:"Name"`
	Path        string         `json:"Path"`
	Paths       []string       `json:"Paths"`
	IsLivePhoto bool           `json:"IsLivePhoto"`
	Size        int64          `json:"Size"`
	State       QueueItemState `json:"State"`
	Message     string         `json:"Message"`
	MediaKey    string         `json:"MediaKey"`
	// SkipCode is set on items skipped before upload, such as incomplete Live
	// Photos; those can still be uploaded as ordinary files.
	SkipCode string `json:"SkipCode"`
}

// QueueStatus is the queue-wide state shown alongside the items.
type QueueStatus struct {
	Paused bool `json:"Paused"`
	// WaitingForNetwork is set while uploads are held after a connection loss.
	WaitingForNetwork bool `json:"WaitingForNetwork"`
	// StoppedReason explains a pause the queue imposed itself, such as a full
	// account. Resuming clears it.
	StoppedReason string `json:"StoppedReason"`
	// RateLimitedUntil is when a pause after HTTP 429 ends, in Unix
	// milliseconds, or 0.
	RateLimitedUntil int64 `json:"RateLimitedUntil"`
	// Scan describes dropped files still being scanned.
	Scan ScanProgress `json:"Scan"`
}

// ScanProgress sums the drops still being scanned before their files queue.
type ScanProgress struct {
	// Phase is "finding" while folders are walked, "checking" while Live Photo
	// metadata is read, or "" when nothing is being scanned.
	Phase string `json:"Phase"`
	// Found counts files selected for upload so far.
	Found int `json:"Found"`
	// Checked and Total count files whose Live Photo metadata was read.
	Checked int `json:"Checked"`
	Total   int `json:"Total"`
}

// scanReportInterval throttles scan progress events.
const scanReportInterval = 200 * time.Millisecond

// QueueReporter is implemented by reporters that track individual queue items.
// Reporters that only need run totals, such as the CLI, can ignore it.
type QueueReporter interface {
	// QueueItems announces newly added items in queue order.
	QueueItems([]QueueItem)
	// QueueItemsUpdated carries the full items whose state changed, in one
	// call for changes made together, such as cancelling a large queue. An
	// update can
	// arrive before QueueItems for the same item.
	QueueItemsUpdated([]QueueItem)
	QueueStatus(QueueStatus)
}

const (
	// maxNetworkRequeues bounds how often one item goes back to the queue after a
	// connection loss before it is reported as failed.
	maxNetworkRequeues          = 5
	defaultNetworkProbeInterval = 5 * time.Second
)

// Pauses after HTTP 429 start here and double on each repeat, up to the cap.
// Variables so tests can shorten them.
var (
	rateLimitPauseStart = 60 * time.Second
	rateLimitPauseMax   = 10 * time.Minute
)

type queueEntry struct {
	item          QueueItem
	work          UploadWorkItem
	batch         *uploadBatch
	key           string // duplicate-detection key of the primary path
	cancel        context.CancelFunc
	bytesCounted  bool
	networkErrors int
}

type uploadBatch struct {
	id        int64
	opts      UploadOptions
	ctx       context.Context
	scanning  bool
	scan      ScanProgress
	remaining int
	// uploads holds items that ended with a media key since the last album pass.
	uploads []albumUpload
}

type albumUpload struct {
	path     string
	mediaKey string
}

// UploadManager runs a persistent upload queue. Files can be added while a run
// is in progress; the run ends, with UploadStop, once the queue drains.
type UploadManager struct {
	mu   sync.Mutex
	cond *sync.Cond

	reporter UploadReporter
	queue    QueueReporter
	logger   *slog.Logger

	running bool
	// tasks counts workers, preflight scans, album passes and network probes.
	tasks             int
	scanning          int
	paused            bool
	waitingForNetwork bool
	stoppedReason     string
	rateLimitedUntil  time.Time
	rateLimitPause    time.Duration
	total             int

	runCtx      context.Context
	runCancel   context.CancelFunc
	albumCtx    context.Context
	albumCancel context.CancelFunc

	nextItemID  int64
	nextBatchID int64
	items       map[int64]*queueEntry
	pending     []*queueEntry
	inQueue     map[string]*queueEntry
	workerSlots []bool
	// threadLimit, when set, replaces each drop's thread count, so changing
	// the setting applies to the queue that is already running.
	threadLimit int
	// itemPaths counts known items per file path, for HasItemPath.
	itemPaths  map[string]int
	scans      map[*uploadBatch]struct{}
	lastScanAt time.Time

	albumMu   sync.Mutex
	albumKeys map[string]string
	// albumLists holds the account's existing albums, listed once per
	// account in the background, so drops into an album that already exists
	// add to it instead of creating a duplicate. Guarded by albumListMu, not
	// albumMu, which album passes hold for as long as they run.
	albumListMu sync.Mutex
	albumLists  map[string]*albumList

	// Network, upload and album hooks, replaced in tests.
	probe         func(ctx context.Context, proxy string) error
	probeInterval time.Duration
	newApi        func(ApiOptions) (*Api, error)
	uploadItem    func(ctx context.Context, api *Api, item UploadWorkItem, opts UploadOptions, workerID int, reporter UploadReporter) (mediaKey string, skipped, existed bool, err error)
	newAlbumAPI   func(ApiOptions) (albumAPI, error)
	listAlbums    func(context.Context, ApiOptions) ([]LibraryAlbum, error)
}

// albumList is one account's existing albums by title, once listed.
type albumList struct {
	done    chan struct{}
	byTitle map[string][]LibraryAlbum
	err     error
}

// NewUploadManager creates a manager that reports progress to reporter. A nil
// reporter or logger discards output. Reporters that also implement
// QueueReporter receive per-item queue events.
func NewUploadManager(reporter UploadReporter, logger *slog.Logger) *UploadManager {
	if reporter == nil {
		reporter = NopReporter{}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	queue, _ := reporter.(QueueReporter)
	m := &UploadManager{
		reporter:      reporter,
		queue:         queue,
		logger:        logger,
		items:         make(map[int64]*queueEntry),
		inQueue:       make(map[string]*queueEntry),
		itemPaths:     make(map[string]int),
		scans:         make(map[*uploadBatch]struct{}),
		albumKeys:     make(map[string]string),
		albumLists:    make(map[string]*albumList),
		probe:         probeGooglePhotos,
		probeInterval: defaultNetworkProbeInterval,
		newApi:        NewApi,
		uploadItem:    uploadWorkItem,
		newAlbumAPI: func(opts ApiOptions) (albumAPI, error) {
			api, err := NewApi(opts)
			if err != nil {
				return nil, err
			}
			return api, nil
		},
		listAlbums: func(ctx context.Context, opts ApiOptions) ([]LibraryAlbum, error) {
			api, err := NewApi(opts)
			if err != nil {
				return nil, err
			}
			return api.ListAlbums(ctx)
		},
	}
	m.cond = sync.NewCond(&m.mu)
	return m
}

func (m *UploadManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// Upload adds paths to the queue, starting a run if none is in progress.
// Scanning happens in the background; progress arrives through the reporter.
func (m *UploadManager) Upload(paths []string, opts UploadOptions) {
	opts = opts.normalized()
	if opts.Api.Account == "" {
		// Pin the account now so switching accounts mid-run cannot redirect
		// files that are already queued.
		opts.Api.Account = currentConfig().Account.Selected
	}

	m.mu.Lock()
	m.startRunLocked()
	m.nextBatchID++
	batch := &uploadBatch{id: m.nextBatchID, opts: opts, ctx: m.runCtx, scanning: true, scan: ScanProgress{Phase: "finding"}}
	m.scans[batch] = struct{}{}
	m.scanning++
	m.reportStatusLocked()
	m.tasks++
	m.mu.Unlock()

	if opts.AlbumAutoMode || (opts.AlbumName != "" && !IsAlbumKey(opts.AlbumName)) {
		m.startAlbumListing(opts.Api)
	}
	go m.preflight(batch, paths)
}

// startAlbumListing lists the account's albums in the background, once per
// account, ready for the album pass at the end of the batch. Listing reads
// the whole library sync, which can take a minute on a large library; the
// uploads usually take longer.
func (m *UploadManager) startAlbumListing(opts ApiOptions) {
	m.albumListMu.Lock()
	defer m.albumListMu.Unlock()
	if _, ok := m.albumLists[opts.Account]; ok {
		return
	}
	list := &albumList{done: make(chan struct{})}
	m.albumLists[opts.Account] = list
	go func() {
		defer close(list.done)
		albums, err := m.listAlbums(context.Background(), opts)
		if err != nil {
			list.err = err
			return
		}
		list.byTitle = make(map[string][]LibraryAlbum)
		for _, album := range albums {
			list.byTitle[album.Title] = append(list.byTitle[album.Title], album)
		}
	}()
}

// existingAlbums waits for the account's album list. It returns nil, after
// warning, when the albums could not be listed: new albums are then created
// as before.
func (m *UploadManager) existingAlbums(ctx context.Context, opts ApiOptions) *albumList {
	m.albumListMu.Lock()
	list := m.albumLists[opts.Account]
	m.albumListMu.Unlock()
	if list == nil {
		return nil
	}
	select {
	case <-list.done:
	case <-ctx.Done():
		return nil
	}
	if list.err != nil {
		m.reporter.Warning(PreflightWarning{
			Code:    "album-lookup-failed",
			Message: fmt.Sprintf("Could not check for existing albums, so new albums may duplicate existing ones: %v", list.err),
		})
		// Try again on the next album pass.
		m.albumListMu.Lock()
		if m.albumLists[opts.Account] == list {
			delete(m.albumLists, opts.Account)
		}
		m.albumListMu.Unlock()
		return nil
	}
	return list
}

// startRunLocked begins a run if none is active. A run that was cancelled but
// is still finishing gets a fresh context so new files are not cancelled too.
func (m *UploadManager) startRunLocked() {
	if m.running && m.runCtx.Err() == nil {
		return
	}
	m.runCtx, m.runCancel = context.WithCancel(context.Background())
	if m.running {
		return
	}
	m.running = true
	m.paused = false
	m.waitingForNetwork = false
	m.stoppedReason = ""
	m.rateLimitedUntil = time.Time{}
	m.rateLimitPause = 0
	m.total = 0
	m.albumCtx, m.albumCancel = context.WithCancel(context.Background())
	// Emitted under the lock so no item event of this run can precede it.
	m.reporter.UploadStart(UploadBatchStart{})
	m.reportStatusLocked()
}

// Cancel stops every queued and active item. Items that already uploaded are
// still added to their albums; calling Cancel again while only album work
// remains stops that too.
func (m *UploadManager) Cancel() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	if m.runCtx.Err() != nil || (len(m.pending) == 0 && m.scanning == 0 && !m.anyActiveLocked()) {
		m.albumCancel()
		m.mu.Unlock()
		return
	}
	m.runCancel()
	var updates []QueueItem
	var finalize []*uploadBatch
	var bytesDelta int64
	for _, entry := range m.pending {
		update, delta := m.endQueuedLocked(entry, "Cancelled")
		updates = append(updates, update)
		bytesDelta += delta
		if b := m.batchDoneLocked(entry.batch); b != nil && !slices.Contains(finalize, b) {
			finalize = append(finalize, b)
		}
	}
	m.pending = nil
	m.waitingForNetwork = false
	m.rateLimitedUntil = time.Time{}
	m.cond.Broadcast()
	m.reportStatusLocked()
	m.mu.Unlock()

	if bytesDelta != 0 {
		m.reporter.TotalBytesDelta(bytesDelta)
	}
	m.reportUpdates(updates)
	for _, batch := range finalize {
		m.startAlbumPass(batch)
	}
}

// SetPaused stops workers from starting new items. Active items finish.
func (m *UploadManager) SetPaused(paused bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.paused == paused && (paused || m.stoppedReason == "") {
		return
	}
	m.paused = paused
	if !paused {
		m.stoppedReason = ""
	}
	m.cond.Broadcast()
	m.reportStatusLocked()
}

// CancelItem removes a queued item or cancels an active one.
func (m *UploadManager) CancelItem(id int64) {
	m.mu.Lock()
	entry := m.items[id]
	if entry == nil {
		m.mu.Unlock()
		return
	}
	switch entry.item.State {
	case QueueItemActive:
		if entry.cancel != nil {
			entry.cancel()
		}
		m.mu.Unlock()
	case QueueItemQueued:
		m.pending = slices.DeleteFunc(m.pending, func(e *queueEntry) bool { return e == entry })
		update, bytesDelta := m.endQueuedLocked(entry, "Removed from queue")
		batch := m.batchDoneLocked(entry.batch)
		m.cond.Broadcast()
		m.mu.Unlock()
		if bytesDelta != 0 {
			m.reporter.TotalBytesDelta(bytesDelta)
		}
		m.reportUpdates([]QueueItem{update})
		if batch != nil {
			m.startAlbumPass(batch)
		}
	default:
		m.mu.Unlock()
	}
}

// RetryItem puts a failed or cancelled item back on the queue.
func (m *UploadManager) RetryItem(id int64) {
	m.retry(func(entry *queueEntry) bool { return entry.item.ID == id })
}

// RetryFailed puts every failed item back on the queue.
func (m *UploadManager) RetryFailed() {
	m.retry(func(entry *queueEntry) bool { return entry.item.State == QueueItemFailed })
}

// UploadSkipped queues every file that preflight skipped, such as incomplete
// Live Photos, to be uploaded as an ordinary file.
func (m *UploadManager) UploadSkipped() {
	m.retry(func(entry *queueEntry) bool { return entry.item.State == QueueItemSkipped })
}

// retryable reports whether an item can go back on the queue: failed and
// cancelled items, and files preflight skipped.
func (e *queueEntry) retryable() bool {
	if e.work.Kind == "" {
		return false
	}
	switch e.item.State {
	case QueueItemFailed, QueueItemCancelled:
		return true
	case QueueItemSkipped:
		return e.item.SkipCode != ""
	}
	return false
}

func (m *UploadManager) retry(match func(*queueEntry) bool) {
	m.mu.Lock()
	var retried []*queueEntry
	for _, entry := range m.items {
		if !entry.retryable() || !match(entry) {
			continue
		}
		if _, queued := m.inQueue[entry.key]; queued {
			continue
		}
		retried = append(retried, entry)
	}
	if len(retried) == 0 {
		m.mu.Unlock()
		return
	}
	slices.SortFunc(retried, func(a, b *queueEntry) int { return int(a.item.ID - b.item.ID) })
	m.startRunLocked()
	var updates []QueueItem
	var bytesDelta int64
	for _, entry := range retried {
		if entry.batch.ctx.Err() != nil {
			entry.batch.ctx = m.runCtx
		}
		entry.item.State = QueueItemQueued
		entry.item.Message = ""
		entry.item.SkipCode = ""
		entry.networkErrors = 0
		entry.batch.remaining++
		m.total++
		m.inQueue[entry.key] = entry
		m.pending = append(m.pending, entry)
		if !entry.bytesCounted {
			entry.bytesCounted = true
			bytesDelta += entry.item.Size
		}
		updates = append(updates, entry.item)
		m.spawnWorkersLocked(entry.batch.opts.Threads)
	}
	// Under the lock: workers could otherwise drain the queue and end the run
	// before this start event is sent.
	m.reporter.UploadStart(UploadBatchStart{Total: m.total})
	m.cond.Broadcast()
	m.mu.Unlock()

	if bytesDelta != 0 {
		m.reporter.TotalBytesDelta(bytesDelta)
	}
	m.reportUpdates(updates)
}

// ClearFinished forgets finished items so they can no longer be retried.
func (m *UploadManager) ClearFinished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, entry := range m.items {
		if entry.item.State.finished() {
			delete(m.items, id)
			for _, path := range entry.item.Paths {
				if m.itemPaths[path]--; m.itemPaths[path] <= 0 {
					delete(m.itemPaths, path)
				}
			}
		}
	}
}

// HasItemPath reports whether path is a file of a known queue item. It lets
// the app serve previews of queued files and nothing else.
func (m *UploadManager) HasItemPath(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.itemPaths[path] > 0
}

// Items returns a snapshot of every known item in queue order.
func (m *UploadManager) Items() []QueueItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]QueueItem, 0, len(m.items))
	for _, entry := range m.items {
		items = append(items, entry.item)
	}
	slices.SortFunc(items, func(a, b QueueItem) int { return int(a.ID - b.ID) })
	return items
}

// Status returns the current queue-wide state.
func (m *UploadManager) Status() QueueStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *UploadManager) statusLocked() QueueStatus {
	status := QueueStatus{Paused: m.paused, WaitingForNetwork: m.waitingForNetwork, StoppedReason: m.stoppedReason}
	if !m.rateLimitedUntil.IsZero() {
		status.RateLimitedUntil = m.rateLimitedUntil.UnixMilli()
	}
	for batch := range m.scans {
		status.Scan.Found += batch.scan.Found
		status.Scan.Checked += batch.scan.Checked
		status.Scan.Total += batch.scan.Total
		if status.Scan.Phase == "" || batch.scan.Phase == "checking" {
			status.Scan.Phase = batch.scan.Phase
		}
	}
	return status
}

func (m *UploadManager) preflight(batch *uploadBatch, paths []string) {
	defer m.taskDone()
	opts := batch.opts
	cancelled := func() bool { return batch.ctx.Err() != nil }

	targetPaths, scanWarnings, err := filterGooglePhotosFilesWithWarnings(paths, opts, cancelled, func(found int) {
		m.updateScan(batch, func(scan *ScanProgress) { scan.Found = found })
	})
	if err != nil {
		if !cancelled() {
			m.reporter.FileResult(FileUploadResult{IsError: true, Error: err, ErrorMessage: err.Error()})
			m.addScanFailure(batch, paths, err)
		}
		m.scanFinished(batch)
		return
	}
	workItems, classifyWarnings := ClassifyUploadWork(targetPaths, LivePhotoClassificationOptions{
		Enabled:             opts.PairLivePhotos,
		SkipIncomplete:      opts.SkipIncompleteLivePhotos,
		IgnoreAppleMetadata: opts.IgnoreAppleMetadata,
		Cancelled:           cancelled,
		Progress: func(done, total int) {
			m.updateScan(batch, func(scan *ScanProgress) {
				scan.Phase, scan.Checked, scan.Total = "checking", done, total
			})
		},
	}, nil)
	if cancelled() {
		m.scanFinished(batch)
		return
	}

	var apiErr error
	if len(workItems) > 0 {
		_, apiErr = m.newApi(opts.Api)
	}
	m.addBatchItems(batch, workItems, append(scanWarnings, classifyWarnings...), apiErr)
	m.scanFinished(batch)
}

// addBatchItems queues a scanned batch. Items whose primary file is already
// queued or uploading are dropped with a warning.
func (m *UploadManager) addBatchItems(batch *uploadBatch, workItems []UploadWorkItem, warnings []PreflightWarning, apiErr error) {
	type candidate struct {
		work UploadWorkItem
		key  string
		size int64
	}
	// Files skipped by preflight become one item each, ready to be uploaded
	// as ordinary files if the user asks.
	type skippedFile struct {
		path, code, message string
		size                int64
	}
	var skippedFiles []skippedFile
	for _, warning := range warnings {
		if !IsSkippedPreflightWarning(warning.Code) {
			continue
		}
		for _, path := range warning.Paths {
			var size int64
			if info, err := os.Stat(path); err == nil {
				size = info.Size()
			}
			skippedFiles = append(skippedFiles, skippedFile{path: path, code: warning.Code, message: warning.Message, size: size})
		}
	}
	candidates := make([]candidate, 0, len(workItems))
	for _, work := range workItems {
		var size int64
		for _, path := range uploadWorkPaths(work) {
			if info, err := os.Stat(path); err == nil {
				size += info.Size()
			}
		}
		candidates = append(candidates, candidate{work: work, key: queueKey(uploadWorkPrimaryPath(work)), size: size})
	}

	m.mu.Lock()
	if batch.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	var added []QueueItem
	var skipped []*queueEntry
	var duplicates []string
	var bytesDelta int64
	for _, file := range skippedFiles {
		work := UploadWorkItem{Kind: UploadWorkSingle, Single: &SingleMedia{Path: file.path}}
		entry := m.newEntryLocked(batch, work, []string{file.path}, false, file.size)
		entry.key = queueKey(file.path)
		entry.item.State = QueueItemSkipped
		entry.item.Message = file.message
		entry.item.SkipCode = file.code
		m.total++
		skipped = append(skipped, entry)
		added = append(added, entry.item)
	}
	for _, c := range candidates {
		if _, queued := m.inQueue[c.key]; queued {
			duplicates = append(duplicates, uploadWorkPrimaryPath(c.work))
			continue
		}
		entry := m.newEntryLocked(batch, c.work, uploadWorkPaths(c.work), c.work.Kind == UploadWorkLivePhoto, c.size)
		entry.key = c.key
		entry.bytesCounted = true
		bytesDelta += c.size
		m.total++
		m.inQueue[c.key] = entry
		m.pending = append(m.pending, entry)
		batch.remaining++
		added = append(added, entry.item)
	}
	total := m.total
	m.mu.Unlock()

	m.reporter.UploadStart(UploadBatchStart{Total: total})
	if m.queue != nil && len(added) > 0 {
		m.queue.QueueItems(added)
	}
	for _, warning := range warnings {
		m.reporter.Warning(warning)
	}
	if len(duplicates) > 0 {
		m.reporter.Warning(PreflightWarning{
			Paths:   duplicates,
			Code:    "already-queued",
			Message: fmt.Sprintf("%d item(s) are already in the queue and were not added again", len(duplicates)),
		})
	}
	for _, entry := range skipped {
		m.reporter.FileResult(FileUploadResult{
			IsLivePhoto: true,
			Skipped:     true,
			SkipCode:    entry.item.SkipCode,
			SkipReason:  entry.item.Message,
			Path:        entry.item.Path,
			Paths:       entry.item.Paths,
		})
	}
	if bytesDelta != 0 {
		m.reporter.TotalBytesDelta(bytesDelta)
	}

	if apiErr != nil {
		// Without a usable account nothing in the batch can upload.
		m.failBatch(batch, apiErr)
		return
	}
	m.mu.Lock()
	m.spawnWorkersLocked(batch.opts.Threads)
	m.cond.Broadcast()
	m.mu.Unlock()
}

func (m *UploadManager) failBatch(batch *uploadBatch, err error) {
	m.mu.Lock()
	var failed []*queueEntry
	m.pending = slices.DeleteFunc(m.pending, func(e *queueEntry) bool {
		if e.batch == batch {
			failed = append(failed, e)
			return true
		}
		return false
	})
	var updates []QueueItem
	for _, entry := range failed {
		entry.item.State = QueueItemFailed
		entry.item.Message = err.Error()
		delete(m.inQueue, entry.key)
		batch.remaining--
		updates = append(updates, entry.item)
	}
	m.mu.Unlock()
	for _, entry := range failed {
		m.reporter.FileResult(FileUploadResult{
			IsError: true, IsLivePhoto: entry.item.IsLivePhoto, Error: err, ErrorMessage: err.Error(),
			Path: entry.item.Path, Paths: entry.item.Paths,
		})
	}
	m.reportUpdates(updates)
}

func (m *UploadManager) addScanFailure(batch *uploadBatch, paths []string, err error) {
	m.mu.Lock()
	if batch.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	entry := m.newEntryLocked(batch, UploadWorkItem{}, paths, false, 0)
	entry.item.Name = "Could not read the dropped files"
	entry.item.State = QueueItemFailed
	entry.item.Message = err.Error()
	m.total++
	total := m.total
	item := entry.item
	m.mu.Unlock()
	m.reporter.UploadStart(UploadBatchStart{Total: total})
	if m.queue != nil {
		m.queue.QueueItems([]QueueItem{item})
	}
}

func (m *UploadManager) newEntryLocked(batch *uploadBatch, work UploadWorkItem, paths []string, livePhoto bool, size int64) *queueEntry {
	m.nextItemID++
	primary := ""
	if len(paths) > 0 {
		primary = paths[0]
	}
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, filepath.Base(path))
	}
	entry := &queueEntry{
		item: QueueItem{
			ID:          m.nextItemID,
			BatchID:     batch.id,
			Name:        strings.Join(names, " + "),
			Path:        primary,
			Paths:       append([]string(nil), paths...),
			IsLivePhoto: livePhoto,
			Size:        size,
			State:       QueueItemQueued,
		},
		work:  work,
		batch: batch,
	}
	m.items[entry.item.ID] = entry
	for _, path := range entry.item.Paths {
		m.itemPaths[path]++
	}
	return entry
}

// updateScan records a batch's scan progress and reports it, at most every
// scanReportInterval.
func (m *UploadManager) updateScan(batch *uploadBatch, update func(*ScanProgress)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	update(&batch.scan)
	if now := time.Now(); now.Sub(m.lastScanAt) >= scanReportInterval {
		m.lastScanAt = now
		m.reportStatusLocked()
	}
}

func (m *UploadManager) scanFinished(batch *uploadBatch) {
	m.mu.Lock()
	batch.scanning = false
	delete(m.scans, batch)
	m.reportStatusLocked()
	m.scanning--
	finalize := m.batchDoneLocked(batch)
	m.cond.Broadcast()
	m.mu.Unlock()
	if finalize != nil {
		m.startAlbumPass(finalize)
	}
}

// SetThreads changes how many files upload at once, right away: more workers
// start on queued files, or surplus workers stop after their current file.
// Nothing in progress is interrupted.
func (m *UploadManager) SetThreads(threads int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.threadLimit = max(1, threads)
	m.spawnWorkersLocked(m.threadLimit)
	m.cond.Broadcast()
}

func (m *UploadManager) liveWorkersLocked() int {
	live := 0
	for _, used := range m.workerSlots {
		if used {
			live++
		}
	}
	return live
}

// spawnWorkersLocked starts workers until there are threads of them, or the
// live thread limit when one is set, or one per queued item.
func (m *UploadManager) spawnWorkersLocked(threads int) {
	if m.threadLimit > 0 {
		threads = m.threadLimit
	}
	live := m.liveWorkersLocked()
	for live < threads && live < len(m.pending)+m.activeCountLocked() {
		id := slices.Index(m.workerSlots, false)
		if id < 0 {
			id = len(m.workerSlots)
			m.workerSlots = append(m.workerSlots, true)
		} else {
			m.workerSlots[id] = true
		}
		live++
		m.tasks++
		go m.runWorker(id)
	}
}

func (m *UploadManager) activeCountLocked() int {
	count := 0
	for _, entry := range m.inQueue {
		if entry.item.State == QueueItemActive {
			count++
		}
	}
	return count
}

func (m *UploadManager) anyActiveLocked() bool {
	return m.activeCountLocked() > 0
}

// nextItem blocks until an item can start, or returns nil when the worker
// should exit because the queue drained, the run was cancelled, or the thread
// limit was lowered below the number of workers.
func (m *UploadManager) nextItem(workerID int) (*queueEntry, context.Context, QueueItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		if m.runCtx.Err() != nil {
			return nil, nil, QueueItem{}
		}
		if m.threadLimit > 0 && m.liveWorkersLocked() > m.threadLimit {
			// Free the slot now, so other workers count one fewer at once.
			m.workerSlots[workerID] = false
			return nil, nil, QueueItem{}
		}
		if !m.paused && !m.waitingForNetwork && m.rateLimitedUntil.IsZero() && len(m.pending) > 0 {
			entry := m.pending[0]
			m.pending = m.pending[1:]
			entry.item.State = QueueItemActive
			entry.item.Message = ""
			ctx, cancel := context.WithCancel(entry.batch.ctx)
			entry.cancel = cancel
			return entry, ctx, entry.item
		}
		if len(m.pending) == 0 && m.scanning == 0 {
			return nil, nil, QueueItem{}
		}
		m.cond.Wait()
	}
}

func (m *UploadManager) runWorker(workerID int) {
	defer func() {
		m.mu.Lock()
		m.workerSlots[workerID] = false
		m.mu.Unlock()
		m.taskDone()
	}()
	m.reporter.ThreadStatus(ThreadStatus{WorkerID: workerID, Status: "idle", Message: "Waiting for files..."})

	apis := make(map[ApiOptions]*Api)
	for {
		entry, ctx, item := m.nextItem(workerID)
		if entry == nil {
			break
		}
		m.reportUpdates([]QueueItem{item})

		opts := entry.batch.opts
		var mediaKey string
		var skipped, existed bool
		var err error
		api := apis[opts.Api]
		if api == nil {
			api, err = m.newApi(opts.Api)
			if err == nil {
				apis[opts.Api] = api
			}
		}
		if err == nil {
			mediaKey, skipped, existed, err = m.uploadItem(ctx, api, entry.work, opts, workerID, m.reporter)
		}
		m.finishItem(workerID, entry, ctx, mediaKey, skipped, existed, err)

		m.reporter.ThreadStatus(ThreadStatus{WorkerID: workerID, Status: "idle", Message: "Waiting for next file..."})
	}
	m.reporter.ThreadStatus(ThreadStatus{WorkerID: workerID, Status: "idle", Message: "Finished"})
}

func (m *UploadManager) finishItem(workerID int, entry *queueEntry, ctx context.Context, mediaKey string, skipped, existed bool, err error) {
	path := entry.item.Path
	cancelled := err != nil && ctx.Err() != nil
	apiErr, isAPIErr := asAPIError(err)
	accountProblem := ""
	if err != nil && !cancelled && mediaKey == "" && isAPIErr {
		switch {
		case apiErr.StorageFull():
			accountProblem = "Your Google account is out of storage. Free up space in Google Photos, then press Resume."
		case apiErr.CredentialRejected():
			accountProblem = "Google rejected this account's credential. Add the account again, then press Resume."
		}
	}
	rateLimited := err != nil && !cancelled && mediaKey == "" && isAPIErr && apiErr.RateLimited()
	network := err != nil && !cancelled && mediaKey == "" && isNetworkError(err)

	m.mu.Lock()
	entry.cancel()
	entry.cancel = nil
	var result *FileUploadResult
	var warning *PreflightWarning
	var bytesDelta int64
	threadStatus := ThreadStatus{WorkerID: workerID, FilePath: path, FileName: entry.item.Name}
	requeued := false
	switch {
	case cancelled:
		entry.item.State = QueueItemCancelled
		entry.item.Message = "Cancelled"
		threadStatus.Status, threadStatus.Message = "idle", "Cancelled"
	case accountProblem != "" && m.runCtx.Err() == nil:
		// Nothing else can upload either, so stop the queue rather than fail
		// every remaining item in turn.
		entry.item.State = QueueItemQueued
		entry.item.Message = accountProblem
		m.pending = append([]*queueEntry{entry}, m.pending...)
		requeued = true
		threadStatus.Status, threadStatus.Message = "idle", "Queue stopped"
		if m.stoppedReason == "" {
			m.paused = true
			m.stoppedReason = accountProblem
			m.reportStatusLocked()
		}
	case rateLimited && m.runCtx.Err() == nil:
		entry.item.State = QueueItemQueued
		entry.item.Message = "Google is rate limiting uploads; waiting before retrying"
		m.pending = append([]*queueEntry{entry}, m.pending...)
		requeued = true
		threadStatus.Status, threadStatus.Message = "idle", "Rate limited"
		if m.rateLimitedUntil.IsZero() {
			m.rateLimitPause = min(max(rateLimitPauseStart, m.rateLimitPause*2), rateLimitPauseMax)
			m.rateLimitedUntil = time.Now().Add(m.rateLimitPause)
			m.tasks++
			go m.holdForRateLimit(m.rateLimitPause)
			m.reportStatusLocked()
		}
	case network && entry.networkErrors < maxNetworkRequeues && m.runCtx.Err() == nil:
		entry.networkErrors++
		entry.item.State = QueueItemQueued
		entry.item.Message = "Connection lost; will retry when it is back"
		m.pending = append([]*queueEntry{entry}, m.pending...)
		requeued = true
		threadStatus.Status, threadStatus.Message = "idle", entry.item.Message
		if !m.waitingForNetwork {
			m.waitingForNetwork = true
			m.tasks++
			go m.waitForNetwork(entry.batch.opts.Api.Proxy)
			m.reportStatusLocked()
		}
	case err != nil && mediaKey != "":
		entry.item.State = QueueItemDone
		entry.item.MediaKey = mediaKey
		entry.item.Message = fmt.Sprintf("Uploaded, but local cleanup failed: %v", err)
		result = &FileUploadResult{IsLivePhoto: entry.item.IsLivePhoto, Path: path, Paths: entry.item.Paths, MediaKey: mediaKey}
		warning = &PreflightWarning{Paths: entry.item.Paths, Code: "local-cleanup-failed", Message: err.Error()}
		threadStatus.Status, threadStatus.Message = "completed", entry.item.Message
	case err != nil:
		entry.item.State = QueueItemFailed
		entry.item.Message = err.Error()
		result = &FileUploadResult{IsError: true, IsLivePhoto: entry.item.IsLivePhoto, Error: err, ErrorMessage: err.Error(), Path: path, Paths: entry.item.Paths}
		threadStatus.Status, threadStatus.Message = "error", fmt.Sprintf("Error: %v", err)
	case skipped || existed:
		entry.item.State = QueueItemSkipped
		entry.item.MediaKey = mediaKey
		code, reason := "remote-duplicate", "Already in your Google Photos library"
		switch {
		case entry.item.IsLivePhoto && mediaKey != "":
			code, reason = "remote-live-photo-exists", "Already a Live Photo in your library"
		case entry.item.IsLivePhoto:
			code, reason = "remote-live-photo-component-exists", "Skipped because a Live Photo component already exists remotely"
		}
		entry.item.Message = reason
		result = &FileUploadResult{IsLivePhoto: entry.item.IsLivePhoto, Skipped: true, SkipCode: code, SkipReason: reason, MediaKey: mediaKey, Path: path, Paths: entry.item.Paths}
	default:
		entry.item.State = QueueItemDone
		entry.item.MediaKey = mediaKey
		entry.item.Message = ""
		m.rateLimitPause = 0
		result = &FileUploadResult{IsLivePhoto: entry.item.IsLivePhoto, Path: path, Paths: entry.item.Paths, MediaKey: mediaKey}
		threadStatus.Status, threadStatus.Message = "completed", "Completed"
	}
	var finalize *uploadBatch
	if !requeued {
		delete(m.inQueue, entry.key)
		entry.batch.remaining--
		if mediaKey != "" {
			entry.batch.uploads = append(entry.batch.uploads, albumUpload{path: path, mediaKey: mediaKey})
		}
		// Single files report no bytes of their own once they stop, so drop them
		// from the total. Live Photos already adjust the total as they go.
		if (entry.item.State == QueueItemFailed || entry.item.State == QueueItemCancelled) &&
			!entry.item.IsLivePhoto && entry.bytesCounted {
			entry.bytesCounted = false
			bytesDelta = -entry.item.Size
		}
		finalize = m.batchDoneLocked(entry.batch)
	}
	item := entry.item
	m.cond.Broadcast()
	m.mu.Unlock()

	if bytesDelta != 0 {
		m.reporter.TotalBytesDelta(bytesDelta)
	}
	if result != nil {
		m.reporter.FileResult(*result)
		if result.IsError {
			m.logger.Error("upload error", "path", path, "error", err)
		} else {
			m.logger.Info("upload finished", "path", path, "state", item.State)
		}
	}
	if warning != nil {
		m.reporter.Warning(*warning)
	}
	if threadStatus.Status != "" {
		m.reporter.ThreadStatus(threadStatus)
	}
	m.reportUpdates([]QueueItem{item})
	if finalize != nil {
		m.startAlbumPass(finalize)
	}
}

// endQueuedLocked marks a queued item cancelled before it started and returns
// the change to the run's byte total.
func (m *UploadManager) endQueuedLocked(entry *queueEntry, message string) (QueueItem, int64) {
	entry.item.State = QueueItemCancelled
	entry.item.Message = message
	delete(m.inQueue, entry.key)
	entry.batch.remaining--
	var delta int64
	if entry.bytesCounted {
		entry.bytesCounted = false
		delta = -entry.item.Size
	}
	return entry.item, delta
}

// batchDoneLocked returns the batch when it has no more queued or active items
// and has finished scanning, so its album pass can start.
func (m *UploadManager) batchDoneLocked(batch *uploadBatch) *uploadBatch {
	if batch.scanning || batch.remaining > 0 || len(batch.uploads) == 0 {
		return nil
	}
	if batch.opts.AlbumName == "" && !batch.opts.AlbumAutoMode {
		batch.uploads = nil
		return nil
	}
	return batch
}

func (m *UploadManager) startAlbumPass(batch *uploadBatch) {
	m.mu.Lock()
	uploads := batch.uploads
	batch.uploads = nil
	albumCtx := m.albumCtx
	if len(uploads) == 0 {
		m.mu.Unlock()
		return
	}
	m.tasks++
	m.mu.Unlock()
	go func() {
		defer m.taskDone()
		m.handleAlbumCreation(albumCtx, batch.opts, uploads)
	}()
}

// holdForRateLimit keeps workers from starting items until the pause ends.
func (m *UploadManager) holdForRateLimit(delay time.Duration) {
	defer m.taskDone()
	m.mu.Lock()
	runCtx := m.runCtx
	m.mu.Unlock()
	select {
	case <-time.After(delay):
	case <-runCtx.Done():
	}
	m.mu.Lock()
	m.rateLimitedUntil = time.Time{}
	m.spawnWorkersLocked(m.maxThreadsLocked())
	m.cond.Broadcast()
	m.reportStatusLocked()
	m.mu.Unlock()
}

// waitForNetwork holds the queue until Google Photos is reachable again.
func (m *UploadManager) waitForNetwork(proxy string) {
	defer m.taskDone()
	m.mu.Lock()
	runCtx := m.runCtx
	m.mu.Unlock()
	for {
		select {
		case <-runCtx.Done():
		case <-time.After(m.probeInterval):
			ctx, cancel := context.WithTimeout(runCtx, 15*time.Second)
			err := m.probe(ctx, proxy)
			cancel()
			if err != nil && runCtx.Err() == nil {
				m.logger.Info("still waiting for network", "error", err)
				continue
			}
		}
		m.mu.Lock()
		m.waitingForNetwork = false
		m.spawnWorkersLocked(m.maxThreadsLocked())
		m.cond.Broadcast()
		m.reportStatusLocked()
		m.mu.Unlock()
		return
	}
}

func (m *UploadManager) maxThreadsLocked() int {
	threads := 1
	for _, entry := range m.pending {
		threads = max(threads, entry.batch.opts.Threads)
	}
	return threads
}

// taskDone ends the run once no task is left and nothing is queued.
func (m *UploadManager) taskDone() {
	m.mu.Lock()
	m.tasks--
	if m.tasks > 0 || !m.running {
		m.mu.Unlock()
		return
	}
	if len(m.pending) > 0 {
		// Workers exited while items were still queued (e.g. after a requeue).
		m.spawnWorkersLocked(m.maxThreadsLocked())
		m.mu.Unlock()
		return
	}
	m.running = false
	m.paused = false
	m.waitingForNetwork = false
	m.stoppedReason = ""
	m.rateLimitedUntil = time.Time{}
	m.runCancel()
	m.albumCancel()
	// Emitted under the lock so a new run cannot start before this one ends.
	m.reporter.UploadStop()
	m.mu.Unlock()
}

func (m *UploadManager) reportStatusLocked() {
	if m.queue != nil {
		m.queue.QueueStatus(m.statusLocked())
	}
}

func (m *UploadManager) reportUpdates(items []QueueItem) {
	if m.queue == nil {
		return
	}
	if len(items) > 0 {
		m.queue.QueueItemsUpdated(items)
	}
}

// IsSkippedPreflightWarning reports whether a preflight warning code means
// its files were skipped rather than uploaded.
func IsSkippedPreflightWarning(code string) bool {
	return code == "incomplete-live-photo-skipped" || code == "ambiguous-filename-stem" || code == "ambiguous-identifier"
}

func queueKey(path string) string {
	key := canonicalUploadPath(path)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

// handleAlbumCreation adds a batch's uploads to its album, or to one album per
// source folder in auto mode. Albums created earlier in the session are reused.
func (m *UploadManager) handleAlbumCreation(ctx context.Context, opts UploadOptions, uploads []albumUpload) {
	if ctx.Err() != nil {
		m.logger.Info("album creation cancelled")
		return
	}
	m.albumMu.Lock()
	defer m.albumMu.Unlock()

	api, err := m.newAlbumAPI(opts.Api)
	if err != nil {
		m.reporter.AlbumError(AlbumError{AlbumName: opts.AlbumName, Error: fmt.Sprintf("failed to initialize API: %v", err)})
		return
	}
	albumManager := NewAlbumManager(api, m.reporter, m.logger, ctx.Done())

	type target struct {
		cacheKey, name string
		mediaKeys      []string
	}
	var targets []target
	if opts.AlbumAutoMode {
		for _, group := range groupUploadsByFolder(uploads) {
			targets = append(targets, target{"dir:" + group.dir, group.name, group.mediaKeys})
		}
	} else if opts.AlbumName != "" {
		mediaKeys := make([]string, 0, len(uploads))
		for _, upload := range uploads {
			mediaKeys = append(mediaKeys, upload.mediaKey)
		}
		targets = append(targets, target{"name:" + opts.AlbumName, opts.AlbumName, mediaKeys})
	}
	// Announce every album first: looking up existing albums can take a
	// while, and the app should show what is waiting rather than nothing.
	for _, t := range targets {
		m.reporter.AlbumProgress(AlbumStatus{AlbumName: albumDisplayName(t.name), TotalItems: len(t.mediaKeys)})
	}
	existing := m.existingAlbums(ctx, opts.Api)
	for _, t := range targets {
		m.addToSessionAlbum(albumManager, existing, t.cacheKey, t.name, t.mediaKeys)
	}
}

// albumDisplayName is the name album events use for nameOrKey.
func albumDisplayName(nameOrKey string) string {
	if IsAlbumKey(nameOrKey) {
		return albumKeyDisplayName(nameOrKey)
	}
	return nameOrKey
}

// addToSessionAlbum adds media to an album named nameOrKey without creating
// a duplicate: it reuses an album used earlier this session (cached under
// cacheKey), else an existing album with exactly that title, and creates one
// only when neither exists. existing may be nil when albums could not be
// listed.
func (m *UploadManager) addToSessionAlbum(albumManager *AlbumManager, existing *albumList, cacheKey, nameOrKey string, mediaKeys []string) {
	var albumKeys []string
	var err error
	cached, isCached := m.albumKeys[cacheKey]
	match, matched := pickExistingAlbum(existing, nameOrKey, len(mediaKeys))
	switch {
	case IsAlbumKey(nameOrKey):
		albumKeys, err = albumManager.AddToAlbum(mediaKeys, nameOrKey)
	case isCached:
		albumKeys, err = albumManager.addToExistingAlbum(mediaKeys, cached, nameOrKey)
	case matched:
		if same := len(existing.byTitle[nameOrKey]); same > 1 {
			m.reporter.Warning(PreflightWarning{
				Code:    "album-name-repeated",
				Message: fmt.Sprintf("%d albums are named %q; added to the one with the most items", same, nameOrKey),
			})
		}
		albumKeys, err = albumManager.addToExistingAlbum(mediaKeys, match.MediaKey, nameOrKey)
		if err == nil {
			m.albumKeys[cacheKey] = match.MediaKey
		}
	default:
		albumKeys, err = albumManager.AddToAlbum(mediaKeys, nameOrKey)
		if err == nil && len(albumKeys) == 1 {
			m.albumKeys[cacheKey] = albumKeys[0]
			if existing != nil {
				// Later drops with this name, even from another folder, reuse it.
				existing.byTitle[nameOrKey] = append(existing.byTitle[nameOrKey], LibraryAlbum{
					MediaKey: albumKeys[0], Title: nameOrKey, ItemCount: len(mediaKeys),
				})
			}
		}
	}
	if err == nil && (isCached || matched) {
		m.albumAdded(existing, nameOrKey, albumKeys, len(mediaKeys))
	}
	if err != nil {
		m.logger.Error("album update failed", "album", nameOrKey, "error", err)
		m.reporter.AlbumError(AlbumError{AlbumName: albumDisplayName(nameOrKey), Error: err.Error()})
		return
	}
	m.logger.Info("album updated", "album", nameOrKey, "items", len(mediaKeys), "albumKeys", albumKeys)
}

// pickExistingAlbum returns the existing album titled name to add count items
// to: with several, the one with the most items, skipping any the items would
// push past the album limit.
func pickExistingAlbum(existing *albumList, name string, count int) (LibraryAlbum, bool) {
	if existing == nil || IsAlbumKey(name) {
		return LibraryAlbum{}, false
	}
	var best LibraryAlbum
	found := false
	for _, album := range existing.byTitle[name] {
		if album.ItemCount+count > AlbumLimit {
			continue
		}
		if !found || album.ItemCount > best.ItemCount {
			best, found = album, true
		}
	}
	return best, found
}

// albumAdded keeps the cached item count of a reused album current.
func (m *UploadManager) albumAdded(existing *albumList, name string, albumKeys []string, count int) {
	if existing == nil || len(albumKeys) != 1 {
		return
	}
	for i, album := range existing.byTitle[name] {
		if album.MediaKey == albumKeys[0] {
			existing.byTitle[name][i].ItemCount += count
		}
	}
}

type folderUploads struct {
	dir       string
	name      string
	mediaKeys []string
}

// groupUploadsByFolder groups uploads by parent folder. Folders that share a
// name are told apart by their parent's name, e.g. "Jan (2023)".
func groupUploadsByFolder(uploads []albumUpload) []folderUploads {
	byDir := make(map[string]*folderUploads)
	var order []string
	for _, upload := range uploads {
		dir := filepath.Dir(upload.path)
		group := byDir[dir]
		if group == nil {
			name := filepath.Base(dir)
			if name == "" || name == "." || name == string(filepath.Separator) {
				name = "Uploads"
			}
			group = &folderUploads{dir: dir, name: name}
			byDir[dir] = group
			order = append(order, dir)
		}
		group.mediaKeys = append(group.mediaKeys, upload.mediaKey)
	}
	slices.Sort(order)

	nameCount := make(map[string]int)
	for _, dir := range order {
		nameCount[byDir[dir].name]++
	}
	groups := make([]folderUploads, 0, len(order))
	used := make(map[string]int)
	for _, dir := range order {
		group := *byDir[dir]
		if nameCount[group.name] > 1 {
			if parent := filepath.Base(filepath.Dir(dir)); parent != "" && parent != "." && parent != string(filepath.Separator) {
				group.name = fmt.Sprintf("%s (%s)", group.name, parent)
			}
		}
		used[group.name]++
		if used[group.name] > 1 {
			group.name = fmt.Sprintf("%s %d", group.name, used[group.name])
		}
		groups = append(groups, group)
	}
	return groups
}
