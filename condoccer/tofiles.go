package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
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
