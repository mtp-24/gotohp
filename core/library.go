package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// Library sync RPC, ported from gpmc
// (github.com/xob0t/google_photos_mobile_client, MIT license).
const (
	librarySyncEndpoint = "https://photosdata-pa.googleapis.com/6439526531001121323/18047484249733410717"
)

// LibraryItem is one media item in the account, with the fields the cleanup
// app needs.
type LibraryItem struct {
	MediaKey string `json:"mediaKey"`
	// DedupKey identifies the item for trashing: its SHA-1 in URL-safe
	// base64 without padding.
	DedupKey string `json:"dedupKey"`
	FileName string `json:"fileName"`
	// SizeBytes is the stored size, 0 when unknown.
	SizeBytes int64 `json:"sizeBytes"`
	// TakenAt is when the photo or video was taken, Unix milliseconds.
	TakenAt int64 `json:"takenAt"`
	// AddedAt is when the item was created in this account, Unix
	// milliseconds. Items moved in from another account carry the move time.
	AddedAt  int64 `json:"addedAt"`
	IsLocked bool  `json:"isLocked"`
	IsVideo  bool  `json:"isVideo"`
	// InTrash is set for items already in the trash.
	InTrash bool `json:"inTrash"`
}

// LibraryAlbum is an album (a collection) in the account.
type LibraryAlbum struct {
	// MediaKey is the album key that adding media to the album takes.
	MediaKey  string `json:"mediaKey"`
	Title     string `json:"title"`
	ItemCount int    `json:"itemCount"`
}

// LibraryPage is one response of a library sync.
type LibraryPage struct {
	Items       []LibraryItem
	Albums      []LibraryAlbum
	ResumeToken string
	SyncToken   string
}

// ScanLibrary lists every item in the account. onPage, when set, is called
// with the running item count after each page.
func (a *Api) ScanLibrary(ctx context.Context, onPage func(items int)) ([]LibraryItem, error) {
	items, _, err := a.syncLibrary(ctx, onPage)
	return items, err
}

// ListAlbums lists the albums in the account. It reads the whole library
// sync, which also carries the albums.
func (a *Api) ListAlbums(ctx context.Context) ([]LibraryAlbum, error) {
	_, albums, err := a.syncLibrary(ctx, nil)
	return albums, err
}

// ListLibrary lists the account's items and albums in one pass, and the
// number of requests it took.
func (a *Api) ListLibrary(ctx context.Context) ([]LibraryItem, []LibraryAlbum, int, error) {
	pages := 0
	items, albums, err := a.syncLibrary(ctx, func(int) { pages++ })
	return items, albums, pages, err
}

func (a *Api) syncLibrary(ctx context.Context, onPage func(items int)) ([]LibraryItem, []LibraryAlbum, error) {
	page, err := a.libraryRequest(ctx, libraryStateRequest(""))
	if err != nil {
		return nil, nil, fmt.Errorf("list library: %w", err)
	}
	items, albums := page.Items, page.Albums
	if onPage != nil {
		onPage(len(items))
	}
	seen := map[string]bool{}
	for page.ResumeToken != "" {
		if seen[page.ResumeToken] {
			return nil, nil, fmt.Errorf("list library: the server repeated a page")
		}
		seen[page.ResumeToken] = true
		page, err = a.libraryRequest(ctx, libraryPageInitRequest(page.ResumeToken))
		if err != nil {
			return nil, nil, fmt.Errorf("list library page %d: %w", len(seen)+1, err)
		}
		items = append(items, page.Items...)
		albums = append(albums, page.Albums...)
		if onPage != nil {
			onPage(len(items))
		}
	}
	return items, albums, nil
}

func (a *Api) libraryRequest(ctx context.Context, body pbm) (LibraryPage, error) {
	serialized := body.marshal()
	headers := map[string]string{
		"accept-encoding":          "gzip",
		"Accept-Language":          a.language,
		"content-type":             "application/x-protobuf",
		"User-Agent":               a.userAgent,
		"x-goog-ext-173412678-bin": "CgcIAhClARgC",
		"x-goog-ext-174067345-bin": "CgIIAg==",
	}
	resp, err := a.send(func() (*http.Request, error) {
		req, err := newPostRequest(librarySyncEndpoint, serialized, headers)
		if err != nil {
			return nil, err
		}
		return req.WithContext(ctx), nil
	})
	if err != nil {
		return LibraryPage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return LibraryPage{}, newAPIError(resp)
	}
	data, err := ReadResponseBody(resp)
	if err != nil {
		return LibraryPage{}, fmt.Errorf("read library response: %w", err)
	}
	return parseLibraryPage(data)
}

// parseLibraryPage reads a sync response, following gpmc's
// db_update_parser: tokens at 1.1 and 1.6, items at 1.2.
func parseLibraryPage(data []byte) (LibraryPage, error) {
	response, err := decodePB(data)
	if err != nil {
		return LibraryPage{}, fmt.Errorf("decode library response: %w", err)
	}

	root := response.msg(1)
	if root == nil {
		return LibraryPage{}, nil
	}
	page := LibraryPage{ResumeToken: root.str(1), SyncToken: root.str(6)}
	for _, raw := range root.all(2) {
		if item, ok := parseLibraryItem(raw); ok {
			page.Items = append(page.Items, item)
		}
	}
	// Collections sit at 1.3: key at 1, title at 2.5, item count at 2.7, as
	// in gpmc's collection parser.
	for _, raw := range root.all(3) {
		if key := raw.str(1); key != "" {
			page.Albums = append(page.Albums, LibraryAlbum{
				MediaKey:  key,
				Title:     raw.str(2, 5),
				ItemCount: int(raw.uint(2, 7)),
			})
		}
	}
	return page, nil
}

func parseLibraryItem(d pbDecoded) (LibraryItem, bool) {
	mediaKey := d.str(1)
	if mediaKey == "" {
		return LibraryItem{}, false
	}
	d2 := d.msg(2)
	item := LibraryItem{MediaKey: mediaKey}
	if d2 == nil {
		return item, true
	}
	item.FileName = d2.str(4)
	item.SizeBytes = int64(d2.uint(10))
	item.TakenAt = epochMillis(d2.uint(7))
	item.AddedAt = epochMillis(d2.uint(9))
	item.IsLocked = d2.uint(39, 1) == 1
	item.InTrash = d2.uint(16, 3) != 0
	item.IsVideo = d.msg(5, 3) != nil
	// The dedup key is the first field of 2.21 numbered 1x, else the SHA-1 at
	// 2.13.1.
	if container := d2.msg(21); container != nil {
		for num, nodes := range container {
			if strings.HasPrefix(fmt.Sprint(num), "1") && len(nodes) > 0 && nodes[0].isRaw {
				item.DedupKey = string(nodes[0].raw)
				break
			}
		}
	}
	if item.DedupKey == "" {
		if hash := d2.bytes(13, 1); len(hash) > 0 {
			item.DedupKey = strings.TrimRight(base64.URLEncoding.EncodeToString(hash), "=")
		}
	}
	return item, true
}

// epochMillis normalizes a Unix timestamp in seconds, milliseconds or
// microseconds to milliseconds; the sync response does not say which.
func epochMillis(v uint64) int64 {
	switch {
	case v == 0:
		return 0
	case v >= 1e15:
		return int64(v / 1000)
	case v >= 1e12:
		return int64(v)
	default:
		return int64(v * 1000)
	}
}
