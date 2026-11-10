package main

// Resources (sloppo condocv2WithDiagrammographerImpls Step 1, Prompt 11): the
// human can add a resource to a condoc between interactions -- files the
// agent should have to hand, as condoccer v1's step prompts carried their
// "Resource N" sections. The files are brought in from
//
//	upload  the browser, from the human's own machine
//	files   this host's files tab: its host-cache and host-store
//	        (local-representative's exchange directories, see
//	        local-representative/files.go)
//	text    pasted into the form, under a name of its choosing
//	highlighted  every file highlighted in local-representative's files
//	        tab, and in its peers' (Prompt 13, see highlighted.go)
//	voice   text dictated with The Conversationalist, kept as one file
//	        as text is
//
// and kept in the condoc repo beside the step files, under resources/<R>/ --
// R numbers the condoc's resources from 1, whichever scope they were added
// in -- with a section in the current scope's file listing them:
//
//	## Resource <R>   its title and its files, linked (the resource commit)
//
//	resource N.R (condoc)    the files and the section
//	  -> resource N.R (pointer)  kept on the pointer's resources
//
// N is the interactions completed when it was added: like an external change
// (external.go) it is a section of its own after interaction N on the Visual
// tab -- a dot in the pointer lane and a green circle with a file in the
// condoc lane, as a step's entry is a dot beside a green circle with an
// arrow. As with a step, the condoc commit comes first and a failure after it
// is retried by doing the same again; both are made between interactions
// only. From the next interaction on the agent reads the section in its step
// file and the files beside it.

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dungeon-keeper/pkg/gitauth"
)

// Where a resource's files came from (Resource.Source).
const (
	resourceFromUpload      = "upload"
	resourceFromFiles       = "files"
	resourceFromText        = "text"
	resourceFromHighlighted = "highlighted" // highlighted.go
	resourceFromVoice       = "voice"
)

const (
	maxResourceFiles = 50
	maxResourceBytes = 50 << 20 // all of one resource's files together
)

// The host's files tab directories (local-representative's
// defaultFileCacheDir and defaultHostStoreDir).
const (
	defaultHostCacheDir = "/host-agent-files/exchange/host-cache"
	defaultHostStoreDir = "/host-agent-files/exchange/host-store"
)

// resourceHeadingRe matches a resource's section in a scope file.
var resourceHeadingRe = regexp.MustCompile(`(?m)^## Resource (\d+)[ \t]*$`)

// Resource is one resource added, kept on the pointer.
type Resource struct {
	Number      int    // R: 1, 2, ... across the whole condoc
	Interaction int    // N: the interactions completed when it was added
	Step        string // the scope it was added in ("" the main scope)
	Title       string
	Source      string   // resourceFromUpload, resourceFromFiles, resourceFromText, resourceFromHighlighted or resourceFromVoice
	Files       []string // its files' names, under resources/<R>/
	Time        int64    // unix seconds
}

// ResourceEntry is a resource as the UI lists it (PointerEntry.Resources).
type ResourceEntry struct {
	Number      int      `json:"number"`
	Interaction int      `json:"interaction"`
	Step        string   `json:"step,omitempty"`
	Title       string   `json:"title,omitempty"`
	Source      string   `json:"source"`
	Files       []string `json:"files"`
	Time        int64    `json:"time,omitempty"`
}

func resourceEntries(rs []Resource) []ResourceEntry {
	var out []ResourceEntry
	for _, r := range rs {
		out = append(out, ResourceEntry{Number: r.Number, Interaction: r.Interaction, Step: r.Step, Title: r.Title,
			Source: r.Source, Files: r.Files, Time: r.Time})
	}
	return out
}

