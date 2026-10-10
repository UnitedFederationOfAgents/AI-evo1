package main

import (
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---- "select from host" (Step4Prompt.md Revision F of
// condocs/initialRobotImpls) ----
//
// The file pickers in LR's and AC's frontends (files-tab upload, control
// YAML import) and condoccer's "Upload" resource source can pick a file from
// this LR's host instead of from the browser's machine. GET /api/host-files
// lists one directory, GET /api/host-files/raw serves one file's bytes, and
// POST /api/files with a JSON body (see importHostFiles) copies host files
// into the host-cache the way an upload would.
//
// Like GET /api/files/<id>, the two reads aren't gated on proxiedHeader:
// agent-coordinator's per-host tabs browse through its transparent
// /host/<id>/* proxy, and condoccer reaches them by dialing this LR. The
// import is a write, so it goes through handleFileUpload's gate.

// HostFileEntry is one entry in a host directory listing.
type HostFileEntry struct {
	Name    string `json:"name"`
	Dir     bool   `json:"dir"` // a directory, or a symlink to one
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"` // unix seconds
}

// HostDirListing is GET /api/host-files' reply.
type HostDirListing struct {
	Path    string          `json:"path"`   // absolute, cleaned
	Parent  string          `json:"parent"` // "" at the root
	Home    string          `json:"home"`
	Entries []HostFileEntry `json:"entries"` // directories first, then by name
}

// hostHome is the directory a host listing starts in: the LR user's home,
// or the root if that can't be found.
func hostHome() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return string(filepath.Separator)
}

// resolveHostPath turns a picker path into an absolute, cleaned one: "" is
// the home directory, and a leading "~" stands for it. A relative path is
// refused rather than guessed at.
func resolveHostPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	home := hostHome()
	switch {
	case p == "" || p == "~":
		return home, nil
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not an absolute path", p)
	}
	return filepath.Clean(p), nil
}

// listHostDir lists dir for the picker. Entries that vanish or can't be
// stat'ed mid-listing are skipped.
func listHostDir(dir string) (HostDirListing, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return HostDirListing{}, err
	}
	out := HostDirListing{Path: dir, Home: hostHome(), Entries: make([]HostFileEntry, 0, len(entries))}
	if parent := filepath.Dir(dir); parent != dir {
		out.Parent = parent
	}
	for _, e := range entries {
		// Stat follows symlinks, so a link to a directory can be opened.
		info, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		ent := HostFileEntry{Name: e.Name(), Dir: info.IsDir(), ModTime: info.ModTime().Unix()}
		if !ent.Dir {
			ent.Size = info.Size()
		}
		out.Entries = append(out.Entries, ent)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, nil
}

// handleHostFiles serves GET /api/host-files?path=<dir>: one directory of
// this host, for the "select from host" picker.
func (s *Server) handleHostFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir, err := resolveHostPath(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	listing, err := listHostDir(dir)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case os.IsNotExist(err):
			status = http.StatusNotFound
		case os.IsPermission(err):
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(listing)
}

// openHostFile opens a regular file the picker chose.
func openHostFile(p string) (*os.File, os.FileInfo, error) {
	path, err := resolveHostPath(p)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", path)
	}
	return f, info, nil
}

// handleHostFileRaw serves GET /api/host-files/raw?path=<file>: one host
// file's bytes, as an attachment named after it.
func (s *Server) handleHostFileRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, info, err := openHostFile(r.URL.Query().Get("path"))
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case os.IsNotExist(err):
			status = http.StatusNotFound
		case os.IsPermission(err):
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	defer f.Close()
	name := filepath.Base(f.Name())
	w.Header().Set("Content-Disposition", `attachment; filename="`+dispositionFilename(name)+`"`)
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// hostImportRequest is POST /api/files' JSON body: host paths to copy into
// the host-cache.
type hostImportRequest struct {
	HostPaths []string `json:"host_paths"`
}

// importHostFiles handles a POST /api/files whose body is JSON rather than
// multipart: each of host_paths is copied into the host-cache under its own
// name, as if it had been uploaded. It runs behind handleFileUpload's gate,
// so it's allowed from a direct client or agent-coordinator's upload relay
// (which forwards the body and Content-Type untouched). Files that can't be
// read are logged and skipped; it fails only if none were saved.
func (s *Server) importHostFiles(w http.ResponseWriter, r *http.Request) {
	var req hostImportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid host import: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.HostPaths) == 0 {
		http.Error(w, "no host_paths provided", http.StatusBadRequest)
		return
	}
	saved := make([]FileInfo, 0, len(req.HostPaths))
	var errs []string
	for _, p := range req.HostPaths {
		info, err := s.importHostFile(p)
		if err != nil {
			log.Printf("files: import of host file %q failed: %v", p, err)
			errs = append(errs, err.Error())
			continue
		}
		saved = append(saved, info)
	}
	if len(saved) == 0 {
		http.Error(w, "no file could be imported: "+strings.Join(errs, "; "), http.StatusBadRequest)
		return
	}
	s.broadcastFiles()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(FilesStateMsg{Files: saved})
}

// importHostFile copies one host file into the host-cache.
func (s *Server) importHostFile(p string) (FileInfo, error) {
	f, _, err := openHostFile(p)
	if err != nil {
		return FileInfo{}, err
	}
	defer f.Close()
	return s.saveFileFrom(filepath.Base(f.Name()), f)
}
