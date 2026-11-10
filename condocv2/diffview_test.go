package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// diffRepoFixture is a repo whose commits are given as subject -> files
// written, in order; it returns its git dir and each commit's sha.
func diffRepoFixture(t *testing.T, commits [][2]string) (string, []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git here")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	var shas []string
	for _, c := range commits {
		for _, f := range strings.Split(c[1], ",") {
			name, body, _ := strings.Cut(f, "=")
			os.WriteFile(filepath.Join(dir, name), []byte(body+"\n"), 0o644)
		}
		git("add", "-A")
		git("commit", "-q", "-m", c[0])
		shas = append(shas, git("rev-parse", "HEAD"))
	}
	return filepath.Join(dir, ".git"), shas
}

// TestDiffRanges: interaction N diffs the condoc's response commit and each
// target's commit; external N.e diffs each branch it moved, from..to.
func TestDiffRanges(t *testing.T) {
	condocDir, c := diffRepoFixture(t, [][2]string{
		{"start", "step.md=# Step"},
		{"condocv2: Foo begin", "step.md=# Step\nbegin"},
		{"condocv2: Foo prompt 1", "step.md=# Step\nprompt 1"},
		{"condocv2: Foo response 1", "step.md=# Step\nresponse 1,notes.md=agent notes"},
		{"condocv2: Foo record 1", "step.md=# Step\nrecord 1"},
	})
	targetDir, tg := diffRepoFixture(t, [][2]string{
		{"start", "main.go=package main"},
		{"condocv2: [condocv2-abcd] Foo commit 1", "main.go=package main\n// one,new.go=package main"},
		{"from outside", "main.go=package main\n// outside"},
	})
	spec := &PointerSpec{
		Name: "Foo", CondocRepo: "a/condocs", Targets: []string{"a/one"},
		Bases: []string{"a/condocs@" + c[0], "a/one@" + tg[0]},
		Externals: []ExternalChange{
			{Interaction: 1, Seq: 1, Repo: "a/one", From: tg[1], To: tg[2], Commits: 1, At: externalAtRefresh},
		},
	}
	gitDir := func(repo string) string {
		if repo == "a/condocs" {
			return condocDir
		}
		return targetDir
	}

	ranges, err := diffRanges(spec, "1", gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 2 || ranges[0].Lane != "condoc" || ranges[1].Repo != "a/one" {
		t.Fatalf("ranges %+v", ranges)
	}
	if ranges[0].From != c[2] || ranges[0].To != c[3] || ranges[1].From != tg[0] || ranges[1].To != tg[1] {
		t.Errorf("interaction 1's ranges %+v", ranges)
	}
	if ranges, err = withFiles(ranges); err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, r := range ranges {
		for _, f := range r.Files {
			files = append(files, r.Repo+"/"+f.Path)
		}
	}
	if strings.Join(files, " ") != "a/condocs/notes.md a/condocs/step.md a/one/main.go a/one/new.go" {
		t.Errorf("files %v", files)
	}
	content, truncated, err := fileDiff(ranges[1], "main.go")
	if err != nil || truncated || !strings.Contains(content, "+// one") {
		t.Errorf("main.go's diff %q (%v, %v)", content, truncated, err)
	}
	if h := diffHunks(content); len(h) != 1 || !strings.HasPrefix(h[0].Header, "@@") {
		t.Errorf("hunks %+v", h)
	}

	ranges, err = diffRanges(spec, "external-1.1", gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 1 || ranges[0].Repo != "a/one" || ranges[0].From != tg[1] || ranges[0].To != tg[2] {
		t.Fatalf("external ranges %+v", ranges)
	}
	if content, _, _ := fileDiff(ranges[0], "main.go"); !strings.Contains(content, "+// outside") || !strings.Contains(content, "-// one") {
		t.Errorf("the external change's diff %q", content)
	}

	for _, bad := range []string{"2", "external-1.2", "external-1", "1.1", "x"} {
		if _, err := diffRanges(spec, bad, gitDir); err == nil {
			t.Errorf("row %q diffs", bad)
		}
	}
}

func TestParseNumstat(t *testing.T) {
	got := parseNumstat([]byte("3\t1\ta.go\x00-\t-\timg.png\x00"))
	if len(got) != 2 || got[0] != (DiffFile{Path: "a.go", Added: 3, Deleted: 1}) || !got[1].Binary {
		t.Errorf("numstat %+v", got)
	}
}
