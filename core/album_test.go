package core

import (
	"log/slog"
	"strings"
	"testing"
)

func TestAlbumKeyDisplayName(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{key: "AF1Qipx", want: "Album (AF1Qipx)"},
		{key: "AF1QipABCD", want: "Album (AF1QipABCD)"},
		{key: "AF1QipABCDEFGH", want: "Album (AF1QipABCD...)"},
	}
	for _, tt := range tests {
		if got := albumKeyDisplayName(tt.key); got != tt.want {
			t.Errorf("albumKeyDisplayName(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

// A short value that still passes IsAlbumKey used to panic while building the
// progress label, crashing the app before any request was made.
func TestAddToAlbumShortAlbumKeyDoesNotPanic(t *testing.T) {
	cancel := make(chan struct{})
	close(cancel)
	manager := NewAlbumManager(nil, NopReporter{}, slog.New(slog.DiscardHandler), cancel)

	_, err := manager.AddToAlbum([]string{"media-key"}, "AF1Qipx")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("AddToAlbum error = %v, want cancellation error", err)
	}
}
