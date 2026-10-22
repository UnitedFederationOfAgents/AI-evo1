package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ---- "to files" on a resource (Revision E of
// condocs/slopspaceSlopspaceCondocsImpls/Step1Prompt.md) ----
//
// The reverse of the "Highlighted" source of Add Resources: a resource
// block's "to files" button copies the files it links, out of the condoc's
// Impls folder, into this condoccer's local-representative's host-cache --
// the files tab -- as ordinary uploads (LR's POST /api/files, the same
// route its own files dialog uses). Nothing in the condoc changes.

// toFilesRequest is POST /api/resource-to-files's JSON body: the condoc, as
// handleResourceFile's "condoc" query param, and the linked files (each a
// single filename in its Impls folder) to copy.
type toFilesRequest struct {
	Condoc    string   `json:"condoc"`
	Filenames []string `json:"filenames"`
}

// maxToFilesSize mirrors local-representative's per-request upload cap: one
// file is sent per request, so it's also the largest file that can go.
const maxToFilesSize = 64 << 20 // 64MiB

// handleResourceToFiles implements POST /api/resource-to-files, answering
// {"copied": [<names as uploaded>]}. Every file is checked before any is
// sent, so a bad filename copies nothing; a failure partway through reports
// which file it stopped at.
func (s *Server) handleResourceToFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req toFilesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Filenames) == 0 {
		http.Error(w, "no filenames provided", http.StatusBadRequest)
		return
	}
	_, mainPath, err := s.resolveCondoc(req.Condoc)
	if err != nil {
		http.Error(w, "unknown condoc", http.StatusNotFound)
		return
	}
	dir := implDir(mainPath)
	for _, name := range req.Filenames {
		if err := checkToFilesSource(dir, name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	base, err := s.lrBase()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	copied := make([]string, 0, len(req.Filenames))
	for _, name := range req.Filenames {
		uploaded, err := uploadToFiles(base, dir, name)
		if err != nil {
			http.Error(w, fmt.Sprintf("copying %s to files: %v", name, err), http.StatusBadGateway)
			return
		}
		copied = append(copied, uploaded)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string][]string{"copied": copied})
}

// checkToFilesSource checks name is a single, visible regular file in dir
// that fits in one upload -- the same filename rules as handleResourceFile.
func checkToFilesSource(dir, name string) error {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid filename %q", name)
	}
	fi, err := os.Stat(filepath.Join(dir, name))
	if err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("no resource file %q", name)
	}
	if fi.Size() > maxToFilesSize {
		return fmt.Errorf("%s is over %s, too large for the files tab", name, formatBytes(maxToFilesSize))
	}
	return nil
}

// uploadToFiles sends dir/name to the local-representative at base as a
// files-tab upload, named as originally added (see resourceOriginalName) --
// LR gives it its own collision-free id. Returns the name it was sent as.
func uploadToFiles(base, dir, name string) (string, error) {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	defer f.Close()

	uploadName := resourceOriginalName(name)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", uploadName)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	resp, err := http.Post(base+"/api/files", mw.FormDataContentType(), &body)
	if err != nil {
		return "", fmt.Errorf("local-representative not reachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("local-representative: %s", strings.TrimSpace(string(msg)))
	}
	return uploadName, nil
}

// ---- already in files (Revision F) ----
//
// A resource whose files are all already in the files tab gets its "to
// files" button lit; pressing it then goes to the files tab with that file
// selected rather than copying it again (see the frontend's ToFilesButton).

// lrFile is the part of local-representative's FileInfo (its files.go) the
// presence check reads.
type lrFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// handleResourceInFiles implements GET /api/resource-in-files?condoc=<path>
// &filename=<name>[&filename=...], answering {"present": {<filename>: <LR
// file id>}} for each linked file already in this condoccer's
// local-representative's files tab. Not being connected to LR answers an
// empty map rather than an error: the button just isn't lit.
func (s *Server) handleResourceInFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	names := q["filename"]
	if len(names) == 0 {
		http.Error(w, "no filenames provided", http.StatusBadRequest)
		return
	}
	_, mainPath, err := s.resolveCondoc(q.Get("condoc"))
	if err != nil {
		http.Error(w, "unknown condoc", http.StatusNotFound)
		return
	}
	dir := implDir(mainPath)
	for _, name := range names {
		if err := checkToFilesSource(dir, name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	present := map[string]string{}
	if base, err := s.lrBase(); err == nil {
		present, err = findInFiles(base, dir, names)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]map[string]string{"present": present})
}

// findInFiles maps each of names (files in dir) to the id of a file in the
// files tab of the local-representative at base with the same content: a
// file named as the resource was originally added (see
// resourceOriginalName), of the same size, whose bytes match. LR lists
// newest first, so the newest copy wins. Names with no copy are left out.
func findInFiles(base, dir string, names []string) (map[string]string, error) {
	resp, err := http.Get(base + "/api/files")
	if err != nil {
		return nil, fmt.Errorf("local-representative not reachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("local-representative: listing files: %s", resp.Status)
	}
	var listing struct {
		Files []lrFile `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return nil, fmt.Errorf("local-representative: listing files: %w", err)
	}

	present := map[string]string{}
	for _, name := range names {
		local, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		want := resourceOriginalName(name)
		for _, f := range listing.Files {
			if f.Name != want || f.Size != int64(len(local)) {
				continue
			}
			if same, err := lrFileEquals(base, f.ID, local); err == nil && same {
				present[name] = f.ID
				break
			}
		}
	}
	return present, nil
}

// lrFileEquals reports whether the files-tab file id at base holds exactly
// content.
func lrFileEquals(base, id string, content []byte) (bool, error) {
	resp, err := http.Get(base + "/api/files/" + url.PathEscape(id))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("fetching %s: %s", id, resp.Status)
	}
	got, err := io.ReadAll(io.LimitReader(resp.Body, int64(len(content))+1))
	if err != nil {
		return false, err
	}
	return bytes.Equal(got, content), nil
}

// resourceOriginalName strips the "<8-hex>_" prefix a resource's on-disk
// filename carries (see resourceLink.Filename), e.g.
// "43e77743_agent-resource-flow.svg" -> "agent-resource-flow.svg", so the
// files tab doesn't stack a second prefix on the first. A name without one
// is returned as-is.
func resourceOriginalName(filename string) string {
	if len(filename) > 9 && filename[8] == '_' {
		if _, err := hex.DecodeString(filename[:8]); err == nil {
			return filename[9:]
		}
	}
	return filename
}
