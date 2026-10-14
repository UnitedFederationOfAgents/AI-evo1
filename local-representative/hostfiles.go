package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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
// the host-cache, and host folders to zip into it (Step5SubstepGPrompt.md
// Revision O: the picker's "to zip" button).
type hostImportRequest struct {
	HostPaths   []string `json:"host_paths"`
	HostFolders []string `json:"host_folders,omitempty"`
}

// importHostFiles handles a POST /api/files whose body is JSON rather than
// multipart: each of host_paths is copied into the host-cache under its own
// name, as if it had been uploaded, and each of host_folders is saved there
// as "<folder>.zip". It runs behind handleFileUpload's gate, so it's allowed
// from a direct client or agent-coordinator's upload relay (which forwards
// the body and Content-Type untouched). Paths that can't be read are logged
// and skipped; it fails only if nothing was saved.
func (s *Server) importHostFiles(w http.ResponseWriter, r *http.Request) {
	var req hostImportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid host import: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.HostPaths) == 0 && len(req.HostFolders) == 0 {
		http.Error(w, "no host_paths or host_folders provided", http.StatusBadRequest)
		return
	}
	saved := make([]FileInfo, 0, len(req.HostPaths)+len(req.HostFolders))
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
	for _, p := range req.HostFolders {
		info, err := s.importHostFolder(p)
		if err != nil {
			log.Printf("files: zip of host folder %q failed: %v", p, err)
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

// Limits on a zipped host folder, so picking something like the home folder
// fails instead of filling the host-cache's disk.
const (
	maxHostZipBytes = 512 << 20 // total size of the files read into the zip
	maxHostZipFiles = 20000
)

// importHostFolder zips one host folder into the host-cache as
// "<folder>.zip". The zip is streamed straight into the host-cache file;
// if it fails partway, saveFileFrom removes what was written.
func (s *Server) importHostFolder(p string) (FileInfo, error) {
	dir, err := resolveHostPath(p)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return FileInfo{}, err
	}
	if !info.IsDir() {
		return FileInfo{}, fmt.Errorf("%s is not a folder", dir)
	}
	name := filepath.Base(dir)
	if name == string(filepath.Separator) || name == "." {
		name = "root"
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(zipHostFolder(pw, dir))
	}()
	saved, err := s.saveFileFrom(name+".zip", pr)
	pr.Close() // stops the zipper if saving failed first
	return saved, err
}

// zipHostFolder writes dir's contents to w as a zip, with paths relative to
// dir. Symlinks, sockets and the like are skipped rather than followed, and
// so are files and subfolders that can't be read; it fails only past the
// maxHostZip limits or if w does.
func zipHostFolder(w io.Writer, dir string) error {
	zw := zip.NewWriter(w)
	var total int64
	files, skipped := 0, 0
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			skipped++
			if d != nil && d.IsDir() && path != dir {
				return fs.SkipDir
			}
			if path == dir {
				return err
			}
			return nil
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			_, err := zw.Create(rel + "/")
			return err
		}
		if !d.Type().IsRegular() {
			skipped++
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			skipped++
			return nil
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			skipped++
			return nil
		}
		files++
		total += info.Size()
		if files > maxHostZipFiles {
			return fmt.Errorf("%s has more than %d files to zip", dir, maxHostZipFiles)
		}
		if total > maxHostZipBytes {
			return fmt.Errorf("%s holds more than %d MiB to zip", dir, maxHostZipBytes>>20)
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = rel
		hdr.Method = zip.Deflate
		dst, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = io.Copy(dst, f)
		return err
	})
	if walkErr != nil {
		return walkErr
	}
	if skipped > 0 {
		log.Printf("files: zipping %s skipped %d unreadable or non-regular entries", dir, skipped)
	}
	return zw.Close()
}
