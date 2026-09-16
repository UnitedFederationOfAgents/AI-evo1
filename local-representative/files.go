package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// fileCacheTTL is how long an uploaded file stays in the host-cache before the
// cleanup sweep deletes it.
const fileCacheTTL = time.Hour

// fileCacheSweepInterval is how often the cleanup sweep scans for expired files.
const fileCacheSweepInterval = time.Minute

// defaultFileCacheDir is where uploaded files land by default. See
// docs/CurrentPersistentFiles.md.
const defaultFileCacheDir = "/host-agent-files/exchange/host-cache"

// proxiedHeader marks a request that arrived via agent-coordinator's
// /host/<id>/ reverse proxy rather than directly from a browser connected to
// this LR (agent-coordinator's proxyToHost sets it). This increment
// intentionally keeps file upload a direct-client-only operation — see
// docs/DistributedExchange.md for how this might extend to chained input.
const proxiedHeader = "X-UFA-Proxied-By"

// FileInfo describes one file sitting in local-representative's host-cache.
type FileInfo struct {
	ID         string `json:"id"`   // on-disk filename; also addresses the file
	Name       string `json:"name"` // original filename as uploaded
	Size       int64  `json:"size"`
	Kind       string `json:"kind"`        // "text", "image", or "other" -- selects the wireframe icon
	UploadedAt int64  `json:"uploaded_at"` // unix seconds
	ExpiresAt  int64  `json:"expires_at"`  // unix seconds; uploaded_at + fileCacheTTL
}

// FilesStateMsg is the payload of "files-state" messages: the current
// host-cache listing, broadcast to browser clients and mirrored up to
// agent-coordinator (read-only there — see proxiedHeader).
type FilesStateMsg struct {
	Files []FileInfo `json:"files"`
}

// textExtensions / imageExtensions drive classifyKind's very small wireframe
// icon set for this increment: "text", "image", or "other".
var textExtensions = map[string]bool{
	".txt": true, ".md": true, ".log": true, ".csv": true, ".json": true,
	".yaml": true, ".yml": true, ".go": true, ".py": true, ".js": true,
	".ts": true, ".tsx": true, ".jsx": true, ".html": true, ".css": true,
	".sh": true, ".xml": true, ".ini": true, ".toml": true, ".conf": true,
}

var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".svg": true, ".bmp": true, ".ico": true,
}

// classifyKind maps a filename to the files tab's icon kind.
func classifyKind(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if textExtensions[ext] {
		return "text"
	}
	if imageExtensions[ext] {
		return "image"
	}
	return "other"
}

// sanitizeFilename strips directory components so an upload can't escape the
// host-cache directory via its claimed filename.
func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		name = "file"
	}
	return name
}

// randomID returns an 8-hex-character prefix used to make uploaded filenames
// collision-free on disk without needing a separate metadata store.
func randomID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

// displayName recovers the name as uploaded from a host-cache filename by
// stripping the "<8-hex>_" prefix randomID/saveUploadedFile add.
func displayName(id string) string {
	if len(id) > 9 && id[8] == '_' {
		if _, err := hex.DecodeString(id[:8]); err == nil {
			return id[9:]
		}
	}
	return id
}

// ensureFileCacheDir creates the host-cache directory if it doesn't exist yet.
func ensureFileCacheDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// listFiles scans the host-cache directory and returns its current contents,
// newest first. The filesystem is the source of truth — no separate in-memory
// index is kept, so files added or removed by hand are reflected immediately.
func (s *Server) listFiles() []FileInfo {
	entries, err := os.ReadDir(s.fileCacheDir)
	if err != nil {
		return nil
	}
	files := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := e.Name()
		files = append(files, FileInfo{
			ID:         id,
			Name:       displayName(id),
			Size:       info.Size(),
			Kind:       classifyKind(id),
			UploadedAt: info.ModTime().Unix(),
			ExpiresAt:  info.ModTime().Add(fileCacheTTL).Unix(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].UploadedAt > files[j].UploadedAt })
	return files
}

