package api

import "net/http"

// fileTupleJSON is one file-backed root on the wire.
type fileTupleJSON struct {
	HashID    string `json:"hashId"`
	FilePath  string `json:"filePath"`
	SizeByte  int64  `json:"sizeByte"`
	MtimeNano int64  `json:"mtimeNano"`
}

// FileTuplesResponse is the body of GET /api/files/tuples.
//
// Files is a pointer so the summary form can omit it while the full form
// always carries it — an empty library is `"files": []`, never an absent key,
// which a client could mistake for a meta-core that lacks the rows.
type FileTuplesResponse struct {
	Files     *[]fileTupleJSON `json:"files,omitempty"`
	Count     int              `json:"count"`
	TotalSize int64            `json:"totalSize"`
}

// handleGetFileTuples handles GET /api/files/tuples.
//
// Lists the FILE-BACKED roots — records carrying filePath, sizeByte and
// mtimeNano, i.e. what the watcher or /api/files/register minted — with their
// sizes, plus the count and total. Gateway / cid-rooted records carry no file
// and are not included; neither is a locator record that only merged a cache
// file's metadata (it has no mtimeNano).
//
// It exists so a client never walks every hashId to answer "how many files,
// how big": GetTuplesForAllFiles reads the three fields for every root in
// chunked MGETs server-side (~250 round-trips for 82k roots, no SCAN), where
// the client-side equivalent was one HTTP request per record.
//
// ?summary=1 omits the rows and returns only {count, totalSize}.
func (s *Server) handleGetFileTuples(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	tuples, err := s.storage.GetTuplesForAllFiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	summary := r.URL.Query().Get("summary")
	withRows := summary == "" || summary == "0" || summary == "false"

	resp := FileTuplesResponse{Count: len(tuples)}
	var rows []fileTupleJSON
	if withRows {
		rows = make([]fileTupleJSON, 0, len(tuples))
	}
	for _, t := range tuples {
		resp.TotalSize += t.Size
		if withRows {
			rows = append(rows, fileTupleJSON{
				HashID:    t.HashID,
				FilePath:  t.FilePath,
				SizeByte:  t.Size,
				MtimeNano: t.MtimeNano,
			})
		}
	}
	if withRows {
		resp.Files = &rows
	}

	writeJSON(w, http.StatusOK, resp)
}
