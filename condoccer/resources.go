package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

// resourceTargetFile returns the step/substep file that receives a
// "## Resource N" block for the condoc described by info -- the same active
// file "revision"/"retry" target. Shared by addResource ("Highlighted"
// source) and handleUploadResource ("Upload" source, Revision C) so both
// "Add Resources" flows agree on where a block lands.
func resourceTargetFile(root string, info CondocInfo) (string, error) {
	switch {
	case info.SubstepFile != "":
		return filepath.Join(root, info.SubstepFile), nil
	case info.StepFile != "":
		return filepath.Join(root, info.StepFile), nil
	default:
		return "", fmt.Errorf("no active step or substep file")
	}
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

	targetFile, err := resourceTargetFile(s.root, info)
	if err != nil {
		return err
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

// ---- "Upload" source of Add Resources (Revision C) ----
//
// A second way to bring a file into a condoc's scope, alongside pulling
// local-representative's currently-highlighted files: a plain multipart
// upload straight from the browser, the same shape as local-representative's
// own files dialog (see local-representative/files.go's handleFileUpload),
// except the bytes land directly in the condoc's Impls folder instead of
// LR's host-cache -- no highlighting, no representable connection, no
// host-cache TTL sweep involved.

// maxResourceUploadSize caps a single "Upload" add-resource request,
// mirroring local-representative's own upload cap.
const maxResourceUploadSize = 64 << 20 // 64MiB

// handleUploadResource implements POST /api/upload-resource: multipart
// fields "path" (condoc's main file, relative to the repo root, same as
// handleResourceFile's "condoc" query param), optional "name"/"description",
// and one or more "file" parts. It resolves the active step/substep file the
// same way addResource does, asserts the .condoc lock for the same reason
// (writing into the Impls folder is itself a working-tree change
// local-representative's dev-repo watcher could notice), saves each
// uploaded file under a collision-free id, and inserts the resulting
// "## Resource N" block.
func (s *Server) handleUploadResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxResourceUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "invalid upload: "+err.Error(), http.StatusBadRequest)
		return
	}

	condocRel := r.FormValue("path")
	if condocRel == "" || filepath.IsAbs(condocRel) || strings.Contains(condocRel, "..") {
		http.Error(w, "invalid condoc path", http.StatusBadRequest)
		return
	}
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		http.Error(w, `no file provided (expected multipart field "file")`, http.StatusBadRequest)
		return
	}

	absPath := filepath.Join(s.root, filepath.FromSlash(condocRel))
	info, err := detectPhase(s.root, absPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	targetFile, err := resourceTargetFile(s.root, info)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// Same lock discipline as addResource: assert it before the first
	// uploaded byte lands in the Impls folder, not just around the markdown
	// edit.
	s.writeCondocLock(fmt.Sprintf("uploading resources into %s", info.Name))
	defer s.removeCondocLock()

	destDir := implDir(absPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	links := make([]resourceLink, 0, len(headers))
	for _, fh := range headers {
		link, err := saveUploadedResource(fh, destDir)
		if err != nil {
			http.Error(w, fmt.Sprintf("saving %s: %v", fh.Filename, err), http.StatusInternalServerError)
			return
		}
		links = append(links, link)
	}

	if err := insertResourceBlock(targetFile, r.FormValue("name"), r.FormValue("description"), links); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// saveUploadedResource writes one multipart file straight into a condoc's
// Impls folder under a collision-free "<8-hex>_<name>" filename -- the same
// on-disk shape fetchHighlightedFilesFrom keeps for files copied from
// local-representative's host-cache (see resourceLink.Filename) -- so both
// "Add Resources" sources land the same way.
func saveUploadedResource(fh *multipart.FileHeader, destDir string) (resourceLink, error) {
	src, err := fh.Open()
	if err != nil {
		return resourceLink{}, err
	}
	defer src.Close()

	name := sanitizeFilename(fh.Filename)
	id := randomID() + "_" + name
	dst, err := os.OpenFile(filepath.Join(destDir, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return resourceLink{}, err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(filepath.Join(destDir, id))
		return resourceLink{}, err
	}
	return resourceLink{Name: name, Filename: id}, nil
}

// sanitizeFilename strips directory components so an upload can't escape the
// Impls folder via its claimed filename (mirrors local-representative's
// files.go helper of the same name).
func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		name = "file"
	}
	return name
}

// randomID returns an 8-hex-character prefix used to make an uploaded
// resource's on-disk filename collision-free (mirrors local-representative's
// files.go helper of the same name).
func randomID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
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
