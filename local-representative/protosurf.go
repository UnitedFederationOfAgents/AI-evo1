package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The demo tab's protosurf area (PrivateRepoOps Step 5, Revision B): the
// prototype surface, a 2D map of the content of the project this host is
// developing (AI-evo1-spiral). The "protosurf" op scans the project's text
// files -- git's tracked and untracked-but-not-ignored ones, or a walk of
// the tree when it isn't a git checkout -- and answers each with its
// language, its role in the system and its line counts. The tab lays them
// out as a treemap, a tile's area its lines, coloured by language, role or
// density. Demo and test coverage are to be drawn on the same map later.

const (
	protosurfMaxFile  = 4 << 20 // a file past it is counted as skipped, not read
	protosurfMaxFiles = 20000
	protosurfTimeout  = 30 * time.Second
	protosurfSniff    = 8 << 10 // read for a NUL byte, to tell binaries
)

// ProtosurfFile is one file on the map (or, fitted to the relay, several
// small ones folded together -- Folded counts them). Dir indexes
// ProtosurfMap.Dirs, Lang its Langs and Role its Roles. Code counts the
// lines that are neither blank nor comments; Comment the comment lines.
// It travels as a JSON array, [dir, name, lang, role, lines, code, comment,
// folded], to keep a whole project's map inside the relay.
type ProtosurfFile struct {
	Dir     int
	Name    string
	Lang    int
	Role    int
	Lines   int
	Code    int
	Comment int
	Folded  int
}

func (f ProtosurfFile) MarshalJSON() ([]byte, error) {
	a := []any{f.Dir, f.Name, f.Lang, f.Role, f.Lines, f.Code, f.Comment}
	if f.Folded > 0 {
		a = append(a, f.Folded)
	}
	return json.Marshal(a)
}

// ProtosurfMap is the protosurf op's answer. Source is how the files were
// found ("git" or "walk"); Skipped counts those left off the map by why
// (binary, lockfile, too large).
type ProtosurfMap struct {
	Root      string          `json:"root"`
	Source    string          `json:"source"`
	ScannedAt int64           `json:"scanned_at"`
	Dirs      []string        `json:"dirs"`
	Langs     []string        `json:"langs"`
	Roles     []string        `json:"roles"`
	Files     []ProtosurfFile `json:"files"`
	Skipped   map[string]int  `json:"skipped,omitempty"`
	Folded    int             `json:"folded,omitempty"`
}

// protosurfLangs are the languages told apart, by extension or name.
var protosurfLangs = map[string]string{
	".go": "go", ".ts": "typescript", ".tsx": "typescript", ".js": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".jsx": "javascript",
	".css": "css", ".html": "html", ".md": "markdown", ".yaml": "yaml", ".yml": "yaml", ".json": "json",
	".sh": "shell", ".bash": "shell", ".py": "python", ".toml": "toml", ".txt": "text",
	"makefile": "make", "dockerfile": "docker",
}

