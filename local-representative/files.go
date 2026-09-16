package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// fileCacheTTL is how long an uploaded file stays in the host-cache before the
// cleanup sweep deletes it, absent a "hold" (see holdTTL).
const fileCacheTTL = time.Hour

// holdTTL is how long a held file stays in the host-cache before the cleanup
// sweep deletes it, counted from when "hold" was pressed (not from upload).
// Pressing "persist" moves the file to the host-store before this ever
// applies, where it is never swept at all.
const holdTTL = 72 * time.Hour

// fileCacheSweepInterval is how often the cleanup sweep scans for expired files.
const fileCacheSweepInterval = time.Minute

// defaultFileCacheDir is where uploaded files land by default. See
// docs/CurrentPersistentFiles.md.
const defaultFileCacheDir = "/host-agent-files/exchange/host-cache"

// defaultHostStoreDir is where a "persist" press moves a file, by default.
// Unlike the host-cache, nothing in the host-store is ever swept -- see
// docs/CurrentPersistentFiles.md.
const defaultHostStoreDir = "/host-agent-files/exchange/host-store"

// proxiedHeader marks a request that arrived via agent-coordinator's
// /host/<id>/ transparent reverse proxy rather than directly from a browser
// connected to this LR (agent-coordinator's proxyToHost sets it). Upload
// refuses any request carrying it, with one deliberate exception — see
// relayedUploadHeader — since a write into an arbitrary host's filesystem
// should stay refused by default. See docs/DistributedExchange.md.
const proxiedHeader = "X-UFA-Proxied-By"

// relayedUploadHeader marks a request that arrived via agent-coordinator's
// dedicated upload-relay route (Path 1 of docs/DistributedExchange.md) rather
// than its transparent /host/<id>/* passthrough. It's the one exception
// handleFileUpload's proxiedHeader refusal carves out: an operator using AC's
// per-host files tab can still upload, same as connecting to this LR
// directly, while an upload arriving through AC's transparent passthrough
// (which strips any client-supplied copy of this header before forwarding)
// stays refused. Both headers must carry acRelayStamp for the exception to
// apply.
//
// The file-details actions added alongside "hold"/"persist"/"delete" do NOT
// use this guard: unlike upload (which writes arbitrary new content supplied
// by whoever's on the other end), they act on a file already listed in a
// host's files tab, which AC only shows once an operator has explicitly
// selected that host -- the same "no ambiguity of target" reasoning that
// already left GET /api/files/<id> ungated. So they pass through AC's
// transparent /host/<id>/* proxy unmodified, same as a direct LR client.
const relayedUploadHeader = "X-UFA-Relayed-Upload-By"

// acRelayStamp is the value agent-coordinator's dedicated upload-relay route
// sets on both proxiedHeader and relayedUploadHeader.
const acRelayStamp = "agent-coordinator"

// manifestPrefix marks a host-cache entry as a hidden sidecar rather than a
// file the "files" tab should list: alongside an uploaded "<id>" this
// increment also writes "<manifestPrefix><id>.yaml" recording the same
// details (name, kind, size, upload/expiry times, hold state) in a small
// flat-YAML file. Its "held"/"expires_at" fields are read back by
// readManifest -- listFiles and the sweep both need them to tell a held file
// (72-hour TTL from when hold was pressed) from a plain cached one (1 hour
// from upload) without relying on the data file's mtime, which the hold
// action deliberately leaves untouched. Uploads whose claimed filename starts
// with this prefix are refused, and the sweep/listing both treat it as
// invisible to the files tab.
const manifestPrefix = ".manifest_"

// manifestName returns the sidecar filename for a host-cache entry "id",
// e.g. "d992a7a9_debug.txt" -> ".manifest_d992a7a9_debug.txt.yaml".
func manifestName(id string) string {
	return manifestPrefix + id + ".yaml"
}

