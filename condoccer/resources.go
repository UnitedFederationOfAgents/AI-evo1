package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ---- "Add Resources" (see Revision A of
// condocs/initialDistributedDevelopmentImpls/Step5SubstepRPrompt.md) ----
//
// The condoccer half of bringing local-representative's highlighted files
// into a condoc's scope: pull every currently-highlighted file from LR, copy
// it into the condoc's Impls folder, and insert a "## Resource N" block
// linking to it into the active step/substep file. (Revision B dropped the
// original "## Resource (N)" parens and added an optional " -- <name>"
// suffix; see insertResourceBlock.)

// resourceLink is one file copied into a condoc's Impls folder by an
// add_resource action, carried through to the inserted "## Resource N"
// block's link.
type resourceLink struct {
	Name     string // original filename, for the link text
	Filename string // on-disk filename inside the Impls folder (the link target); the host-cache's own <8-hex>_<name> id, kept as-is to stay collision-free
}

// addResource implements the "add_resource" action: resolves the active
// step/substep file the same way "revision"/"retry" do, pulls every
// highlighted file from local-representative into the condoc's Impls folder,
// and inserts a "## Resource N" block linking to them just above the
// pending revision/retry placeholder. The .condoc lock is asserted for the
// duration -- unlike an ordinary phase transition, this doesn't change
// info.Phase, so nothing else would otherwise stop local-representative's
// dev-repo watcher from rebuilding out from under the copy+edit.
func (s *Server) addResource(mainPath string, info CondocInfo, action ActionRequest) error {
	if action.ResourceType != "highlighted" {
		return fmt.Errorf("unknown resource type: %q", action.ResourceType)
	}

	var targetFile string
	switch {
	case info.SubstepFile != "":
		targetFile = filepath.Join(s.root, info.SubstepFile)
	case info.StepFile != "":
		targetFile = filepath.Join(s.root, info.StepFile)
	default:
		return fmt.Errorf("no active step or substep file")
	}

	// Assert the lock before the copy even starts, not just around the
	// markdown edit: dropping new files into the condoc's Impls folder is
	// itself a working-tree change local-representative's dev-repo watcher
	// could notice, same as the file it links from.
	s.writeCondocLock(fmt.Sprintf("copying resources into %s", info.Name))
	defer s.removeCondocLock()

	links, err := s.fetchHighlightedFiles(implDir(mainPath))
	if err != nil {
		return err
	}
	if len(links) == 0 {
		return fmt.Errorf("no highlighted files on local-representative")
	}

	return insertResourceBlock(targetFile, action.ResourceName, action.Content, links)
}

// fetchHighlightedFiles asks local-representative (over the same connection
// condoccer already maintains for status/commands -- see repr.go) which
// files are currently highlighted, and copies each one's bytes into destDir.
// It talks straight HTTP to LR's own dashboard port, disclosed over the
// representable connection's "hello" message (see representable.Client.
// PeerHTTPPort) -- LR's GET /api/files and GET /api/files/<id> are
// deliberately ungated on proxiedHeader (see docs/DistributedExchange.md),
// so this same call would also work unmodified if condoccer were ever
// reached through agent-coordinator's transparent proxy instead of dialing
// LR directly.
func (s *Server) fetchHighlightedFiles(destDir string) ([]resourceLink, error) {
	s.reprMu.Lock()
	client := s.reprClient
	host := s.reprHost
	s.reprMu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("not connected to local-representative")
	}
	lrHTTPPort := client.PeerHTTPPort()
	if lrHTTPPort == "" {
		return nil, fmt.Errorf("local-representative has not disclosed its HTTP port")
	}
	base := "http://" + net.JoinHostPort(host, lrHTTPPort)
	return fetchHighlightedFilesFrom(base, destDir)
}

// fetchHighlightedFilesFrom does the actual HTTP work for
// fetchHighlightedFiles against a resolved "http://host:port" base URL --
// split out so it can be exercised against an httptest.Server without a real
// representable connection.
func fetchHighlightedFilesFrom(base, destDir string) ([]resourceLink, error) {
	resp, err := http.Get(base + "/api/files")
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

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, err
	}

	var links []resourceLink
	for _, f := range listing.Files {
		if !f.Highlighted {
			continue
		}
		if err := downloadFile(base, f.ID, destDir); err != nil {
			return nil, fmt.Errorf("copying %s: %w", f.Name, err)
		}
		links = append(links, resourceLink{Name: f.Name, Filename: f.ID})
	}
	return links, nil
}

