package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain lets this test binary stand in for dtt: the built-in declarative
// tools' scripts run "$DTT_BIN tool-engine ...", and in a test DTT_BIN is
// this binary.
func TestMain(m *testing.M) {
	if os.Getenv("DTT_TEST_BE_DTT") == "1" {
		os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

var testNow = time.Date(2026, 10, 24, 15, 4, 5, 0, time.UTC)

func testEngine(mode string, force bool) *engine {
	e := newEngine(mode, force, io.Discard)
	e.now = func() time.Time { return testNow }
	return e
}

// roundTrip passes an image through JSON, as one read back from a file is.
func roundTrip(t *testing.T, img *Image) *Image {
	t.Helper()
	b, err := json.Marshal(img)
	if err != nil {
		t.Fatal(err)
	}
	out, err := parseImage(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func readExample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.FromSlash(compositionsRoot), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func outcomes(img *Image) string {
	var parts []string
	for _, f := range img.Fields {
		parts = append(parts, f.Field+"="+f.Outcome)
	}
	return strings.Join(parts, " ")
}

func TestExamplesCheck(t *testing.T) {
	all, err := readExamples()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 5 {
		t.Fatalf("only %d examples", len(all))
	}
	for _, ex := range all {
		if ex.Error != "" || ex.Tool == "" {
			t.Fatalf("%s: tool %q, error %s", ex.Name, ex.Tool, ex.Error)
		}
		if ex.Kind == "call" {
			continue // a tool's inputs, not a document; TestExamples checks it
		}
		doc, err := parseDocument(ex.Source)
		if err != nil {
			t.Fatalf("%s: %v", ex.Name, err)
		}
		if err := testEngine("read", false).check(doc); err != nil {
			t.Fatalf("%s: %v", ex.Name, err)
		}
	}
}

func TestGetPathMember(t *testing.T) {
	state := map[string]any{"labels": []any{"a", "b"}, "x[y]": "literal"}
	for _, tc := range []struct {
		path string
		want any
	}{{"labels[a]", "a"}, {"labels[c]", nil}, {"x[y]", "literal"}, {"nothing[a]", nil}} {
		if got := getPath(state, []string{tc.path}); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestTabbedDocument reads the files example as it was first written, with
// tabs in its indents.
func TestTabbedDocument(t *testing.T) {
	src := "filesystem.items:\n  select:\n    abspath: \"/tmp/\"\n  alias: temp-folder\n\ntemp-folder:\n  assert:\n    items:\n" +
		"\t  my-json.json:\n\t    type: json\n\t\tcontent:\n\t\t  name: \"My Json File.\"\n\t\t  contents1: stuff\n" +
		"\t\t  contents2:\n\t\t    nested: \n\t\t\t  nested:\n\t\t\t    stuff\n\t\t\tflatter:\n\t\t\t  stuff\n"
	doc, err := parseDocument(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc) != 2 || doc[1].key != "temp-folder" {
		t.Fatalf("statements %+v", doc)
	}
	a, err := parseFSAssert(doc[1].assert)
	if err != nil {
		t.Fatal(err)
	}
	v, err := testEngine("read", false).value(a.items[0].fields[0].want)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if want := `{"name":"My Json File.","contents1":"stuff","contents2":{"nested":{"nested":"stuff"},"flatter":"stuff"}}`; string(b) != want {
		t.Errorf("content %s\nwant    %s", b, want)
	}
}

func TestDocumentErrors(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"unknown root", "gitlab.x:\n  read: *\n", "neither a DTT path"},
		{"alias select", "timestamp:\n  alias: t\nt:\n  select: now\n", "is an alias"},
		{"alias twice", "timestamp:\n  alias: t\ntimestamp:\n  alias: t\n", "already given"},
		{"bad ref", "filesystem.items:\n  select: {abspath: /tmp}\n  assert:\n    items:\n      a.txt:\n        content: ${{nowhere.unix}}\n", "${{nowhere.unix}}"},
		{"timestamp assert", "timestamp:\n  assert:\n    unix: 1\n", "only be read"},
		{"stray from", "filesystem.items:\n  select: {abspath: /tmp}\n  assert:\n    items:\n      a.txt:\n        from.content: x\n", "has no content beside it"},
		{"relative abspath", "filesystem.items:\n  select: {abspath: tmp}\n", "absolute"},
		{"pr needs from", "github.orgs.o.repos.r:\n  assert:\n    prs:\n      select:\n        to: main\n", "needs from"},
		{"pr state key", "github.orgs.o.repos.r:\n  assert:\n    prs:\n      select: {from: a}\n      state:\n        colour: red\n", `takes no "colour"`},
		{"aws path", "aws.accounts.current.lambda.functions:\n  read: *\n", "aws paths"},
		{"empty", "# nothing\n", "no statements"},
		{"phase again", "timestamp:\n  phase: a\ntimestamp:\n  select: \"2026-10-24T00:00:00Z\"\n  phase: b\ntimestamp:\n  select: \"2026-10-25T00:00:00Z\"\n  phase: a\n", "phase a began on line 1"},
		{"phase name", "timestamp:\n  phase: Big Phase\n", "phase names"},
		{"call select", "dtt.calls:\n  select: {doc: x}\n", `takes no "doc"`},
		{"call path", "dtt.things:\n  select: {document: x}\n", "dtt.calls"},
		{"call missing", "dtt.calls:\n  select: {document: nowhere.dtt.yaml}\n", "nowhere.dtt.yaml isn't there"},
		{"call variable", "dtt.calls:\n  select: {document: localrep-demo-prepare.dtt.yaml}\n  assert:\n    variables:\n      nod: x\n", "only declares node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := testEngine("read", false).runSource(context.Background(), tc.src, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
	// A built-in tool reads only its own provider's statements.
	e := testEngine("read", false)
	e.only = "github"
	if _, err := e.runSource(context.Background(), readExample(t, "filesystem-json.dtt.yaml"), nil); err == nil || !strings.Contains(err.Error(), "the github tool") {
		t.Fatalf("got %v", err)
	}
}

func TestFilesystemAssert(t *testing.T) {
	dir := t.TempDir()
	src := strings.Replace(readExample(t, "filesystem-json.dtt.yaml"), `"/tmp/"`, dir, 1)
	file := filepath.Join(dir, "my-json.json")
	ctx := context.Background()

	// Reading changes nothing, and says what an assert would do.
	img, err := testEngine("read", false).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=would-change content=would-change" {
		t.Errorf("read: %s", got)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("read wrote the file")
	}

	img, err = testEngine("assert", false).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=changed content=changed" {
		t.Errorf("first assert: %s", got)
	}
	var got map[string]any
	b, _ := os.ReadFile(file)
	if err := json.Unmarshal(b, &got); err != nil || got["name"] != "My Json File." {
		t.Fatalf("my-json.json holds %s (%v)", b, err)
	}
	if !strings.HasPrefix(string(b), "{\n  \"name\": \"My Json File.\",\n  \"contents1\": \"stuff\",") {
		t.Errorf("keys should stay in document order: %s", b)
	}
	if len(img.Objects) != 2 || img.Objects[0].Alias != "temp-folder" || img.Objects[1].State["exists"] != true {
		t.Errorf("objects %+v", img.Objects)
	}
	// Every field is measured, and the directory is measured again after
	// the assert, keeping what it was first measured as.
	if s := img.Objects[1].State; s["mode"] != "0644" || s["owner"] == nil || s["group"] == nil {
		t.Errorf("item state %+v", s)
	}
	if items := stringList(img.Objects[0].State["items"]); len(items) != 1 || items[0] != "my-json.json" {
		t.Errorf("directory items %v", img.Objects[0].State["items"])
	}
	if m := img.Objects[0].Measured; m == nil || len(stringList(m["items"])) != 0 {
		t.Errorf("directory measured %+v", m)
	}

	if img, _ = testEngine("assert", false).runSource(ctx, src, nil); outcomes(img) != "content=unchanged" {
		t.Errorf("second assert: %s", outcomes(img))
	}

	// Someone else's content is held...
	os.WriteFile(file, []byte(`{"name": "theirs"}`), 0o644)
	seen, _ := testEngine("read", false).runSource(ctx, src, nil)
	if img, _ = testEngine("assert", false).runSource(ctx, src, nil); outcomes(img) != "content=held" {
		t.Errorf("assert over other content: %s", outcomes(img))
	}
	if b, _ := os.ReadFile(file); string(b) != `{"name": "theirs"}` {
		t.Errorf("a held field was changed: %s", b)
	}
	// ...unless the image the caller saw it in is supplied...
	if img, _ = testEngine("assert", false).runSource(ctx, src, roundTrip(t, seen)); outcomes(img) != "content=changed" {
		t.Errorf("assert with the image: %s", outcomes(img))
	}
	// ...but not when it changed again since that image.
	os.WriteFile(file, []byte(`{"name": "theirs again"}`), 0o644)
	if img, _ = testEngine("assert", false).runSource(ctx, src, roundTrip(t, seen)); outcomes(img) != "content=held" {
		t.Errorf("assert with a stale image: %s", outcomes(img))
	}
	if img, _ = testEngine("assert", true).runSource(ctx, src, nil); outcomes(img) != "content=changed" {
		t.Errorf("assert --force: %s", outcomes(img))
	}

	// exists: false removes it -- only from the image, as it isn't empty.
	rm := src + "\ntemp-folder:\n  assert:\n    items:\n      my-json.json:\n        exists: false\n"
	rm = strings.Replace(rm, "my-json.json:\n        type: json", "other.json:\n        type: json", 1)
	if img, _ = testEngine("assert", false).runSource(ctx, rm, nil); !strings.Contains(outcomes(img), "exists=held") {
		t.Errorf("removing without an image: %s", outcomes(img))
	}
	seen, _ = testEngine("read", false).runSource(ctx, rm, nil)
	if img, _ = testEngine("assert", false).runSource(ctx, rm, roundTrip(t, seen)); !strings.Contains(outcomes(img), "exists=changed") {
		t.Errorf("removing with an image: %s", outcomes(img))
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("my-json.json is still there")
	}
}

func TestFilesystemDelete(t *testing.T) {
	dir := t.TempDir()
	src := strings.Replace(readExample(t, "filesystem-json.dtt.yaml"), `"/tmp/"`, dir, 1)
	file := filepath.Join(dir, "my-json.json")
	ctx := context.Background()
	made, err := testEngine("assert", false).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Delete mode removes what the assert made, by the from rule: held
	// without the image (or force), removed with it; content isn't asserted.
	if img, _ := testEngine("delete", false).runSource(ctx, src, nil); outcomes(img) != "exists=held" {
		t.Errorf("delete without an image: %s", outcomes(img))
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("a held delete removed the file")
	}
	img, err := testEngine("delete", false).runSource(ctx, src, roundTrip(t, made))
	if err != nil || outcomes(img) != "exists=changed" {
		t.Fatalf("delete with the image: %v %s", err, outcomes(img))
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("my-json.json is still there")
	}
	// The selected directory itself is only measured, never removed.
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("delete removed the directory: %v", err)
	}
	if img, _ = testEngine("delete", true).runSource(ctx, src, nil); outcomes(img) != "exists=unchanged" {
		t.Errorf("a second delete: %s", outcomes(img))
	}
}

// fakeGitHub is just enough of GitHub's REST API for pull requests, and for
// user-1's repositories (besides AI-sandboxing) when repos isn't nil.
// user-1 is a user, whose token this is, unless org is set.
type fakeGitHub struct {
	mu     sync.Mutex
	pulls  []*fakePull
	issues []*fakeIssue // numbered after the pulls, as GitHub shares the numbers
	repos  map[string]*fakeRepo
	org    bool
	calls  []string
}

type fakeRepo struct {
	Name, Description, DefaultBranch string
	Private, AutoInit                bool
}

func (r *fakeRepo) json() map[string]any {
	return map[string]any{"name": r.Name, "full_name": "user-1/" + r.Name, "private": r.Private, "description": r.Description,
		"default_branch": r.DefaultBranch, "html_url": "https://github.test/" + r.Name}
}

type fakePull struct {
	Number      int
	Title, Body string
	Head, Base  string
	Closed      bool
	Labels      []string
	Reviewers   []string
}

func (p *fakePull) json() map[string]any {
	labels, reviewers := []any{}, []any{}
	for _, l := range p.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	for _, r := range p.Reviewers {
		reviewers = append(reviewers, map[string]any{"login": r})
	}
	return map[string]any{"number": p.Number, "title": p.Title, "body": p.Body, "html_url": fmt.Sprintf("https://github.test/pull/%d", p.Number),
		"head": map[string]any{"ref": p.Head, "label": "user-1:" + p.Head}, "base": map[string]any{"ref": p.Base},
		"labels": labels, "requested_reviewers": reviewers}
}

type fakeIssue struct {
	Number      int
	Title, Body string
	Closed      bool
	Labels      []string
	Assignees   []string
}

func (is *fakeIssue) json() map[string]any {
	labels, assignees := []any{}, []any{}
	for _, l := range is.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	for _, a := range is.Assignees {
		assignees = append(assignees, map[string]any{"login": a})
	}
	return map[string]any{"number": is.Number, "title": is.Title, "body": is.Body, "html_url": fmt.Sprintf("https://github.test/issues/%d", is.Number),
		"labels": labels, "assignees": assignees}
}

// without returns list less the names in drop.
func without(list []string, drop ...string) []string {
	var out []string
	for _, s := range list {
		keep := true
		for _, d := range drop {
			keep = keep && s != d
		}
		if keep {
			out = append(out, s)
		}
	}
	return out
}

func anyStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, s := range list {
		out = append(out, fmt.Sprint(s))
	}
	return out
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const repo = "/repos/user-1/AI-sandboxing"
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	var in map[string]any
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&in)
	}
	str := func(k string) string { s, _ := in[k].(string); return s }
	// The number in .../pulls/<n>[/...] or .../issues/<n>[/...].
	pull := func(prefix string) *fakePull {
		rest := strings.TrimPrefix(r.URL.Path, repo+prefix)
		n, _ := strconv.Atoi(strings.SplitN(rest, "/", 2)[0])
		if n < 1 || n > len(f.pulls) {
			return nil
		}
		return f.pulls[n-1]
	}
	// The issue (not a PR) in .../issues/<n>[/...], or nil.
	issue := func() *fakeIssue {
		rest := strings.TrimPrefix(r.URL.Path, repo+"/issues/")
		n, _ := strconv.Atoi(strings.SplitN(rest, "/", 2)[0])
		for _, is := range f.issues {
			if is.Number == n {
				return is
			}
		}
		return nil
	}
	switch p := r.URL.Path; {
	case r.Method == "GET" && p == repo+"/issues":
		out := []any{}
		for _, pr := range f.pulls {
			if !pr.Closed {
				out = append(out, map[string]any{"number": pr.Number, "title": pr.Title, "pull_request": map[string]any{}})
			}
		}
		for _, is := range f.issues {
			if !is.Closed {
				out = append(out, is.json())
			}
		}
		if r.URL.Query().Get("page") > "1" {
			out = []any{}
		}
		reply(out)
	case r.Method == "POST" && p == repo+"/issues":
		is := &fakeIssue{Number: len(f.pulls) + len(f.issues) + 1, Title: str("title"), Body: str("body")}
		f.issues = append(f.issues, is)
		w.WriteHeader(http.StatusCreated)
		reply(is.json())
	case strings.HasPrefix(p, repo+"/issues/") && issue() != nil:
		is := issue()
		switch {
		case strings.HasSuffix(p, "/assignees") && r.Method == "POST":
			is.Assignees = append(is.Assignees, anyStrings(in["assignees"])...)
		case strings.HasSuffix(p, "/assignees") && r.Method == "DELETE":
			is.Assignees = without(is.Assignees, anyStrings(in["assignees"])...)
		case strings.HasSuffix(p, "/labels") && r.Method == "POST":
			is.Labels = append(is.Labels, anyStrings(in["labels"])...)
		case strings.Contains(p, "/labels/") && r.Method == "DELETE":
			is.Labels = without(is.Labels, p[strings.LastIndex(p, "/")+1:])
		case r.Method == "PATCH":
			if _, ok := in["body"]; ok {
				is.Body = str("body")
			}
			is.Closed = str("state") == "closed"
		}
		reply(is.json())
	case r.Method == "GET" && p == repo:
		reply(map[string]any{"full_name": "user-1/AI-sandboxing", "default_branch": "main", "html_url": "https://github.test/repo"})
	case r.Method == "GET" && p == repo+"/pulls":
		head, base := r.URL.Query().Get("head"), r.URL.Query().Get("base")
		out := []any{}
		for _, pr := range f.pulls {
			if !pr.Closed && (head == "" || "user-1:"+pr.Head == head) && (base == "" || pr.Base == base) {
				out = append(out, pr.json())
			}
		}
		if r.URL.Query().Get("page") > "1" {
			out = []any{}
		}
		reply(out)
	case r.Method == "POST" && p == repo+"/pulls":
		pr := &fakePull{Number: len(f.pulls) + 1, Title: str("title"), Body: str("body"), Head: strings.TrimPrefix(str("head"), "user-1:"), Base: str("base")}
		f.pulls = append(f.pulls, pr)
		w.WriteHeader(http.StatusCreated)
		reply(pr.json())
	case strings.HasPrefix(p, repo+"/pulls/") && strings.HasSuffix(p, "/requested_reviewers"):
		pr := pull("/pulls/")
		if r.Method == "POST" {
			pr.Reviewers = append(pr.Reviewers, anyStrings(in["reviewers"])...)
		} else {
			pr.Reviewers = without(pr.Reviewers, anyStrings(in["reviewers"])...)
		}
		reply(pr.json())
	case r.Method == "GET" && strings.HasPrefix(p, repo+"/pulls/"):
		reply(pull("/pulls/").json())
	case r.Method == "PATCH" && strings.HasPrefix(p, repo+"/pulls/"):
		pr := pull("/pulls/")
		if _, ok := in["title"]; ok {
			pr.Title = str("title")
		}
		if _, ok := in["body"]; ok {
			pr.Body = str("body")
		}
		pr.Closed = str("state") == "closed"
		reply(pr.json())
	case r.Method == "POST" && strings.HasPrefix(p, repo+"/issues/") && strings.HasSuffix(p, "/labels"):
		pr := pull("/issues/")
		pr.Labels = append(pr.Labels, anyStrings(in["labels"])...)
		reply([]any{})
	case r.Method == "DELETE" && strings.HasPrefix(p, repo+"/issues/") && strings.Contains(p, "/labels/"):
		pr := pull("/issues/")
		pr.Labels = without(pr.Labels, p[strings.LastIndex(p, "/")+1:])
		reply([]any{})
	case f.repos != nil && r.Method == "GET" && p == "/users/user-1":
		typ := "User"
		if f.org {
			typ = "Organization"
		}
		reply(map[string]any{"login": "user-1", "type": typ, "html_url": "https://github.test/user-1"})
	case f.repos != nil && r.Method == "GET" && p == "/user":
		login := "user-1"
		if f.org {
			login = "someone"
		}
		reply(map[string]any{"login": login})
	case f.repos != nil && r.Method == "POST" && p == map[bool]string{false: "/user/repos", true: "/orgs/user-1/repos"}[f.org]:
		repo := &fakeRepo{Name: str("name"), Description: str("description"), DefaultBranch: "main"}
		repo.Private, _ = in["private"].(bool)
		repo.AutoInit, _ = in["auto_init"].(bool)
		f.repos[repo.Name] = repo
		w.WriteHeader(http.StatusCreated)
		reply(repo.json())
	case f.repos != nil && f.repos[strings.TrimPrefix(p, "/repos/user-1/")] != nil:
		name := strings.TrimPrefix(p, "/repos/user-1/")
		repo := f.repos[name]
		switch r.Method {
		case "PATCH":
			if v, ok := in["private"].(bool); ok {
				repo.Private = v
			}
			if _, ok := in["description"]; ok {
				repo.Description = str("description")
			}
			if _, ok := in["default_branch"]; ok {
				repo.DefaultBranch = str("default_branch")
			}
		case "DELETE":
			delete(f.repos, name)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		reply(repo.json())
	default:
		w.WriteHeader(http.StatusNotFound)
		reply(map[string]string{"message": "Not Found"})
	}
}

func TestGithubPatternAssert(t *testing.T) {
	fake := &fakeGitHub{pulls: []*fakePull{
		{Number: 1, Head: "condoc/a/main", Base: "main", Labels: []string{"wip"}},
		{Number: 2, Head: "condoc/b/main", Base: "main", Labels: []string{"condoc"}, Reviewers: []string{"user-1"}},
		{Number: 3, Head: "feature/x", Base: "main"},
		{Number: 4, Head: "condoc/c/main", Base: "release"},
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "github-condoc-labels.dtt.yaml")
	ctx := context.Background()
	engine := func(mode string) *engine {
		e := testEngine(mode, false)
		e.gh = &githubClient{api: srv.URL, http: srv.Client()}
		return e
	}

	img, err := engine("read").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "labels[condoc]=would-change labels[needs-review]=would-change reviewers[user-1]=would-change "+
		"labels[condoc]=unchanged labels[needs-review]=would-change reviewers[user-1]=unchanged" {
		t.Errorf("read: %s", got)
	}
	// The repository is measured whole, its open PRs (with their labels)
	// included, without a read: *.
	if prs, _ := img.Objects[0].State["prs"].([]any); len(prs) != 4 {
		t.Errorf("repo prs %v", img.Objects[0].State["prs"])
	}

	if img, err = engine("assert").runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if img.Summary["changed"] != 4 || img.Summary["held"] != 0 {
		t.Errorf("assert: %s", outcomes(img))
	}
	if got := strings.Join(fake.pulls[0].Labels, ","); got != "wip,condoc,needs-review" {
		t.Errorf("#1's labels %s: others should be left alone", got)
	}
	if got := strings.Join(fake.pulls[1].Labels, ","); got != "condoc,needs-review" {
		t.Errorf("#2's labels %s", got)
	}
	if len(fake.pulls[2].Labels)+len(fake.pulls[3].Labels) != 0 {
		t.Error("an unmatched PR was labelled")
	}
	// The image holds the PRs as the assert left them.
	for _, o := range img.Objects {
		if o.ID == "github.users.user-1.repos.AI-sandboxing.prs[condoc/a/main->main]" {
			if got := stringList(o.State["labels"]); strings.Join(got, ",") != "condoc,needs-review,wip" {
				t.Errorf("#1 in the image: %v", got)
			}
			if o.Measured == nil {
				t.Error("#1 has no measured state")
			}
		}
	}

	// Removing a member follows the from rule: held, unless a from value
	// names it or is the whole list as it is.
	lacks := strings.Replace(src, "has: [condoc, needs-review]   # add these, leave others alone", "lacks: [wip]", 1)
	if img, _ = engine("assert").runSource(ctx, lacks, nil); !strings.Contains(outcomes(img), "labels[wip]=held") {
		t.Errorf("removing wip: %s", outcomes(img))
	}
	member := strings.Replace(lacks, "lacks: [wip]", "lacks: [wip]\n        from.labels: wip", 1)
	if img, _ = engine("assert").runSource(ctx, member, nil); !strings.Contains(outcomes(img), "labels[wip]=changed") {
		t.Errorf("removing wip from wip: %s", outcomes(img))
	}
	if got := strings.Join(fake.pulls[0].Labels, ","); got != "condoc,needs-review" {
		t.Errorf("#1's labels %s", got)
	}
	whole := strings.Replace(src, "has: [condoc, needs-review]   # add these, leave others alone",
		"[condoc]\n        from.labels:\n          was: [condoc, needs-review]", 1)
	if img, _ = engine("assert").runSource(ctx, whole, nil); !strings.Contains(outcomes(img), "labels[needs-review]=changed") {
		t.Errorf("the whole list: %s", outcomes(img))
	}
	if got := strings.Join(fake.pulls[0].Labels, ","); got != "condoc" {
		t.Errorf("#1's labels %s", got)
	}

	// A pattern that matches nothing is fine.
	none := strings.Replace(src, "condoc/*/main", "nothing/*", 1)
	if img, err = engine("assert").runSource(ctx, none, nil); err != nil || len(img.Fields) != 0 {
		t.Errorf("no match: %v %s", err, outcomes(img))
	}
}

func TestFilesystemTemplateExample(t *testing.T) {
	dir := t.TempDir()
	src := strings.Replace(readExample(t, "filesystem-text-template.dtt.yaml"), "/etc/ufa/", dir, 1)
	motd, old := filepath.Join(dir, "motd.txt"), filepath.Join(dir, "old-settings.ini")
	os.WriteFile(motd, []byte("someone else's\n"), 0o600)
	os.WriteFile(old, []byte("x=1\n"), 0o644)
	ctx := context.Background()

	img, err := testEngine("assert", false).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	// motd.txt's content is from *, but its mode isn't empty: held. The old
	// file isn't removed without an image.
	if got := outcomes(img); got != "content=changed mode=held exists=held" {
		t.Errorf("first assert: %s", got)
	}
	if b, _ := os.ReadFile(motd); string(b) != "Managed by dtt. Last asserted "+testNow.Format(time.RFC3339)+".\n" {
		t.Errorf("motd.txt %q", b)
	}
	// With the image, the mode and the removal go through.
	img, err = testEngine("assert", false).runSource(ctx, src, roundTrip(t, img))
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "content=unchanged mode=changed exists=changed" {
		t.Errorf("assert with the image: %s", got)
	}
	if info, err := os.Stat(motd); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("motd.txt's mode: %v %v", info.Mode(), err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("old-settings.ini is still there")
	}

	// A file the run creates had no mode, so its mode always applies.
	os.Remove(motd)
	if img, _ = testEngine("assert", false).runSource(ctx, src, nil); outcomes(img) != "exists=changed content=changed mode=changed exists=unchanged" {
		t.Errorf("creating motd.txt: %s", outcomes(img))
	}
	if _, err := parseDocument(strings.Replace(src, `mode: "0644"`, `mode: "0999"`, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := testEngine("read", false).runSource(ctx, strings.Replace(src, `mode: "0644"`, `mode: "0999"`, 1), nil); err == nil || !strings.Contains(err.Error(), "octal") {
		t.Errorf("a bad mode: %v", err)
	}
}

func TestGithubAssert(t *testing.T) {
	fake := &fakeGitHub{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "github-prs.dtt.yaml")
	ctx := context.Background()
	engineAt := func(mode string, force bool, at time.Time) *engine {
		e := testEngine(mode, force)
		e.now = func() time.Time { return at }
		e.gh = &githubClient{api: srv.URL, http: srv.Client()}
		return e
	}

	// Neither PR exists: both are created, the time in their bodies.
	img, err := engineAt("assert", false, testNow).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=changed body=changed exists=changed body=changed" {
		t.Errorf("first assert: %s", got)
	}
	if len(fake.pulls) != 2 || fake.pulls[0].Title != "Merge one branch" || fake.pulls[1].Head != "condoc/blah2/main" {
		t.Fatalf("pulls %+v", fake.pulls)
	}
	stamp := testNow.Format(time.RFC1123)
	if want := "This PR will reintegrate a branch.\nThe more you know.\n" + stamp + "\n"; fake.pulls[0].Body != want {
		t.Errorf("body %q, want %q", fake.pulls[0].Body, want)
	}
	repo := img.Objects[0]
	if repo.Alias != "the-repo" || repo.State["default_branch"] != "main" || repo.State["prs"] == nil {
		t.Errorf("repo %+v", repo)
	}
	first := roundTrip(t, img)

	// An hour on, both bodies hold the earlier time, which is neither empty
	// nor a from value: held.
	later := testNow.Add(time.Hour)
	if img, _ = engineAt("assert", false, later).runSource(ctx, src, nil); outcomes(img) != "exists=unchanged body=held exists=unchanged body=held" {
		t.Errorf("a later assert: %s", outcomes(img))
	}
	// With the first run's image they're as it saw them, so they change.
	if img, _ = engineAt("assert", false, later).runSource(ctx, src, first); outcomes(img) != "exists=unchanged body=changed exists=unchanged body=changed" {
		t.Errorf("assert with the image: %s", outcomes(img))
	}
	if !strings.Contains(fake.pulls[0].Body, later.Format(time.RFC1123)) {
		t.Errorf("body %q", fake.pulls[0].Body)
	}

	// blah2's body may also change from its notimeyet value; blah1's may not.
	notYet := "This PR will reintegrate a branch.\nThe more you know.\n"
	fake.pulls[0].Body, fake.pulls[1].Body = notYet, notYet
	if img, _ = engineAt("assert", false, later).runSource(ctx, src, nil); outcomes(img) != "exists=unchanged body=held exists=unchanged body=changed" {
		t.Errorf("assert over notimeyet bodies: %s", outcomes(img))
	}
	if fake.pulls[0].Body != notYet {
		t.Errorf("blah1's held body changed: %q", fake.pulls[0].Body)
	}

	// Reading makes no calls that change anything.
	fake.calls = nil
	if img, _ = engineAt("read", true, later).runSource(ctx, src, nil); outcomes(img) != "exists=unchanged body=would-change exists=unchanged body=unchanged" {
		t.Errorf("read --force: %s", outcomes(img))
	}
	for _, c := range fake.calls {
		if !strings.HasPrefix(c, "GET ") {
			t.Errorf("read made %s", c)
		}
	}
}

func TestGithubIssueExample(t *testing.T) {
	fake := &fakeGitHub{pulls: []*fakePull{{Number: 1, Title: "a PR", Head: "x", Base: "main"}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "github-issue.dtt.yaml")
	ctx := context.Background()
	engine := func(mode string, force bool) *engine {
		e := testEngine(mode, force)
		e.gh = &githubClient{api: srv.URL, http: srv.Client()}
		return e
	}
	const title = "DTT check-in 2026-10-24"
	const id = "github.users.user-1.repos.AI-sandboxing.issues[" + title + "]"

	// Reading changes nothing; the repository's issues leave its PRs out.
	img, err := engine("read", false).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=would-change body=would-change labels[dtt]=would-change assignees[user-1]=would-change" {
		t.Errorf("read: %s", got)
	}
	if len(fake.issues) != 0 {
		t.Fatal("read opened an issue")
	}
	if issues, _ := img.Objects[1].State["issues"].([]any); issues == nil || len(issues) != 0 {
		t.Errorf("repo issues %v", img.Objects[1].State["issues"])
	}

	// Asserting opens it, labelled and assigned; the image holds it.
	if img, err = engine("assert", false).runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if img.Summary["changed"] != 4 {
		t.Errorf("assert: %s", outcomes(img))
	}
	if len(fake.issues) != 1 {
		t.Fatalf("issues %+v", fake.issues)
	}
	is := fake.issues[0]
	if is.Title != title || !strings.HasPrefix(is.Body, "Opened by dtt on "+testNow.Format(time.RFC1123)) ||
		strings.Join(is.Labels, ",") != "dtt" || strings.Join(is.Assignees, ",") != "user-1" {
		t.Errorf("issue %+v", is)
	}
	var found bool
	for _, o := range img.Objects {
		if o.ID == id {
			found = o.State["exists"] == true && o.State["number"] == 2
		}
	}
	if !found {
		t.Errorf("the issue isn't in the image as opened: %+v", img.Objects)
	}
	assertImg := roundTrip(t, img)

	// A second assert finds it by title and changes nothing.
	if img, _ = engine("assert", false).runSource(ctx, src, nil); img.Summary["changed"] != 0 || len(fake.issues) != 1 {
		t.Errorf("a second assert: %s", outcomes(img))
	}

	// Delete closes it: held without an image, done with the assert's.
	if img, _ = engine("delete", false).runSource(ctx, src, nil); outcomes(img) != "exists=held" || is.Closed {
		t.Errorf("delete without an image: %s", outcomes(img))
	}
	if img, err = engine("delete", false).runSource(ctx, src, assertImg); err != nil || outcomes(img) != "exists=changed" || !is.Closed {
		t.Errorf("delete with the image: %v %s", err, outcomes(img))
	}

	// A closed issue isn't matched, so asserting again opens a new one.
	if img, _ = engine("assert", false).runSource(ctx, src, nil); len(fake.issues) != 2 {
		t.Errorf("assert after closing: %s", outcomes(img))
	}

	if err := testEngine("read", false).check(mustParse(t, strings.Replace(src, "title: DTT", "name: DTT", 1))); err == nil || !strings.Contains(err.Error(), "an issue's select") {
		t.Errorf("a select without title: %v", err)
	}
}

func mustParse(t *testing.T, src string) []*statement {
	t.Helper()
	doc, err := parseDocument(src)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// fakeAWS answers the boto3 calls the aws provider makes for ECS.
type fakeAWS struct {
	tags  map[string]map[string]string // task ARN -> tags
	calls []string
}

// awsParams reads a boto3 call's params into v, as boto3 would see them.
func awsParams(params map[string]any, v any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (f *fakeAWS) run(ctx context.Context, region, service, op string, params map[string]any) ([]byte, error) {
	f.calls = append(f.calls, service+" "+op)
	switch op {
	case "list_clusters":
		return []byte(`{"clusterArns": ["arn:aws:ecs:x:1:cluster/c1"]}`), nil
	case "list_tasks":
		return []byte(`{"taskArns": ["arn:task/a", "arn:task/b"]}`), nil
	case "describe_tasks":
		var tasks []any
		for _, arn := range []string{"arn:task/a", "arn:task/b"} {
			var tags []any
			for _, k := range sortedKeys(f.tags[arn]) {
				tags = append(tags, map[string]string{"key": k, "value": f.tags[arn][k]})
			}
			tasks = append(tasks, map[string]any{"taskArn": arn, "clusterArn": "arn:aws:ecs:x:1:cluster/c1", "lastStatus": "RUNNING", "tags": tags})
		}
		return json.Marshal(map[string]any{"tasks": tasks})
	case "tag_resource":
		var p struct {
			ResourceArn string `json:"resourceArn"`
			Tags        []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"tags"`
		}
		if err := awsParams(params, &p); err != nil {
			return nil, err
		}
		for _, s := range p.Tags {
			f.tags[p.ResourceArn][s.Key] = s.Value
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected aws %s %s %v", service, op, params)
}

func TestAWSAssert(t *testing.T) {
	fake := &fakeAWS{tags: map[string]map[string]string{
		"arn:task/a": {"watched-by": "dtt"},
		"arn:task/b": {"watched-by": "someone-else"},
	}}
	src := readExample(t, "aws-ecs-tags.dtt.yaml")
	engineAt := func(force bool, at time.Time) *engine {
		e := testEngine("assert", force)
		e.now = func() time.Time { return at }
		e.awsRun = fake.run
		return e
	}
	img, err := engineAt(false, testNow).runSource(context.Background(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "tags.last-watched=changed" {
		t.Errorf("first assert: %s", got)
	}
	if got := fake.tags["arn:task/a"]["last-watched"]; got != strconv.FormatInt(testNow.Unix(), 10) {
		t.Errorf("last-watched %q", got)
	}
	if _, ok := fake.tags["arn:task/b"]["last-watched"]; ok {
		t.Error("an unselected task was tagged")
	}
	later := testNow.Add(time.Minute)
	if img, _ = engineAt(false, later).runSource(context.Background(), src, nil); outcomes(img) != "tags.last-watched=held" {
		t.Errorf("a later assert: %s", outcomes(img))
	}
	if img, _ = engineAt(true, later).runSource(context.Background(), src, nil); outcomes(img) != "tags.last-watched=changed" {
		t.Errorf("a later assert --force: %s", outcomes(img))
	}
	if got := fake.tags["arn:task/a"]["last-watched"]; got != strconv.FormatInt(later.Unix(), 10) {
		t.Errorf("last-watched %q after --force", got)
	}
}

// TestDeclarativeToolsInterface runs the files example through the tools
// interface: dtt run filesystem, as local-representative's tool.filesystem
// op does.
func TestDeclarativeToolsInterface(t *testing.T) {
	t.Setenv("DTT_TEST_BE_DTT", "1")
	e := newEnv(t)
	code, out := e.dtt("", "list", "--json")
	for _, name := range []string{`"tool.github"`, `"tool.git"`, `"tool.filesystem"`, `"tool.aws"`, `"tool.localrep"`} {
		if code != exitOK || !strings.Contains(out, name) {
			t.Fatalf("list exited %d without %s: %s", code, name, out)
		}
	}

	dir := t.TempDir()
	doc := filepath.Join(t.TempDir(), "doc.dtt.yaml")
	os.WriteFile(doc, []byte(strings.Replace(readExample(t, "filesystem-json.dtt.yaml"), `"/tmp/"`, dir, 1)), 0o644)
	wd := filepath.Join(t.TempDir(), "wd")
	code, out = e.dtt("", "run", "filesystem", "--arg", "document_file="+doc, "--arg", "mode=assert", "--workdir", wd, "--json")
	if code != exitOK {
		t.Fatalf("run exited %d: %s", code, out)
	}
	var res RunResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Outputs["changed"] != "2" || res.Outputs["held"] != "0" || res.Outputs["objects"] != "2" {
		t.Errorf("outputs %+v", res.Outputs)
	}
	// The image comes back in the result itself (the runner shows it).
	imgOut, err := parseImage(res.Outputs["image_json"])
	if err != nil || imgOut.Mode != "assert" || len(imgOut.Objects) != 2 || imgOut.Objects[1].State["content"] == nil {
		t.Fatalf("image_json %v: %s", err, res.Outputs["image_json"])
	}
	if len(res.Files) != 1 || res.Files[0].Name != "image" {
		t.Fatalf("files %+v", res.Files)
	}
	if _, err := os.Stat(filepath.Join(dir, "my-json.json")); err != nil {
		t.Fatal(err)
	}

	// The image from that run, passed back, lets a later assert change what
	// it saw.
	os.WriteFile(filepath.Join(dir, "my-json.json"), []byte(`{}`+"\n"), 0o644)
	code, out = e.dtt("", "run", "filesystem", "--arg", "document_file="+doc, "--arg", "mode=assert", "--json")
	if code != exitOK || !strings.Contains(out, `"changed": "1"`) {
		t.Fatalf("an assert over {} (empty) exited %d: %s", code, out)
	}
	os.WriteFile(filepath.Join(dir, "my-json.json"), []byte(`{"x": "y"}`), 0o644)
	code, out = e.dtt("", "run", "filesystem", "--arg", "document_file="+doc, "--arg", "mode=assert", "--json")
	if code != exitOK || !strings.Contains(out, `"held": "1"`) {
		t.Fatalf("an assert over other content exited %d: %s", code, out)
	}
	code, out = e.dtt("", "run", "filesystem", "--arg", "document_file="+doc, "--arg", "mode=read", "--workdir", wd, "--json")
	if code != exitOK {
		t.Fatalf("read exited %d: %s", code, out)
	}
	code, out = e.dtt("", "run", "filesystem", "--arg", "document_file="+doc, "--arg", "mode=assert", "--arg", "from_image="+filepath.Join(wd, imageFileName), "--json")
	if code != exitOK || !strings.Contains(out, `"changed": "1"`) {
		t.Fatalf("an assert with the read's image exited %d: %s", code, out)
	}

	// A document for another provider is refused before anything runs.
	code, out = e.dtt("", "run", "github", "--arg", "document_file="+doc, "--json")
	if code != exitToolFailed || !strings.Contains(out, "the github tool") {
		t.Fatalf("github over a files document exited %d: %s", code, out)
	}

	// dtt read / assert print the image themselves.
	code, out = e.dtt("", "read", doc)
	var img Image
	if err := json.Unmarshal([]byte(out), &img); code != exitOK || err != nil || img.Format != imageFormat || img.Mode != "read" {
		t.Fatalf("dtt read exited %d (%v): %s", code, err, out)
	}
	if code, out = e.dtt("nope:\n  read: *\n", "assert", "-", "--json"); code != exitBadInput {
		t.Fatalf("dtt assert of a bad document exited %d: %s", code, out)
	}

	// Variables: the variables input, and dtt assert's --var.
	vdoc := filepath.Join(t.TempDir(), "vars.dtt.yaml")
	os.WriteFile(vdoc, []byte("variables:\n  name: nobody\nfilesystem.items:\n  select:\n    abspath: "+dir+"\n  assert:\n    items:\n      who.txt:\n        content: ${{var.name}}\n"), 0o644)
	if code, out = e.dtt("", "run", "filesystem", "--arg", "document_file="+vdoc, "--arg", "mode=assert", "--arg", "variables=name=ada", "--json"); code != exitOK {
		t.Fatalf("run with variables exited %d: %s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "who.txt")); string(b) != "ada" {
		t.Errorf("who.txt %q", b)
	}
	if code, out = e.dtt("", "assert", vdoc, "--var", "nme=bob", "--json"); code != exitBadInput || !strings.Contains(out, "only declares name") {
		t.Errorf("an undeclared --var exited %d: %s", code, out)
	}
}

func TestGithubRepoExample(t *testing.T) {
	fake := &fakeGitHub{repos: map[string]*fakeRepo{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "github-repo-dated.dtt.yaml")
	ctx := context.Background()
	engineAt := func(mode string, force bool, at time.Time) *engine {
		e := testEngine(mode, force)
		e.now = func() time.Time { return at }
		e.gh = &githubClient{api: srv.URL, http: srv.Client()}
		return e
	}
	const id = "github.users.user-1.repos[scratch-2026-10-24]"

	// Reading changes nothing, and says the repository would be made.
	img, err := engineAt("read", false, testNow).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=would-change private=would-change description=would-change default_branch=would-change auto_init=would-change" {
		t.Errorf("read: %s", got)
	}
	if len(fake.repos) != 0 {
		t.Fatal("read made a repository")
	}

	if img, err = engineAt("assert", false, testNow).runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=changed private=changed description=changed default_branch=changed auto_init=changed" {
		t.Errorf("assert: %s", got)
	}
	made := roundTrip(t, img)
	r := fake.repos["scratch-2026-10-24"]
	if r == nil || !r.Private || !r.AutoInit || r.Description != "Scratch space for 2026-10-24." {
		t.Fatalf("repos %+v", fake.repos)
	}
	// The image holds the repository as made; auto_init reads back false.
	var obj *ImageObject
	for _, o := range img.Objects {
		if o.ID == id {
			obj = o
		}
	}
	if obj == nil || obj.State["exists"] != true || obj.State["auto_init"] != false || obj.Measured["exists"] != false {
		t.Fatalf("objects %+v", img.Objects)
	}

	// Asserting again changes nothing, and auto_init isn't asserted on a
	// repository that exists.
	if img, _ = engineAt("assert", false, testNow).runSource(ctx, src, nil); outcomes(img) != "exists=unchanged private=unchanged description=unchanged default_branch=unchanged" {
		t.Errorf("a second assert: %s", outcomes(img))
	}
	// The next day's run makes the next day's repository.
	if _, err = engineAt("assert", false, testNow.Add(24*time.Hour)).runSource(ctx, src, nil); err != nil || fake.repos["scratch-2026-10-25"] == nil || len(fake.repos) != 2 {
		t.Fatalf("the next day: %v %+v", err, fake.repos)
	}

	// The example without its approve_delete line.
	var bare strings.Builder
	for _, l := range strings.SplitAfter(src, "\n") {
		if !strings.Contains(l, "approve_delete:") {
			bare.WriteString(l)
		}
	}
	unapproved := bare.String()
	if unapproved == src {
		t.Fatal("the example has no approve_delete")
	}

	// Delete mode: refused without approve_delete...
	if _, err = engineAt("delete", true, testNow).runSource(ctx, unapproved, nil); err == nil || !strings.Contains(err.Error(), "approve_delete") || fake.repos["scratch-2026-10-24"] == nil {
		t.Fatalf("delete mode without approval: %v", err)
	}
	// ...and with it, the from rule decides: held with no image or force...
	if img, err = engineAt("delete", false, testNow).runSource(ctx, src, nil); err != nil || outcomes(img) != "exists=held" || fake.repos["scratch-2026-10-24"] == nil {
		t.Fatalf("delete mode without an image: %v %s", err, outcomes(img))
	}
	// ...and deleted with the image the assert returned -- on a later day
	// too, since timestamp is then the moment that image saw.
	if img, err = engineAt("delete", false, testNow.Add(24*time.Hour)).runSource(ctx, src, made); err != nil || outcomes(img) != "exists=changed" {
		t.Fatalf("delete mode with the image: %v %s", err, outcomes(img))
	}
	if fake.repos["scratch-2026-10-24"] != nil || fake.repos["scratch-2026-10-25"] == nil {
		t.Fatalf("delete mode deleted the wrong repositories: %+v", fake.repos)
	}
	for _, o := range img.Objects {
		if o.ID == id && (o.State["exists"] != false || o.Measured["exists"] != true) {
			t.Errorf("the deleted repository's image: %+v", o)
		}
	}
	if img.Mode != "delete" {
		t.Errorf("image mode %q", img.Mode)
	}
	// Deleting again changes nothing.
	if img, err = engineAt("delete", false, testNow).runSource(ctx, src, made); err != nil || outcomes(img) != "exists=unchanged" {
		t.Errorf("a second delete: %v %s", err, outcomes(img))
	}
	// The next day's repository was never in the image, so it is untouched.
	if img, _ = engineAt("read", false, testNow.Add(24*time.Hour)).runSource(ctx, src, nil); strings.Contains(outcomes(img), "would-change") {
		t.Errorf("read after delete: %s", outcomes(img))
	}

	// exists: false in assert mode is refused without approve_delete...
	gone := strings.Replace(unapproved, "        exists: true\n", "        exists: false\n", 1)
	if _, err = engineAt("assert", true, testNow.Add(24*time.Hour)).runSource(ctx, gone, nil); err == nil || !strings.Contains(err.Error(), "approve_delete") {
		t.Fatalf("deleting without approval: %v", err)
	}
	if _, err = engineAt("assert", true, testNow.Add(24*time.Hour)).runSource(ctx, gone+"        approve_delete: user-1/someone-else\n", nil); err == nil || !strings.Contains(err.Error(), "approve_delete") {
		t.Fatalf("deleting with the wrong approval: %v", err)
	}
	// ...and with it, follows the from rule.
	approved := gone + "        approve_delete: user-1/scratch-${{timestamp.date}}\n"
	if img, _ = engineAt("assert", false, testNow.Add(24*time.Hour)).runSource(ctx, approved, nil); outcomes(img) != "exists=held" || fake.repos["scratch-2026-10-25"] == nil {
		t.Errorf("deleting without force: %s", outcomes(img))
	}
	if img, err = engineAt("assert", true, testNow.Add(24*time.Hour)).runSource(ctx, approved, nil); err != nil || outcomes(img) != "exists=changed" || fake.repos["scratch-2026-10-25"] != nil {
		t.Errorf("deleting with force: %v %s", err, outcomes(img))
	}
}

func TestGithubRepoHyenaPluginExample(t *testing.T) {
	fake := &fakeGitHub{repos: map[string]*fakeRepo{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "github-repo-hyena-plugin.dtt.yaml")
	ctx := context.Background()
	engineWith := func(mode string, force bool, vars map[string]string) *engine {
		e := testEngine(mode, force)
		e.vars = vars
		e.gh = &githubClient{api: srv.URL, http: srv.Client()}
		return e
	}

	// The seed defaults to 01: a public HyenaExternalUfaPlugin01.
	img, err := engineWith("assert", false, nil).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := fake.repos["HyenaExternalUfaPlugin01"]
	if r == nil || r.Private || !r.AutoInit || len(fake.repos) != 1 {
		t.Fatalf("the default seed: %+v", fake.repos)
	}
	made := roundTrip(t, img)

	// A blank seed makes the repository named exactly HyenaExternalUfaPlugin.
	blank := map[string]string{"seed": ""}
	if _, err = engineWith("assert", false, blank).runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if r = fake.repos["HyenaExternalUfaPlugin"]; r == nil || r.Private || len(fake.repos) != 2 {
		t.Fatalf("a blank seed: %+v", fake.repos)
	}

	// Delete mode with the default seed's image deletes only that one.
	if img, err = engineWith("delete", false, nil).runSource(ctx, src, made); err != nil || outcomes(img) != "exists=changed" {
		t.Fatalf("delete mode: %v %s", err, outcomes(img))
	}
	if fake.repos["HyenaExternalUfaPlugin01"] != nil || fake.repos["HyenaExternalUfaPlugin"] == nil {
		t.Fatalf("delete mode deleted the wrong repositories: %+v", fake.repos)
	}
}

func TestGithubRepoOwnerKind(t *testing.T) {
	ctx := context.Background()
	src := readExample(t, "github-repo-dated.dtt.yaml")
	asOrg := strings.Replace(src, "github.users.user-1.repos:", "github.orgs.user-1.repos:", 1)
	run := func(fake *fakeGitHub, doc string) error {
		srv := httptest.NewServer(fake)
		defer srv.Close()
		e := testEngine("assert", false)
		e.gh = &githubClient{api: srv.URL, http: srv.Client()}
		_, err := e.runSource(ctx, doc, nil)
		return err
	}

	// A user named as an org (and the other way round) says which path it needs.
	user := &fakeGitHub{repos: map[string]*fakeRepo{}}
	if err := run(user, asOrg); err == nil || !strings.Contains(err.Error(), "github.users.user-1.repos") || len(user.repos) != 0 {
		t.Fatalf("a user as an org: %v %+v", err, user.repos)
	}
	org := &fakeGitHub{repos: map[string]*fakeRepo{}, org: true}
	if err := run(org, src); err == nil || !strings.Contains(err.Error(), "github.orgs.user-1.repos") || len(org.repos) != 0 {
		t.Fatalf("an org as a user: %v %+v", err, org.repos)
	}

	// An org's path makes the repository through /orgs/<owner>/repos.
	if err := run(org, asOrg); err != nil || org.repos["scratch-2026-10-24"] == nil {
		t.Fatalf("an org: %v %+v", err, org.repos)
	}
}

// fakeEC2 answers the boto3 ec2 calls for VPCs and subnets.
type fakeEC2 struct {
	vpcs    []*fakeVPC
	subnets []*fakeSubnet
	calls   []string
	n       int
}

type fakeVPC struct {
	ID, CIDR           string
	Hostnames, Support bool
	Tags               map[string]string
}

type fakeSubnet struct {
	ID, VPC, CIDR, AZ string
	Public            bool
	Tags              map[string]string
}

func fakeTags(tags map[string]string) []any {
	out := []any{}
	for _, k := range sortedKeys(tags) {
		out = append(out, map[string]string{"Key": k, "Value": tags[k]})
	}
	return out
}

// ec2Params is every param the provider passes an ec2 call.
type ec2Params struct {
	VpcIds, Resources                                         []string
	VpcId, SubnetId, CidrBlock, AvailabilityZone, Attribute   string
	Tags                                                      []ec2Tag
	TagSpecifications                                         []ec2TagSpec
	Filters                                                   []ec2Filter
	MapPublicIpOnLaunch, EnableDnsHostnames, EnableDnsSupport *ec2Bool
}

type ec2TagSpec struct {
	ResourceType string
	Tags         []ec2Tag
}

type ec2Filter struct {
	Name   string
	Values []string
}

type ec2Bool struct{ Value bool }

// addTags adds tags (create_tags's, or the first of TagSpecifications') to t.
func addTags(t map[string]string, tags []ec2Tag) map[string]string {
	if t == nil {
		t = map[string]string{}
	}
	for _, kv := range tags {
		t[kv.Key] = kv.Value
	}
	return t
}

func (p ec2Params) specTags() []ec2Tag {
	if len(p.TagSpecifications) == 0 {
		return nil
	}
	return p.TagSpecifications[0].Tags
}

func (f *fakeEC2) run(ctx context.Context, region, service, op string, params map[string]any) ([]byte, error) {
	if region != "us-east-1" || service != "ec2" {
		return nil, fmt.Errorf("unexpected aws %s %s in %q", service, op, region)
	}
	var p ec2Params
	if err := awsParams(params, &p); err != nil {
		return nil, err
	}
	f.calls = append(f.calls, op)
	vpc := func(id string) *fakeVPC {
		for _, v := range f.vpcs {
			if v.ID == id {
				return v
			}
		}
		return nil
	}
	subnetJSON := func(s *fakeSubnet) map[string]any {
		return map[string]any{"SubnetId": s.ID, "VpcId": s.VPC, "CidrBlock": s.CIDR, "AvailabilityZone": s.AZ,
			"MapPublicIpOnLaunch": s.Public, "State": "available", "Tags": fakeTags(s.Tags)}
	}
	vpcJSON := func(v *fakeVPC) map[string]any {
		return map[string]any{"VpcId": v.ID, "CidrBlock": v.CIDR, "State": "available", "IsDefault": false, "Tags": fakeTags(v.Tags)}
	}
	switch op {
	case "describe_vpcs":
		out := []any{}
		for _, v := range f.vpcs {
			if len(p.VpcIds) == 0 || contains(p.VpcIds, v.ID) {
				out = append(out, vpcJSON(v))
			}
		}
		return json.Marshal(map[string]any{"Vpcs": out})
	case "describe_vpc_attribute":
		v := vpc(p.VpcId)
		val, key := v.Support, "EnableDnsSupport"
		if p.Attribute == "enableDnsHostnames" {
			val, key = v.Hostnames, "EnableDnsHostnames"
		}
		return json.Marshal(map[string]any{"VpcId": v.ID, key: map[string]bool{"Value": val}})
	case "describe_subnets":
		if len(p.Filters) != 1 || p.Filters[0].Name != "vpc-id" || len(p.Filters[0].Values) != 1 {
			return nil, fmt.Errorf("unexpected describe_subnets filters %+v", p.Filters)
		}
		out := []any{}
		for _, s := range f.subnets {
			if s.VPC == p.Filters[0].Values[0] {
				out = append(out, subnetJSON(s))
			}
		}
		return json.Marshal(map[string]any{"Subnets": out})
	case "create_vpc":
		f.n++
		v := &fakeVPC{ID: fmt.Sprintf("vpc-%d", f.n), CIDR: p.CidrBlock, Support: true, Tags: addTags(nil, p.specTags())}
		f.vpcs = append(f.vpcs, v)
		return json.Marshal(map[string]any{"Vpc": vpcJSON(v)})
	case "modify_vpc_attribute":
		v := vpc(p.VpcId)
		if p.EnableDnsHostnames != nil {
			v.Hostnames = p.EnableDnsHostnames.Value
		}
		if p.EnableDnsSupport != nil {
			v.Support = p.EnableDnsSupport.Value
		}
		return nil, nil
	case "create_tags":
		for _, id := range p.Resources {
			if v := vpc(id); v != nil {
				v.Tags = addTags(v.Tags, p.Tags)
			}
			for _, s := range f.subnets {
				if s.ID == id {
					s.Tags = addTags(s.Tags, p.Tags)
				}
			}
		}
		return nil, nil
	case "create_subnet":
		f.n++
		s := &fakeSubnet{ID: fmt.Sprintf("subnet-%d", f.n), VPC: p.VpcId, CIDR: p.CidrBlock, AZ: p.AvailabilityZone, Tags: addTags(nil, p.specTags())}
		f.subnets = append(f.subnets, s)
		return json.Marshal(map[string]any{"Subnet": subnetJSON(s)})
	case "modify_subnet_attribute":
		for _, s := range f.subnets {
			if s.ID == p.SubnetId && p.MapPublicIpOnLaunch != nil {
				s.Public = p.MapPublicIpOnLaunch.Value
			}
		}
		return nil, nil
	case "delete_subnet":
		var keep []*fakeSubnet
		for _, s := range f.subnets {
			if s.ID != p.SubnetId {
				keep = append(keep, s)
			}
		}
		f.subnets = keep
		return nil, nil
	case "delete_vpc":
		for _, s := range f.subnets {
			if s.VPC == p.VpcId {
				return nil, fmt.Errorf("aws ec2 delete_vpc: DependencyViolation: The vpc '%s' has dependencies and cannot be deleted", p.VpcId)
			}
		}
		var keep []*fakeVPC
		for _, v := range f.vpcs {
			if v.ID != p.VpcId {
				keep = append(keep, v)
			}
		}
		f.vpcs = keep
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected aws ec2 %s %v", op, params)
}

func TestAWSVPCExample(t *testing.T) {
	fake := &fakeEC2{}
	src := readExample(t, "aws-vpc-sandbox.dtt.yaml")
	ctx := context.Background()
	engine := func(mode string, force bool) *engine {
		e := testEngine(mode, force)
		e.awsRun = fake.run
		return e
	}
	const base = "aws.accounts.current.regions.us-east-1.ec2.vpcs"

	// Nothing matches the select, which names one VPC: reading says it (and
	// its subnet) would be made, and changes nothing.
	img, err := engine("read", false).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	made := "exists=%[1]s cidr_block=%[1]s enable_dns_hostnames=%[1]s tags.managed-by=%[1]s exists=%[1]s availability_zone=%[1]s tags.Name=%[1]s tags.managed-by=%[1]s"
	if got := outcomes(img); got != fmt.Sprintf(made, "would-change") {
		t.Errorf("read: %s", got)
	}
	if len(img.Objects) != 2 || img.Objects[0].ID != base+"[Name=ufa-sandbox]" || img.Objects[0].State["exists"] != false {
		t.Errorf("objects %+v", img.Objects[0])
	}
	for _, c := range fake.calls {
		if !strings.HasPrefix(c, "describe_") {
			t.Errorf("read made %s", c)
		}
	}

	if img, err = engine("assert", false).runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != fmt.Sprintf(made, "changed") {
		t.Errorf("assert: %s", got)
	}
	if len(fake.vpcs) != 1 || len(fake.subnets) != 1 {
		t.Fatalf("vpcs %+v subnets %+v", fake.vpcs, fake.subnets)
	}
	v, s := fake.vpcs[0], fake.subnets[0]
	if v.CIDR != "10.42.0.0/16" || !v.Hostnames || v.Tags["Name"] != "ufa-sandbox" || v.Tags["managed-by"] != "dtt" {
		t.Errorf("vpc %+v", v)
	}
	if s.VPC != v.ID || s.CIDR != "10.42.1.0/24" || s.AZ != "us-east-1a" || s.Tags["Name"] != "ufa-sandbox-a" {
		t.Errorf("subnet %+v", s)
	}
	// The image knows them by the VPC's id now, measured as the run left
	// them.
	ids := map[string]*ImageObject{}
	for _, o := range img.Objects {
		ids[o.ID] = o
	}
	vo, so := ids[base+"["+v.ID+"]"], ids[base+"["+v.ID+"].subnets[10.42.1.0/24]"]
	if vo == nil || so == nil || vo.State["enable_dns_hostnames"] != true || len(vo.State["subnets"].([]any)) != 1 || vo.Measured["exists"] != false {
		t.Fatalf("objects %+v", img.Objects)
	}

	// Asserting again finds them by the tag, and changes nothing.
	if img, err = engine("assert", false).runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "cidr_block=unchanged availability_zone=unchanged exists=unchanged enable_dns_hostnames=unchanged tags.managed-by=unchanged tags.Name=unchanged tags.managed-by=unchanged" {
		t.Errorf("a second assert: %s", got)
	}

	// cidr_block can't change: a read reports it held, an assert fails
	// before changing anything, even with force.
	other := strings.Replace(src, "10.42.0.0/16", "10.43.0.0/16", 1)
	if img, err = engine("read", false).runSource(ctx, other, nil); err != nil || !strings.Contains(outcomes(img), "cidr_block=held") {
		t.Errorf("reading another cidr_block: %v %s", err, outcomes(img))
	}
	fake.calls = nil
	if _, err = engine("assert", true).runSource(ctx, other, nil); err == nil || !strings.Contains(err.Error(), "can't change") {
		t.Errorf("asserting another cidr_block: %v", err)
	}
	for _, c := range fake.calls {
		if !strings.HasPrefix(c, "describe_") {
			t.Errorf("a failed assert made %s", c)
		}
	}

	// Deleting: a VPC asserted gone can't keep its subnets...
	gone := strings.Replace(src, "    exists: true\n", "    exists: false\n", 1)
	if _, err = engine("assert", true).runSource(ctx, gone, nil); err == nil || !strings.Contains(err.Error(), "subnets can only be asserted gone") {
		t.Errorf("deleting the VPC alone: %v", err)
	}
	// ...and deleting follows the from rule, subnets first.
	gone = strings.Replace(gone, "availability_zone: us-east-1a", "exists: false", 1)
	if img, _ = engine("assert", false).runSource(ctx, gone, nil); outcomes(img) != "exists=held exists=held" || len(fake.vpcs) != 1 {
		t.Errorf("deleting without force: %s", outcomes(img))
	}
	if img, err = engine("assert", true).runSource(ctx, gone, nil); err != nil || outcomes(img) != "exists=changed exists=changed" {
		t.Errorf("deleting with force: %v %s", err, outcomes(img))
	}
	if len(fake.vpcs)+len(fake.subnets) != 0 {
		t.Errorf("left vpcs %+v subnets %+v", fake.vpcs, fake.subnets)
	}
}
