package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

// ---- Sources and renders ----
//
// composer keeps no media of its own. Its sources are the audio, video and
// image files in local-representative's files area -- where IANAR saves its
// recordings ("ianar-recording-*.webm"/".mp4") and The Conversationalist its
// saved Polly clips ("polly-*.mp3") -- read over LR's GET /api/files and
// GET /api/files/<id>. The browser fetches a source's bytes through
// GET /api/source/<id> here rather than from LR directly, so they're
// same-origin to the page whichever prefix it was served under, and the
// canvas they're drawn onto can still be recorded.
//
// A finished render comes back as POST /api/render and is uploaded into the
// same files area, as "composer-<composition>-<stamp>.webm" (".weba" for an
// audio composition's).

// Extensions composer can use, by media kind.
var mediaExtensions = map[string]string{
	".mp3": "audio", ".wav": "audio", ".ogg": "audio", ".oga": "audio", ".m4a": "audio",
	".aac": "audio", ".flac": "audio", ".opus": "audio", ".weba": "audio",
	".webm": "video", ".mp4": "video", ".m4v": "video", ".mov": "video", ".ogv": "video",
	".png": "image", ".jpg": "image", ".jpeg": "image", ".gif": "image", ".webp": "image",
}

// mediaKind is "audio", "video" or "image" for a file name composer can
// use, else "".
func mediaKind(name string) string {
	return mediaExtensions[strings.ToLower(path.Ext(name))]
}

// sourceOrigin says which sub-app a file came from, by the name it was
// saved under: "ianar", "polly", "composer" or "".
func sourceOrigin(name string) string {
	switch {
	case strings.HasPrefix(name, "ianar-"):
		return "ianar"
	case strings.HasPrefix(name, "polly-"):
		return "polly"
	case strings.HasPrefix(name, "composer-"):
		return "composer"
	}
	return ""
}

// Source is one usable file in local-representative's files area.
type Source struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Media      string `json:"media"`            // "audio" | "video" | "image"
	Origin     string `json:"origin,omitempty"` // see sourceOrigin
	UploadedAt int64  `json:"uploaded_at,omitempty"`
}

// SourcesMsg is the "sources" WebSocket payload. Error is set (and Sources
// empty) when local-representative couldn't be asked.
type SourcesMsg struct {
	Sources []Source `json:"sources"`
	Error   string   `json:"error,omitempty"`
}

// lrFetchTimeout bounds listing local-representative's files.
const lrFetchTimeout = 10 * time.Second

