package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// The diagram HTTP API the frontend uses. Paths are relative to wherever
// the app is served (/, /apps/dg/ or /host/<id>/apps/dg/).
//
//	GET  /api/diagrams         the sidebar's list: [{name, title, steps}]
//	GET  /api/diagrams/<name>  one example: {diagram, dtt, d2}
//	POST /api/convert          {dtt} or {diagram} -> {diagram, dtt, d2}, or 400 {error}
//	POST /api/export/gif       {frames, delay_ms} -> image/gif (see gif.go)

// DiagramSummary is one entry in the sidebar's list.
type DiagramSummary struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Steps int    `json:"steps"` // 0 for a static diagram
}

// DiagramView is a diagram along with its DTT document and D2 export.
type DiagramView struct {
	Diagram *Diagram `json:"diagram"`
	DTT     string   `json:"dtt"`
	D2      string   `json:"d2"`
}

// ConvertRequest carries either a DTT document to read, or a diagram (as
// the canvas has it after an edit) to write one for.
type ConvertRequest struct {
	DTT     string   `json:"dtt,omitempty"`
	Diagram *Diagram `json:"diagram,omitempty"`
}

const maxConvertBody = 1 << 20

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) handleDiagrams(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/diagrams"), "/")
	if name == "" {
		list := make([]DiagramSummary, 0, len(s.examples))
		for _, ex := range s.examples {
			list = append(list, DiagramSummary{Name: ex.diagram.Name, Title: ex.diagram.Title, Steps: len(ex.diagram.Steps)})
		}
		writeJSON(w, http.StatusOK, list)
		return
	}
	for _, ex := range s.examples {
		if ex.diagram.Name == name {
			writeJSON(w, http.StatusOK, DiagramView{Diagram: ex.diagram, DTT: ex.source, D2: ex.diagram.D2()})
			return
		}
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("no diagram named %q", name))
}

func (s *Server) handleConvert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req ConvertRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConvertBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("reading the request: %v", err))
		return
	}
	switch {
	case req.Diagram != nil:
		// The canvas's diagram: write its document afresh.
		d := req.Diagram
		d.normalize()
		if err := d.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, DiagramView{Diagram: d, DTT: d.DTT(), D2: d.D2()})
	case strings.TrimSpace(req.DTT) != "":
		// The DTT view's text: keep it as the user wrote it.
		d, err := parseDiagram(req.DTT)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, DiagramView{Diagram: d, DTT: req.DTT, D2: d.D2()})
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("send dtt or diagram"))
	}
}