// broadcastFiles pushes the current host-cache listing to browser clients and
// mirrors it up to agent-coordinator.
func (s *Server) broadcastFiles() {
	st := FilesStateMsg{Files: s.listFiles()}
	s.broadcast("files-state", st)
	if ac := s.getACClient(); ac != nil {
		ac.SendData("files-state", st)
	}
}

// cleanupFilesLoop periodically deletes host-cache files older than
// fileCacheTTL. Must be called in its own goroutine.
func (s *Server) cleanupFilesLoop() {
	ticker := time.NewTicker(fileCacheSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.sweepExpiredFiles()
	}
}

// sweepExpiredFiles removes host-cache files past fileCacheTTL and, if any
// were removed, broadcasts the updated listing.
func (s *Server) sweepExpiredFiles() {
	entries, err := os.ReadDir(s.fileCacheDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-fileCacheTTL)
	removed := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(s.fileCacheDir, e.Name())
			if err := os.Remove(path); err != nil {
				log.Printf("files: failed to remove expired %s: %v", e.Name(), err)
				continue
			}
			log.Printf("files: removed expired host-cache file %s (uploaded %s ago)",
				e.Name(), time.Since(info.ModTime()).Round(time.Second))
			removed = true
		}
	}
	if removed {
		s.broadcastFiles()
	}
}

// handleFilesAPI serves the files tab's REST surface: GET lists the
// host-cache, POST uploads a new file into it.
func (s *Server) handleFilesAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FilesStateMsg{Files: s.listFiles()})
	case http.MethodPost:
		s.handleFileUpload(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleFileUpload accepts a multipart "file" upload into the host-cache. It
// is rejected when the request arrived through agent-coordinator's reverse
// proxy: this increment only allows the direct local-representative client to
// write into the host-cache (see docs/DistributedExchange.md).
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(proxiedHeader) != "" {
		http.Error(w,
			"file upload is only permitted from a direct local-representative client, "+
				"not through agent-coordinator (see docs/DistributedExchange.md)",
			http.StatusForbidden)
		return
	}

	const maxUploadSize = 64 << 20 // 64MiB per request -- generous for a wireframe-icon MVP
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "invalid upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		http.Error(w, `no file provided (expected multipart field "file")`, http.StatusBadRequest)
		return
	}

	saved := make([]FileInfo, 0, len(headers))
	for _, fh := range headers {
		info, err := s.saveUploadedFile(fh)
		if err != nil {
			log.Printf("files: upload of %q failed: %v", fh.Filename, err)
			continue
		}
		saved = append(saved, info)
	}
	if len(saved) == 0 {
		http.Error(w, "no file could be saved", http.StatusInternalServerError)
		return
	}

	s.broadcastFiles()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(FilesStateMsg{Files: saved})
}

// saveUploadedFile writes one multipart file into the host-cache under a
// collision-free "<8-hex>_<original-name>" filename.
func (s *Server) saveUploadedFile(fh *multipart.FileHeader) (FileInfo, error) {
	src, err := fh.Open()
	if err != nil {
		return FileInfo{}, err
	}
	defer src.Close()

	name := sanitizeFilename(fh.Filename)
	id := randomID() + "_" + name
	dst, err := os.OpenFile(filepath.Join(s.fileCacheDir, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return FileInfo{}, err
	}
	defer dst.Close()

	n, err := io.Copy(dst, src)
	if err != nil {
		os.Remove(filepath.Join(s.fileCacheDir, id))
		return FileInfo{}, err
	}

	now := time.Now()
	log.Printf("files: uploaded %s (%d bytes) -> host-cache/%s", name, n, id)
	return FileInfo{
		ID:         id,
		Name:       name,
		Size:       n,
		Kind:       classifyKind(name),
		UploadedAt: now.Unix(),
		ExpiresAt:  now.Add(fileCacheTTL).Unix(),
	}, nil
}
