package core

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSessionRestoresUnfinishedQueuePaused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue-session.json")
	files := writeMedia(t, t.TempDir(), "a.jpg", "b.jpg", "c.jpg")
	opts := testOpts(1)
	opts.AlbumName = "Trip"

	// The first app uploads a.jpg, is uploading b.jpg, and quits.
	first, firstRec, firstUp, _ := newTestQueue(t)
	first.sessionPath = path
	first.Upload(files, opts)
	firstUp.waitStarted(t, "a.jpg")
	firstUp.gate("a.jpg") <- nil
	firstUp.waitStarted(t, "b.jpg")
	if err := first.SaveSession(); err != nil {
		t.Fatal(err)
	}
	first.Cancel()
	firstRec.waitStop(t)

	m, rec, up, albums := newTestQueue(t)
	m.sessionPath = path
	summary, err := m.RestoreSession()
	if err != nil {
		t.Fatal(err)
	}
	if want := (SessionSummary{Restored: true, Finished: 1, Remaining: 2, AlbumFiles: 1}); summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	if !m.Status().Paused {
		t.Fatal("restored queue is not paused")
	}
	up.assertNothingStarts(t)
	if got := rec.itemByName("a.jpg"); got.State != QueueItemDone || got.MediaKey != "key-a.jpg" {
		t.Fatalf("a.jpg = %+v, want done with its key", got)
	}
	if got := rec.itemByName("b.jpg").State; got != QueueItemQueued {
		t.Fatalf("interrupted b.jpg state = %q, want queued", got)
	}

	m.SetPaused(false)
	up.waitStarted(t, "b.jpg")
	up.gate("b.jpg") <- nil
	up.waitStarted(t, "c.jpg")
	up.gate("c.jpg") <- nil
	rec.waitStop(t)

	if up.attempts["a.jpg"] != 0 {
		t.Fatal("a.jpg, finished before the restart, was checked again")
	}
	if got := albums.created["Trip"]; !slices.Equal(got, []string{"key-a.jpg", "key-b.jpg", "key-c.jpg"}) {
		t.Fatalf("album = %v, want every file, including the one finished before the restart", got)
	}
}

func TestSessionRestoreReusesAlbumFromBeforeRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue-session.json")
	dir := t.TempDir()
	opts := testOpts(1)
	opts.AlbumName = "Trip"

	first, firstRec, firstUp, _ := newTestQueue(t)
	first.sessionPath = path
	first.Upload(writeMedia(t, dir, "a.jpg"), opts)
	firstUp.waitStarted(t, "a.jpg")
	firstUp.gate("a.jpg") <- nil
	firstRec.waitStop(t) // a.jpg is in the new album "Trip"
	first.Upload(writeMedia(t, dir, "b.jpg"), opts)
	firstUp.waitStarted(t, "b.jpg")
	if err := first.SaveSession(); err != nil {
		t.Fatal(err)
	}
	first.Cancel()
	firstRec.waitStop(t)

	m, rec, up, albums := newTestQueue(t)
	m.sessionPath = path
	if _, err := m.RestoreSession(); err != nil {
		t.Fatal(err)
	}
	m.SetPaused(false)
	up.waitStarted(t, "b.jpg")
	up.gate("b.jpg") <- nil
	rec.waitStop(t)
	if len(albums.created) != 0 || !slices.Equal(albums.added["AF1Qip-Trip"], []string{"key-b.jpg"}) {
		t.Fatalf("created=%v added=%v, want b.jpg added to the album made before the restart", albums.created, albums.added)
	}
}

func TestSessionDiscardsFinishedQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue-session.json")
	first, firstRec, firstUp, _ := newTestQueue(t)
	first.sessionPath = path
	first.Upload(writeMedia(t, t.TempDir(), "a.jpg"), testOpts(1))
	firstUp.waitStarted(t, "a.jpg")
	firstUp.gate("a.jpg") <- nil
	firstRec.waitStop(t)
	if err := first.SaveSession(); err != nil {
		t.Fatal(err)
	}

	m, _, _, _ := newTestQueue(t)
	m.sessionPath = path
	summary, err := m.RestoreSession()
	if err != nil || summary.Restored {
		t.Fatalf("summary=%+v err=%v, want nothing restored", summary, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finished session file still there: %v", err)
	}
}

func TestSessionSaveRemovesFileWhenCleared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue-session.json")
	m, rec, up, _ := newTestQueue(t)
	m.sessionPath = path
	m.Upload(writeMedia(t, t.TempDir(), "a.jpg"), testOpts(1))
	up.waitStarted(t, "a.jpg")
	up.gate("a.jpg") <- errors.New("boom")
	rec.waitStop(t)
	if err := m.SaveSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session with a failed item not saved: %v", err)
	}
	m.ClearFinished()
	if err := m.SaveSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleared session file still there: %v", err)
	}
}
