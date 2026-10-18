// Package thumbnail serves small previews of local files for the upload
// queue, made by the operating system's own thumbnailer.
package thumbnail

import (
	"container/list"
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

var errNoThumbnail = errors.New("no thumbnail")

const (
	// Size is the thumbnail edge in points.
	Size = 32
	// maxConcurrent bounds thumbnails made at once, so a fast scroll cannot
	// compete with uploads for CPU and disk.
	maxConcurrent = 3
	// cacheBytes caps the memory the cache may hold; each thumbnail is a
	// few kilobytes.
	cacheBytes = 8 << 20
	timeout    = 10 * time.Second
)

// Server answers GET <prefix>?path=<file> with a JPEG thumbnail, for paths
// the allowed func accepts only.
type Server struct {
	allowed  func(path string) bool
	generate func(path string, size int, timeout time.Duration) ([]byte, error)
	slots    chan struct{}

	mu        sync.Mutex
	cache     map[string]*list.Element
	order     *list.List // most recently used first
	usedBytes int
}

type cacheEntry struct {
	key  string
	data []byte // nil records that no thumbnail could be made
}

// NewServer returns a Server that serves thumbnails of paths allowed accepts.
func NewServer(allowed func(path string) bool) *Server {
	return &Server{
		allowed:  allowed,
		generate: generate,
		slots:    make(chan struct{}, maxConcurrent),
		cache:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" || !s.allowed(path) {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	// The key changes when the file does, so an edited file gets a new preview.
	key := path + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + "\x00" + strconv.FormatInt(info.Size(), 10)
	data, found := s.lookup(key)
	if !found {
		select {
		case s.slots <- struct{}{}:
		case <-r.Context().Done():
			// The row scrolled away before a slot was free.
			return
		}
		if r.Context().Err() != nil {
			<-s.slots
			return
		}
		data, _ = s.generate(path, Size, timeout)
		<-s.slots
		s.store(key, data)
	}
	if data == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}

func (s *Server) lookup(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.cache[key]
	if !ok {
		return nil, false
	}
	s.order.MoveToFront(element)
	return element.Value.(*cacheEntry).data, true
}

func (s *Server) store(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cache[key]; ok {
		return
	}
	s.cache[key] = s.order.PushFront(&cacheEntry{key: key, data: data})
	s.usedBytes += len(data) + len(key)
	for s.usedBytes > cacheBytes && s.order.Len() > 1 {
		oldest := s.order.Back()
		entry := oldest.Value.(*cacheEntry)
		s.order.Remove(oldest)
		delete(s.cache, entry.key)
		s.usedBytes -= len(entry.data) + len(entry.key)
	}
}
