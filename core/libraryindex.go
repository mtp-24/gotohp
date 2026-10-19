package core

import (
	"context"
	"encoding/base64"
	"strings"
	"sync/atomic"
	"time"
)

// libraryIndexMinItems is the drop size from which the queue lists the
// library to check files against it. Listing reads the whole library, so
// for small drops one lookup per file is cheaper.
const libraryIndexMinItems = 500

// libraryListingMaxAge is how long a finished listing is reused for new
// drops before the library is listed again.
const libraryListingMaxAge = 10 * time.Minute

// libraryListing is one listing of an account's library: its albums, for
// reusing albums by name, and its items by fingerprint, so most files of a
// large drop that are already in the library are found without asking
// Google about each one.
type libraryListing struct {
	done chan struct{}
	// Set before done closes.
	finishedAt time.Time
	byTitle    map[string][]LibraryAlbum
	byHash     map[string]string
	items      int
	pages      int
	err        error

	// Files found in the listing, and files the listing did not have that
	// were looked up one by one.
	matched atomic.Int64
	online  atomic.Int64
}

func newLibraryListing() *libraryListing {
	return &libraryListing{done: make(chan struct{})}
}

// finish records the listing's result and releases waiters.
func (l *libraryListing) finish(items []LibraryItem, albums []LibraryAlbum, pages int, err error) {
	defer close(l.done)
	l.finishedAt = time.Now()
	l.pages = pages
	if err != nil {
		l.err = err
		return
	}
	l.items = len(items)
	l.byTitle = make(map[string][]LibraryAlbum)
	for _, album := range albums {
		l.byTitle[album.Title] = append(l.byTitle[album.Title], album)
	}
	l.byHash = make(map[string]string, len(items))
	for _, item := range items {
		// Items in the trash or the Locked Folder do not count as being in
		// the library; those files are looked up one by one instead.
		if item.InTrash || item.IsLocked || item.DedupKey == "" {
			continue
		}
		l.byHash[strings.TrimRight(item.DedupKey, "=")] = item.MediaKey
	}
}

func (l *libraryListing) ready() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// fresh reports whether a new drop can use this listing: it is still
// running, or finished well and recently.
func (l *libraryListing) fresh() bool {
	if !l.ready() {
		return true
	}
	return l.err == nil && time.Since(l.finishedAt) < libraryListingMaxAge
}

// find returns the media key of the library item with this SHA-1, or "" when
// the listing has none or is not ready.
func (l *libraryListing) find(sha1 []byte) string {
	if !l.ready() || l.err != nil {
		return ""
	}
	return l.byHash[strings.TrimRight(base64.URLEncoding.EncodeToString(sha1), "=")]
}

type libraryListingKey struct{}

func withLibraryListing(ctx context.Context, listing *libraryListing) context.Context {
	if listing == nil {
		return ctx
	}
	return context.WithValue(ctx, libraryListingKey{}, listing)
}

func libraryListingFrom(ctx context.Context) *libraryListing {
	listing, _ := ctx.Value(libraryListingKey{}).(*libraryListing)
	return listing
}

type remoteMediaFinder interface {
	FindRemoteMediaByHash(hash []byte) (string, error)
}

// findRemoteMedia returns the media key of the library item with this
// SHA-1, or "" when there is none. It checks the library listing attached to
// ctx first and asks Google only about files the listing does not have, so
// a listing that misses an item costs a lookup, never a duplicate upload.
func findRemoteMedia(ctx context.Context, api remoteMediaFinder, sha1 []byte) (string, error) {
	listing := libraryListingFrom(ctx)
	if listing != nil {
		if key := listing.find(sha1); key != "" {
			listing.matched.Add(1)
			return key, nil
		}
	}
	key, err := api.FindRemoteMediaByHash(sha1)
	if err == nil && listing != nil {
		listing.online.Add(1)
	}
	return key, err
}

// check describes the listing for the queue status.
func (l *libraryListing) check() LibraryCheck {
	check := LibraryCheck{Phase: "listing", Matched: l.matched.Load(), Lookups: l.online.Load()}
	if l.ready() {
		check.Phase, check.Items, check.Requests = "ready", l.items, l.pages
		if l.err != nil {
			check.Phase = "failed"
		}
	}
	return check
}