// FileInfo describes one file sitting in local-representative's host-cache or
// host-store.
type FileInfo struct {
	ID         string `json:"id"`   // on-disk filename; also addresses the file
	Name       string `json:"name"` // original filename as uploaded
	Size       int64  `json:"size"`
	Kind       string `json:"kind"`        // "text", "image", or "other" -- selects the wireframe icon
	State      string `json:"state"`       // "cached", "held", or "persisted" -- selects the icon's color
	UploadedAt int64  `json:"uploaded_at"` // unix seconds
	ExpiresAt  int64  `json:"expires_at"`  // unix seconds; 0 once State is "persisted"
}

// FilesStateMsg is the payload of "files-state" messages: the current
// host-cache + host-store listing, broadcast to browser clients and mirrored
// up to agent-coordinator, which can now also relay uploads back down to this
// LR through its dedicated upload-relay route (see proxiedHeader,
// relayedUploadHeader).
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

// ensureFileCacheDir creates a files-tab directory (host-cache or host-store)
// if it doesn't exist yet.
func ensureFileCacheDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// manifestFields is the small subset of a manifest sidecar's flat "key:
// value" YAML that's actually read back (see writeManifest): whether a
// host-cache entry is held, and the expiry that implies.
type manifestFields struct {
	Held      bool
	ExpiresAt int64
}

// readManifest reads dir/.manifest_<id>.yaml, if present. ok is false when
// there is no manifest to read (a fresh-upload race, or one removed by hand)
// -- callers fall back to mtime-derived defaults in that case.
func readManifest(dir, id string) (manifestFields, bool) {
	b, err := os.ReadFile(filepath.Join(dir, manifestName(id)))
	if err != nil {
		return manifestFields{}, false
	}
	var m manifestFields
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"`)
		switch key {
		case "held":
			m.Held = val == "true"
		case "expires_at":
			if t, err := time.Parse(time.RFC3339, val); err == nil {
				m.ExpiresAt = t.Unix()
			}
		}
	}
	return m, true
}

// listFiles scans the host-cache and host-store directories and returns
// their combined current contents, newest first. The filesystem (plus each
// host-cache entry's manifest, for hold state) is the source of truth -- no
// further in-memory index is kept.
func (s *Server) listFiles() []FileInfo {
	files := s.scanCacheDir()
	files = append(files, s.scanStoreDir()...)
	sort.Slice(files, func(i, j int) bool { return files[i].UploadedAt > files[j].UploadedAt })
	return files
}

// scanCacheDir lists host-cache entries: "cached" (expires fileCacheTTL after
// upload) by default, or "held" (expires holdTTL after the hold press, per
// its manifest -- see readManifest) when its manifest sidecar says so.
func (s *Server) scanCacheDir() []FileInfo {
	entries, err := os.ReadDir(s.fileCacheDir)
	if err != nil {
		return nil
	}
	files := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), manifestPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := e.Name()
		fi := FileInfo{
			ID:         id,
			Name:       displayName(id),
			Size:       info.Size(),
			Kind:       classifyKind(id),
			State:      "cached",
			UploadedAt: info.ModTime().Unix(),
			ExpiresAt:  info.ModTime().Add(fileCacheTTL).Unix(),
		}
		if m, ok := readManifest(s.fileCacheDir, id); ok && m.Held {
			fi.State = "held"
			fi.ExpiresAt = m.ExpiresAt
		}
		files = append(files, fi)
	}
	return files
}

// scanStoreDir lists host-store entries: files a "persist" press has moved
// out of the sweep's reach entirely, so ExpiresAt is left at zero.
func (s *Server) scanStoreDir() []FileInfo {
	entries, err := os.ReadDir(s.hostStoreDir)
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
			State:      "persisted",
			UploadedAt: info.ModTime().Unix(),
		})
	}
	return files
}

