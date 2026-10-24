package main

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	ufaconfig "ufa-configurable"
)

// This file finds T3s. There is no registry index: every call scans, in
// order, the built-in set embedded in this binary, the user tools directory
// (~/.ufa/tools by default) and then each configured tools-dirs entry. A
// tool is a directory named after it holding tool.yaml and its script.
// The first tool found under a name wins; later ones are listed as
// shadowed. "dtt install" is "validate, then copy into the user tools
// directory", so what's on disk is the only record of what's installed.

//go:embed builtin
var builtinFS embed.FS

const builtinRoot = "builtin"

// config is dtt's ufa-configurable settings (~/.ufa/config/dtt.yaml over
// global.yaml).
type config struct {
	toolsDir  string   // tools-dir: where install and create --install put tools
	extraDirs []string // tools-dirs: more directories to scan, after it
	uvBin     string   // uv: the uv binary runtime uv tools run through
}

func loadConfig(dir string) (config, error) {
	conf, err := ufaconfig.Load("dtt", dir)
	if err != nil {
		return config{}, err
	}
	cfg := config{
		toolsDir: conf.String("tools-dir", defaultToolsDir()),
		uvBin:    conf.String("uv", "uv"),
	}
	if cfg.toolsDir, err = expandHome(cfg.toolsDir); err != nil {
		return config{}, err
	}
	for _, d := range splitList(conf.String("tools-dirs", "")) {
		p, err := expandHome(d)
		if err != nil {
			return config{}, err
		}
		cfg.extraDirs = append(cfg.extraDirs, p)
	}
	return cfg, nil
}

func defaultToolsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".ufa", "tools")
	}
	return filepath.Join(home, ".ufa", "tools")
}

// splitList parses a comma/whitespace-separated list, dropping empty entries.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
}

// DirInfo is one place the catalog looked.
type DirInfo struct {
	Origin string `json:"origin"`
	Path   string `json:"path"`
	Status string `json:"status"` // ok, missing, error
	Detail string `json:"detail,omitempty"`
}

// Problem is a directory that looked like a tool but couldn't be read.
type Problem struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// Shadowed is a tool hidden by an earlier one of the same name.
type Shadowed struct {
	Name string `json:"name"`
	Path string `json:"path"`
	By   string `json:"by"`
}

type catalog struct {
	tools    []*Tool
	dirs     []DirInfo
	problems []Problem
	shadowed []Shadowed
}

func (c *catalog) find(name string) *Tool {
	for _, t := range c.tools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// scanCatalog finds every tool, as described above.
func scanCatalog(cfg config) *catalog {
	c := &catalog{}
	add := func(t *Tool) {
		if prev := c.find(t.Name); prev != nil {
			c.shadowed = append(c.shadowed, Shadowed{Name: t.Name, Path: t.Dir, By: prev.info(false).Path})
			return
		}
		c.tools = append(c.tools, t)
	}

	builtins, err := fs.ReadDir(builtinFS, builtinRoot)
	c.dirs = append(c.dirs, DirInfo{Origin: "builtin", Path: builtinRoot, Status: "ok"})
	if err != nil {
		c.dirs[0].Status, c.dirs[0].Detail = "error", err.Error()
	}
	for _, e := range builtins {
		if !e.IsDir() {
			continue
		}
		t, err := readBuiltin(e.Name())
		if err != nil {
			c.problems = append(c.problems, Problem{Path: builtinRoot + "/" + e.Name(), Error: err.Error()})
			continue
		}
		add(t)
	}

	scan := func(origin, dir string) {
		info := DirInfo{Origin: origin, Path: dir, Status: "ok"}
		entries, err := os.ReadDir(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			info.Status = "missing"
		case err != nil:
			info.Status, info.Detail = "error", err.Error()
		}
		c.dirs = append(c.dirs, info)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if st, err := os.Stat(p); err != nil || !st.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(p, toolFile)); err != nil {
				continue // not a tool
			}
			t, err := readToolDir(p, true)
			if err != nil {
				c.problems = append(c.problems, Problem{Path: p, Error: err.Error()})
				continue
			}
			if t.Name != e.Name() {
				c.problems = append(c.problems, Problem{Path: p, Error: fmt.Sprintf("tool %q is in a directory named %q -- a tool's directory is named after it", t.Name, e.Name())})
				continue
			}
			t.Origin = origin
			add(t)
		}
	}
	scan("user", cfg.toolsDir)
	for _, d := range cfg.extraDirs {
		scan("tools-dirs", d)
	}
	sort.SliceStable(c.tools, func(i, j int) bool { return c.tools[i].Name < c.tools[j].Name })
	return c
}

