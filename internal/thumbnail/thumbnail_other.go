//go:build !darwin || !cgo

package thumbnail

import "time"

// Supported reports whether this platform can make thumbnails. Elsewhere the
// queue shows a placeholder instead.
const Supported = false

func generate(string, int, time.Duration) ([]byte, error) {
	return nil, errNoThumbnail
}
