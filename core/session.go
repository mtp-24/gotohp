package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// The queue is saved to disk as it changes, so quitting the app does not
// lose it: the next launch restores it paused, finished files and all, and
// files that already uploaded are not checked again.

const sessionVersion = 1

// sessionSaveInterval bounds how often the saved session is rewritten while
// the queue changes. Files that finished in the last interval before a
// crash are checked again after restoring, which finds them in the library.
const sessionSaveInterval = 5 * time.Second

type savedSession struct {
	Version   int               `json:"version"`
	SavedAt   time.Time         `json:"savedAt"`
	Batches   []savedBatch      `json:"batches"`
	Items     []savedItem       `json:"items"`
	AlbumKeys map[string]string `json:"albumKeys,omitempty"`
}

type savedBatch struct {
	ID      int64         `json:"id"`
	Options UploadOptions `json:"options"`
	// AlbumUploads are files uploaded but not yet added to their album.
	AlbumUploads []savedAlbumUpload `json:"albumUploads,omitempty"`
}

type savedAlbumUpload struct {
	Path     string `json:"path"`
	MediaKey string `json:"mediaKey"`
}

type savedItem struct {
	Item QueueItem      `json:"item"`
	Work UploadWorkItem `json:"work"`
	Key  string         `json:"key"`
}

// SessionSummary describes a restored session.
type SessionSummary struct {
	Restored bool `json:"Restored"`
	// Finished counts items that uploaded, were already in the library, or
	// were skipped; Remaining counts items still to upload.
	Finished  int `json:"Finished"`
	Remaining int `json:"Remaining"`
	Failed    int `json:"Failed"`
	// AlbumFiles counts uploaded files still to be added to their album.
	AlbumFiles int `json:"AlbumFiles"`
}

// SessionPath is where the app saves its queue, next to its config.
func SessionPath() string {
	ensureConfigLoaded()
	return filepath.Join(filepath.Dir(ConfigPath), "queue-session.json")
}

// EnableSession saves the queue to path whenever it changes, at most every
// few seconds. Call SaveSession before the app exits to save the latest
// state.
func (m *UploadManager) EnableSession(path string) {
	m.mu.Lock()
	m.sessionPath = path
	m.mu.Unlock()
	go func() {
		ticker := time.NewTicker(sessionSaveInterval)
		defer ticker.Stop()
		for range ticker.C {
			if m.sessionDirty.Swap(false) {
				if err := m.SaveSession(); err != nil {
					m.logger.Error("save queue session", "error", err)
				}
			}
		}
	}()
}

// markChanged schedules a session save.
func (m *UploadManager) markChanged() {
	m.sessionDirty.Store(true)
}

// SaveSession writes the queue to the session file now. With nothing in the
// queue, it removes the file.
func (m *UploadManager) SaveSession() error {
	m.sessionSaveMu.Lock()
	defer m.sessionSaveMu.Unlock()
	m.mu.Lock()
	path := m.sessionPath
	if path == "" {
		m.mu.Unlock()
		return nil
	}
	session := m.snapshotLocked()
	m.mu.Unlock()
	session.AlbumKeys = m.albumKeysSnapshot()

	if len(session.Items) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Written aside and renamed, so a crash mid-write keeps the last save.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *UploadManager) snapshotLocked() savedSession {
	session := savedSession{Version: sessionVersion, SavedAt: time.Now()}
	batches := make(map[*uploadBatch]bool)
	for _, entry := range m.items {
		item := entry.item
		if item.State == QueueItemActive {
			// Interrupted mid-upload: queued again, and found in the library
			// if it got there.
			item.State = QueueItemQueued
			item.Message = ""
		}
		session.Items = append(session.Items, savedItem{Item: item, Work: entry.work, Key: entry.key})
		batches[entry.batch] = true
	}
	slices.SortFunc(session.Items, func(a, b savedItem) int { return int(a.Item.ID - b.Item.ID) })
	for batch := range batches {
		saved := savedBatch{ID: batch.id, Options: batch.opts}
		for _, upload := range batch.pendingAlbumUploads() {
			saved.AlbumUploads = append(saved.AlbumUploads, savedAlbumUpload{Path: upload.path, MediaKey: upload.mediaKey})
		}
		session.Batches = append(session.Batches, saved)
	}
	slices.SortFunc(session.Batches, func(a, b savedBatch) int { return int(a.ID - b.ID) })
	return session
}

// pendingAlbumUploads returns the batch's uploads not yet in their album,
// including those of album passes still running.
func (b *uploadBatch) pendingAlbumUploads() []albumUpload {
	var uploads []albumUpload
	for _, pass := range b.albumInFlight {
		uploads = append(uploads, pass...)
	}
	return append(uploads, b.uploads...)
}

