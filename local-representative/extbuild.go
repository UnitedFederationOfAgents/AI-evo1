package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// This file is the revised .ufabuild paradigm, per user-1/sloppo's
// condocs/privateRepoOpsImpls/Step6Prompt.md Revision D, as it runs for
// external paths registered as plug-in dev repos (extwatch.go) -- the first
// being ../HyenaExternalUfaPlugin. The watched repo itself uses the same
// request helpers below since condocs/switchToUfabuildImpls/Step1.md (see
// ufabuild.go), with its own build and auto-rebuild flow.
//
// In the revised paradigm .ufabuild is gitignored by convention: it is a
// local build request, never committed. The sequence for a plug-in dev repo:
//
//  1. Each poll pulls the repo's upstream (fetch, then fast-forward only;
//     never on a dirty tree, a detached HEAD, local-only commits, or while a
//     build runs there).
//  2. When HEAD moves -- by that pull or otherwise -- the commits it moved
//     over are scanned for a condoc tag, "[condocv2-xxxx]", which condocv2
//     puts on every commit holding an agent's work, after any conventional
//     commit prefix ("feat: [SRJ-3456] [condocv2-g6jc] Message"), or for a
//     "[ufabuild]" or "[ufabuild-xxxx]" tag, which asks for a build the same
//     way from a commit made outside condocv2. The v1 "[condoc-xxxx]" tag no
//     longer counts: condocs v1 is gone.
//  3. If one has it, the watcher writes .ufabuild at the repo's root naming
//     the commit to build. It makes sure .ufabuild is ignored first: if the
//     repo's .gitignore doesn't already, it is added to .git/info/exclude.
//  4. A .ufabuild present -- written in step 3, by the system tab's "build",
//     or by hand -- runs 'make deploy-dev-binaries DEV_PLUGIN_BIN=...' at the
//     repo's root. That target is the repo's hook: it builds the plug-in to
//     $(DEV_PLUGIN_BIN), and LR deploys it to /AI-evo1-dev/plugin/<app-id>
//     (Revision F, extlaunch.go).
//  5. The request is removed once its build has run, pass or fail (a failed
//     one isn't retried until another request lands). After a successful
//     build the plug-in's app id is approved on this host again, so its
//     hello is routed straight away, and a running instance of it -- a
//     managed app LR launched from the system tab -- is restarted.
//
// The app id is approved at registration too: registering the path is the
// operator's decision to trust the plug-in, as registering one manually on
// the plugins tab is.

// condocTagRe matches the tag condocv2 puts on a commit holding agent work,
// or a "[ufabuild]" / "[ufabuild-xxxx]" tag put on one by hand to ask for a
// build the same way.
var condocTagRe = regexp.MustCompile(`\[(?:condocv2-[0-9a-z]{4}|ufabuild(?:-[0-9a-z]{4})?)\]`)

