package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dkslop "dungeon-keeper/pkg/slopspace"
	dktypes "dungeon-keeper/pkg/types"
)

// slopTest is dungeon-keeper's slopspaces, work signals and deploy path in
// a temp dir, a stand-in for its worker, and a fake local-representative
// whose files tab is the preferred file cache.
type slopTest struct {
	t   *testing.T
	env *slopEnv
	lr  *fakeLR
	srv *httptest.Server

	// agent does a signal's work in files (the deployed slopspace's), and
	// says whether it succeeded.
	agent func(files string, sig *dktypes.WorkSignal) bool
	// onWait runs at each wait, before the worker (nil for none).
	onWait func()
	ran    int
}

func newSlopTest(t *testing.T) *slopTest {
	root := t.TempDir()
	cfg := dktypes.DefaultConfig()
	cfg.SlopspacesDir = filepath.Join(root, "slopspaces")
	cfg.WorkSignalsDir = filepath.Join(root, "work")
	cfg.AgentSlopspaceRoot = filepath.Join(root, "agent")
	cfg.AgentRecordsPath = filepath.Join(root, "records")
	lr := newFakeLR()
	srv := httptest.NewServer(lr)
	t.Cleanup(srv.Close)
	return &slopTest{t: t, env: newSlopEnv(cfg), lr: lr, srv: srv}
}

// engine is a test engine on st's slopspaces and fake LR whose waits run
// st's worker.
func (st *slopTest) engine(mode string) *engine {
	e := testEngine(mode, false)
	e.slop = st.env
	e.lr = &localrepClient{base: st.srv.URL, http: st.srv.Client()}
	e.sleep = func(ctx context.Context, _ time.Duration) error {
		if st.onWait != nil {
			st.onWait()
		}
		st.work()
		return ctx.Err()
	}
	return e
}

// work is dungeon-keeper's worker: each pending signal runs in the
// slopspace deployed for agent-worker, and is completed.
func (st *slopTest) work() {
	s := st.env
	pending, err := s.signals.FindPendingForAgentType(ssAgentType)
	if err != nil {
		st.t.Fatal(err)
	}
	for _, p := range pending {
		sig, _, err := s.signals.Read(p)
		if err != nil {
			st.t.Fatal(err)
		}
		id, _ := s.spaces.GetDeployedID(ssAgentType)
		if id == "" {
			st.t.Fatalf("signal %s with no slopspace deployed", p)
		}
		if err := s.signals.AppendEvent(p, &dktypes.WorkEvent{StatusUpdate: "slopspace", SlopspaceID: id}); err != nil {
			st.t.Fatal(err)
		}
		ok := st.agent(s.agentFiles(), sig)
		if err := s.signals.AppendEvent(p, &dktypes.WorkEvent{StatusUpdate: "output", Output: "did " + sig.Role}); err != nil {
			st.t.Fatal(err)
		}
		comment := ""
		if !ok {
			comment = "agent invocation failed: exit status 1"
		}
		if err := s.signals.Complete(p, ok, comment); err != nil {
			st.t.Fatal(err)
		}
		st.ran++
	}
}

