package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file tests delays, phases split by ---, the filesystem directory's
// exists and copy_from, and the understanding-phases specification that
// uses them all.

func TestDelay(t *testing.T) {
	var waited []time.Duration
	e := testEngine("read", false)
	e.sleep = func(_ context.Context, d time.Duration) error { waited = append(waited, d); return nil }
	img, err := e.runSource(context.Background(), "delay:\n  select: 1m30s\n  alias: pause\ndelay:\n  select: \"2\"\n  after: pause\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(waited) != 2 || waited[0] != 90*time.Second || waited[1] != 2*time.Second {
		t.Fatalf("waited %v", waited)
	}
	s := img.Objects[0].State
	if img.Objects[0].ID != "delay[pause]" || s["began"] != testNow.Format(time.RFC3339) || s["completed"] != testNow.Format(time.RFC3339) ||
		s["began_unix"] != testNow.Unix() || s["duration"] != "1m30s" || s["seconds"] != 90.0 {
		t.Errorf("delay state %+v", img.Objects[0])
	}

	// A cancelled run stops waiting.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := testEngine("read", false).runSource(ctx, "delay:\n  select: 1h\n", nil); err == nil || !strings.Contains(err.Error(), "waiting 1h0m0s") {
		t.Errorf("cancelled: %v", err)
	}

	for _, tc := range []struct{ src, want string }{
		{"delay:\n  select: soon\n", "should be a duration"},
		{"delay:\n  select: 2h\n", "from 0 to 1h0m0s"},
		{"delay.x:\n  select: 1s\n", "just delay"},
		{"delay:\n  select: 1s\n  assert:\n    seconds: 2\n", "not asserted"},
		{"delay:\n  select: 1s\n  after: nothing\n", "after: nothing"},
	} {
		if _, err := testEngine("read", false).runSource(context.Background(), tc.src, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v, want %q", tc.src, err, tc.want)
		}
	}
}

func TestDelimitedPhases(t *testing.T) {
	doc, err := parseDocument("---\ntimestamp:\n  select: now\n---\ntimestamp:\n  select: \"1\"\n  phase: second\n---\n# only a comment\ntimestamp:\n  select: \"2\"\n")
	if err != nil {
		t.Fatal(err)
	}
	e := testEngine("read", false)
	if err := e.check(doc); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range e.phases {
		names = append(names, p.Name)
	}
	if got := strings.Join(names, " "); got != "phase-1 second phase-3" {
		t.Errorf("phases %s", got)
	}
	img, err := testEngine("read", false).runSource(context.Background(), "timestamp:\n  select: now\n---\ntimestamp:\n  select: \"1\"\n", nil)
	if err != nil || len(img.Phases) != 2 || img.Phases[1].Status != "done" || img.Objects[1].Phase != "phase-2" {
		t.Fatalf("image %+v (%v)", img, err)
	}
	if _, err := testEngine("read", false).runSource(context.Background(), "timestamp:\n  phase: a\ntimestamp:\n  select: \"1\"\n  phase: b\n---\ntimestamp:\n  select: \"2\"\n", nil); err == nil || !strings.Contains(err.Error(), "each part is one phase") {
		t.Errorf("two phases in a part: %v", err)
	}
}

