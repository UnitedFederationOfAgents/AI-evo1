package slopspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DTT images for the canvas's read documents (dTT-IntegrationImpls Step 1,
// Revision N).
//
// A read document only measures, so there's nothing for the human to weigh
// once it's accepted: accepting it runs it (dtt read) and its image goes into
// readspaces/dtt-images, mirroring its place on the canvas:
//
//	readspaces/dtt-canvas/read/prs.dtt.yaml
//	readspaces/dtt-images/read/prs.image.json   what dtt read returned
//	readspaces/dtt-images/read/prs.error.txt    only when the run failed
//
// Deploy also runs any canvas read document that has no image yet -- one
// accepted before this existed, or one the human dropped onto the canvas
// themselves -- so the agent sees an image for every read on the canvas.
// Write documents change things, so they're never run here on their own: the
// human runs them (tools tab or dtt assert) and puts the image in dtt-images
// if they want the agent to see it, or a caller that has the human's say-so
// runs one with RunCanvasDoc -- condocv2 runs every proposal it accepts, each
// in its own sub-iteration of the condoc.
//
// A failed run never fails the review or the deploy; the error file says what
// went wrong, and the next deploy tries again.
//
// A read (or a proposal's pre-read) has dttReadTimeout. An assert or delete
// has dttWriteTimeout, as one may wait on agent work its document asks for
// (slopspace.work.executions, up to 55 minutes each) -- the condoc waits for
// it, as it would for the human running it.

const (
	dttImageSuffix  = ".image.json"
	dttErrorSuffix  = ".error.txt"
	dttReadTimeout  = 2 * time.Minute
	dttWriteTimeout = 3 * time.Hour
	dttImageFormat  = "dtt-image-v1"
)

// dttTimeout is how long dtt may run a document in mode.
func dttTimeout(mode string) time.Duration {
	if mode == "read" {
		return dttReadTimeout
	}
	return dttWriteTimeout
}

// findDTT locates the dtt binary: $DTT_BIN, next to this executable, in the
// dev bin dir ($AI_EVO1_DEV_BIN, /AI-evo1-dev/bin), then on PATH -- the same
// places local-representative looks.
func findDTT() (string, error) {
	if p := os.Getenv("DTT_BIN"); p != "" {
		return p, nil
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "dtt"))
	}
	devBin := os.Getenv("AI_EVO1_DEV_BIN")
	if devBin == "" {
		devBin = "/AI-evo1-dev/bin"
	}
	candidates = append(candidates, filepath.Join(devBin, "dtt"))
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}
	if p, err := exec.LookPath("dtt"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("could not locate dtt (looked at $DTT_BIN, next to dungeon-keeper, in %s, and on PATH)", devBin)
}

// dttImagePaths are where the image and error file for the canvas document
// rel (relative to the canvas, e.g. read/prs.dtt.yaml) go under images.
func dttImagePaths(images, rel string) (image, errFile string) {
	base := filepath.Join(images, filepath.FromSlash(rel))
	for _, ext := range []string{".dtt.yaml", ".dtt.yml", ".yaml", ".yml"} {
		if strings.HasSuffix(base, ext) {
			base = strings.TrimSuffix(base, ext)
			break
		}
	}
	return base + dttImageSuffix, base + dttErrorSuffix
}

// isReadDoc reports whether rel (relative to the canvas or dtt-requests) is
// a read document.
func isReadDoc(rel string) bool {
	return strings.HasPrefix(filepath.ToSlash(rel), DTTReadDir+"/")
}

// readCanvasImage runs the canvas document rel with dtt read and writes its
// image (and, if it failed, an error file) under the slopspace's
// readspaces/dtt-images. The returned error says why there's no good image.
func (m *Manager) readCanvasImage(root, rel string) error {
	return m.runCanvasDoc(root, rel, "read")
}

// DTTDocMode is the mode a DTT document rel (relative to the canvas or
// dtt-requests) is run in: read for a read/ document; for a write/ one,
// delete when its leading # comments say "Mode: delete" (as the guide asks
// the agent to note the mode), otherwise assert.
func DTTDocMode(rel string, content []byte) string {
	if isReadDoc(rel) {
		return "read"
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			break
		}
		if m := dttModeRe.FindStringSubmatch(line); m != nil && strings.EqualFold(m[1], "delete") {
			return "delete"
		}
	}
	return "assert"
}

var dttModeRe = regexp.MustCompile(`(?i)\bmode:\s*(read|assert|delete)\b`)

// RunCanvasDoc runs one canvas document of a returned slopspace -- rel is
// relative to the canvas, e.g. write/repo.dtt.yaml -- in its mode
// (DTTDocMode), writing the image (or error file) into dtt-images as
// readCanvasImage does. It returns the mode, the image's path and error
// file's path, and why there's no good image. For callers that decide when a
// document runs (DecideProposals); an assert or delete changes things, so
// nothing here ever runs one on its own.
func (m *Manager) RunCanvasDoc(id, rel string) (mode, image, errFile string, err error) {
	metadata, err := m.Get(id)
	if err != nil {
		return "", "", "", err
	}
	if metadata.Deployed {
		return "", "", "", fmt.Errorf("slopspace %s is deployed; run its canvas documents once it's returned", shortID(id))
	}
	content, err := os.ReadFile(filepath.Join(metadata.RootPath, ReadSpacesDir, DTTCanvasDir, filepath.FromSlash(rel)))
	if err != nil {
		return "", "", "", fmt.Errorf("%s isn't on the DTT canvas: %w", rel, err)
	}
	mode = DTTDocMode(rel, content)
	image, errFile = dttImagePaths(filepath.Join(metadata.RootPath, ReadSpacesDir, DTTImagesDir), rel)
	return mode, image, errFile, m.runCanvasDoc(metadata.RootPath, rel, mode)
}

