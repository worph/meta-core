package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/metazla/meta-core/internal/storage"
)

// newSweepTestServer is a Server over miniredis with one literature card
// carrying a copied `workcid`.
func newSweepTestServer(t *testing.T) *Server {
	t.Helper()
	mr := miniredis.RunT(t)
	c := storage.NewClient("")
	if err := c.Connect("redis://" + mr.Addr()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	for k, v := range map[string]string{
		"domain":   "literature",
		"fileType": "card",
		"workcid":  "bagdsaaaua5qw42lmnfzxi3lbnztwcorrga2tonzy",
	} {
		if err := c.SetProperty("card-1", k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	return &Server{storage: c}
}

type sweepResponse struct {
	Success bool           `json:"success"`
	DryRun  bool           `json:"dryRun"`
	Planned int            `json:"planned"`
	Fixed   int            `json:"fixed"`
	ByKey   map[string]int `json:"byKey"`
}

func postSweep(t *testing.T, s *Server, query string) (*httptest.ResponseRecorder, sweepResponse) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sweep-literature-echoes"+query, nil)
	s.handleSweepLiteratureEchoes(rr, req)
	var body sweepResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", rr.Body.String(), err)
		}
	}
	return rr, body
}

// A bare call must never delete: the default is the dry run.
func TestSweepLiteratureEchoes_DefaultsToDryRun(t *testing.T) {
	s := newSweepTestServer(t)

	rr, body := postSweep(t, s, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	if !body.Success || !body.DryRun || body.Planned != 1 || body.Fixed != 0 || body.ByKey["workcid"] != 1 {
		t.Errorf("dry run = %+v", body)
	}
	if v, _ := s.storage.GetProperty("card-1", "workcid"); v == "" {
		t.Errorf("dry run deleted workcid")
	}

	rr, body = postSweep(t, s, "?apply=true")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	if body.DryRun || body.Planned != 1 || body.Fixed != 1 || body.ByKey["workcid"] != 1 {
		t.Errorf("apply = %+v", body)
	}
	if v, _ := s.storage.GetProperty("card-1", "workcid"); v != "" {
		t.Errorf("apply left workcid = %q", v)
	}
}

// A typo must not pass for a sweep that ran.
func TestSweepLiteratureEchoes_RejectsUnparseableApply(t *testing.T) {
	s := newSweepTestServer(t)

	rr, _ := postSweep(t, s, "?apply=yes-please")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}