// pluginAppIDFromPath derives a plug-in app id from a directory's name:
// HyenaExternalUfaPlugin is hyena-external-ufa-plugin. It may not be a valid
// app id (one starting with a digit, say); registration checks.
func pluginAppIDFromPath(abs string) string {
	var b strings.Builder
	lowerBefore := false
	dash := func() {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	for _, r := range filepath.Base(abs) {
		switch {
		case r >= 'A' && r <= 'Z':
			if lowerBefore {
				dash()
			}
			b.WriteRune(r - 'A' + 'a')
			lowerBefore = false
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lowerBefore = true
		default:
			dash()
			lowerBefore = false
		}
	}
	id := strings.Trim(b.String(), "-")
	if len(id) > 32 {
		id = strings.TrimRight(id[:32], "-")
	}
	return id
}

// preparePluginDevRepo is a new plug-in dev repo's setup: make sure its
// .ufabuild is ignored, and approve its app id on this host. What went
// wrong, if anything, is reported on the path's row.
func (w *repoWatch) preparePluginDevRepo(abs, appID string) {
	var notes []string
	if err := ensureUfabuildIgnored(abs); err != nil {
		notes = append(notes, err.Error())
	}
	note := w.approvePluginApp(appID)
	w.mu.Lock()
	if i := w.externalIndexLocked(abs); i >= 0 {
		e := w.ext.entries[i]
		e.PluginNote = note
		if len(notes) > 0 {
			e.BuildError = strings.Join(notes, "; ")
		}
	}
	w.mu.Unlock()
}

// approvePluginApp approves appID through ext.approve, returning the note
// for the path's row.
func (w *repoWatch) approvePluginApp(appID string) string {
	if w.ext.approve == nil {
		return appID + " not approved: approve it on the plugins tab"
	}
	if err := w.ext.approve(appID); err != nil {
		return "approving " + appID + ": " + err.Error()
	}
	return appID + " approved on this host"
}

// ensureUfabuildIgnored makes sure git ignores .ufabuild in the repo at abs:
// if neither its .gitignore nor anything else does, /.ufabuild is added to
// its .git/info/exclude (a local file, so nothing to commit). A tracked
// .ufabuild -- the old, committed paradigm -- is refused: the request would
// then be a change to commit.
func ensureUfabuildIgnored(abs string) error {
	git := externalGit(abs)
	if _, err := git("ls-files", "--error-unmatch", "--", ufabuildFile); err == nil {
		return fmt.Errorf("%s is tracked in %s: untrack it (git rm --cached %s) and gitignore it", ufabuildFile, abs, ufabuildFile)
	}
	if _, err := git("check-ignore", "-q", ufabuildFile); err == nil {
		return nil
	}
	p, err := git("rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("finding %s's info/exclude: %v (%s)", abs, err, p)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(abs, p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString("\n# local-representative's " + ufabuildFile + " build requests -- gitignored by convention\n/" + ufabuildFile + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		log.Printf("dev-repo: %s didn't ignore %s; added it to %s", abs, ufabuildFile, p)
	}
	return err
}

// writeUfabuildRequest writes the .ufabuild request at abs for commit,
// noting the condoc tag (or "manual") that asked for it and the commit that
// carried it.
func writeUfabuildRequest(abs, commit, tag, tagged string) error {
	if err := ensureUfabuildIgnored(abs); err != nil {
		return err
	}
	content := fmt.Sprintf("# A build request for local-representative's dev-repo watcher: gitignored,\n"+
		"# and removed once 'make deploy-dev-binaries' has run for it.\n"+
		"commit: %s\ntag: %s\ntagged: %s\nrequested_at: %s\n",
		commit, tag, tagged, time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(filepath.Join(abs, ufabuildFile), []byte(content), 0o644)
}

// readUfabuildRequestFile reads the .ufabuild request at abs, if there is one:
// its content, and the short commit it names ("pending" for one naming none,
// e.g. made by hand with touch).
func readUfabuildRequestFile(abs string) (content, label string, ok bool) {
	b, err := os.ReadFile(filepath.Join(abs, ufabuildFile))
	if err != nil {
		return "", "", false
	}
	content, label = string(b), "pending"
	for _, line := range strings.Split(content, "\n") {
		if v, found := strings.CutPrefix(strings.TrimSpace(line), "commit:"); found && strings.TrimSpace(v) != "" {
			label = shortSHA(strings.TrimSpace(v))
			break
		}
	}
	return content, label, true
}

// requestTaggedBuild scans the commits HEAD moved over, from..to, for a
// condoc tag, and writes a .ufabuild request for to if one has it.
func requestTaggedBuild(abs, from, to string) error {
	out, err := externalGit(abs)("log", "--format=%H%x1f%s", from+".."+to)
	if err != nil {
		return fmt.Errorf("git log %s..%s: %v (%s)", shortSHA(from), shortSHA(to), err, out)
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
		// git log lists newest first, so this is the latest tagged commit.
		if err := writeUfabuildRequest(abs, to, strings.Trim(tag, "[]"), sha); err != nil {
			return fmt.Errorf("writing %s: %v", ufabuildFile, err)
		}
		log.Printf("dev-repo: %s: %s in %s; wrote a %s request for %s", abs, tag, shortSHA(sha), ufabuildFile, shortSHA(to))
		return nil
	}
	return nil
}

// requestExternalBuild writes a .ufabuild request for a plug-in dev repo's
// current HEAD -- the system tab's "build". p is the path as registered or
// as the absolute path it resolved to.
func (w *repoWatch) requestExternalBuild(p string) error {
	p = strings.TrimSpace(p)
	w.mu.RLock()
	abs, plugin, found := "", false, false
	for _, e := range w.ext.entries {
		if e.Path == p || e.Abs == p {
			abs, plugin, found = e.Abs, e.Plugin, true
			break
		}
	}
	w.mu.RUnlock()
	var err error
	switch {
	case !found:
		err = fmt.Errorf("%s is not registered", p)
	case !plugin:
		err = fmt.Errorf("%s isn't registered as a plug-in dev repo", p)
	default:
		var head string
		if head, err = externalGit(abs)("rev-parse", "HEAD"); err != nil {
			err = fmt.Errorf("%s: git rev-parse HEAD: %v (%s)", abs, err, head)
		} else {
			err = writeUfabuildRequest(abs, head, "manual", head)
		}
	}
	w.mu.Lock()
	w.ext.err = ""
	if err != nil {
		w.ext.err = err.Error()
		log.Printf("dev-repo: external build of %q: %v", p, err)
	}
	w.mu.Unlock()
	w.notify()
	return err
}

// startExternalBuilds refreshes each plug-in dev repo's pending .ufabuild
// request and starts a build for one not already building. Called with
// ext.pollMu held, so two polls never start the same build. Returns whether
// anything reported changed.
func (w *repoWatch) startExternalBuilds() bool {
	type pending struct{ content, label string }
	w.mu.RLock()
	var dirs []string
	for _, e := range w.ext.entries {
		if e.Plugin {
			dirs = append(dirs, e.Abs)
		}
	}
	w.mu.RUnlock()
	reqs := map[string]*pending{}
	for _, abs := range dirs {
		if content, label, ok := readUfabuildRequestFile(abs); ok {
			reqs[abs] = &pending{content, label}
		}
	}

	changed := false
	type start struct{ abs, appID, content string }
	var starts []start
	w.mu.Lock()
	for _, e := range w.ext.entries {
		if !e.Plugin || e.Building {
			continue
		}
		r := reqs[e.Abs]
		label := ""
		if r != nil {
			label = r.label
		}
		if e.Ufabuild != label {
			e.Ufabuild = label
			changed = true
		}
		if r != nil && e.Git {
			e.Building = true
			changed = true
			starts = append(starts, start{e.Abs, e.AppID, r.content})
		}
	}
	w.mu.Unlock()
	for _, s := range starts {
		go w.runExternalBuild(s.abs, s.appID, s.content)
	}
	return changed
}

// runExternalBuild runs 'make deploy-dev-binaries' in a plug-in dev repo for
// the .ufabuild request whose content is req, removes the request (unless it
// was rewritten meanwhile: that one builds next), and after a successful
// build deploys the binary it staged to /AI-evo1-dev/plugin, approves the
// plug-in and restarts it if it's running (extlaunch.go). Its entry was
// marked Building by startExternalBuilds, and stays so until the restart is
// done.
func (w *repoWatch) runExternalBuild(abs, appID, req string) {
	var out []byte
	buildErr := prepareDevPluginStaging(appID)
	if buildErr == nil {
		args := devPluginMakeArgs(appID)
		log.Printf("dev-repo: running 'make %s' in %s for its %s request", strings.Join(args, " "), abs, ufabuildFile)
		cmd := exec.Command("make", args...)
		cmd.Dir = abs
		out, buildErr = cmd.CombinedOutput()
		if buildErr == nil {
			if err := deployDevPlugin(appID); err != nil {
				buildErr, out = err, nil
			}
		}
	}

	reqPath := filepath.Join(abs, ufabuildFile)
	if cur, err := os.ReadFile(reqPath); err == nil && string(cur) == req {
		os.Remove(reqPath)
	}
	head, headErr := externalGit(abs)("rev-parse", "--short=7", "HEAD")
	note, launchNote := "", ""
	if buildErr == nil {
		log.Printf("dev-repo: %s built", abs)
		note = w.approvePluginApp(appID)
		if w.ext.built != nil {
			launchNote = w.ext.built(appID)
		}
	} else {
		log.Printf("dev-repo: building %s failed: %v", abs, buildErr)
	}

	w.mu.Lock()
	if i := w.externalIndexLocked(abs); i >= 0 {
		e := w.ext.entries[i]
		e.Building = false
		e.Ufabuild = ""
		e.BuildAt = time.Now().Unix()
		if buildErr != nil {
			e.BuildError = buildErr.Error()
			if tail := lastLines(string(out), 5); tail != "" {
				e.BuildError += ": " + tail
			}
		} else {
			e.BuildError = ""
			if headErr == nil {
				e.BuiltHead = head
			}
			e.PluginNote = note
			e.Deployed = true
			e.LaunchNote = launchNote
		}
	}
	w.mu.Unlock()
	w.notify()
}

// lastLines is the last n non-empty lines of s, joined with " | ".
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// pullExternal brings a plug-in dev repo up to its upstream: fetch, then
// fast-forward only. It returns why it didn't, if it didn't ("" when it
// pulled or there was nothing to pull).
func pullExternal(abs string) string {
	st := readExternal(abs)
	switch {
	case !st.Git:
		return "" // the row's note already says why
	case st.Branch == "":
		return "HEAD is detached: not pulling"
	case st.Dirty:
		return "uncommitted changes: not pulling"
	}
	git := externalGit(abs)
	if _, err := git("rev-parse", "--abbrev-ref", "@{u}"); err != nil {
		return "no upstream branch: not pulling"
	}
	if out, err := git("fetch", "--quiet"); err != nil {
		return "git fetch failed: " + lastLines(out, 2)
	}
	behind, err := git("rev-list", "--count", "HEAD..@{u}")
	if err != nil || behind == "0" {
		return ""
	}
	if ahead, err := git("rev-list", "--count", "@{u}..HEAD"); err != nil {
		return "git rev-list failed: " + lastLines(ahead, 2)
	} else if ahead != "0" {
		return ahead + " local commit(s) not on the upstream: not pulling"
	}
	if out, err := git("merge", "--ff-only", "--quiet", "@{u}"); err != nil {
		return "git merge --ff-only failed: " + lastLines(out, 2)
	}
	log.Printf("dev-repo: pulled %s commit(s) into %s", behind, abs)
	return ""
}
