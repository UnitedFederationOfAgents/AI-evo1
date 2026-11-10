package main

// The diff view: condoccer's multi-repo git diff view, brought to condocv2
// for the rows of the timeline that move repos --
//
//	N            interaction N's commits: the agent's work, in its response
//	             commit on the condoc repo and its commit on each target
//	external-N.e external change N.e (external.go): each branch pulled in
//	             from outside condocv2, from where it was to where it went
//
// -- diffed repo by repo in the slopspace's git dirs, which stay in its
// writespaces-secure whether or not it is deployed. The UI (DiffViewer.tsx)
// opens it from the visual's "+/-" on those rows and from the text's records;
// condocv2 serves it at
//
//	api/diff/<stem>/<row>                         each repo's range and the files it changed
//	api/diff/<stem>/<row>/<owner>/<name>/<path>   one file's unified diff, and its hunks
//
// Once the slopspace is gone (merged) there is nothing to diff: its PRs have
// the changes.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const diffPrefix = "/api/diff/"

// diffLimit cuts a file's diff short.
const diffLimit = 256 << 10

// diffRowRe is a row: "N", or "external-N.e".
var diffRowRe = regexp.MustCompile(`^(?:(external)-(\d+)\.(\d+)|(\d+))$`)

// DiffRange is one repo's part of a row's diff.
type DiffRange struct {
	Repo    string     `json:"repo"`
	Lane    string     `json:"lane"` // "condoc", or the target's owner/name
	From    string     `json:"from"`
	To      string     `json:"to"`
	Commits int        `json:"commits"`
	URL     string     `json:"url,omitempty"` // the To commit
	Files   []DiffFile `json:"files"`

	gitDir string
}

// DiffFile is one file a range changed.
type DiffFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary,omitempty"`
}

// DiffHunk is a "@@" header of a file's diff, at its line in the content.
type DiffHunk struct {
	Header string `json:"header"`
	Line   int    `json:"line"`
}

func (r DiffRange) git(args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"--git-dir", r.gitDir}, args...)...)
}

