package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// ---- "Highlighted" and "From host" sources of Add Resource ----
//
// Alongside uploading a file from the browser's machine, a resource can come
// from local-representative: "Highlighted" attaches every file currently
// highlighted in LR's files tab (and, through it, every other LR connected
// to agent-coordinator), and "From host" attaches files picked on LR's host.
// session-manager doesn't read its own filesystem, it fetches over LR's
// GET /api/files, /api/files/<id>, /api/file-peers, /api/host-files and
// /api/host-files/raw -- all ungated on proxiedHeader -- and hands each
// file to "clauditable add-resource" like an upload.

// lrFetchTimeout bounds one request to local-representative for a resource.
const lrFetchTimeout = 2 * time.Minute

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

// lrResource is one file to fetch from a local-representative: its URL and
// the name it's attached under.
type lrResource struct {
	URL  string
	Name string
}

// handleAddHighlightedResources attaches every highlighted file to session id.
func (s *Server) handleAddHighlightedResources(c *wsClient, id string) {
	result := AnnotationResultMsg{ID: id, Kind: "resource"}
	if err := s.ensureLocalSession(id); err != nil {
		result.Error = err.Error()
		s.finishAnnotation(c, result)
		return
	}
	base, err := s.lrBase()
	if err != nil {
		result.Error = err.Error()
		s.finishAnnotation(c, result)
		return
	}
	files, err := listAllHighlightedFiles(base)
	if err == nil && len(files) == 0 {
		err = fmt.Errorf("no highlighted files on any local-representative -- highlight some in its files tab first")
	}
	if err != nil {
		result.Error = err.Error()
		s.finishAnnotation(c, result)
		return
	}
	s.addLRResources(c, id, files, result)
}

// handleAddHostResources attaches the files at hostPaths on
// local-representative's host to session id.
func (s *Server) handleAddHostResources(c *wsClient, id string, hostPaths []string) {
	result := AnnotationResultMsg{ID: id, Kind: "resource"}
	var files []lrResource
	base, err := s.lrBase()
	switch {
	case len(hostPaths) == 0:
		err = fmt.Errorf("no host files chosen")
	case err == nil:
		err = s.ensureLocalSession(id)
	}
	if err != nil {
		result.Error = err.Error()
		s.finishAnnotation(c, result)
		return
	}
	for _, p := range hostPaths {
		files = append(files, lrResource{
			URL:  base + "/api/host-files/raw?path=" + url.QueryEscape(p),
			Name: path.Base(p),
		})
	}
	s.addLRResources(c, id, files, result)
}

// addLRResources fetches and attaches each file in turn, stopping at the
// first failure; the reply names what was attached before it.
func (s *Server) addLRResources(c *wsClient, id string, files []lrResource, result AnnotationResultMsg) {
	var names []string
	for _, f := range files {
		record, err := s.addLRResource(id, f)
		if err != nil {
			result.Error = fmt.Sprintf("%s: %v", f.Name, err)
			if len(names) > 0 {
				result.Error += fmt.Sprintf(" (already added: %s)", strings.Join(names, ", "))
			}
			break
		}
		names = append(names, f.Name)
		result.Records = append(result.Records, record)
		result.Record = record
	}
	result.Name = strings.Join(names, ", ")
	if result.Error != "" && len(names) > 0 {
		// Some were attached: still refresh the transcript.
		s.sendToClient(c, "annotation-result", result)
		if view, err := viewSession(s.recordsPath, id); err == nil {
			s.sendToClient(c, "session-view", view)
		}
		s.broadcastSessions()
		return
	}
	s.finishAnnotation(c, result)
}

// addLRResource downloads one file (up to maxResourceBytes) to a temp file
// and attaches it, returning the new record ID.
func (s *Server) addLRResource(id string, f lrResource) (string, error) {
	httpClient := &http.Client{Timeout: lrFetchTimeout}
	resp, err := httpClient.Get(f.URL)
	if err != nil {
		return "", fmt.Errorf("local-representative not reachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if m := strings.TrimSpace(string(msg)); m != "" {
			return "", fmt.Errorf("local-representative: %s", m)
		}
		return "", fmt.Errorf("local-representative: unexpected status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxResourceBytes {
		return "", fmt.Errorf("over %d MiB", maxResourceBytes>>20)
	}

	tmp, err := os.CreateTemp("", "session-manager-resource-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxResourceBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n > maxResourceBytes {
		return "", fmt.Errorf("over %d MiB", maxResourceBytes>>20)
	}
	return s.runClauditable(id, nil, "add-resource", "-name", f.Name, tmp.Name())
}

// fileSource is one files tab listAllHighlightedFiles reads, as LR's
// GET /api/file-peers reports it: Node labels it, Base is where its
// GET /api/files and GET /api/files/<id> live.
type fileSource struct {
	Node string `json:"node"`
	Base string `json:"base"`
}

// listAllHighlightedFiles lists the highlighted files of this LR (at base)
// and of each other LR it reports from GET /api/file-peers. This LR failing
// is an error; a peer that can't be listed is skipped, so one unreachable
// node doesn't block the rest. A file id already listed from an earlier
// source is left out, and a peer's files are named "<name> (<node>)".
func listAllHighlightedFiles(base string) ([]lrResource, error) {
	seen := map[string]bool{}
	files, err := listHighlightedFiles(base, "", seen)
	if err != nil {
		return nil, err
	}
	for _, p := range listFilePeers(base) {
		got, err := listHighlightedFiles(p.Base, p.Node, seen)
		if err != nil {
			continue
		}
		files = append(files, got...)
	}
	return files, nil
}

// listFilePeers asks this LR for the other LRs connected to
// agent-coordinator (see local-representative/filepeers.go). Any failure,
// including an LR that predates GET /api/file-peers, just means no peers.
func listFilePeers(base string) []fileSource {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(base + "/api/file-peers")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var msg struct {
		Peers []fileSource `json:"peers"`
	}
	if json.NewDecoder(resp.Body).Decode(&msg) != nil {
		return nil
	}
	return msg.Peers
}

// listHighlightedFiles lists one files tab's highlighted files, skipping
// (and adding to) seen.
func listHighlightedFiles(base, node string, seen map[string]bool) ([]lrResource, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(base + "/api/files")
	if err != nil {
		return nil, fmt.Errorf("listing local-representative's files: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing local-representative's files: unexpected status %d", resp.StatusCode)
	}
	var listing struct {
		Files []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Highlighted bool   `json:"highlighted"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return nil, fmt.Errorf("listing local-representative's files: %w", err)
	}
	var files []lrResource
	for _, f := range listing.Files {
		if !f.Highlighted || seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		name := f.Name
		if name == "" {
			name = f.ID
		}
		if node != "" {
			name = fmt.Sprintf("%s (%s)", name, node)
		}
		files = append(files, lrResource{URL: base + "/api/files/" + url.PathEscape(f.ID) + "?download=1", Name: name})
	}
	return files, nil
}

// handleHostFiles implements GET /api/host-files?path=<dir> -- the "From
// host" picker's listing -- by passing it through to local-representative.
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
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Get(base + "/api/host-files?" + r.URL.RawQuery)
	if err != nil {
		http.Error(w, "local-representative not reachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if ctype := resp.Header.Get("Content-Type"); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck -- best-effort once the status is written
}