func readSpec(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.FromSlash(specificationsRoot), name, name+".dtt.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// cached is what the fake LR's files tab holds named name: each copy's
// bytes, oldest first.
func (st *slopTest) cached(name string) []string {
	var out []string
	for _, f := range st.lr.files {
		if f["name"] == name {
			out = append(out, string(st.lr.blobs[f["id"].(string)]))
		}
	}
	return out
}

func objectState(img *Image, id string) map[string]any {
	for _, o := range img.Objects {
		if o.ID == id {
			return o.State
		}
	}
	return nil
}

// TestSlopspaceDogToCat runs the slopspace-dog-to-cat specification: the
// slopspace, its dog story and the execution that makes it a cat story.
func TestSlopspaceDogToCat(t *testing.T) {
	st := newSlopTest(t)
	st.agent = func(files string, sig *dktypes.WorkSignal) bool {
		if sig.WorkType != dktypes.WorkTypeSlopspace || sig.Agent != "claude" || sig.Mode != "write" || !strings.Contains(sig.Prompt, "dog-story.md") {
			t.Errorf("signal %+v", sig)
		}
		p := filepath.Join(files, "dog-story.md")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		cat := strings.NewReplacer("beagle", "tabby", "barked twice", "meowed twice", "tail thumping", "tail swishing", "tennis ball", "toy mouse").Replace(string(b))
		return os.WriteFile(p, []byte(cat), 0o644) == nil
	}
	src, ctx := readSpec(t, "slopspace-dog-to-cat"), context.Background()
	key := fmt.Sprintf("dog-to-cat-%d", testNow.Unix())
	upload := key + ".md"

	read, err := st.engine("read").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if all, _ := st.env.spaces.List(); len(all) != 0 || st.ran != 0 || st.lr.uploads != 0 {
		t.Fatalf("read made %d slopspaces, ran %d, uploaded %d", len(all), st.ran, st.lr.uploads)
	}
	if got := outcomes(read); !strings.HasSuffix(got, "exists=would-change") {
		t.Errorf("read: %s", got)
	}

	made, err := st.engine("assert").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(made); got != "exists=changed exists=changed content=changed exists=changed exists=changed" {
		t.Errorf("assert: %s", got)
	}
	// The cat story, uploaded to the preferred file cache once the work was done.
	if got := st.cached(upload); len(got) != 1 || !strings.Contains(got[0], "tabby") || strings.Contains(got[0], "beagle") {
		t.Errorf("the files tab's %s: %q", upload, got)
	}
	up := objectState(made, "localrep.files["+upload+"]")
	if c, _ := up["content"].(string); up["exists"] != true || up["cache"] != "preferred" || up["files_state"] != "cached" || !strings.Contains(c, "toy mouse") || up["sha256"] == nil {
		t.Errorf("uploaded %+v", up)
	}
	m, mk, err := st.env.findSpace(key)
	if err != nil || m == nil {
		t.Fatalf("no slopspace %s (%v)", key, err)
	}
	if m.Deployed || len(mk.DeployedFor) != 0 || m.Iteration != 1 {
		t.Errorf("after the work: deployed %v, deployed for %v, iteration %d", m.Deployed, mk.DeployedFor, m.Iteration)
	}
	if !m.DTTRequestsDisabled {
		t.Error("DTT proposals on")
	}
	story := objectState(made, "slopspace.files["+key+":dog-story.md]")
	if c, _ := story["content"].(string); !strings.Contains(c, "tabby") || strings.Contains(c, "beagle") {
		t.Errorf("the story the image holds: %q", c)
	}
	if b, _ := os.ReadFile(filepath.Join(m.RootPath, dkslop.WriteSpacesDir, dkslop.FilesDir, "dog-story.md")); !strings.Contains(string(b), "toy mouse") {
		t.Errorf("the slopspace's story: %q", b)
	}
	exec := objectState(made, "slopspace.work.executions["+key+"]")
	if exec["status"] != "completed" || exec["exists"] != true || exec["slopspace_id"] != m.ID || exec["output"] != "did dtt-"+key || exec["id"] != fmt.Sprintf("dtt-%s-%d", key, testNow.Unix()) {
		t.Errorf("execution %+v", exec)
	}

	// Again: done already, the story the agent changed is held, and the
	// upload holds it already.
	again, err := st.engine("assert").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(again); got != "exists=unchanged content=held sha256=unchanged" || st.ran != 1 || st.lr.uploads != 1 {
		t.Errorf("again: %s, ran %d, uploaded %d", got, st.ran, st.lr.uploads)
	}

	// Deleting with the image deletes the slopspace and the upload, and
	// keeps the work.
	gone, err := st.engine("delete").runSource(ctx, src, roundTrip(t, made))
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(gone); got != "exists=changed exists=unchanged exists=kept exists=changed" {
		t.Errorf("delete: %s", got)
	}
	if all, _ := st.env.spaces.List(); len(all) != 0 {
		t.Errorf("delete left %d slopspaces", len(all))
	}
	if got := st.cached(upload); len(got) != 0 {
		t.Errorf("delete left %s in the files tab", upload)
	}
}

// TestCacheFileReplaced: a cached file whose copy_of changes is uploaded
// again from the image that saw it, the copy it replaces deleted, and held
// without one.
func TestCacheFileReplaced(t *testing.T) {
	st := newSlopTest(t)
	p := filepath.Join(t.TempDir(), "out.txt")
	src := fmt.Sprintf("localrep.files:\n  select:\n    name: out.txt\n  assert:\n    copy_of: %s\n", p)
	ctx := context.Background()
	if err := os.WriteFile(p, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	made, err := st.engine("assert").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := st.engine("assert").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(held); got != "sha256=held" || st.lr.uploads != 1 {
		t.Errorf("without the image: %s, uploaded %d", got, st.lr.uploads)
	}
	again, err := st.engine("assert").runSource(ctx, src, roundTrip(t, made))
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(again); got != "sha256=changed" {
		t.Errorf("with the image: %s", got)
	}
	if got := st.cached("out.txt"); len(got) != 1 || got[0] != "two\n" {
		t.Errorf("the files tab's out.txt: %q", got)
	}

	// A content and a copy_of together, a relative copy_of and another
	// cache are mistakes.
	for _, bad := range []string{
		"localrep.files:\n  select:\n    name: x.txt\n  assert:\n    content: a\n    copy_of: /tmp/x\n",
		"localrep.files:\n  select:\n    name: x.txt\n  assert:\n    copy_of: tmp/x\n",
		"localrep.files:\n  select:\n    name: x.txt\n    cache: elsewhere\n",
	} {
		doc, err := parseDocument(bad)
		if err == nil {
			err = st.engine("read").check(doc)
		}
		if err == nil {
			t.Errorf("no mistake found in %q", bad)
		}
	}
}

// TestSlopspaceCatStory runs the slopspace-cat-story specification: phase
// submit submits the work, phase collect waits for it and collects the
// story into a new directory.
func TestSlopspaceCatStory(t *testing.T) {
	st := newSlopTest(t)
	const story = "# Marmalade and the Machine\n\nA cat and a robot vacuum make peace.\n"
	st.agent = func(files string, sig *dktypes.WorkSignal) bool {
		return os.WriteFile(filepath.Join(files, "cat-story.md"), []byte(story), 0o644) == nil
	}
	src, root, ctx := readSpec(t, "slopspace-cat-story"), t.TempDir()+"/", context.Background()
	key := fmt.Sprintf("cat-story-%d", testNow.Unix())
	dir := filepath.Join(root, "slopspace-cat-story-"+fmt.Sprint(testNow.Unix()))
	engine := func(mode string) *engine {
		e := st.engine(mode)
		e.vars = map[string]string{"dir": root}
		return e
	}

	doc, err := parseDocument(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine("read").check(doc); err != nil {
		t.Fatal(err)
	}
	read, err := engine("read").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if all, _ := st.env.spaces.List(); len(all) != 0 || st.ran != 0 {
		t.Fatalf("read made %d slopspaces, ran %d", len(all), st.ran)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("read made the directory")
	}
	if got := outcomes(read); !strings.HasPrefix(got, "exists=would-change exists=would-change completed=would-change") {
		t.Errorf("read: %s", got)
	}

	// Submitting leaves the slopspace deployed, the work pending.
	st.onWait = func() {
		if st.ran > 0 {
			return
		}
		m, mk, _ := st.env.findSpace(key)
		if m == nil || !m.Deployed || len(mk.DeployedFor) != 1 {
			t.Errorf("waiting for the work: the slopspace is %+v (%+v)", m, mk)
		}
	}
	made, err := engine("assert").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	var phases []string
	for _, p := range made.Phases {
		phases = append(phases, p.Name+"="+p.Status)
	}
	if got := strings.Join(phases, " "); got != "submit=done collect=done" {
		t.Errorf("phases %s", got)
	}
	if got := outcomes(made); got != "exists=changed exists=changed completed=changed exists=changed exists=changed content=changed exists=changed" {
		t.Errorf("assert: %s", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cat-story.md")); err != nil || string(b) != story {
		t.Errorf("collected %q (%v)", b, err)
	}
	upload := key + ".md"
	if got := st.cached(upload); len(got) != 1 || got[0] != story {
		t.Errorf("the files tab's %s: %q", upload, got)
	}
	m, mk, _ := st.env.findSpace(key)
	if m == nil || m.Deployed || len(mk.DeployedFor) != 0 {
		t.Errorf("after the completion: %+v (%+v)", m, mk)
	}
	sub := objectState(made, "slopspace.work.submissions["+key+"]")
	id := fmt.Sprintf("dtt-%s-%d", key, testNow.Unix())
	done := objectState(made, "slopspace.work.completions["+id+"]")
	if sub["id"] != id || done["completed"] != true || done["file_content"] != story {
		t.Errorf("submission %+v\ncompletion %+v", sub, done)
	}

	gone, err := engine("delete").runSource(ctx, src, roundTrip(t, made))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outcomes(gone), "held") {
		t.Errorf("delete: %s", outcomes(gone))
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("deleting left the directory")
	}
	if got := st.cached(upload); len(got) != 0 {
		t.Errorf("deleting left %s in the files tab", upload)
	}
	if all, _ := st.env.spaces.List(); len(all) != 0 {
		t.Errorf("delete left %d slopspaces", len(all))
	}
}

// TestSlopspaceWaitsItsTurn: work waits for another slopspace deployed for
// the agent to be returned, and leaves it as it was.
func TestSlopspaceWaitsItsTurn(t *testing.T) {
	st := newSlopTest(t)
	other, err := st.env.spaces.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.env.spaces.Deploy(other.ID, ssAgentType); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(st.env.agentFiles(), "theirs.txt")
	if err := os.WriteFile(theirs, []byte("a condoc's"), 0o644); err != nil {
		t.Fatal(err)
	}
	waits := 0
	st.onWait = func() {
		waits++
		if waits == 2 {
			if err := st.env.spaces.Return(other.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	st.agent = func(files string, _ *dktypes.WorkSignal) bool {
		if _, err := os.Stat(filepath.Join(files, "theirs.txt")); err == nil {
			t.Error("the work ran in the other slopspace")
		}
		return true
	}
	src := `slopspace.spaces:
  select: {key: mine}
  assert: {exists: "true"}
slopspace.work.executions:
  select: {space: mine, key: turn}
  assert:
    prompt: do it
`
	if _, err := st.engine("assert").runSource(context.Background(), src, nil); err != nil {
		t.Fatal(err)
	}
	if waits < 2 || st.ran != 1 {
		t.Errorf("waited %d, ran %d", waits, st.ran)
	}
	back, _ := st.env.spaces.Get(other.ID)
	if b, err := os.ReadFile(filepath.Join(back.RootPath, dkslop.WriteSpacesDir, dkslop.FilesDir, "theirs.txt")); err != nil || string(b) != "a condoc's" {
		t.Errorf("the other slopspace's file: %q (%v)", b, err)
	}
}

// TestSlopspaceFailedWork: failed work is an error, its slopspace is
// returned, and the next assert submits it again.
func TestSlopspaceFailedWork(t *testing.T) {
	st := newSlopTest(t)
	fail := true
	st.agent = func(string, *dktypes.WorkSignal) bool { return !fail }
	src := `slopspace.spaces:
  select: {key: flaky}
  assert: {exists: "true"}
slopspace.work.executions:
  select: {space: flaky, key: flaky-work}
  assert:
    prompt: try
    agent: pi
    model: some-model
    mode: execute
    timeout: 60
`
	ctx := context.Background()
	img, err := st.engine("assert").runSource(ctx, src, nil)
	if err == nil || !strings.Contains(err.Error(), "failed: agent invocation failed") {
		t.Fatalf("got %v", err)
	}
	if s := objectState(img, "slopspace.work.executions[flaky-work]"); s["status"] != "failed" || s["exists"] != false || s["agent"] != "pi" || s["model"] != "some-model" {
		t.Errorf("execution %+v", s)
	}
	if m, _, _ := st.env.findSpace("flaky"); m == nil || m.Deployed {
		t.Errorf("slopspace %+v", m)
	}
	fail = false
	e := st.engine("assert")
	e.now = func() time.Time { return testNow.Add(time.Second) }
	if _, err := e.runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	sigs, _ := st.env.signalsFor("dtt-flaky-work")
	if len(sigs) != 2 || st.ran != 2 {
		t.Errorf("%d signals, ran %d", len(sigs), st.ran)
	}
}

// TestSlopspaceCompletionWithoutFile: work that completes without the file
// the completion grabs is an error.
func TestSlopspaceCompletionWithoutFile(t *testing.T) {
	st := newSlopTest(t)
	st.agent = func(string, *dktypes.WorkSignal) bool { return true }
	src := `slopspace.spaces:
  select: {key: empty}
  assert: {exists: "true"}
slopspace.work.submissions:
  select: {space: empty, key: nothing}
  alias: sent
  assert:
    prompt: write nothing
---
slopspace.work.completions:
  select:
    id: ${{sent.id}}
    file: missing.md
  assert:
    completed: true
`
	_, err := st.engine("assert").runSource(context.Background(), src, nil)
	if err == nil || !strings.Contains(err.Error(), "left no missing.md") {
		t.Fatalf("got %v", err)
	}
	if m, _, _ := st.env.findSpace("empty"); m == nil || m.Deployed {
		t.Errorf("slopspace %+v", m)
	}
}

func TestSlopspaceChecks(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"slopspace.things:\n  select: {key: a}\n", "slopspace paths are"},
		{"slopspace.spaces:\n  select: {key: Bad Key}\n", "keys are lower-case"},
		{"slopspace.spaces:\n  select: {name: a}\n", "name"},
		{"slopspace.files:\n  select: {space: a, path: ../x}\n", "relative path"},
		{"slopspace.work.executions:\n  select: {space: a, key: b}\n  assert: {exists: \"false\"}\n", "can only be asserted to exist"},
		{"slopspace.work.executions:\n  select: {space: a, key: b}\n  assert: {mode: shout}\n", "mode is prompt"},
		{"slopspace.work.submissions:\n  select: {space: a, key: b}\n  assert: {wait: soon}\n", "wait is a number"},
		{"slopspace.work.completions:\n  select: {id: x}\n  assert: {completed: \"false\"}\n", "only be asserted true"},
		{"slopspace.work.completions:\n  select: {id: x}\n  assert: {prompt: hi}\n", "prompt"},
	} {
		doc, err := parseDocument(tc.src)
		if err == nil {
			err = testEngine("read", false).check(doc)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v, want %q", tc.src, err, tc.want)
		}
	}

	st := newSlopTest(t)
	for _, tc := range []struct{ src, want string }{
		{"slopspace.work.executions:\n  select: {space: nowhere, key: b}\n  assert: {prompt: hi}\n", "doesn't exist"},
		{"slopspace.spaces:\n  select: {key: a}\n  assert: {exists: \"true\"}\nslopspace.work.executions:\n  select: {space: a, key: b}\n  assert: {agent: pi}\n", "give the prompt"},
		{"slopspace.work.completions:\n  select: {id: dtt-none-1}\n  assert: {completed: \"true\"}\n", "no work dtt-none-1"},
		{"slopspace.work.completions:\n  select: {id: someone-elses}\n", "isn't work dtt submitted"},
	} {
		if _, err := st.engine("assert").runSource(context.Background(), tc.src, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v, want %q", tc.src, err, tc.want)
		}
	}
}
