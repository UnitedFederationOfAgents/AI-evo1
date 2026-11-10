package main

// The "Highlighted" source of Add resource (sloppo
// condocv2WithDiagrammographerImpls Step 1, Prompt 13): the files highlighted
// in local-representative's files tab -- its "highlight" button, the yellow
// ring -- and in the files tab of every other LR connected to
// agent-coordinator, as session-manager's Add Resource takes them
// (session-manager/hostresources.go). condocv2 reads them over LR's HTTP
// dashboard rather than from the host-cache on disk, so the peers' come too:
//
//	GET <lr>/api/files          this LR's files, each with "highlighted"
//	GET <lr>/api/file-peers     the other LRs, each as a base URL that
//	                            serves the same two calls through AC
//	GET <base>/api/files/<id>?download=1
//
// all ungated on proxiedHeader (local-representative/filepeers.go).

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// lrFetchTimeout bounds fetching one highlighted file.
const lrFetchTimeout = 2 * time.Minute

// HighlightedFile is one highlighted file, for the resource form.
type HighlightedFile struct {
	Key  string `json:"key"`            // "<id>", or "<node>/<id>" for a peer's
	ID   string `json:"id"`             // its id in its files tab
	Name string `json:"name"`           // its name as uploaded
	Node string `json:"node,omitempty"` // the peer it is on ("" this host's LR)
	Size int64  `json:"size"`

	src string // where its content is
}

// lrBase is the base URL of the connected local-representative's dashboard:
// the host condocv2 reached it on, and the HTTP port its "hello" disclosed.
func (s *Server) lrBase() (string, error) {
	s.reprMu.Lock()
	client, host := s.reprClient, s.reprHost
	s.reprMu.Unlock()
	if client == nil {
		return "", fmt.Errorf("not connected to local-representative, which keeps the highlighted files")
	}
	port := client.PeerHTTPPort()
	if port == "" {
		return "", fmt.Errorf("local-representative has not disclosed its HTTP port")
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// highlightedFiles lists the highlighted files for the "list-highlighted"
// message.
func (s *Server) highlightedFiles() ([]HighlightedFile, error) {
	base, err := s.lrBase()
	if err != nil {
		return nil, err
	}
	return listAllHighlighted(base)
}

// listAllHighlighted lists the highlighted files of the LR at base and of
// each peer it reports. That LR failing is an error; a peer that can't be
// listed is skipped, so one unreachable node doesn't hide the rest. A file
// id already listed is left out.
func listAllHighlighted(base string) ([]HighlightedFile, error) {
	seen := map[string]bool{}
	files, err := listHighlighted(base, "", seen)
	if err != nil {
		return nil, err
	}
	for _, p := range listFilePeers(base) {
		if got, err := listHighlighted(p.Base, p.Node, seen); err == nil {
			files = append(files, got...)
		}
	}
	return files, nil
}

// filePeer is one entry of GET /api/file-peers.
type filePeer struct {
	Node string `json:"node"`
	Base string `json:"base"`
}

// listFilePeers asks the LR at base for its peers. Any failure, an LR from
// before GET /api/file-peers included, just means none.
func listFilePeers(base string) []filePeer {
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(base + "/api/file-peers")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var msg struct {
		Peers []filePeer `json:"peers"`
	}
	if json.NewDecoder(resp.Body).Decode(&msg) != nil {
		return nil
	}
	return msg.Peers
}

// listHighlighted lists one files tab's highlighted files, skipping (and
// adding to) seen.
func listHighlighted(base, node string, seen map[string]bool) ([]HighlightedFile, error) {
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(base + "/api/files")
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
			Size        int64  `json:"size"`
			Highlighted bool   `json:"highlighted"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return nil, fmt.Errorf("listing local-representative's files: %w", err)
	}
	var files []HighlightedFile
	for _, f := range listing.Files {
		if !f.Highlighted || f.ID == "" || seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		h := HighlightedFile{Key: f.ID, ID: f.ID, Name: f.Name, Node: node, Size: f.Size,
			src: base + "/api/files/" + url.PathEscape(f.ID) + "?download=1"}
		if h.Name == "" {
			h.Name = hostFileName(f.ID)
		}
		if node != "" {
			h.Key = node + "/" + f.ID
		}
		files = append(files, h)
	}
	return files, nil
}

// highlightedResourceFiles fetches the highlighted files keys names -- all
// of them when keys is empty -- from the LR at base and its peers. They are
// listed afresh, so a file un-highlighted since the form listed it is
// refused rather than taken.
func highlightedResourceFiles(base string, keys []string) ([]resourceFile, error) {
	listed, err := listAllHighlighted(base)
	if err != nil {
		return nil, err
	}
	if len(listed) == 0 {
		return nil, fmt.Errorf("no files are highlighted -- highlight some in local-representative's files tab first")
	}
	take := listed
	if len(keys) > 0 {
		byKey := map[string]HighlightedFile{}
		for _, h := range listed {
			byKey[h.Key] = h
		}
		take = nil
		for _, k := range keys {
			h, ok := byKey[k]
			if !ok {
				return nil, fmt.Errorf("%s is no longer highlighted", k)
			}
			take = append(take, h)
		}
	}
	if len(take) > maxResourceFiles {
		return nil, fmt.Errorf("%d files: a resource takes at most %d", len(take), maxResourceFiles)
	}
	var files []resourceFile
	left := int64(maxResourceBytes)
	for _, h := range take {
		b, err := fetchLRFile(h.src, left)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", h.Name, err)
		}
		left -= int64(len(b))
		files = append(files, resourceFile{name: h.Name, data: b})
	}
	return files, nil
}

// fetchLRFile downloads one file of at most limit bytes.
func fetchLRFile(u string, limit int64) ([]byte, error) {
	resp, err := (&http.Client{Timeout: lrFetchTimeout}).Get(u)
	if err != nil {
		return nil, fmt.Errorf("local-representative not reachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if m := strings.TrimSpace(string(msg)); m != "" {
			return nil, fmt.Errorf("local-representative: %s", m)
		}
		return nil, fmt.Errorf("local-representative: unexpected status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("the resource's files come to over %s", byteSize(maxResourceBytes))
	}
	return b, nil
}