// ResourceRequest is an "add-resource" message: the resource's title and its
// files from one source.
type ResourceRequest struct {
	Stem   string `json:"stem"`
	Title  string `json:"title"`
	Source string `json:"source"`
	// upload: each file's name and its content, base64.
	Uploads []ResourceUpload `json:"uploads,omitempty"`
	// files: the host files tab entries' ids.
	HostFiles []string `json:"hostFiles,omitempty"`
	// highlighted: the highlighted files' keys (HighlightedFile.Key) to take,
	// or none for all of them.
	Highlighted []string `json:"highlighted,omitempty"`
	// text and voice: one file, named Name.
	Name string `json:"name,omitempty"`
	Text string `json:"text,omitempty"`
}

type ResourceUpload struct {
	Name string `json:"name"`
	Data string `json:"data"` // base64
}

type resourceFile struct {
	name string
	data []byte
}

func (p *PointerSpec) resource(number int) *Resource {
	for i := range p.Resources {
		if p.Resources[i].Number == number {
			return &p.Resources[i]
		}
	}
	return nil
}

// resourceBlocked says why a resource can't be added now ("" when it can):
// between interactions, as for a step.
func (p *PointerSpec) resourceBlocked() string {
	return p.stepBlocked()
}

// resourceDir is where resource R's files are, beside the step files.
func resourceDir(mainPath string, number int) string {
	return filepath.Join(implDir(mainPath), "resources", strconv.Itoa(number))
}

// resourceLink is one of resource R's files as a step file links it.
func resourceLink(number int, name string) string {
	return path.Join("resources", strconv.Itoa(number), name)
}

func resourceCommitMsg(spec *PointerSpec, n, number int) string {
	return fmt.Sprintf("condocv2: %s resource %d.%d", spec.Name, n, number)
}

// resourceFileName keeps a file's name to one plain path component, so it
// can't escape its resource's directory or hide.
func resourceFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.TrimLeft(name, ".")
	if name == "" || name == "/" {
		return "file"
	}
	return name
}

// uniqueNames gives each file a name of its own: a second "a.txt" is
// "a-2.txt".
func uniqueNames(files []resourceFile) {
	seen := map[string]bool{}
	for i := range files {
		name := resourceFileName(files[i].name)
		if seen[strings.ToLower(name)] {
			ext := filepath.Ext(name)
			stem := strings.TrimSuffix(name, ext)
			for k := 2; ; k++ {
				if c := fmt.Sprintf("%s-%d%s", stem, k, ext); !seen[strings.ToLower(c)] {
					name = c
					break
				}
			}
		}
		seen[strings.ToLower(name)] = true
		files[i].name = name
	}
}

// hostFileName is a host files tab entry's name as uploaded, without the
// "<8 hex>_" its id starts with (local-representative's displayName).
func hostFileName(id string) string {
	if len(id) > 9 && id[8] == '_' {
		if _, err := strconv.ParseUint(id[:8], 16, 32); err == nil {
			return id[9:]
		}
	}
	return id
}

// HostFile is one entry of this host's files tab, for the resource form.
type HostFile struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	State string `json:"state"` // "cached" (host-cache) or "persisted" (host-store)
	Time  int64  `json:"time"`
}