// downloadFile streams one host-cache/host-store file's raw bytes from
// local-representative's content endpoint into destDir, keeping its
// existing (already collision-free) on-disk id as the filename.
func downloadFile(base, id, destDir string) error {
	resp, err := http.Get(base + "/api/files/" + id + "?download=1")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	out, err := os.Create(filepath.Join(destDir, id))
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

// nextResourceNum returns the next "## Resource N" number for a step or
// substep file's content, based on the highest N already present (0 if none).
func nextResourceNum(content string) int {
	n := 0
	for _, m := range resourceHeadingRe.FindAllStringSubmatch(content, -1) {
		var num int
		fmt.Sscanf(m[1], "%d", &num)
		if num > n {
			n = num
		}
	}
	return n + 1
}

// insertResourceBlock inserts a new "## Resource N" block -- optionally
// "## Resource N -- <name>" when a display name is given (Revision B; also
// where the original "## Resource (N)" parens were dropped) -- the optional
// description followed by a link per file in links -- immediately above the
// file's pending "## <REPLACE-Revision|Retry> X" placeholder (see
// replaceIterationPlaceholder), with blank-line spacing matching the rest of
// a step/substep file's sections.
func insertResourceBlock(path, name, description string, links []resourceLink) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)

	loc := placeholderLineRe.FindStringIndex(content)
	if loc == nil {
		return fmt.Errorf("no pending revision/retry placeholder found in %s", filepath.Base(path))
	}

	n := nextResourceNum(content)
	heading := fmt.Sprintf("## Resource %d", n)
	if nm := strings.TrimSpace(name); nm != "" {
		heading += " -- " + nm
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", heading)
	if desc := strings.TrimSpace(description); desc != "" {
		fmt.Fprintf(&b, "%s\n\n", desc)
	}
	for _, l := range links {
		fmt.Fprintf(&b, "- [%s](%s)\n", l.Name, l.Filename)
	}
	b.WriteString("\n")

	before := strings.TrimRight(content[:loc[0]], "\n")
	after := content[loc[0]:]
	newContent := before + "\n\n" + b.String() + after
	return os.WriteFile(path, []byte(newContent), 0644)
}

// handleResourceFile serves one file's raw bytes out of a condoc's Impls
// folder: GET /api/resource/<filename>?condoc=<mainPath>[&download=1]. This
// is what lets the frontend render a "## Resource N" block's linked image or
// text file inline, and offer a full-size view for images -- condoccer
// otherwise serves nothing but its own embedded frontend and /ws (see
// setupRoutes). It's reverse-proxied the same way as the rest of condoccer's
// UI (local-representative's proxyToCondoccer, agent-coordinator's
// equivalent), so this works unmodified whether reached directly or through
// either proxy.
//
// condoc identifies which condoc's Impls folder to look in by its main file's
// path relative to the repo root (CondocInfo.Path, already known to the
// frontend) rather than the Impls folder itself, so a client can't be tricked
// into asking for an arbitrary directory. filename must be a single path
// segment (mirrors local-representative's handleFileRaw); condoc must resolve
// within the repo root.
func (s *Server) handleResourceFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filename := strings.TrimPrefix(r.URL.Path, "/api/resource/")
	if filename == "" || strings.Contains(filename, "/") || strings.HasPrefix(filename, ".") {
		http.NotFound(w, r)
		return
	}
	condocRel := r.URL.Query().Get("condoc")
	if condocRel == "" || filepath.IsAbs(condocRel) || strings.Contains(condocRel, "..") {
		http.NotFound(w, r)
		return
	}

	dir := implDir(filepath.Join(s.root, filepath.FromSlash(condocRel)))
	f, err := os.Open(filepath.Join(dir, filename))
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

	disposition := "inline"
	if r.URL.Query().Get("download") != "" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", disposition+`; filename="`+dispositionFilename(filename)+`"`)
	ctype := mime.TypeByExtension(filepath.Ext(filename))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	http.ServeContent(w, r, filename, info.ModTime(), f)
}

// dispositionFilename makes a filename safe to embed inside a
// Content-Disposition header value (mirrors local-representative's files.go
// helper of the same name).
func dispositionFilename(name string) string {
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, `"`, "'")
	return name
}
