package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/metazla/meta-core/internal/storage"
)

func newFileTuplesTestServer(t *testing.T) (*Server, *storage.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := storage.NewClient("")
	if err := c.Connect("redis://" + mr.Addr()); err != nil {
		t.Fatalf("connect to miniredis: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &Server{storage: c}, c
}

func getFileTuples(t *testing.T, s *Server, query string) (int, FileTuplesResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/files/tuples"+query, nil)
	rr := httptest.NewRecorder()
	s.handleGetFileTuples(rr, req)
	var resp FileTuplesResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	return rr.Code, resp
}

func mustMint(t *testing.T, c *storage.Client, path string, size, mtime int64) string {
	t.Helper()
	id, err := c.Mint(path, size, mtime)
	if err != nil {
		t.Fatalf("mint %s: %v", path, err)
	}
	return id
}

// Only records that are actually files count: a gateway record and a record
// that merely carries a path + size (no mtimeNano) are both left out.
func TestFileTuples_OnlyFileBackedRoots(t *testing.T) {
	s, c := newFileTuplesTestServer(t)
	a := mustMint(t, c, "/files/watch/a.mkv", 1000, 1)
	b := mustMint(t, c, "/files/watch/b.mkv", 2500, 2)
	if err := c.SetMetadataFlat("bagcsgatewayrecord", map[string]string{"title": "Some Release"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetMetadataFlat("01partialroot", map[string]string{"filePath": "/files/cache/x.mkv", "sizeByte": "999"}); err != nil {
		t.Fatal(err)
	}

	code, resp := getFileTuples(t, s, "")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if resp.Count != 2 || resp.TotalSize != 3500 {
		t.Fatalf("want count 2 / totalSize 3500, got %d / %d", resp.Count, resp.TotalSize)
	}
	if resp.Files == nil || len(*resp.Files) != 2 {
		t.Fatalf("want 2 rows, got %+v", resp.Files)
	}
	got := []string{(*resp.Files)[0].HashID, (*resp.Files)[1].HashID}
	want := []string{a, b}
	sort.Strings(got)
	sort.Strings(want)
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("want roots %v, got %v", want, got)
	}
}

func TestFileTuples_SummaryOmitsRows(t *testing.T) {
	s, c := newFileTuplesTestServer(t)
	mustMint(t, c, "/files/watch/a.mkv", 1000, 1)
	mustMint(t, c, "/files/watch/b.mkv", 2500, 2)

	code, resp := getFileTuples(t, s, "?summary=1")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if resp.Files != nil {
		t.Fatalf("summary must omit rows, got %d", len(*resp.Files))
	}
	if resp.Count != 2 || resp.TotalSize != 3500 {
		t.Fatalf("want count 2 / totalSize 3500, got %d / %d", resp.Count, resp.TotalSize)
	}
}

// An empty library still carries "files": [] — clients treat an absent key as
// "this meta-core cannot list files", so it must not disappear when empty.
func TestFileTuples_EmptyLibraryKeepsFilesKey(t *testing.T) {
	s, _ := newFileTuplesTestServer(t)

	code, resp := getFileTuples(t, s, "")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if resp.Files == nil || len(*resp.Files) != 0 || resp.Count != 0 {
		t.Fatalf("want an empty files array and count 0, got %+v", resp)
	}
}

func TestFileTuples_StorageNotConnected(t *testing.T) {
	s := &Server{storage: storage.NewClient("")}

	code, _ := getFileTuples(t, s, "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", code)
	}
}
