package main

import (
	"fmt"
	"strings"
)

// ufabuildFile is the committed build-request file the dev-repo watcher's
// "ufabuild watch" mode builds from, per
// je-sidestuff/sloppo's condocs/SlopspaceSlopspaceCondocs.md Step 2 (see
// docs/DevMode.md). It is a
// YAML file at the watched repo's root naming the *parent* of the commit
// that last changed it:
//
//	parent: 1a2b3c4
//
// So committing an update to it as the sole change, with the commit you
// were on as its parent, asks every LR following the branch with ufabuild
// watch on to build.
const ufabuildFile = ".ufabuild"

// ufabuildRequest is the watched repo's current .ufabuild build request, as
// read at one HEAD (see readUfabuildRequest).
type ufabuildRequest struct {
	// Commit is the full SHA of the commit that last changed .ufabuild, when
	// that .ufabuild names the commit's own parent -- i.e. a valid request.
	// Each distinct request is built once. "" when there is no valid request.
	Commit string
	// Note says why there's no valid request ("" when there is one), for the
	// dashboard: no .ufabuild, a removed one, an unparseable one, or one
	// naming some commit other than its commit's parent.
	Note string
}

// readUfabuildRequest finds the commit that last changed .ufabuild along
// HEAD's first-parent history and checks it names that commit's immediate
// parent: the commit must have exactly one parent (a merge's "parent" is
// ambiguous), and the named SHA must resolve to exactly that parent -- not
// an older ancestor, an ambiguous abbreviation or an unknown commit. Callers
// must hold opMu, like every other git invocation the watcher makes.
func (w *repoWatch) readUfabuildRequest() ufabuildRequest {
	commit, err := w.runGit("log", "-1", "--first-parent", "--format=%H", "--", ufabuildFile)
	if err != nil {
		return ufabuildRequest{Note: fmt.Sprintf("git log: %v", err)}
	}
	if commit == "" {
		return ufabuildRequest{Note: "no " + ufabuildFile + " committed"}
	}
	content, err := w.runGit("show", commit+":"+ufabuildFile)
	if err != nil {
		return ufabuildRequest{Note: ufabuildFile + " was removed in " + shortSHA(commit)}
	}
	named, err := parseUfabuild(content)
	if err != nil {
		return ufabuildRequest{Note: fmt.Sprintf("%s in %s: %v", ufabuildFile, shortSHA(commit), err)}
	}
	parents, err := w.runGit("rev-parse", commit+"^@")
	if err != nil {
		return ufabuildRequest{Note: fmt.Sprintf("git rev-parse: %v", err)}
	}
	fields := strings.Fields(parents)
	switch len(fields) {
	case 0:
		return ufabuildRequest{Note: ufabuildFile + " was added in a root commit, which has no parent"}
	case 1:
	default:
		return ufabuildRequest{Note: fmt.Sprintf("%s was changed in merge %s, which has no single immediate parent", ufabuildFile, shortSHA(commit))}
	}
	parent := fields[0]
	resolved, err := w.runGit("rev-parse", "--verify", "--quiet", named+"^{commit}")
	if err != nil || resolved == "" {
		return ufabuildRequest{Note: fmt.Sprintf("%s in %s names %s, which isn't a single known commit (its parent is %s)", ufabuildFile, shortSHA(commit), named, shortSHA(parent))}
	}
	if resolved != parent {
		return ufabuildRequest{Note: fmt.Sprintf("%s in %s names %s, not its parent %s", ufabuildFile, shortSHA(commit), named, shortSHA(parent))}
	}
	return ufabuildRequest{Commit: commit}
}

// parseUfabuild reads the parent commit a .ufabuild names: a mapping with a
// "parent" key holding a 7-40 character hex SHA (abbreviated or full),
// returned lowercased.
func parseUfabuild(content string) (string, error) {
	doc, err := parseYAML(content)
	if err != nil {
		return "", err
	}
	if doc == nil || doc.kind != yMap {
		return "", fmt.Errorf("expected a mapping with a parent key")
	}
	p := doc.get("parent")
	if p == nil || p.kind != yScalar {
		return "", fmt.Errorf("missing parent")
	}
	sha := strings.ToLower(strings.TrimSpace(p.str))
	if len(sha) < 7 || len(sha) > 40 {
		return "", fmt.Errorf("parent %q isn't a 7-40 character commit SHA", p.str)
	}
	for _, c := range sha {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", fmt.Errorf("parent %q isn't a commit SHA", p.str)
		}
	}
	return sha, nil
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// refreshUfabuild re-reads the .ufabuild request when HEAD has moved since
// it was last read. Callers must hold opMu but not mu. Returns true if the
// request changed.
func (w *repoWatch) refreshUfabuild() bool {
	w.mu.RLock()
	head, readAt := w.head, w.ufabuildReadAt
	w.mu.RUnlock()
	if head == readAt && head != "" {
		return false
	}
	req := w.readUfabuildRequest()
	w.mu.Lock()
	defer w.mu.Unlock()
	changed := w.ufabuild != req
	w.ufabuild = req
	w.ufabuildReadAt = head
	return changed
}

func (w *repoWatch) setUfabuildWatch(v bool) {
	w.mu.Lock()
	w.ufabuildWatch = v
	w.mu.Unlock()
	w.notify()
}

// setUfabuildWatch toggles the dev-repo watcher's ufabuild watch -- the
// system tab's "ufabuild watch" toggle or agent-coordinator's
// "__system:ufabuild-watch <on|off>". A no-op when no repo is being watched.
func (s *Server) setUfabuildWatch(v bool) {
	if s.repoWatch == nil {
		return
	}
	s.repoWatch.setUfabuildWatch(v)
}
