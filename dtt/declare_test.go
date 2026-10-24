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
	b, err := os.ReadFile(filepath.Join("examples", name))
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
	for _, name := range []string{"github-prs.dtt.yaml", "filesystem-json.dtt.yaml", "aws-ecs-tags.dtt.yaml"} {
		doc, err := parseDocument(readExample(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := testEngine("read", false).check(doc); err != nil {
			t.Fatalf("%s: %v", name, err)
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
	specs, err := parseFSAssert(doc[1].assert)
	if err != nil {
		t.Fatal(err)
	}
	v, err := testEngine("read", false).value(specs[0].fields[0].want)
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

// fakeGitHub is just enough of GitHub's REST API for pull requests.
type fakeGitHub struct {
	mu    sync.Mutex
	pulls []*fakePull
	calls []string
}

type fakePull struct {
	Number      int
	Title, Body string
	Head, Base  string
	Closed      bool
}

func (p *fakePull) json() map[string]any {
	return map[string]any{"number": p.Number, "title": p.Title, "body": p.Body, "html_url": fmt.Sprintf("https://github.test/pull/%d", p.Number),
		"head": map[string]any{"ref": p.Head}, "base": map[string]any{"ref": p.Base}}
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const repo = "/repos/je-sidestuff/AI-sandboxing"
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	var in map[string]string
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&in)
	}
	switch {
	case r.Method == "GET" && r.URL.Path == repo:
		reply(map[string]any{"full_name": "je-sidestuff/AI-sandboxing", "default_branch": "main", "html_url": "https://github.test/repo"})
	case r.Method == "GET" && r.URL.Path == repo+"/pulls":
		head, base := r.URL.Query().Get("head"), r.URL.Query().Get("base")
		out := []any{}
		for _, p := range f.pulls {
			if !p.Closed && (head == "" || "je-sidestuff:"+p.Head == head) && (base == "" || p.Base == base) {
				out = append(out, p.json())
			}
		}
		reply(out)
	case r.Method == "POST" && r.URL.Path == repo+"/pulls":
		p := &fakePull{Number: len(f.pulls) + 1, Title: in["title"], Body: in["body"], Head: strings.TrimPrefix(in["head"], "je-sidestuff:"), Base: in["base"]}
		f.pulls = append(f.pulls, p)
		w.WriteHeader(http.StatusCreated)
		reply(p.json())
	case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, repo+"/pulls/"):
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, repo+"/pulls/"))
		p := f.pulls[n-1]
		if v, ok := in["title"]; ok {
			p.Title = v
		}
		if v, ok := in["body"]; ok {
			p.Body = v
		}
		p.Closed = in["state"] == "closed"
		reply(p.json())
	default:
		w.WriteHeader(http.StatusNotFound)
		reply(map[string]string{"message": "Not Found"})
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

// fakeAWS answers the AWS CLI calls the aws provider makes.
type fakeAWS struct {
	tags  map[string]map[string]string // task ARN -> tags
	calls []string
}

func (f *fakeAWS) run(ctx context.Context, region string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args[:2], " "))
	switch args[1] {
	case "list-clusters":
		return []byte(`{"clusterArns": ["arn:aws:ecs:x:1:cluster/c1"]}`), nil
	case "list-tasks":
		return []byte(`{"taskArns": ["arn:task/a", "arn:task/b"]}`), nil
	case "describe-tasks":
		var tasks []any
		for _, arn := range []string{"arn:task/a", "arn:task/b"} {
			var tags []any
			for _, k := range sortedKeys(f.tags[arn]) {
				tags = append(tags, map[string]string{"key": k, "value": f.tags[arn][k]})
			}
			tasks = append(tasks, map[string]any{"taskArn": arn, "clusterArn": "arn:aws:ecs:x:1:cluster/c1", "lastStatus": "RUNNING", "tags": tags})
		}
		return json.Marshal(map[string]any{"tasks": tasks})
	case "tag-resource":
		var set []struct{ Key, Value string }
		if err := json.Unmarshal([]byte(args[len(args)-1]), &set); err != nil {
			return nil, err
		}
		for _, s := range set {
			f.tags[args[3]][s.Key] = s.Value
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected aws %v", args)
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
	for _, name := range []string{`"tool.github"`, `"tool.filesystem"`, `"tool.aws"`} {
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
}
