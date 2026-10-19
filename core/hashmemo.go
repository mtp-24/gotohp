package core

import (
	"context"
	"sync"
)

// hashMemo remembers, for one upload session, which media item each file
// fingerprint ended up as, and which fingerprints are being uploaded right
// now. Takeout exports put the same photo in several folders (an album folder
// and its year folder), so one drop can hold identical files; without this,
// both are checked before either finishes, and both are uploaded.
type hashMemo struct {
	mu   sync.Mutex
	keys map[string]string
	busy map[string]chan struct{}
}

func newHashMemo() *hashMemo {
	return &hashMemo{keys: make(map[string]string), busy: make(map[string]chan struct{})}
}

type hashMemoKey struct{}

func withHashMemo(ctx context.Context, memo *hashMemo) context.Context {
	return context.WithValue(ctx, hashMemoKey{}, memo)
}

func hashMemoFrom(ctx context.Context) *hashMemo {
	memo, _ := ctx.Value(hashMemoKey{}).(*hashMemo)
	return memo
}

// begin returns the media key already known for hash, or claims hash for the
// caller, who must then call end. While another caller holds the claim, it
// waits for that one to finish.
func (h *hashMemo) begin(ctx context.Context, hash string) (mediaKey string, claimed bool, err error) {
	for {
		h.mu.Lock()
		if key, ok := h.keys[hash]; ok {
			h.mu.Unlock()
			return key, false, nil
		}
		wait, busy := h.busy[hash]
		if !busy {
			h.busy[hash] = make(chan struct{})
			h.mu.Unlock()
			return "", true, nil
		}
		h.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	}
}

// end releases a claim, recording mediaKey when the file is now in the
// library. With no key, a waiting caller takes over the claim.
func (h *hashMemo) end(hash, mediaKey string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if mediaKey != "" {
		h.keys[hash] = mediaKey
	}
	if wait, ok := h.busy[hash]; ok {
		close(wait)
		delete(h.busy, hash)
	}
}