// diffRanges resolves a row to a range in each repo it moved, the condoc repo
// first, then each target. gitDir is where a repo's git dir is.
func diffRanges(spec *PointerSpec, row string, gitDir func(repo string) string) ([]DiffRange, error) {
	m := diffRowRe.FindStringSubmatch(row)
	if m == nil {
		return nil, fmt.Errorf("bad row %q: want N or external-N.e", row)
	}
	repos := append([]string{spec.CondocRepo}, spec.Targets...)
	laneOf := func(repo string) string {
		if repo == spec.CondocRepo {
			return "condoc"
		}
		return repo
	}
	var out []DiffRange
	if m[1] == "external" {
		n, _ := strconv.Atoi(m[2])
		e, _ := strconv.Atoi(m[3])
		for _, repo := range repos {
			for _, x := range spec.Externals {
				if x.Interaction == n && x.Seq == e && x.Repo == repo {
					out = append(out, DiffRange{Repo: repo, Lane: laneOf(repo), From: x.From, To: x.To, Commits: x.Commits, gitDir: gitDir(repo)})
				}
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%s has no external change %d.%d", spec.Name, n, e)
		}
		return out, nil
	}

	// Interaction N's commits, read off each branch since it began as the
	// timeline reads them: the condoc's response, each target's commit (more
	// than one when a stage was retried).
	n, _ := strconv.Atoi(m[4])
	bases := repoShaMap(spec.Bases)
	for _, repo := range repos {
		r := DiffRange{Repo: repo, Lane: laneOf(repo), gitDir: gitDir(repo)}
		kind := "commit"
		if r.Lane == "condoc" {
			kind = "response"
		}
		rangeArg := "HEAD"
		if base := bases[repo]; base != "" {
			rangeArg = base + "..HEAD"
		}
		logOut, err := r.git("log", "--reverse", "--first-parent", gitLogFormat, rangeArg).Output()
		if err != nil {
			return nil, fmt.Errorf("%s: git log: %v", repo, err)
		}
		var shas []string
		for _, e := range timelineEvents(spec.Name, r.Lane, repo, parseGitLog(logOut), kind) {
			if e.Interaction == n && e.Sub == 0 {
				shas = append(shas, e.SHA)
			}
		}
		if len(shas) == 0 {
			continue
		}
		parent, err := r.git("rev-parse", "--verify", "--quiet", shas[0]+"^").Output()
		if err != nil {
			return nil, fmt.Errorf("%s: %s has no parent to diff from", repo, shas[0][:7])
		}
		r.From, r.To, r.Commits = strings.TrimSpace(string(parent)), shas[len(shas)-1], len(shas)
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("interaction %d of %s has committed nothing yet", n, spec.Name)
	}
	return out, nil
}

// withFiles fills in each range's files changed.
func withFiles(ranges []DiffRange) ([]DiffRange, error) {
	for i := range ranges {
		r := &ranges[i]
		if r.To != "" {
			r.URL = repoCommitURL(r.Repo, r.To)
		}
		out, err := r.git("diff", "--numstat", "--no-renames", "-z", r.From, r.To).Output()
		if err != nil {
			return nil, fmt.Errorf("%s: git diff: %v", r.Repo, err)
		}
		r.Files = parseNumstat(out)
	}
	return ranges, nil
}

// parseNumstat reads "git diff --numstat -z" output: "added\tdeleted\tpath\0"
// per file, "-" for a binary file's counts.
func parseNumstat(out []byte) []DiffFile {
	files := []DiffFile{}
	for _, rec := range strings.Split(string(out), "\x00") {
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		f := DiffFile{Path: parts[2], Binary: parts[0] == "-"}
		f.Added, _ = strconv.Atoi(parts[0])
		f.Deleted, _ = strconv.Atoi(parts[1])
		files = append(files, f)
	}
	return files
}

// diffHunks indexes the "@@" hunk headers of a unified diff.
func diffHunks(content string) []DiffHunk {
	hunks := []DiffHunk{}
	for i, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "@@") {
			hunks = append(hunks, DiffHunk{Header: line, Line: i})
		}
	}
	return hunks
}

// fileDiff is one file's diff in r, cut short at diffLimit.
func fileDiff(r DiffRange, file string) (content string, truncated bool, err error) {
	out, err := r.git("diff", "--no-renames", r.From, r.To, "--", file).Output()
	if err != nil {
		return "", false, fmt.Errorf("%s: git diff: %v", r.Repo, err)
	}
	if len(out) > diffLimit {
		return string(out[:diffLimit]), true, nil
	}
	return string(out), false, nil
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, diffPrefix), "/", 5)
	if len(parts) != 2 && len(parts) != 5 || parts[0] == "" {
		http.Error(w, "want "+diffPrefix+"<stem>/<row>[/<owner>/<name>/<path>]", http.StatusBadRequest)
		return
	}
	spec, err := s.readSpec(parts[0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if spec.merged() || spec.SlopspaceID == "" {
		http.Error(w, "the condoc's slopspace is gone: its PRs have the changes", http.StatusNotFound)
		return
	}
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	ranges, err := diffRanges(&spec, parts[1], func(repo string) string { return slopGitDir(meta, repo) })
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if len(parts) == 2 {
		if ranges, err = withFiles(ranges); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"row": parts[1], "repos": ranges})
		return
	}
	repo, file := parts[2]+"/"+parts[3], path.Clean(parts[4])
	if file == "." || path.IsAbs(file) || file == ".." || strings.HasPrefix(file, "../") {
		http.Error(w, "bad file", http.StatusBadRequest)
		return
	}
	for _, rg := range ranges {
		if rg.Repo != repo {
			continue
		}
		content, truncated, err := fileDiff(rg, file)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"repo": repo, "path": file, "content": content, "hunks": diffHunks(content), "truncated": truncated,
		})
		return
	}
	http.Error(w, fmt.Sprintf("%s isn't in row %s", repo, parts[1]), http.StatusNotFound)
}
