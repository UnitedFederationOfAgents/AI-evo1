package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// ---- "select from host" for the "Upload" source of Add Resources
// (Revision F of condocs/initialRobotImpls/Step4Prompt.md) ----
//
// Alongside uploading from the browser's machine, the "Upload" source can
// pick files on local-representative's host. condoccer doesn't read its own
// filesystem for this: the picker browses through GET /api/host-files here,
// which passes straight through to LR's own (local-representative/
// hostfiles.go), and POST /api/host-resource copies each chosen file from
// LR's GET /api/host-files/raw into the Impls folder.

// handleHostFiles implements GET /api/host-files?path=<dir> by passing it
// through to this condoccer's local-representative.
func (s *Server) handleHostFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	base, err := s.lrBase()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	resp, err := http.Get(base + "/api/host-files?" + r.URL.RawQuery)
	if err != nil {
		http.Error(w, "local-representative not reachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if ctype := resp.Header.Get("Content-Type"); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck — best-effort once the status is written
}

// hostResourceRequest is POST /api/host-resource's JSON body: the condoc
// (its main file relative to the repo root, as for /api/upload-resource),
// the block's optional name and description, and the host files to copy.
type hostResourceRequest struct {
	Path        string   `json:"path"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	HostPaths   []string `json:"host_paths"`
}

// handleHostResource implements POST /api/host-resource: the "select from
// host" counterpart of handleUploadResource, landing the same way.
func (s *Server) handleHostResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req hostResourceRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.HostPaths) == 0 {
		http.Error(w, "no host_paths provided", http.StatusBadRequest)
		return
	}
	base, err := s.lrBase()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	s.writeResourceFiles(w, req.Path, req.Name, req.Description, len(req.HostPaths),
		func(i int, destDir string) (resourceLink, string, error) {
			link, err := fetchHostResource(base, req.HostPaths[i], destDir)
			return link, req.HostPaths[i], err
		})
}

// fetchHostResource copies one file from the local-representative at base
// (GET /api/host-files/raw) into destDir under a collision-free id, named
// after the file itself.
func fetchHostResource(base, hostPath, destDir string) (resourceLink, error) {
	resp, err := http.Get(base + "/api/host-files/raw?path=" + url.QueryEscape(hostPath))
	if err != nil {
		return resourceLink{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resourceLink{}, fmt.Errorf("local-representative: %s", strings.TrimSpace(string(msg)))
	}
	return saveResourceFrom(path.Base(hostPath), resp.Body, destDir)
}