// readToolDir reads and checks the tool in dir; withScript also checks its
// entrypoint script is there.
func readToolDir(dir string, withScript bool) (*Tool, error) {
	src, err := os.ReadFile(filepath.Join(dir, toolFile))
	if err != nil {
		return nil, err
	}
	t, err := parseTool(string(src))
	if err != nil {
		return nil, err
	}
	if withScript {
		if err := t.checkScript(dir); err != nil {
			return nil, err
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	t.Dir = abs
	return t, nil
}

func readBuiltin(name string) (*Tool, error) {
	src, err := builtinFS.ReadFile(path.Join(builtinRoot, name, toolFile))
	if err != nil {
		return nil, err
	}
	t, err := parseTool(string(src))
	if err != nil {
		return nil, err
	}
	if t.Name != name {
		return nil, fmt.Errorf("built-in tool %q is in a directory named %q", t.Name, name)
	}
	if _, err := fs.Stat(builtinFS, path.Join(builtinRoot, name, t.Script)); err != nil {
		return nil, fmt.Errorf("entrypoint.script %s: %v", t.Script, err)
	}
	t.Origin = "builtin"
	return t, nil
}

// extractBuiltin writes built-in tool name's files into dst, so its script
// can be run from disk. A runtime exec script is made executable.
func extractBuiltin(t *Tool, dst string) error {
	root := path.Join(builtinRoot, t.Name)
	return fs.WalkDir(builtinFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := builtinFS.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if t.Runtime == "exec" && rel == t.Script {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
}

// install validates the tool in src and copies it into the user tools
// directory, replacing an installed copy only with force. It returns the
// installed tool.
func install(cfg config, src string, force bool) (*Tool, error) {
	t, err := readToolDir(src, true)
	if err != nil {
		return nil, fmt.Errorf("%s isn't a valid tool: %v", src, err)
	}
	if bt, err := readBuiltin(t.Name); err == nil && bt != nil {
		return nil, fmt.Errorf("a built-in tool is already called %q -- pick another name", t.Name)
	}
	dst := filepath.Join(cfg.toolsDir, t.Name)
	if same, _ := sameDir(t.Dir, dst); same {
		t.Origin = "user"
		return t, nil // created in place (dtt create --install): nothing to copy
	}
	if _, err := os.Stat(dst); err == nil && !force {
		return nil, fmt.Errorf("%s is already installed at %s -- pass --force to replace it", t.Name, dst)
	}
	if err := os.MkdirAll(cfg.toolsDir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(cfg.toolsDir, ".installing-"+t.Name+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(t.Dir, tmp); err != nil {
		return nil, fmt.Errorf("copying %s: %v", t.Dir, err)
	}
	old := ""
	if _, err := os.Stat(dst); err == nil {
		old = dst + ".old"
		os.RemoveAll(old)
		if err := os.Rename(dst, old); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		if old != "" {
			os.Rename(old, dst)
		}
		return nil, err
	}
	if old != "" {
		os.RemoveAll(old)
	}
	installed, err := readToolDir(dst, true)
	if err != nil {
		return nil, err
	}
	installed.Origin = "user"
	return installed, nil
}

func sameDir(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(sa, sb), nil
}

// copyTree copies src's regular files and directories into dst (which
// exists), keeping their permissions. Hidden entries (a .venv, say) and
// symlinks are left out.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case !info.Mode().IsRegular():
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