// protosurfLockfiles are generated and left off the map, which they'd
// otherwise dwarf.
var protosurfLockfiles = map[string]bool{"go.sum": true, "package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "uv.lock": true, "poetry.lock": true}

// protosurfSkipDirs aren't walked when the tree isn't a git checkout.
var protosurfSkipDirs = map[string]bool{".git": true, "node_modules": true, "dist": true, "build": true, "vendor": true, ".venv": true, "__pycache__": true, "target": true}

func protosurfLang(name string) string {
	lower := strings.ToLower(name)
	if l, ok := protosurfLangs[lower]; ok {
		return l
	}
	if l, ok := protosurfLangs[path.Ext(lower)]; ok {
		return l
	}
	return "other"
}

// protosurfRole is the part a file plays in the system, from its path:
// condoc (the condocs and their *Impls steps), test, frontend, declaration
// (dtt documents and tools), docs, build (and config), service (the apps'
// own code) or other.
func protosurfRole(rel, lang string) string {
	segs := strings.Split(rel, "/")
	name := strings.ToLower(segs[len(segs)-1])
	dirs := segs[:len(segs)-1]
	for _, d := range dirs {
		if d == "condocs" || strings.HasSuffix(d, "Impls") {
			return "condoc"
		}
	}
	for _, d := range dirs {
		if d == "testdata" || d == "test" || d == "tests" {
			return "test"
		}
	}
	if strings.HasSuffix(name, "_test.go") || strings.Contains(name, ".test.") || strings.Contains(name, ".spec.") || (strings.HasPrefix(name, "test_") && lang == "python") {
		return "test"
	}
	if strings.HasSuffix(name, ".dtt.yaml") || name == "tool.yaml" {
		return "declaration"
	}
	for _, d := range dirs {
		if d == "frontend" {
			if lang == "json" || strings.HasPrefix(name, "vite.config") || strings.HasPrefix(name, "tsconfig") {
				return "build"
			}
			return "frontend"
		}
	}
	for _, d := range dirs {
		if d == ".github" || d == ".devcontainer" || d == "scripts" {
			return "build"
		}
	}
	switch {
	case lang == "markdown" || lang == "text" || name == "license":
		return "docs"
	case lang == "make" || lang == "docker" || lang == "shell" || lang == "json" || lang == "toml" || lang == "yaml" ||
		name == "go.mod" || strings.HasPrefix(name, "."):
		return "build"
	case lang == "go" || lang == "python" || lang == "typescript" || lang == "javascript":
		return "service"
	}
	return "other"
}

// protosurfComments are each language's line and block comment markers.
var protosurfComments = map[string]struct{ line, open, close string }{
	"go": {"//", "/*", "*/"}, "typescript": {"//", "/*", "*/"}, "javascript": {"//", "/*", "*/"}, "css": {"", "/*", "*/"},
	"shell": {"#", "", ""}, "python": {"#", "", ""}, "yaml": {"#", "", ""}, "toml": {"#", "", ""}, "make": {"#", "", ""}, "docker": {"#", "", ""},
	"html": {"", "<!--", "-->"}, "markdown": {"", "<!--", "-->"},
}

// countLines counts b's lines, its code lines (neither blank nor comment)
// and its comment lines, telling comments by their leading marker -- a
// line that only starts or ends a block counts as a comment.
func countLines(b []byte, lang string) (lines, code, comment int) {
	cm := protosurfComments[lang]
	inBlock := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), protosurfMaxFile+1) // a whole file can be one line
	for sc.Scan() {
		lines++
		t := strings.TrimSpace(sc.Text())
		switch {
		case inBlock:
			comment++
			if strings.Contains(t, cm.close) {
				inBlock = false
			}
		case t == "":
		case cm.line != "" && strings.HasPrefix(t, cm.line):
			comment++
		case cm.open != "" && strings.HasPrefix(t, cm.open):
			comment++
			inBlock = !strings.Contains(t[len(cm.open):], cm.close)
		default:
			code++
		}
	}
	return lines, code, comment
}