// locateFile returns the directory (host-cache or host-store) currently
// holding entry "id", checking the host-cache first (the common case).
func (s *Server) locateFile(id string) (dir string, ok bool) {
	if _, err := os.Stat(filepath.Join(s.fileCacheDir, id)); err == nil {
		return s.fileCacheDir, true
	}
	if _, err := os.Stat(filepath.Join(s.hostStoreDir, id)); err == nil {
		return s.hostStoreDir, true
	}
	return "", false
}

// broadcastFiles pushes the current files-tab listing to browser clients and
// mirrors it up to agent-coordinator.
func (s *Server) broadcastFiles() {
	st := FilesStateMsg{Files: s.listFiles()}
	s.broadcast("files-state", st)
	if ac := s.getACClient(); ac != nil {
		ac.SendData("files-state", st)
	}
}

// cleanupFilesLoop periodically deletes host-cache files past their expiry
// (fileCacheTTL for a plain cached entry, holdTTL-from-hold for a held one).
// The host-store is never swept. Must be called in its own goroutine.
func (s *Server) cleanupFilesLoop() {
	ticker := time.NewTicker(fileCacheSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.sweepExpiredFiles()
	}
}

// sweepExpiredFiles removes host-cache files past their expiry (and their
// manifest sidecars), plus any manifest left orphaned by its data file having
// been removed some other way. If anything visible to the files tab was
// removed, it broadcasts the updated listing.
func (s *Server) sweepExpiredFiles() {
	entries, err := os.ReadDir(s.fileCacheDir)
	if err != nil {
		return
	}
	present := make(map[string]bool, len(entries))
	for _, e := range entries {
		present[e.Name()] = true
	}

	now := time.Now()
	removed := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, manifestPrefix) {
			id := strings.TrimSuffix(strings.TrimPrefix(name, manifestPrefix), ".yaml")
			if !present[id] {
				if err := os.Remove(filepath.Join(s.fileCacheDir, name)); err != nil {
					log.Printf("files: failed to remove orphaned manifest %s: %v", name, err)
				}
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		expiresAt := info.ModTime().Add(fileCacheTTL)
		if m, ok := readManifest(s.fileCacheDir, name); ok && m.Held {
			expiresAt = time.Unix(m.ExpiresAt, 0)
		}
		if now.After(expiresAt) {
			path := filepath.Join(s.fileCacheDir, name)
			if err := os.Remove(path); err != nil {
				log.Printf("files: failed to remove expired %s: %v", name, err)
				continue
			}
			s.removeManifest(name)
			log.Printf("files: removed expired host-cache file %s (expired %s ago)",
				name, time.Since(expiresAt).Round(time.Second))
			removed = true
		}
	}
	if removed {
		s.broadcastFiles()
	}
}

