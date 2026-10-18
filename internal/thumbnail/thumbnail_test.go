package thumbnail

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writePhoto(t *testing.T, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	path := filepath.Join(t.TempDir(), "photo.jpg")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := jpeg.Encode(file, img, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

func get(server http.Handler, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/thumbnail?path="+url.QueryEscape(path), nil))
	return recorder
}

func TestServerOnlyServesAllowedPaths(t *testing.T) {
	path := writePhoto(t, 8, 8)
	server := NewServer(func(string) bool { return false })
	server.generate = func(string, int, time.Duration) ([]byte, error) { return []byte("jpeg"), nil }
	if code := get(server, path).Code; code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a path outside the queue", code)
	}
}

func TestServerCachesAndBoundsWork(t *testing.T) {
	var calls, running, peak atomic.Int32
	server := NewServer(func(string) bool { return true })
	server.generate = func(string, int, time.Duration) ([]byte, error) {
		calls.Add(1)
		now := running.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return []byte("jpeg"), nil
	}
	paths := make([]string, 12)
	for i := range paths {
		paths[i] = writePhoto(t, 4, 4)
	}
	var wg sync.WaitGroup
	for _, path := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code := get(server, path).Code; code != http.StatusOK {
				t.Errorf("status = %d", code)
			}
		}()
	}
	wg.Wait()
	if got := peak.Load(); got > maxConcurrent {
		t.Fatalf("%d thumbnails made at once, want at most %d", got, maxConcurrent)
	}
	before := calls.Load()
	get(server, paths[0])
	if calls.Load() != before {
		t.Fatal("cached thumbnail was made again")
	}
}

func TestServerEvictsBeyondCacheLimit(t *testing.T) {
	server := NewServer(func(string) bool { return true })
	big := bytes.Repeat([]byte{1}, cacheBytes/4)
	for i := range 10 {
		server.store(string(rune('a'+i)), big)
	}
	if server.usedBytes > cacheBytes {
		t.Fatalf("cache holds %d bytes, limit %d", server.usedBytes, cacheBytes)
	}
}

func TestGenerateMakesASmallJPEG(t *testing.T) {
	if !Supported {
		t.Skip("no thumbnailer on this platform")
	}
	path := writePhoto(t, 3000, 2000)
	start := time.Now()
	data, err := generate(path, Size, timeout)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" {
		t.Fatalf("not a JPEG: %v (%s)", err, format)
	}
	if config.Width > Size*2 || config.Height > Size*2 {
		t.Fatalf("thumbnail %dx%d is larger than %d px", config.Width, config.Height, Size*2)
	}
	t.Logf("%d bytes, %dx%d, in %s", len(data), config.Width, config.Height, time.Since(start))
}