// protosurfList is the project's files, relative and slash-separated: git's
// (tracked, and untracked but not ignored) when root is a git checkout,
// else a walk skipping protosurfSkipDirs and dot-directories.
func protosurfList(ctx context.Context, root string) ([]string, string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err == nil {
		var files []string
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				files = append(files, f)
			}
		}
		return files, "git", nil
	}
	var files []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if p != root && (protosurfSkipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && d.Name() != ".github" && d.Name() != ".devcontainer")) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			if rel, err := filepath.Rel(root, p); err == nil {
				files = append(files, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	return files, "walk", err
}

// scanProtosurf maps the project at root.
func scanProtosurf(ctx context.Context, root string) (ProtosurfMap, error) {
	// A link to the project is walked as the project.
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	st, err := os.Stat(root)
	if err != nil {
		return ProtosurfMap{}, err
	}
	if !st.IsDir() {
		return ProtosurfMap{}, fmt.Errorf("%s isn't a directory", root)
	}
	list, source, err := protosurfList(ctx, root)
	if err != nil {
		return ProtosurfMap{}, err
	}
	sort.Strings(list)
	m := ProtosurfMap{Root: root, Source: source, ScannedAt: time.Now().UnixMilli(), Skipped: map[string]int{}}
	dirs, langs, roles := map[string]int{}, map[string]int{}, map[string]int{}
	index := func(idx map[string]int, all *[]string, k string) int {
		i, ok := idx[k]
		if !ok {
			i = len(*all)
			idx[k] = i
			*all = append(*all, k)
		}
		return i
	}
	for _, rel := range list {
		if ctx.Err() != nil {
			return ProtosurfMap{}, ctx.Err()
		}
		if len(m.Files) >= protosurfMaxFiles {
			m.Skipped["past the file limit"]++
			continue
		}
		name := path.Base(rel)
		if protosurfLockfiles[name] {
			m.Skipped["lockfile"]++
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue // deleted since, or a link
		}
		if fi.Size() > protosurfMaxFile {
			m.Skipped["too large"]++
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			m.Skipped["unreadable"]++
			continue
		}
		if bytes.IndexByte(b[:min(len(b), protosurfSniff)], 0) >= 0 {
			m.Skipped["binary"]++
			continue
		}
		lang := protosurfLang(name)
		lines, code, comment := countLines(b, lang)
		if lines == 0 {
			m.Skipped["empty"]++
			continue
		}
		dir := path.Dir(rel)
		if dir == "." {
			dir = ""
		}
		m.Files = append(m.Files, ProtosurfFile{
			Dir:     index(dirs, &m.Dirs, dir),
			Name:    name,
			Lang:    index(langs, &m.Langs, lang),
			Role:    index(roles, &m.Roles, protosurfRole(rel, lang)),
			Lines:   lines,
			Code:    code,
			Comment: comment,
		})
	}
	if len(m.Skipped) == 0 {
		m.Skipped = nil
	}
	return m, nil
}

// fitProtosurf folds m's smallest files, a directory's at a time, into one
// entry per directory, language and role ("+N files") until fits says it
// fits, doubling the size that counts as small each pass.
func fitProtosurf(m *ProtosurfMap, fits func() bool) {
	for small := 32; !fits() && small <= 1<<22; small *= 2 {
		type key struct{ dir, lang, role int }
		var kept []ProtosurfFile
		groups := map[key][]ProtosurfFile{}
		var order []key
		for _, f := range m.Files {
			if f.Lines >= small {
				kept = append(kept, f)
				continue
			}
			k := key{f.Dir, f.Lang, f.Role}
			if groups[k] == nil {
				order = append(order, k)
			}
			groups[k] = append(groups[k], f)
		}
		for _, k := range order {
			g := groups[k]
			if len(g) == 1 {
				// Nothing to fold it with: kept as it was.
				kept = append(kept, g[0])
				continue
			}
			agg := ProtosurfFile{Dir: k.dir, Lang: k.lang, Role: k.role}
			for _, f := range g {
				agg.Folded += max(f.Folded, 1)
				agg.Lines += f.Lines
				agg.Code += f.Code
				agg.Comment += f.Comment
			}
			agg.Name = fmt.Sprintf("+%d files", agg.Folded)
			kept = append(kept, agg)
		}
		m.Folded = 0
		for _, f := range kept {
			m.Folded += f.Folded
		}
		m.Files = kept
	}
}

// handleProtosurf answers the protosurf op: the map of the project at Path,
// else at this host's own project root (protosurfRoot).
func (w *toolsWatch) handleProtosurf(req ToolsRequest, fromAC bool, reply ToolsReply) ToolsReply {
	root := strings.TrimSpace(req.Path)
	if root != "" && fromAC {
		// Through agent-coordinator, only the host's own project: a path
		// would list any directory's file names.
		reply.Error = "agent-coordinator can map only this host's own project -- give a path from the host's own demo tab"
		return reply
	}
	if root == "" {
		if w.protosurfRoot == nil {
			reply.Error = "this host doesn't know where its project is -- give a path"
			return reply
		}
		r, err := w.protosurfRoot()
		if err != nil {
			reply.Error = err.Error()
			return reply
		}
		root = r
	}
	ctx, cancel := context.WithTimeout(context.Background(), protosurfTimeout)
	defer cancel()
	m, err := scanProtosurf(ctx, root)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("mapping %s took over %s", root, protosurfTimeout)
		}
		reply.Error = err.Error()
		return reply
	}
	reply.OK, reply.Protosurf = true, &m
	lines := 0
	for _, f := range m.Files {
		lines += f.Lines
	}
	reply.Detail = fmt.Sprintf("%d file(s), %d line(s) in %s (%s)", len(m.Files), lines, root, m.Source)
	if fromAC {
		fits := func() bool {
			b, err := json.Marshal(reply)
			return err == nil && len(b) <= maxRelayedTools
		}
		fitProtosurf(&m, fits)
		if !fits() {
			// Spread too thin to fold: a small file a directory, in many.
			reply.OK, reply.Protosurf = false, nil
			reply.Error = fmt.Sprintf("the map of %s is too large to relay through agent-coordinator -- see this host's own demo tab", root)
			return reply
		}
		if m.Folded > 0 {
			reply.Detail += fmt.Sprintf(" -- %d small file(s) folded together to fit the relay; this host's own demo tab shows each", m.Folded)
		}
	}
	return reply
}