// PreReadProposal reads a write proposal still awaiting the human's decision
// -- rel is relative to dtt-requests, e.g. write/repo.dtt.yaml -- with dtt
// read, before it is accepted or rejected (privateRepoOpsImpls Step 4,
// Revision B): reading only measures, so it needs no decision, and the
// human sees what an assert would change. It stays pending. Its image goes
// into dtt-images beside where the assert's would, as
// write/repo.read.image.json (the error file write/repo.read.error.txt), so
// the agent sees it too and the assert's own never replaces it. It returns
// the image's and error file's paths, and why there's no good image.
func (m *Manager) PreReadProposal(id, rel string) (image, errFile string, err error) {
	metadata, err := m.Get(id)
	if err != nil {
		return "", "", err
	}
	if metadata.Deployed {
		return "", "", fmt.Errorf("slopspace %s is deployed; read its proposals once it's returned", shortID(id))
	}
	waiting := false
	if d := metadata.PendingReview(); d != nil {
		for _, p := range d.Pending() {
			waiting = waiting || p == rel
		}
	}
	if !waiting {
		return "", "", fmt.Errorf("%q isn't a DTT proposal awaiting review in slopspace %s", rel, shortID(id))
	}
	doc := filepath.Join(metadata.RootPath, WriteSpacesDir, DTTRequestsDir, filepath.FromSlash(rel))
	image, errFile = dttImagePaths(filepath.Join(metadata.RootPath, ReadSpacesDir, DTTImagesDir), rel)
	image = strings.TrimSuffix(image, dttImageSuffix) + ".read" + dttImageSuffix
	errFile = strings.TrimSuffix(errFile, dttErrorSuffix) + ".read" + dttErrorSuffix
	return image, errFile, m.runDTTDoc(doc, rel, "read", image, errFile)
}

// runCanvasDoc runs the canvas document rel with dtt in mode (read, assert
// or delete) and writes its image, or an error file, under dtt-images.
func (m *Manager) runCanvasDoc(root, rel, mode string) error {
	imagePath, errPath := dttImagePaths(filepath.Join(root, ReadSpacesDir, DTTImagesDir), rel)
	return m.runDTTDoc(filepath.Join(root, ReadSpacesDir, DTTCanvasDir, filepath.FromSlash(rel)), rel, mode, imagePath, errPath)
}

// runDTTDoc runs the document doc (rel names it in errors) with dtt in mode
// and writes its image to imagePath, or an error file to errPath.
func (m *Manager) runDTTDoc(doc, rel, mode, imagePath, errPath string) error {
	if err := os.MkdirAll(filepath.Dir(imagePath), 0o755); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	fail := func(err error) error {
		err = fmt.Errorf("%s: %w", rel, err)
		os.WriteFile(errPath, []byte(time.Now().Format(time.RFC3339)+" dtt "+mode+" failed: "+err.Error()+"\n"), 0o644)
		return err
	}

	bin, err := m.findDTT()
	if err != nil {
		return fail(err)
	}
	timeout := dttTimeout(mode)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, mode, "--json", doc)
	cmd.Dir = filepath.Dir(doc)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	// dtt read prints the image even when a run fails part way; on bad input
	// it prints {"ok": false, "error": ...} instead, which isn't one.
	var head struct {
		Format string `json:"format"`
		Error  string `json:"error"`
	}
	json.Unmarshal(stdout.Bytes(), &head)
	if head.Format == dttImageFormat {
		if err := os.WriteFile(imagePath, stdout.Bytes(), 0o644); err != nil {
			return fail(err)
		}
	}
	if runErr != nil || head.Format != dttImageFormat {
		msg := head.Error
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		if ctx.Err() != nil {
			msg = fmt.Sprintf("stopped after %s", timeout)
		}
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		if msg == "" {
			msg = "dtt " + mode + " returned no image"
		}
		return fail(errors.New(msg))
	}
	os.Remove(errPath)
	return nil
}

// readCanvasImages runs readCanvasImage for each of rels that is a read
// document, describing the outcome for a review's detail ("" when there were
// none).
func (m *Manager) readCanvasImages(root string, rels []string) string {
	var ok, failed []string
	for _, rel := range rels {
		if !isReadDoc(rel) {
			continue
		}
		if err := m.readCanvasImage(root, rel); err != nil {
			failed = append(failed, err.Error())
		} else {
			ok = append(ok, rel)
		}
	}
	var parts []string
	if len(ok) > 0 {
		parts = append(parts, fmt.Sprintf("read %d into dtt-images", len(ok)))
	}
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d read(s) failed (%s)", len(failed), summariseProposals(failed, 2)))
	}
	return strings.Join(parts, "; ")
}

// missingCanvasImages lists the canvas's read documents, relative to the
// canvas, that have no image in dtt-images.
func missingCanvasImages(root string) []string {
	canvas := filepath.Join(root, ReadSpacesDir, DTTCanvasDir)
	images := filepath.Join(root, ReadSpacesDir, DTTImagesDir)
	var out []string
	filepath.WalkDir(filepath.Join(canvas, DTTReadDir), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(canvas, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if img, _ := dttImagePaths(images, rel); fileExists(img) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