// RestoreSession loads the queue saved by an earlier run of the app, once,
// if the manager has nothing queued yet. Unfinished files are restored
// paused, so the user chooses when to resume. Sessions with nothing left to
// do are discarded.
func (m *UploadManager) RestoreSession() (SessionSummary, error) {
	m.mu.Lock()
	path := m.sessionPath
	m.mu.Unlock()
	if path == "" {
		return SessionSummary{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return SessionSummary{}, nil
	}
	if err != nil {
		return SessionSummary{}, err
	}
	var saved savedSession
	if err := json.Unmarshal(data, &saved); err != nil {
		return SessionSummary{}, fmt.Errorf("read saved queue: %w", err)
	}
	if saved.Version != sessionVersion {
		return SessionSummary{}, fmt.Errorf("saved queue has unknown version %d", saved.Version)
	}

	var summary SessionSummary
	for _, item := range saved.Items {
		switch item.Item.State {
		case QueueItemQueued, QueueItemActive:
			summary.Remaining++
		case QueueItemFailed:
			summary.Failed++
		default:
			summary.Finished++
		}
	}
	for _, batch := range saved.Batches {
		summary.AlbumFiles += len(batch.AlbumUploads)
	}
	if summary.Remaining == 0 && summary.Failed == 0 && summary.AlbumFiles == 0 {
		// Everything finished last time; start with an empty list.
		_ = os.Remove(path)
		return SessionSummary{}, nil
	}

	m.mu.Lock()
	if m.running || len(m.items) > 0 {
		// Files were dropped before the saved queue was restored.
		m.mu.Unlock()
		return SessionSummary{}, nil
	}
	run := summary.Remaining > 0 || summary.AlbumFiles > 0
	if run {
		m.startRunLocked()
		m.paused = true
	}
	batchCtx := m.runCtx
	if !run {
		// Nothing runs until an item is retried, which gives the batch the
		// run's context then.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		batchCtx = ctx
	}
	batches := make(map[int64]*uploadBatch, len(saved.Batches))
	for _, b := range saved.Batches {
		batch := &uploadBatch{id: b.ID, opts: b.Options, ctx: batchCtx}
		for _, upload := range b.AlbumUploads {
			batch.uploads = append(batch.uploads, albumUpload{path: upload.Path, mediaKey: upload.MediaKey})
		}
		batches[b.ID] = batch
		m.nextBatchID = max(m.nextBatchID, b.ID)
	}
	var items []QueueItem
	var bytes int64
	for _, saved := range saved.Items {
		batch := batches[saved.Item.BatchID]
		if batch == nil {
			continue
		}
		entry := &queueEntry{item: saved.Item, work: saved.Work, batch: batch, key: saved.Key}
		if entry.item.State == QueueItemActive {
			entry.item.State = QueueItemQueued
		}
		if entry.item.State == QueueItemQueued {
			entry.item.Message = ""
			if entry.work.Kind == "" {
				entry.item.State = QueueItemFailed
				entry.item.Message = "Could not restore this item; drop the file again"
			} else if _, dup := m.inQueue[entry.key]; !dup {
				m.inQueue[entry.key] = entry
				m.pending = append(m.pending, entry)
				batch.remaining++
				entry.bytesCounted = true
				bytes += entry.item.Size
			} else {
				entry.item.State = QueueItemCancelled
				entry.item.Message = "Already in the queue"
			}
		}
		m.items[entry.item.ID] = entry
		for _, path := range entry.item.Paths {
			m.itemPaths[path]++
		}
		m.nextItemID = max(m.nextItemID, entry.item.ID)
		if run {
			m.total++
		}
		items = append(items, entry.item)
	}
	slices.SortFunc(m.pending, func(a, b *queueEntry) int { return int(a.item.ID - b.item.ID) })
	total := m.total
	m.reportStatusLocked()
	m.mu.Unlock()

	m.setAlbumKeys(saved.AlbumKeys)
	if m.queue != nil && len(items) > 0 {
		m.queue.QueueItems(items)
	}
	if run {
		m.reporter.UploadStart(UploadBatchStart{Total: total})
		if bytes != 0 {
			m.reporter.TotalBytesDelta(bytes)
		}
	}
	for _, batch := range batches {
		// Albums are looked up again, so a restored batch adds to the album
		// it was adding to instead of creating another one.
		forAlbums := batch.opts.needsAlbumList() && (batch.remaining > 0 || len(batch.uploads) > 0)
		large := !batch.opts.ForceUpload && batch.remaining >= libraryIndexMinItems
		if run && (forAlbums || large) {
			m.setBatchLibrary(batch, m.libraryListing(batch.opts.Api))
		}
	}
	m.mu.Lock()
	var albumPasses []*uploadBatch
	for _, batch := range batches {
		if batch.remaining > 0 {
			m.spawnWorkersLocked(batch.opts.Threads)
		} else if b := m.batchDoneLocked(batch); b != nil && run {
			albumPasses = append(albumPasses, b)
		}
	}
	m.cond.Broadcast()
	m.mu.Unlock()
	for _, batch := range albumPasses {
		m.startAlbumPass(batch)
	}
	if run {
		// Nothing else may hold the run open while it waits paused with no
		// worker; this ends it once album passes and workers are done.
		m.mu.Lock()
		m.tasks++
		m.mu.Unlock()
		m.taskDone()
	}
	summary.Restored = true
	return summary, nil
}