// hostFiles lists the host's files tab, newest first. Its hidden sidecars
// (".manifest_", ".markup_") are left out, as the tab leaves them out.
func (s *Server) hostFiles() []HostFile {
	var out []HostFile
	for i, dir := range []string{s.hostCacheDir, s.hostStoreDir} {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		state := "cached"
		if i == 1 {
			state = "persisted"
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, HostFile{ID: e.Name(), Name: hostFileName(e.Name()), Size: info.Size(), State: state, Time: info.ModTime().Unix()})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out
}

// readHostFile reads a host files tab entry by its id.
func (s *Server) readHostFile(id string) ([]byte, error) {
	if id == "" || id != filepath.Base(id) || strings.HasPrefix(id, ".") {
		return nil, fmt.Errorf("bad host file %q", id)
	}
	for _, dir := range []string{s.hostCacheDir, s.hostStoreDir} {
		if dir == "" {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, id)); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("no file %q in this host's files tab (it may have expired from the host-cache)", id)
}

// resourceFiles reads a request's files from its source.
func (s *Server) resourceFiles(req ResourceRequest) ([]resourceFile, error) {
	var files []resourceFile
	switch req.Source {
	case resourceFromUpload:
		for _, u := range req.Uploads {
			b, err := base64.StdEncoding.DecodeString(u.Data)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", u.Name, err)
			}
			files = append(files, resourceFile{name: u.Name, data: b})
		}
	case resourceFromFiles:
		for _, id := range req.HostFiles {
			b, err := s.readHostFile(id)
			if err != nil {
				return nil, err
			}
			files = append(files, resourceFile{name: hostFileName(id), data: b})
		}
	case resourceFromText, resourceFromVoice:
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = "resource.md"
			if req.Source == resourceFromVoice {
				name = "dictation.md"
			}
		}
		if strings.TrimSpace(req.Text) == "" {
			if req.Source == resourceFromVoice {
				return nil, fmt.Errorf("nothing has been dictated")
			}
			return nil, fmt.Errorf("the text is empty")
		}
		files = append(files, resourceFile{name: name, data: []byte(req.Text)})
	case resourceFromHighlighted:
		base, err := s.lrBase()
		if err != nil {
			return nil, err
		}
		if files, err = highlightedResourceFiles(base, req.Highlighted); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown resource source %q: want upload, files, highlighted, text or voice", req.Source)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files")
	}
	if len(files) > maxResourceFiles {
		return nil, fmt.Errorf("%d files: a resource takes at most %d", len(files), maxResourceFiles)
	}
	total := 0
	for _, f := range files {
		total += len(f.data)
	}
	if total > maxResourceBytes {
		return nil, fmt.Errorf("%s: a resource takes at most %s", byteSize(int64(total)), byteSize(maxResourceBytes))
	}
	uniqueNames(files)
	return files, nil
}

func byteSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// resourceBody is "## Resource R": its title, where it came from and its
// files, linked.
func resourceBody(r Resource, sizes map[string]int) string {
	var b strings.Builder
	if r.Title != "" {
		b.WriteString(escapeSectionHeadings(r.Title) + "\n\n")
	}
	from := map[string]string{
		resourceFromUpload:      "uploaded",
		resourceFromFiles:       "from the host's files",
		resourceFromText:        "entered as text",
		resourceFromHighlighted: "highlighted in the files tab",
		resourceFromVoice:       "dictated by voice",
	}[r.Source]
	fmt.Fprintf(&b, "Added after interaction %d, %s:\n\n", r.Interaction, from)
	for _, f := range r.Files {
		fmt.Fprintf(&b, "- [%s](%s) (%s)\n", f, resourceLink(r.Number, f), byteSize(int64(sizes[f])))
	}
	return b.String()
}

// setResourceSection puts "## Resource R" with body at the end of a scope
// file, in place of any left by an attempt that failed after it.
func setResourceSection(path string, number int, body string) error {
	content, err := readFileString(path)
	if err != nil {
		return err
	}
	for _, loc := range resourceHeadingRe.FindAllStringSubmatchIndex(content, -1) {
		if content[loc[2]:loc[3]] != strconv.Itoa(number) {
			continue
		}
		end := len(content)
		if next := sectionBoundaries(content[loc[1]:]); len(next) > 0 {
			end = loc[1] + next[0]
		}
		content = content[:loc[0]] + content[end:]
		break
	}
	content = strings.TrimRight(content, " \t\n") + fmt.Sprintf("\n\n## Resource %d\n\n", number) + strings.TrimSpace(body) + "\n"
	return writeFileString(path, content)
}

