package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// ---- HTTP API ----
//
// What the compose tab does over its WebSocket, a program can do over HTTP:
// the dtt composer tool (dtt/builtin/composer) uses these to save a
// composition and render it into local-representative's files area.
//
//   GET    /api/status               {repr_status, ffmpeg, ffprobe, ffmpeg_error, tabs}
//   GET    /api/compositions         {compositions: [summary...]}
//   GET    /api/compositions/<id>    the composition
//   PUT    /api/compositions/<id>    create or replace it ({composition, created})
//   DELETE /api/compositions         delete them all ({deleted: count})
//   DELETE /api/compositions/<id>
//   POST   /api/render-jobs          see renderjobs.go
//   GET    /api/render-jobs/<id>

// maxCompositionBytes bounds a PUT body.
const maxCompositionBytes = 1 << 20

// StatusMsg is GET /api/status's answer.
type StatusMsg struct {
	ReprStatus  string `json:"repr_status"`            // the link to local-representative
	FFmpeg      string `json:"ffmpeg,omitempty"`       // ffmpeg's path, when composer can render on its own
	FFprobe     string `json:"ffprobe,omitempty"`      // ffprobe's path, when found
	FFmpegError string `json:"ffmpeg_error,omitempty"` // why there's no ffmpeg
	Tabs        int    `json:"tabs"`                   // open compose tabs, which can take browser jobs
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.reprMu.Lock()
	msg := StatusMsg{ReprStatus: s.reprStatus}
	s.reprMu.Unlock()
	if s.lrBaseOverride != "" {
		msg.ReprStatus = "connected"
	}
	rc := s.renderCapability()
	msg.FFmpeg, msg.FFprobe, msg.FFmpegError = rc.FFmpeg, rc.FFprobe, rc.Error
	s.mu.RLock()
	msg.Tabs = len(s.clients)
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, msg)
}

func (s *Server) handleCompositionsAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/compositions"), "/")
	if strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	switch {
	case id == "" && r.Method == http.MethodGet:
		list, err := s.store.list()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"compositions": list})

	case id == "" && r.Method == http.MethodDelete:
		n, err := s.store.removeAll()
		if n > 0 {
			s.broadcastCompositions()
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": n})

	case id != "" && r.Method == http.MethodGet:
		c, err := s.store.get(id)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, c)

	case id != "" && r.Method == http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, maxCompositionBytes+1))
		if err != nil || len(body) > maxCompositionBytes {
			writeJSONError(w, http.StatusBadRequest, "the composition should be JSON of at most 1 MiB")
			return
		}
		var c Composition
		if err := json.Unmarshal(body, &c); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad composition: "+err.Error())
			return
		}
		c.ID = id
		saved, created, err := s.store.put(c)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]any{"composition": saved, "created": created})
		s.broadcast("composition", saved)
		s.broadcastCompositions()

	case id != "" && r.Method == http.MethodDelete:
		if err := s.store.remove(id); err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
		s.broadcastCompositions()

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
