package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/metazla/meta-core/internal/config"
	"github.com/metazla/meta-core/internal/storage"
)

// newRegisterTestServer wires handleRegisterFile to an in-memory Redis and a
// temp FILES_PATH. Built directly, not through NewServer, so no watcher runs —
// which is exactly the configuration this route exists for.
func newRegisterTestServer(t *testing.T) (*Server, string, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := storage.NewClient("")
	if err := c.Connect("redis://" + mr.Addr()); err != nil {
		t.Fatalf("connect to miniredis: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	files := t.TempDir()
	return &Server{storage: c, config: &config.Config{FilesPath: files}}, files, mr
}

func writeFixture(t *testing.T, files, rel, body string) string {
	t.Helper()
	full := filepath.Join(files, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func postRegister(t *testing.T, s *Server, path string) (int, registerFileResponse) {
	t.Helper()
	body, _ := json.Marshal(registerFileRequest{Path: path})
	req := httptest.NewRequest(http.MethodPost, "/api/files/register", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	s.handleRegisterFile(rr, req)
	var resp registerFileResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	return rr.Code, resp
}

func streamLen(t *testing.T, mr *miniredis.Miniredis) int {
	t.Helper()
	entries, err := mr.Stream("file:events")
	if err != nil {
		return 0
	}
	return len(entries)
}

// The whole point of the route: with the watcher off, a file written under
// plugin/ becomes a record resolvable by its midhash, and meta-share hears
// about it on file:events.
func TestRegisterFile_MintsAliasAndPublishesEvent(t *testing.T) {
	s, files, mr := newRegisterTestServer(t)
	rel := "plugin/subtitles/ab12cd34.eng.srt"
	full := writeFixture(t, files, rel, "1\n00:00:01,000 --> 00:00:02,000\nHello\n")

	code, resp := postRegister(t, s, rel)
	if code != http.StatusCreated || !resp.Created {
		t.Fatalf("want 201 created, got %d %+v", code, resp)
	}
	if !strings.HasPrefix(resp.CID, "b") || resp.Root == "" {
		t.Fatalf("want a multibase cid and a root, got %+v", resp)
	}

	root, err := s.storage.GetByCID(resp.CID)
	if err != nil || root != resp.Root {
		t.Fatalf("cid alias must resolve to the root: got %q err=%v, want %q", root, err, resp.Root)
	}
	if fp, _ := s.storage.GetProperty(resp.Root, "filePath"); fp != full {
		t.Fatalf("filePath = %q, want %q", fp, full)
	}

	entries, err := mr.Stream("file:events")
	if err != nil || len(entries) != 1 {
		t.Fatalf("want exactly one file:events entry, got %d err=%v", len(entries), err)
	}
	vals := strings.Join(entries[0].Values, "|")
	if !strings.Contains(vals, "midhash256|"+resp.CID) || !strings.Contains(vals, "path|"+rel) {
		t.Fatalf("event must carry the midhash and the relative path, got %q", vals)
	}
}

// Re-uploading the same subtitle is a no-op: same root, no second event (which
// would make meta-share re-chunk and re-provide the file).
func TestRegisterFile_IsIdempotent(t *testing.T) {
	s, files, mr := newRegisterTestServer(t)
	rel := "plugin/subtitles/same.eng.srt"
	writeFixture(t, files, rel, "same bytes")

	_, first := postRegister(t, s, rel)
	code, second := postRegister(t, s, rel)
	if code != http.StatusOK || second.Created {
		t.Fatalf("second register must be 200 created=false, got %d %+v", code, second)
	}
	if second.Root != first.Root || second.CID != first.CID {
		t.Fatalf("second register must return the same record: %+v vs %+v", second, first)
	}
	if n := streamLen(t, mr); n != 1 {
		t.Fatalf("an idempotent re-register must not publish again, stream has %d", n)
	}
}

// Only plugin output may be registered here. /files/watch is the user's
// library and belongs to the watcher; traversal must not escape either.
func TestRegisterFile_RejectsPathsOutsidePlugin(t *testing.T) {
	s, files, _ := newRegisterTestServer(t)
	writeFixture(t, files, "watch/movie.mkv", "x")
	for _, p := range []string{
		"", "plugin", "plugin/", "watch/movie.mkv", "/watch/movie.mkv",
		"plugin/../watch/movie.mkv", "../etc/passwd", "plugin/../../etc/passwd",
	} {
		if code, _ := postRegister(t, s, p); code != http.StatusBadRequest {
			t.Errorf("path %q: want 400, got %d", p, code)
		}
	}
}

func TestRegisterFile_MissingFileAndDirectory(t *testing.T) {
	s, files, _ := newRegisterTestServer(t)
	if code, _ := postRegister(t, s, "plugin/subtitles/nope.srt"); code != http.StatusNotFound {
		t.Errorf("missing file: want 404, got %d", code)
	}
	if err := os.MkdirAll(filepath.Join(files, "plugin", "subtitles", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _ := postRegister(t, s, "plugin/subtitles/dir"); code != http.StatusBadRequest {
		t.Errorf("directory: want 400, got %d", code)
	}
}
