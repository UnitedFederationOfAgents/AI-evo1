package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// This file is how a T3's entrypoint is started. Tools are written in one
// of two languages, named by tool.yaml's entrypoint.runtime:
//
//   - python: script is a .py file. It runs through "uv run --script" when
//     uv is installed, so the dependencies it declares inline (PEP 723) are
//     fetched for it; otherwise through python3, and those dependencies
//     must already be installed. "uv" is read as python, its older name.
//   - go: script is the .go file holding func main. The package in its
//     directory is built with the go toolchain into the run's working
//     directory, then that binary is run, in the module of the nearest
//     go.mod up to the tool's directory. A package with no go.mod at all
//     (one using only the standard library) is built from a copy with a
//     go.mod dtt writes, so the tool's directory is never written to. Go's
//     build cache makes every build after the first quick.
//
// The runtime exec, which runs the script itself, stays for the built-in
// declarative tools (a line of shell handing over to "dtt tool-engine") and
// for tools made with it before; dtt create no longer offers it.

const (
	goBuildTimeout = 5 * time.Minute
	goBinDir       = ".dtt-go-bin" // in the working directory
	goSrcDir       = ".dtt-go-src" // the copy built when the package has no go.mod
	goModVersion   = "1.21"
)

// pythonCommand is the command line that runs a python tool's script, or
// (as the second result) why it can't run here.
func pythonCommand(cfg config, t *Tool, script string) ([]string, string) {
	if uv, err := exec.LookPath(cfg.uvBin); err == nil {
		return []string{uv, "run", "--quiet", "--script", script}, ""
	}
	if py, err := exec.LookPath(cfg.pythonBin); err == nil {
		return []string{py, script}, ""
	}
	return nil, fmt.Sprintf("%s is a python tool, and neither uv (looked for %q) nor python (looked for %q) is installed here -- install uv (https://docs.astral.sh/uv/), which also fetches the script's dependencies, or point dtt's \"uv\" or \"python\" setting at one", t.Name, cfg.uvBin, cfg.pythonBin)
}

// buildGoTool builds go tool t (whose files are in toolDir) into workdir and
// returns the binary's path. The build's output goes to log, ahead of the
// run's own.
func buildGoTool(ctx context.Context, cfg config, t *Tool, toolDir, workdir string, log io.Writer) (string, error) {
	goBin, err := exec.LookPath(cfg.goBin)
	if err != nil {
		return "", fmt.Errorf("%s is a go tool, and the go toolchain isn't installed here (looked for %q) -- install go (https://go.dev/dl/) or point dtt's \"go\" setting at it", t.Name, cfg.goBin)
	}
	pkgDir := filepath.Dir(filepath.Join(toolDir, t.Script))
	// The module is the nearest go.mod from the script's directory up to the
	// tool's own, so script: cmd/x/main.go can sit in a module at the root.
	buildDir, target, modRoot := "", ".", ""
	for d := pkgDir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			modRoot = d
			break
		}
		if rel, err := filepath.Rel(toolDir, d); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			break
		}
	}
	if modRoot != "" {
		buildDir = modRoot
		if rel, _ := filepath.Rel(modRoot, pkgDir); rel != "." {
			target = "./" + filepath.ToSlash(rel)
		}
	} else {
		buildDir = filepath.Join(workdir, goSrcDir)
		if err := copyGoPackage(pkgDir, buildDir, t.Name); err != nil {
			return "", fmt.Errorf("preparing %s's go package: %v", t.Name, err)
		}
	}
	bin := filepath.Join(workdir, goBinDir, t.Name)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}

	buildCtx, cancel := context.WithTimeout(ctx, goBuildTimeout)
	defer cancel()
	fmt.Fprintf(log, "dtt: building %s with %s\n", t.Name, goBin)
	cmd := exec.CommandContext(buildCtx, goBin, "build", "-o", bin, target)
	cmd.Dir = buildDir
	cmd.Stdout, cmd.Stderr = log, log
	// GOWORK=off: a go.work above the tool (this repo's, say) mustn't decide
	// what the tool builds against.
	cmd.Env = append(os.Environ(), "GOWORK=off")
	start := time.Now()
	if err := cmd.Run(); err != nil {
		if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("building %s didn't finish within %s", t.Name, goBuildTimeout)
		}
		return "", fmt.Errorf("building %s failed (%v) -- its log has the compiler's messages", t.Name, err)
	}
	fmt.Fprintf(log, "dtt: built %s in %s\n", t.Name, time.Since(start).Round(time.Millisecond))
	return bin, nil
}

// copyGoPackage copies the non-test .go files in src into a fresh dst and
// writes a go.mod beside them, so a standard-library-only package builds
// as a module of its own.
func copyGoPackage(src, dst, name string) error {
	os.RemoveAll(dst)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, n))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, n), data, 0o644); err != nil {
			return err
		}
	}
	mod := fmt.Sprintf("module dtt-tool/%s\n\ngo %s\n", name, goModVersion)
	return os.WriteFile(filepath.Join(dst, "go.mod"), []byte(mod), 0o644)
}
