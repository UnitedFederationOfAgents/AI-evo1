package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ufabuildFile is the build-request file the dev-repo watcher builds from,
// per user-1/sloppo's condocs/switchToUfabuildImpls/Step1.md (see
// docs/DevMode.md). It is the same revised .ufabuild paradigm plug-in dev
// repos use (extbuild.go): a local request at the repo's root, gitignored by
// convention and never committed. The watcher writes one whenever a commit
// it pulls in carries a condoc tag ("[condocv2-xxxx]", which condocv2 puts on
// every commit holding an agent's work) or a "[ufabuild]" / "[ufabuild-xxxx]"
// tag (put on a commit by hand), naming the commit to build:
//
//	commit: <sha>
//	tag: condocv2-xxxx
//	tagged: <sha of the tagged commit>
//	requested_at: <RFC 3339 time>
//
// scripts/ufabuild.sh writes one by hand for the current HEAD. Only a
// request makes the rebuild button ready; with auto-rebuild on, it builds
// once the request has sat for autoRebuildDebounce without a newer one
// landing. A successful build removes the request; a failed one leaves it
// for the operator to retry with the rebuild button.
const ufabuildFile = ".ufabuild"

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// scanForUfabuild scans the commits HEAD has moved over since the last scan
// for a condoc tag, writing a .ufabuild request for the new HEAD if one has
// it (requestTaggedBuild, extbuild.go). The starting HEAD counts as scanned
// (see newRepoWatch), so only commits that land while watching -- by a pull,
// a branch switch or by hand -- ask for a build. Then, once per HEAD, it
// catches up against the build record (catchUpUfabuild). Callers must hold
// opMu but not mu.
func (w *repoWatch) scanForUfabuild() {
	head, err := w.runGit("rev-parse", "HEAD")
	if err != nil {
		log.Printf("dev-repo: git rev-parse HEAD failed: %v", err)
		return
	}
	w.mu.RLock()
	from, caughtUp := w.ufabuildScanned, w.ufabuildCaughtUp
	w.mu.RUnlock()
	if from != head {
		var scanErr string
		if from != "" {
			if err := requestTaggedBuild(w.root, from, head); err != nil {
				scanErr = err.Error()
				log.Printf("dev-repo: scanning for condoc tags: %v", err)
			}
		}
		w.mu.Lock()
		w.ufabuildScanned = head
		w.ufabuildScanErr = scanErr
		w.mu.Unlock()
	}
	if caughtUp != head {
		w.catchUpUfabuild(head)
	}
}

// catchUpUfabuild is the scan's safety net: with no request waiting, it
// writes one for head if a sub-app that changed since its last recorded
// build (planBuild, ufaplan.go) was changed by a condoc-tagged commit since
// that build. The incremental scan only sees commits HEAD moves over while
// it is watching, so a tagged commit it missed -- one that landed while the
// watcher was stalled, or before an LR that started without a prior
// instance's scan point -- would otherwise sit unbuilt behind a panel saying
// it's waiting for one. Only a tagged commit that changed the sub-app itself
// counts, so an untagged change still never asks for a build on its own. A
// sub-app never built here, or built from a commit this repo doesn't have,
// doesn't count either: there's no "since" to scan. Runs once per HEAD, so a
// build that doesn't update the record can't make it ask again and again.
// Callers must hold opMu but not mu.
func (w *repoWatch) catchUpUfabuild(head string) {
	w.mu.Lock()
	w.ufabuildCaughtUp = head
	tracked := w.ufabuildTracked
	w.mu.Unlock()
	if tracked {
		return
	}
	if _, err := os.Stat(filepath.Join(w.root, ufabuildFile)); err == nil {
		return // a request is already waiting
	}
	apps, err := w.loadSubApps()
	if err != nil {
		return // can't tell what changed: leave it to the scan
	}
	plan, err := w.planSubApps(apps)
	if err != nil {
		return
	}
	rec := readBuiltRecord(w.binDir)
	for _, a := range apps {
		built := rec[a.name]
		if built == "" || !slices.Contains(plan, a.name) {
			continue
		}
		if _, err := w.runGit("cat-file", "-e", built+"^{commit}"); err != nil {
			continue
		}
		sha, tag, err := w.taggedCommitChanging(built, head, a.inputs)
		if err != nil {
			log.Printf("dev-repo: catching up on condoc tags since %s's build: %v", a.name, err)
			continue
		}
		if tag == "" {
			continue
		}
		if err := writeUfabuildRequest(w.root, head, strings.Trim(tag, "[]"), sha); err != nil {
			log.Printf("dev-repo: writing %s: %v", ufabuildFile, err)
			w.mu.Lock()
			w.ufabuildScanErr = fmt.Sprintf("writing %s: %v", ufabuildFile, err)
			w.mu.Unlock()
			return
		}
		log.Printf("dev-repo: %s in %s isn't built yet (%s was last built from %s); wrote a %s request for %s",
			tag, shortSHA(sha), a.name, shortSHA(built), ufabuildFile, shortSHA(head))
		return
	}
}