func TestFilesystemDirExistsAndCopy(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("A ${{not.a.ref}}\n"), 0o644)
	os.WriteFile(filepath.Join(src, "b.bin"), []byte{0, 1, 2, 255}, 0o644)
	doc := fmt.Sprintf("filesystem.items:\n  select:\n    abspath: %s\n  alias: d\n  assert:\n    exists: true\n    copy_from: %s\n", dst, src)
	ctx := context.Background()

	img, err := testEngine("read", false).runSource(ctx, doc, nil)
	if err != nil || outcomes(img) != "exists=would-change exists=would-change content=would-change exists=would-change content=would-change" {
		t.Fatalf("read: %s (%v)", outcomes(img), err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("read made the directory")
	}
	made, err := testEngine("assert", false).runSource(ctx, doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a.txt": "A ${{not.a.ref}}\n", "b.bin": "\x00\x01\x02\xff"} {
		if b, _ := os.ReadFile(filepath.Join(dst, name)); string(b) != want {
			t.Errorf("%s holds %q", name, b)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "sub")); err == nil {
		t.Error("a directory was copied")
	}
	if items := stringList(made.Objects[0].State["items"]); len(items) != 2 {
		t.Errorf("dst items %v", items)
	}
	if img, _ = testEngine("assert", false).runSource(ctx, doc, nil); outcomes(img) != "exists=unchanged content=unchanged content=unchanged" {
		t.Errorf("again: %s", outcomes(img))
	}
	// A source that isn't there fails an assert.
	if _, err := testEngine("assert", false).runSource(ctx, strings.Replace(doc, src, src+"-gone", 1), nil); err == nil || !strings.Contains(err.Error(), "copy_from") {
		t.Errorf("missing source: %v", err)
	}

	// exists: false removes the directory and all in it -- by the from rule.
	rm := fmt.Sprintf("filesystem.items:\n  select:\n    abspath: %s\n  assert:\n    exists: false\n", src)
	if img, _ = testEngine("assert", false).runSource(ctx, rm, nil); outcomes(img) != "exists=held" {
		t.Errorf("removing without from: %s", outcomes(img))
	}
	if img, err = testEngine("assert", false).runSource(ctx, rm+"    from.exists: \"true\"\n", nil); err != nil || outcomes(img) != "exists=changed" {
		t.Errorf("removing: %s (%v)", outcomes(img), err)
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("src is still there")
	}

	// Deleting with the assert's image removes the copies and the directory.
	if img, err = testEngine("delete", false).runSource(ctx, doc, roundTrip(t, made)); err != nil || !strings.Contains(outcomes(img), "exists=changed") {
		t.Fatalf("delete: %s (%v)", outcomes(img), err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("dst is still there")
	}

	for _, tc := range []struct{ src, want string }{
		{"filesystem.items:\n  select: {abspath: /tmp}\n  assert:\n    copy_from: tmp\n", "copy_from should be"},
		{"filesystem.items:\n  select: {abspath: /tmp}\n  assert:\n    from.exists: x\n", "has no exists beside it"},
		{"filesystem.items:\n  select: {abspath: /tmp}\n  assert:\n    sizes: 1\n", `takes no "sizes"`},
	} {
		if _, err := testEngine("read", false).runSource(ctx, tc.src, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v, want %q", tc.src, err, tc.want)
		}
	}
	if err := removeFSDir("/tmp"); err == nil {
		t.Error("removeFSDir(/tmp)")
	}
}

// TestUnderstandingPhases runs the understanding-phases specification
// (without its waits): phase make-first makes a directory and a file, copy
// copies it into a second, clean-up removes the first; deleting with the
// image removes the second.
func TestUnderstandingPhases(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(filepath.FromSlash(specificationsRoot), "understanding-phases", "understanding-phases.dtt.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	src, root, ctx := string(b), t.TempDir()+"/", context.Background()
	first := filepath.Join(root, fmt.Sprintf("understanding-phases-%d-first", testNow.Unix()))
	second := filepath.Join(root, fmt.Sprintf("understanding-phases-%d-second", testNow.Unix()))
	engine := func(mode string) *engine {
		e := testEngine(mode, false)
		e.vars = map[string]string{"dir": root, "wait": "0s"}
		return e
	}

	doc, err := parseDocument(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine("read").check(doc); err != nil {
		t.Fatal(err)
	}
	steps, _ := planSteps(doc)
	var got []string
	for _, s := range steps {
		got = append(got, s.ID+":"+s.Provider+"<"+strings.Join(s.After, ",")+">"+string(rune('0'+s.Level))+"@"+s.Phase)
	}
	want := "s1:variables<>0@make-first s2:timestamp<>0@make-first s3:filesystem<s1,s2>1@make-first s4:delay<s1,s3>2@make-first " +
		"s5:filesystem<s1,s2,s3,s4>3@copy s6:delay<s1,s5>4@copy s7:filesystem<s3,s6>5@clean-up s8:delay<s1,s7>6@clean-up"
	if strings.Join(got, " ") != want {
		t.Errorf("plan %s\nwant %s", strings.Join(got, " "), want)
	}

	if _, err := engine("read").runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("read made %v", entries)
	}

	made, err := engine("assert").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(made); got != "exists=changed exists=changed content=changed exists=changed exists=changed content=changed exists=changed" {
		t.Errorf("assert: %s", got)
	}
	var phases []string
	for _, p := range made.Phases {
		phases = append(phases, p.Name+"="+p.Status)
	}
	if got := strings.Join(phases, " "); got != "make-first=done copy=done clean-up=done" {
		t.Errorf("phases %s", got)
	}
	if _, err := os.Stat(first); err == nil {
		t.Error("clean-up left the first directory")
	}
	if b, err := os.ReadFile(filepath.Join(second, "hello.txt")); err != nil || !strings.Contains(string(b), "phase make-first") || !strings.Contains(string(b), testNow.Format(time.RFC3339)) {
		t.Errorf("the copy holds %q (%v)", b, err)
	}
	waits := 0
	for _, o := range made.Objects {
		if strings.HasPrefix(o.ID, "delay[") {
			waits++
			if o.State["began"] == nil || o.State["completed"] == nil {
				t.Errorf("%s: %+v", o.ID, o.State)
			}
		}
	}
	if waits != 3 {
		t.Errorf("%d delays", waits)
	}

	// Deleting with the image (through JSON, as dtt keeps it).
	if _, err := engine("delete").runSource(ctx, src, roundTrip(t, made)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(second); err == nil {
		t.Error("deleting left the second directory")
	}
}

// TestUnderstandingPhasesUnpacked reads the specification from where dtt
// examples --specifications unpacks it, as the calls tab does.
func TestUnderstandingPhasesUnpacked(t *testing.T) {
	e := newEnv(t)
	if code, out := e.dtt("", "examples", "--specifications", "--json"); code != exitOK || !strings.Contains(out, `"understanding-phases"`) {
		t.Fatalf("dtt examples --specifications exited %d: %s", code, out)
	}
	dir := filepath.Join(e.examplesDir, "specifications", "understanding-phases")
	code, out := e.dtt("", "read", dir, "--var", "dir="+t.TempDir()+"/", "--var", "wait=0")
	if code != exitOK || !strings.Contains(out, `"clean-up"`) {
		t.Fatalf("read exited %d: %s", code, out)
	}
}