// handleFilesAPI serves the files tab's REST surface: GET lists the
// combined host-cache/host-store, POST uploads a new file into the
// host-cache. A single file's bytes (for the viewer / download button) and
// its per-file actions (delete, hold, persist) are served by the sibling
// handleFileItem, registered at the "/api/files/" subtree.
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
// is rejected when the request arrived through agent-coordinator's
// transparent reverse proxy: this LR only accepts uploads from a direct
// local-representative client, or from agent-coordinator's dedicated
// upload-relay route (marked by relayedUploadHeader — see
// docs/DistributedExchange.md, Path 1).
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(proxiedHeader) != "" && r.Header.Get(relayedUploadHeader) != acRelayStamp {
		http.Error(w,
			"file upload is only permitted from a direct local-representative client, "+
				"or relayed through agent-coordinator's own upload-relay route "+
				"(see docs/DistributedExchange.md)",
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
// collision-free "<8-hex>_<original-name>" filename, plus a hidden
// ".manifest_<id>.yaml" sidecar recording the same details (see
// manifestPrefix).
func (s *Server) saveUploadedFile(fh *multipart.FileHeader) (FileInfo, error) {
	src, err := fh.Open()
	if err != nil {
		return FileInfo{}, err
	}
	defer src.Close()

	name := sanitizeFilename(fh.Filename)
	if strings.HasPrefix(name, manifestPrefix) {
		return FileInfo{}, fmt.Errorf("upload rejected: filenames starting with %q are reserved for host-cache manifests", manifestPrefix)
	}
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
	info := FileInfo{
		ID:         id,
		Name:       name,
		Size:       n,
		Kind:       classifyKind(name),
		State:      "cached",
		UploadedAt: now.Unix(),
		ExpiresAt:  now.Add(fileCacheTTL).Unix(),
	}
	s.writeManifest(info)
	return info, nil
}

// writeManifest writes the hidden ".manifest_<id>.yaml" sidecar for a
// host-cache entry (see manifestPrefix). It's a flat "key: value" mapping,
// the same small YAML subset ufa-configurable reads elsewhere in this repo.
// name/kind/size/uploaded_at are for an operator to read by hand -- nothing
// parses them back. held/expires_at ARE read back, by readManifest, so a
// held file's extended TTL survives an LR restart (the data file's own mtime
// only ever reflects its original upload time). A write failure is logged
// and otherwise swallowed: the action that triggered it (upload/hold) already
// succeeded and shouldn't fail over a sidecar note.
func (s *Server) writeManifest(info FileInfo) {
	var b strings.Builder
	b.WriteString("# host-cache manifest -- hidden from the files tab; held/expires_at are read back by readManifest\n")
	fmt.Fprintf(&b, "id: %q\n", info.ID)
	fmt.Fprintf(&b, "name: %q\n", info.Name)
	fmt.Fprintf(&b, "kind: %s\n", info.Kind)
	fmt.Fprintf(&b, "size: %d\n", info.Size)
	fmt.Fprintf(&b, "uploaded_at: %s\n", time.Unix(info.UploadedAt, 0).UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "held: %t\n", info.State == "held")
	fmt.Fprintf(&b, "expires_at: %s\n", time.Unix(info.ExpiresAt, 0).UTC().Format(time.RFC3339))
	path := filepath.Join(s.fileCacheDir, manifestName(info.ID))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		log.Printf("files: failed to write manifest for %s: %v", info.ID, err)
	}
}

// removeManifest deletes the host-cache manifest sidecar for entry "id",
// ignoring a missing file.
func (s *Server) removeManifest(id string) {
	path := filepath.Join(s.fileCacheDir, manifestName(id))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("files: failed to remove manifest for %s: %v", id, err)
	}
}

// dispositionFilename makes an uploaded (attacker-influenced) display name
// safe to embed inside a Content-Disposition header value.
func dispositionFilename(name string) string {
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, `"`, "'")
	return name
}

