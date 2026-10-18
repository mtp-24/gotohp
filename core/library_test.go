package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func libraryItemPB(mediaKey, fileName string, addedAtMs int, locked bool, hash []byte) pbm {
	d2 := pbm{{4, fileName}, {7, addedAtMs - 1000}, {9, addedAtMs}, {10, 1234}, {13, pbm{{1, hash}}}}
	if locked {
		d2 = append(d2, pbf{39, pbm{{1, 1}}})
	}
	return pbm{{1, mediaKey}, {2, d2}, {5, pbm{{1, 1}, {2, pbm{}}}}}
}

func TestParseLibraryPage(t *testing.T) {
	hash := bytes.Repeat([]byte{0xfb}, 20)
	response := pbm{{1, pbm{
		{1, "resume-1"},
		{2, libraryItemPB("AF1Qip-a", "IMG_1.HEIC", 1760000000000, true, hash)},
		{2, libraryItemPB("AF1Qip-b", "IMG_2.HEIC", 1760000001000, false, hash)},
		{6, "sync-1"},
	}}}.marshal()

	page, err := parseLibraryPage(response)
	if err != nil {
		t.Fatal(err)
	}
	if page.ResumeToken != "resume-1" || page.SyncToken != "sync-1" || len(page.Items) != 2 {
		t.Fatalf("page = %+v", page)
	}
	a := page.Items[0]
	if a.MediaKey != "AF1Qip-a" || a.FileName != "IMG_1.HEIC" || !a.IsLocked || a.AddedAt != 1760000000000 ||
		a.SizeBytes != 1234 || a.DedupKey != strings.TrimRight(base64.URLEncoding.EncodeToString(hash), "=") || a.IsVideo {
		t.Fatalf("item = %+v", a)
	}
	if page.Items[1].IsLocked {
		t.Fatal("second item should not be locked")
	}
}

func TestEpochMillis(t *testing.T) {
	for in, want := range map[uint64]int64{0: 0, 1760000000: 1760000000000, 1760000000123: 1760000000123, 1760000000123456: 1760000000123} {
		if got := epochMillis(in); got != want {
			t.Errorf("epochMillis(%d) = %d, want %d", in, got, want)
		}
	}
}

// routeTransport answers requests by URL and records what was sent.
type routeTransport struct {
	responses map[string][][]byte
	bodies    map[string][][]byte
}

func (r *routeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	r.bodies[req.URL.String()] = append(r.bodies[req.URL.String()], body)
	queue := r.responses[req.URL.String()]
	var out []byte
	if len(queue) > 0 {
		out, r.responses[req.URL.String()] = queue[0], queue[1:]
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(out))}, nil
}

func testLibraryApi(transport http.RoundTripper) *Api {
	return &Api{
		client:            &http.Client{Transport: transport},
		authResponseCache: map[string]string{"Expiry": "9999999999", "Auth": "token"},
		clientVersionCode: 49029607,
		androidAPIVersion: 28,
	}
}

func TestScanLibraryFollowsResumeTokens(t *testing.T) {
	hash := bytes.Repeat([]byte{1}, 20)
	transport := &routeTransport{bodies: map[string][][]byte{}, responses: map[string][][]byte{
		librarySyncEndpoint: {
			pbm{{1, pbm{{1, "page-2"}, {2, libraryItemPB("k1", "a.jpg", 1760000000000, true, hash)}}}}.marshal(),
			pbm{{1, pbm{{2, libraryItemPB("k2", "b.jpg", 1760000000000, true, hash)}}}}.marshal(),
		},
	}}
	var counts []int
	items, err := testLibraryApi(transport).ScanLibrary(context.Background(), func(n int) { counts = append(counts, n) })
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !slices.Equal(counts, []int{1, 2}) {
		t.Fatalf("items = %d, counts = %v", len(items), counts)
	}
	// The second request carries the resume token as field 1.4.
	second, err := decodePB(transport.bodies[librarySyncEndpoint][1])
	if err != nil || second.str(1, 4) != "page-2" {
		t.Fatalf("second request resume token = %q, err = %v", second.str(1, 4), err)
	}
}