// slopAddResource adds a resource to the condoc: its files and section in
// the condoc, then the pointer.
func (s *Server) slopAddResource(req ResourceRequest) error {
	title := strings.Join(strings.Fields(req.Title), " ") // one line, on the pointer too
	if !s.claim(req.Stem) {
		return errBusy(req.Stem)
	}
	defer s.release(req.Stem)
	spec, err := s.readHeldSpec(req.Stem)
	if err != nil {
		return err
	}
	if why := spec.resourceBlocked(); why != "" {
		return fmt.Errorf("%s can't take a resource now: %s", spec.Name, why)
	}
	files, err := s.resourceFiles(req)
	if err != nil {
		return fmt.Errorf("resource for %s: %v", spec.Name, err)
	}
	_, main, err := s.idleMainFile(&spec)
	if err != nil {
		return err
	}
	r := Resource{Number: len(spec.Resources) + 1, Interaction: spec.Interactions, Step: spec.Step, Title: title,
		Source: req.Source, Time: time.Now().Unix()}
	dir := resourceDir(main, r.Number)
	// Whatever a failed attempt left goes: this one's files are the resource.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sizes := map[string]int{}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, 0o644); err != nil {
			return err
		}
		r.Files = append(r.Files, f.name)
		sizes[f.name] = len(f.data)
	}
	if err := setResourceSection(scopeFilePath(main, spec.Step), r.Number, resourceBody(r, sizes)); err != nil {
		return err
	}
	msg := resourceCommitMsg(&spec, r.Interaction, r.Number)
	if err := s.commitCondocRepo(&spec, msg); err != nil {
		return err
	}
	spec.Resources = append(spec.Resources, r)
	return s.writeSpec(&spec, msg)
}

// resourceNote is what the visual says of a resource: its title, else its
// files.
func resourceNote(r *Resource) string {
	if r.Title != "" {
		return r.Title
	}
	return strings.Join(r.Files, ", ")
}

// resourceFilePrefix serves a resource's files for the UI: GET
// /api/resource/<stem>/<R>/<name>. A merged condoc's slopspace is gone, so it
// reads (or redirects to) the file in the merged condoc repo instead, as
// handleDTTFile does.
const resourceFilePrefix = "/api/resource/"

func (s *Server) handleResourceFile(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, resourceFilePrefix), "/", 3)
	if len(parts) != 3 || parts[2] == "" {
		http.Error(w, "want "+resourceFilePrefix+"<stem>/<resource>/<file>", http.StatusBadRequest)
		return
	}
	stem, name := parts[0], parts[2]
	number, err := strconv.Atoi(parts[1])
	if err != nil || number < 1 || name != resourceFileName(name) {
		http.Error(w, "bad resource file", http.StatusBadRequest)
		return
	}
	spec, err := s.readSpec(stem)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var b []byte
	if spec.merged() || spec.SlopspaceID == "" {
		file := path.Join(path.Dir(filepath.ToSlash(stepFilePath(spec.CondocPath))), resourceLink(number, name))
		if gitauth.IsLocal(spec.CondocRepo) {
			if b, err = localFileContent(spec.CondocRepo, spec.Branch, file); err != nil {
				b, err = localFileContent(spec.CondocRepo, "", file)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
		} else if prefix := strings.TrimSuffix(spec.CondocURL, spec.CondocPath); prefix != spec.CondocURL {
			http.Redirect(w, r, prefix+file, http.StatusFound)
			return
		} else {
			http.Error(w, "the condoc's slopspace is gone", http.StatusNotFound)
			return
		}
	} else {
		meta, err := s.slop.ss.Get(spec.SlopspaceID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if b, err = os.ReadFile(filepath.Join(resourceDir(slopMainFile(meta, &spec), number), name)); err != nil {
			http.Error(w, "no such resource file in the condoc", http.StatusNotFound)
			return
		}
	}
	ct := http.DetectContentType(b)
	if strings.HasPrefix(ct, "text/html") {
		ct = "text/plain; charset=utf-8" // shown, never run
	}
	w.Header().Set("Content-Type", ct)
	// Whatever was uploaded is shown, never run (an SVG's script, say).
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}
