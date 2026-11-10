package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCountLines(t *testing.T) {
	src := "package x\n\n// a comment\n/* a block\n   still it */\nfunc f() {} // trailing\n"
	if l, c, k := countLines([]byte(src), "go"); l != 6 || c != 2 || k != 3 {
		t.Errorf("go: lines %d code %d comment %d, want 6 2 3", l, c, k)
	}
	if l, c, k := countLines([]byte("# title\n\nprose\n<!-- note -->\n"), "markdown"); l != 4 || c != 2 || k != 1 {
		t.Errorf("markdown: lines %d code %d comment %d, want 4 2 1", l, c, k)
	}
	if l, c, k := countLines([]byte("#!/bin/sh\necho hi"), "shell"); l != 2 || c != 1 || k != 1 {
		t.Errorf("shell: lines %d code %d comment %d, want 2 1 1", l, c, k)
	}
}

func TestProtosurfRole(t *testing.T) {
	for _, c := range []struct{ rel, want string }{
		{"local-representative/main.go", "service"},
		{"local-representative/main_test.go", "test"},
		{"local-representative/frontend/src/Demo.tsx", "frontend"},
		{"local-representative/frontend/package.json", "build"},
		{"local-representative/demos/robot-read.demo.dtt.yaml", "declaration"},
		{"dtt/builtin/localrep/tool.yaml", "declaration"},
		{"condocs/InitialRobot.md", "condoc"},
		{"agent-coordinator/autolaunchChainsImpls/Step1Prompt.md", "condoc"},
		{"docs/DevMode.md", "docs"},
		{"Makefile", "build"},
		{".github/workflows/ci.yml", "build"},
		{"local-representative/go.mod", "build"},
		{"LICENSE", "docs"},
	} {
		if got := protosurfRole(c.rel, protosurfLang(filepath.Base(c.rel))); got != c.want {
			t.Errorf("%s: role %s, want %s", c.rel, got, c.want)
		}
	}
}

func TestScanProtosurf(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app/main.go", "package main\n\nfunc main() {}\n")
	write("app/frontend/src/App.tsx", "export const x = 1\n")
	write("app/frontend/node_modules/dep/index.js", "module.exports = 1\n")
	write("app/frontend/package-lock.json", "{}\n")
	write("app/logo.png", "\x89PNG\x00\x00")
	write("README.md", "# hi\n")
	m, err := scanProtosurf(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != "walk" {
		t.Skipf("the temp dir is inside a git checkout (%s), so this isn't the walk", m.Source)
	}
	got := map[string]string{}
	for _, f := range m.Files {
		got[filepath.ToSlash(filepath.Join(m.Dirs[f.Dir], f.Name))] = m.Langs[f.Lang] + "/" + m.Roles[f.Role]
	}
	want := map[string]string{"app/main.go": "go/service", "app/frontend/src/App.tsx": "typescript/frontend", "README.md": "markdown/docs"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("files %v, want %v", got, want)
	}
	if m.Skipped["lockfile"] != 1 || m.Skipped["binary"] != 1 {
		t.Errorf("skipped %v, want a lockfile and a binary", m.Skipped)
	}
}

func TestFitProtosurf(t *testing.T) {
	m := ProtosurfMap{Dirs: []string{"a"}, Langs: []string{"go"}, Roles: []string{"service"}}
	m.Files = append(m.Files, ProtosurfFile{Name: "big.go", Lines: 5000, Code: 4000})
	for i := 0; i < 400; i++ {
		m.Files = append(m.Files, ProtosurfFile{Name: fmt.Sprintf("small%03d.go", i), Lines: 10, Code: 8})
	}
	fitProtosurf(&m, func() bool {
		b, _ := json.Marshal(m)
		return len(b) < 2000
	})
	if len(m.Files) != 2 || m.Files[0].Name != "big.go" || m.Files[1].Folded != 400 || m.Files[1].Lines != 4000 || m.Folded != 400 {
		t.Errorf("fitted to %+v, want big.go and one fold of the 400", m.Files)
	}
	b, _ := json.Marshal(m.Files[1])
	if !strings.HasPrefix(string(b), `[0,"+400 files",0,0,4000,3200,0,400]`) {
		t.Errorf("a fold travels as %s", b)
	}
}