// handleFileItem dispatches the "/api/files/<id>" subtree: GET/DELETE act
// directly on "<id>" (raw bytes / delete), while POST "<id>/hold" and POST
// "<id>/persist" drive the file-details dialog's state-changing buttons. None
// of these are gated on proxiedHeader — see relayedUploadHeader's comment for
// why that's a deliberate difference from upload.
func (s *Server) handleFileItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/files/")
	id, action, hasAction := strings.Cut(rest, "/")
	if id == "" || strings.Contains(id, "/") || strings.HasPrefix(id, ".") {
		http.NotFound(w, r)
		return
	}
	if hasAction {
		switch {
		case r.Method == http.MethodPost && action == "hold":
			s.handleFileHold(w, r, id)
		case r.Method == http.MethodPost && action == "persist":
			s.handleFilePersist(w, r, id)
		default:
			http.NotFound(w, r)
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleFileRaw(w, r)
	case http.MethodDelete:
		s.handleFileDelete(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleFileRaw serves one file's raw bytes: GET /api/files/<id>, whether it
// currently lives in the host-cache or the host-store. Without ?download=1
// the response is "inline" (the files tab's viewer embeds/fetches it
// directly); with ?download=1 it's an "attachment" so the browser saves it
// (the file details pane's download button links here). Unlike upload, this
// is not gated on proxiedHeader — a GET reaching this through
// agent-coordinator's /host/<id>/* proxy (e.g. from AC's read-only files tab)
// is served the same as a direct request; see docs/DistributedExchange.md.
// Manifest sidecars are not addressable here: any id starting with "."
// (which includes manifestPrefix) 404s, as does anything that isn't a single
// path segment.
func (s *Server) handleFileRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if id == "" || strings.Contains(id, "/") || strings.HasPrefix(id, ".") {
		http.NotFound(w, r)
		return
	}

	dir, ok := s.locateFile(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(dir, id))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	name := displayName(id)
	disposition := "inline"
	if r.URL.Query().Get("download") != "" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", disposition+`; filename="`+dispositionFilename(name)+`"`)
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// handleFileHold implements the file-details dialog's "hold" button: extends
// a host-cache entry's expiry to holdTTL from now and records that (plus the
// held flag itself) in its manifest sidecar so listFiles/the sweep both honor
// it, even across an LR restart -- see readManifest. Only host-cache entries
// can be held; an already-persisted file has nothing to hold.
func (s *Server) handleFileHold(w http.ResponseWriter, r *http.Request, id string) {
	path := filepath.Join(s.fileCacheDir, id)
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	info := FileInfo{
		ID:         id,
		Name:       displayName(id),
		Size:       fi.Size(),
		Kind:       classifyKind(id),
		State:      "held",
		UploadedAt: fi.ModTime().Unix(),
		ExpiresAt:  time.Now().Add(holdTTL).Unix(),
	}
	s.writeManifest(info)
	log.Printf("files: held %s (72h cache)", id)
	s.broadcastFiles()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

// handleFilePersist implements the file-details dialog's "persist" button
// (shown in place of "hold" once a file is held): moves the entry out of the
// host-cache into the host-store, where the sweep never looks, so it
// outlives holdTTL. The host-cache manifest sidecar is dropped in the move --
// a host-store entry needs no expiry bookkeeping, since it has none.
//
// This is reachable directly (not only after a prior hold) for robustness;
// the "hold first" flow is a UI convention, not a backend requirement.
func (s *Server) handleFilePersist(w http.ResponseWriter, r *http.Request, id string) {
	srcPath := filepath.Join(s.fileCacheDir, id)
	fi, err := os.Stat(srcPath)
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	if err := ensureFileCacheDir(s.hostStoreDir); err != nil {
		http.Error(w, "host-store dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	dstPath := filepath.Join(s.hostStoreDir, id)
	if err := moveFile(srcPath, dstPath); err != nil {
		http.Error(w, "persisting file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.removeManifest(id)
	log.Printf("files: persisted %s -> host-store/%s", id, id)

	info := FileInfo{
		ID:         id,
		Name:       displayName(id),
		Size:       fi.Size(),
		Kind:       classifyKind(id),
		State:      "persisted",
		UploadedAt: fi.ModTime().Unix(),
	}
	s.broadcastFiles()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

// handleFileDelete implements the file-details dialog's "delete" button
// (behind a confirm dialog in the UI): removes a file and its manifest
// sidecar from wherever it currently lives -- host-cache (cached or held) or
// host-store (persisted).
func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request, id string) {
	dir, ok := s.locateFile(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := os.Remove(filepath.Join(dir, id)); err != nil {
		http.Error(w, "deleting file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Remove(filepath.Join(dir, manifestName(id))); err != nil && !os.IsNotExist(err) {
		log.Printf("files: failed to remove manifest for %s: %v", id, err)
	}
	log.Printf("files: deleted %s", id)
	s.broadcastFiles()
	w.WriteHeader(http.StatusNoContent)
}

// moveFile renames src to dst, falling back to a copy-then-remove when
// they're on different filesystems (os.Rename returns syscall.EXDEV in that
// case) -- e.g. host-cache and host-store mounted separately.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}