// taggedCommitChanging finds the latest condoc-tagged commit in from..to
// that changed one of inputs, as planBuild counts a change
// (buildInputChanged), returning it and its tag ("" when none did).
func (w *repoWatch) taggedCommitChanging(from, to string, inputs []string) (sha, tag string, err error) {
	out, err := w.runGit("log", "--format=%H%x1f%s", from+".."+to)
	if err != nil {
		return "", "", fmt.Errorf("git log %s..%s: %v (%s)", shortSHA(from), shortSHA(to), err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		sha, subject, found := strings.Cut(line, "\x1f")
		if !found {
			continue
		}
		tag := condocTagRe.FindString(subject)
		if tag == "" {
			continue
		}
		files, err := w.runGit("diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "--root", sha)
		if err != nil {
			return "", "", fmt.Errorf("git diff-tree %s: %v (%s)", shortSHA(sha), err, files)
		}
		// git log lists newest first, so the first match is the latest.
		if buildInputChanged(strings.Fields(files), inputs) {
			return sha, tag, nil
		}
	}
	return "", "", nil
}

// refreshUfabuild re-reads the watched repo's .ufabuild request. A tracked
// .ufabuild -- the old, committed paradigm, still on branches from before
// it -- isn't a request. Callers must hold opMu but not mu. Returns true if
// the reported request changed.
func (w *repoWatch) refreshUfabuild() bool {
	content, label, ok := readUfabuildRequestFile(w.root)
	tracked := false
	if ok {
		if _, err := w.runGit("ls-files", "--error-unmatch", "--", ufabuildFile); err == nil {
			content, label, tracked = "", "", true
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	changed := w.ufabuildLabel != label || w.ufabuildContent != content || w.ufabuildTracked != tracked
	w.ufabuildContent, w.ufabuildLabel, w.ufabuildTracked = content, label, tracked
	return changed
}

// ufabuildNoteLocked says what's notable about the current request, for the
// panel's ".ufabuild:" line. Callers must hold mu (read or write).
func (w *repoWatch) ufabuildNoteLocked() string {
	switch {
	case w.ufabuildTracked:
		return ufabuildFile + " is tracked on this branch (the old committed paradigm): ignored"
	case w.ufabuildScanErr != "":
		return w.ufabuildScanErr
	case w.ufabuildLabel == "" && w.pullErr != "":
		return "no request, and can't pull the upstream to look for one: " + w.pullErr
	case w.ufabuildLabel == "":
		return "no request: waiting for a [condocv2-xxxx] or [ufabuild] commit"
	case w.ufabuildContent == w.ufabuildFailed:
		return "its last build failed: press rebuild to retry"
	}
	return ""
}

// ufabuildScannedHead is the commit the last condoc-tag scan reached, carried
// across a restart in lrState.UfabuildScanned.
func (w *repoWatch) ufabuildScannedHead() string {
	if w == nil {
		return ""
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.ufabuildScanned
}

// setUfabuildScanned restores the scan point a prior instance reached (see
// lrState.UfabuildScanned), so tagged commits that landed around a restart
// still write a request on the next poll.
func (w *repoWatch) setUfabuildScanned(sha string) {
	w.mu.Lock()
	w.ufabuildScanned = sha
	w.mu.Unlock()
}

func (w *repoWatch) pullErrSnapshot() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.pullErr
}

// describeUfabuild is a one-line summary of the current request for logs.
func (w *repoWatch) describeUfabuild() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.ufabuildLabel == "" {
		return "manual rebuild"
	}
	return fmt.Sprintf("%s request for %s", ufabuildFile, w.ufabuildLabel)
}