// lrBase is the base URL of this instance's local-representative's
// dashboard, from the representable connection's "hello" (see
// representable.Client.PeerHTTPPort).
func (s *Server) lrBase() (string, error) {
	s.reprMu.Lock()
	client := s.reprClient
	host := s.reprHost
	s.reprMu.Unlock()
	if client == nil {
		return "", fmt.Errorf("not connected to local-representative")
	}
	port := client.PeerHTTPPort()
	if port == "" {
		return "", fmt.Errorf("local-representative has not disclosed its HTTP port")
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// lrBaseFn is lrBase, overridable in tests.
func (s *Server) lrBaseFn() (string, error) {
	if s.lrBaseOverride != "" {
		return s.lrBaseOverride, nil
	}
	return s.lrBase()
}

// listSources lists the usable files at base, newest first.
func listSources(base string) ([]Source, error) {
	resp, err := (&http.Client{Timeout: lrFetchTimeout}).Get(base + "/api/files")
	if err != nil {
		return nil, fmt.Errorf("listing local-representative's files: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing local-representative's files: unexpected status %d", resp.StatusCode)
	}
	var listing struct {
		Files []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Size       int64  `json:"size"`
			UploadedAt int64  `json:"uploaded_at"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return nil, fmt.Errorf("listing local-representative's files: %w", err)
	}
	out := []Source{}
	for _, f := range listing.Files {
		name := f.Name
		if name == "" {
			name = f.ID
		}
		kind := mediaKind(name)
		if kind == "" {
			continue
		}
		out = append(out, Source{
			ID: f.ID, Name: name, Size: f.Size, Media: kind, Origin: sourceOrigin(name), UploadedAt: f.UploadedAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UploadedAt > out[j].UploadedAt })
	return out, nil
}

// sourcesMsg asks local-representative for the current sources.
func (s *Server) sourcesMsg() SourcesMsg {
	base, err := s.lrBaseFn()
	if err != nil {
		return SourcesMsg{Sources: []Source{}, Error: err.Error()}
	}
	sources, err := listSources(base)
	if err != nil {
		return SourcesMsg{Sources: []Source{}, Error: err.Error()}
	}
	return SourcesMsg{Sources: sources}
}

// handleSource implements GET /api/source/<id>: one source's bytes, passed
// through from local-representative with its Range handling intact so
// audio/video elements can seek.
func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/source/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "missing source id", http.StatusBadRequest)
		return
	}
	base, err := s.lrBaseFn()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, base+"/api/files/"+url.PathEscape(id), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, h := range []string{"Range", "If-Range", "If-Modified-Since"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "local-representative not reachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck -- best-effort once the status is written
}

// maxRenderBytes matches local-representative's per-upload limit
// (files.go's maxUploadSize), less room for the multipart framing.
const maxRenderBytes = 63 << 20

// renderUploadTimeout bounds handing a render to local-representative.
const renderUploadTimeout = 2 * time.Minute

// RenderResultMsg is POST /api/render's answer, and the "render-result"
// WebSocket message every client gets once a render is saved.
type RenderResultMsg struct {
	Composition string `json:"composition"`
	FileID      string `json:"file_id,omitempty"`
	Name        string `json:"name,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Renderer    string `json:"renderer,omitempty"` // "ffmpeg" or "browser"; see RenderInfo
	Error       string `json:"error,omitempty"`
}

// handleRender implements POST /api/render?composition=<id>[&job=<id>]: the
// body is the browser's finished recording (video/webm or audio/webm),
// uploaded to local-representative's files area and recorded on the
// composition. job is set when the browser rendered it for a render job
// (see renderjobs.go), which this finishes.
func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("composition")
	jobID := r.URL.Query().Get("job")
	reply := func(status int, result RenderResultMsg) {
		if jobID != "" {
			s.jobs.finish(jobID, result)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(result) //nolint:errcheck
	}
	c, err := s.store.get(id)
	if err != nil {
		reply(http.StatusNotFound, RenderResultMsg{Composition: id, Error: err.Error()})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRenderBytes+1))
	if err != nil {
		reply(http.StatusBadRequest, RenderResultMsg{Composition: id, Error: "reading render: " + err.Error()})
		return
	}
	result, status := s.saveRender(c, body, rendererBrowser, jobID)
	reply(status, result)
}

// saveRender uploads a finished render of c into local-representative's
// files area, records it on c and tells every client. It returns the result
// and the HTTP status that goes with it. Both the browser's renders and
// ffmpeg's (ffmpeg.go) end here; renderer says which made it, and jobID is
// the render job it's for ("" for a Render button press).
func (s *Server) saveRender(c *Composition, body []byte, renderer, jobID string) (RenderResultMsg, int) {
	result := RenderResultMsg{Composition: c.ID, Renderer: renderer}
	if len(body) == 0 {
		result.Error = "empty render"
		return result, http.StatusBadRequest
	}
	if len(body) > maxRenderBytes {
		result.Error = fmt.Sprintf("render is over %d MiB, local-representative's upload limit -- make it shorter or smaller", maxRenderBytes>>20)
		return result, http.StatusRequestEntityTooLarge
	}
	base, err := s.lrBaseFn()
	if err != nil {
		result.Error = err.Error()
		return result, http.StatusServiceUnavailable
	}
	name := renderName(c, time.Now())
	uploaded, err := uploadToFiles(base, name, body)
	if err != nil {
		result.Error = err.Error()
		return result, http.StatusBadGateway
	}
	result.FileID, result.Name, result.Size = uploaded.ID, uploaded.Name, int64(len(body))
	from := "Render button"
	if jobID != "" {
		from = "render job " + jobID
	}
	log.Printf("render saved: %s (%d bytes, composition %s, renderer %s, %s)", uploaded.Name, result.Size, c.ID, renderer, from)
	if _, err := s.store.addRender(c.ID, RenderInfo{FileID: uploaded.ID, Name: uploaded.Name, Size: result.Size, At: time.Now().Unix(), Renderer: renderer}); err != nil {
		// The upload itself worked; only the history entry is missing.
		result.Error = "saved, but not recorded on the composition: " + err.Error()
	}
	s.broadcast("render-result", result)
	s.broadcastCompositions()
	if updated, err := s.store.get(c.ID); err == nil {
		s.broadcast("composition", updated)
	}
	go s.broadcastSources()
	return result, http.StatusOK
}

// broadcastSources re-lists the sources for every client -- after a render
// added one, or once local-representative is reachable.
func (s *Server) broadcastSources() {
	s.broadcast("sources", s.sourcesMsg())
}

// renderName is "composer-<id>-<stamp>.webm", or ".weba" (audio-only WebM)
// for an audio composition.
func renderName(c *Composition, now time.Time) string {
	ext := ".webm"
	if c.audioOnly() {
		ext = ".weba"
	}
	return fmt.Sprintf("composer-%s-%s%s", c.ID, now.Format("2006-01-02T15-04-05"), ext)
}

// uploadedFile mirrors the subset of local-representative's FileInfo JSON
// (local-representative/files.go) this caller reads back.
type uploadedFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// uploadToFiles POSTs one file into local-representative's files area
// exactly as its own upload widget does: multipart/form-data with a single
// "file" field (see local-representative/files.go's handleFileUpload, and
// ianar/artifacts.go's function of the same name).
func uploadToFiles(base, filename string, content []byte) (*uploadedFile, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(content); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/files", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := (&http.Client{Timeout: renderUploadTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("uploading %s: %w", filename, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if m := strings.TrimSpace(string(msg)); m != "" {
			return nil, fmt.Errorf("uploading %s: local-representative: %s", filename, m)
		}
		return nil, fmt.Errorf("uploading %s: unexpected status %d", filename, resp.StatusCode)
	}
	var result struct {
		Files []uploadedFile `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("uploading %s: %w", filename, err)
	}
	if len(result.Files) == 0 {
		return nil, fmt.Errorf("uploading %s: local-representative returned no file", filename)
	}
	return &result.Files[0], nil
}
