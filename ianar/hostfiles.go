package main

import (
	"io"
	"net/http"
)

// handleHostFiles passes GET /api/host-files and /api/host-files/raw
// straight through to local-representative (local-representative/
// hostfiles.go), so the sequence tab's "select from host" YAML import
// (Step4Prompt.md Revision F of condocs/initialRobotImpls) can browse and
// read LR's host whether this UI is reached through LR's /robot/ proxy,
// agent-coordinator's /host/<id>/robot/, or directly.
func (s *Server) handleHostFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	base, err := s.lrBaseURL()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	resp, err := http.Get(base + r.URL.Path + "?" + r.URL.RawQuery)
	if err != nil {
		http.Error(w, "local-representative not reachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Disposition"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck — best-effort once the status is written
}
