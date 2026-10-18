package core

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFilterGooglePhotosFilesSkipsAppleDoubleFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{
		"IMG_0716.JPG":   []byte("\xff\xd8\xff\xe0jpeg"),
		"._IMG_0716.JPG": append([]byte{0x00, 0x05, 0x16, 0x07}, []byte("appledouble")...),
		"._clip.mov":     append([]byte{0x00, 0x05, 0x16, 0x07}, []byte("appledouble")...),
		// A real photo whose name happens to start with "._" is still uploaded.
		"._real.jpg": []byte("\xff\xd8\xff\xe0jpeg"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, disableFilter := range []bool{false, true} {
		got, err := FilterGooglePhotosFiles([]string{dir}, UploadOptions{DisableUnsupportedFilesFilter: disableFilter})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{filepath.Join(dir, "IMG_0716.JPG"), filepath.Join(dir, "._real.jpg")}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("disableFilter=%v: selected files = %q, want %q", disableFilter, got, want)
		}
		if _, warnings := ClassifyUploadWork(got, LivePhotoClassificationOptions{Enabled: true}, nil); len(warnings) != 0 {
			t.Errorf("disableFilter=%v: unexpected Live Photo warnings: %+v", disableFilter, warnings)
		}
	}
}
