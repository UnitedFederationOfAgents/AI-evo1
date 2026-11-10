package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// progressLines decodes the progress lines in out, and returns the rest.
func progressLines(t *testing.T, out string) ([]ProgressEvent, string) {
	t.Helper()
	var evs []ProgressEvent
	var rest []string
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, string(progressPrefix)) {
			rest = append(rest, l)
			continue
		}
		var ev ProgressEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("progress line %q: %v", l, err)
		}
		evs = append(evs, ev)
	}
	return evs, strings.Join(rest, "\n")
}

// sequence is evs as "begin s1", "end s1 ok", ... after the plan.
func sequence(evs []ProgressEvent) string {
	var parts []string
	for _, ev := range evs {
		switch ev.Progress {
		case "plan":
			parts = append(parts, "plan")
		case "begin":
			parts = append(parts, "begin "+ev.Step+stageMark(ev))
		case "end":
			parts = append(parts, "end "+ev.Step+stageMark(ev)+" "+ev.Status)
		}
	}
	return strings.Join(parts, ", ")
}

func stageMark(ev ProgressEvent) string {
	if ev.Stage == "remeasure" {
		return "'"
	}
	return ""
}

func TestPlanSteps(t *testing.T) {
	doc, err := parseDocument(`variables:
  dir: /tmp/x
timestamp:
  select: now
filesystem.items:
  select:
    abspath: "${{var.dir}}"
  alias: folder
folder:
  assert:
    items:
      stamp.txt:
        content: "${{timestamp.date}}"
`)
	if err != nil {
		t.Fatal(err)
	}
	e := testEngine("read", false)
	e.vars = map[string]string{}
	if err := e.check(doc); err != nil {
		t.Fatal(err)
	}
	steps, _ := planSteps(doc)
	got := []string{}
	for _, s := range steps {
		got = append(got, s.ID+":"+s.Provider+"<"+strings.Join(s.After, ",")+">"+string(rune('0'+s.Level)))
	}
	if want := "s1:variables<>0 s2:timestamp<>0 s3:filesystem<s1>1 s4:filesystem<s2,s3>2"; strings.Join(got, " ") != want {
		t.Fatalf("steps %s, want %s", strings.Join(got, " "), want)
	}
	if steps[3].Alias != "folder" || !steps[3].Asserts || steps[2].Asserts {
		t.Errorf("steps %+v", steps)
	}
}

func TestEngineProgress(t *testing.T) {
	dir := t.TempDir()
	src := strings.Replace(readExample(t, "filesystem-json.dtt.yaml"), `"/tmp/"`, dir, 1)
	var buf bytes.Buffer
	e := testEngine("assert", false)
	e.progress = newProgressOut(&buf)
	if _, err := e.runSource(context.Background(), src, nil); err != nil {
		t.Fatal(err)
	}
	evs, rest := progressLines(t, buf.String())
	if strings.TrimSpace(rest) != "" {
		t.Fatalf("not progress: %q", rest)
	}
	// The assert changed the file, so its folder is measured again.
	if got, want := sequence(evs), "plan, begin s1, end s1 ok, begin s2, end s2 ok, begin s1', end s1' ok"; got != want {
		t.Fatalf("progress %s, want %s", got, want)
	}
	if evs[0].Format != progressFormat || evs[0].Mode != "assert" || len(evs[0].Steps) != 2 || evs[0].Steps[1].After[0] != "s1" {
		t.Errorf("plan %+v", evs[0])
	}
	end := evs[4] // s2's: its blob
	if len(end.Objects) == 0 || end.Objects[0].Alias != "temp-folder" || end.Summary["changed"] != 2 || len(end.Fields) != 2 {
		t.Errorf("s2's end: %d objects, summary %v, %d fields", len(end.Objects), end.Summary, len(end.Fields))
	}

	// A step that fails ends failed, with its error, and the steps after it
	// never begin.
	dttProviders["failing"] = failingProvider{}
	defer delete(dttProviders, "failing")
	buf.Reset()
	e = testEngine("read", false)
	e.progress = newProgressOut(&buf)
	if _, err := e.runSource(context.Background(), "timestamp:\n  select: now\nfailing.thing:\n  read: \"*\"\nfailing.other:\n  read: \"*\"\n", nil); err == nil {
		t.Fatal("the failing step didn't fail the run")
	}
	evs, _ = progressLines(t, buf.String())
	if got, want := sequence(evs), "plan, begin s1, end s1 ok, begin s2, end s2 failed"; got != want {
		t.Fatalf("progress %s, want %s", got, want)
	}
	if !strings.Contains(evs[len(evs)-1].Error, "it broke") || len(evs[0].Steps) != 3 {
		t.Errorf("the failure %+v, plan %+v", evs[len(evs)-1], evs[0])
	}
}

// failingProvider names objects it can never measure.
type failingProvider struct{}

func (failingProvider) checkSelect(st *statement) error              { return nil }
func (failingProvider) checkAssert(path []string, body *yNode) error { return nil }
func (failingProvider) resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	return nil, errors.New("it broke")
}
func (failingProvider) assert(ctx context.Context, e *engine, o *dobj, body *yNode) error { return nil }

// TestRunProgress runs a declarative tool through dtt run: its progress
// comes back on stdout, as JSON lines ahead of the result with --json
// --progress, and as text without --json.
func TestRunProgress(t *testing.T) {
	t.Setenv("DTT_TEST_BE_DTT", "1")
	e := newEnv(t)
	dir := t.TempDir()
	doc := filepath.Join(t.TempDir(), "doc.dtt.yaml")
	os.WriteFile(doc, []byte(strings.Replace(readExample(t, "filesystem-json.dtt.yaml"), `"/tmp/"`, dir, 1)), 0o644)

	code, out := e.dtt("", "run", "declare", "--arg", "document_file="+doc, "--arg", "mode=read", "--progress", "--json")
	if code != exitOK {
		t.Fatalf("run exited %d: %s", code, out)
	}
	evs, rest := progressLines(t, out)
	if got, want := sequence(evs), "plan, begin s1, end s1 ok, begin s2, end s2 ok"; got != want {
		t.Fatalf("progress %s, want %s", got, want)
	}
	var res RunResult
	if err := json.Unmarshal([]byte(rest), &res); err != nil || !res.OK {
		t.Fatalf("the result after the progress (%v): %s", err, rest)
	}

	// Without --progress, --json prints the result alone.
	if code, out = e.dtt("", "run", "declare", "--arg", "document_file="+doc, "--json"); code != exitOK || strings.Contains(out, `"progress"`) {
		t.Fatalf("run --json exited %d: %s", code, out)
	}

	// As text, each step's beginning and end.
	code, out = e.dtt("", "run", "declare", "--arg", "document_file="+doc)
	if code != exitOK || !strings.Contains(out, "plan: 2 step(s), read") || !strings.Contains(out, "s2   begin  temp-folder") ||
		!strings.Contains(out, "s2   done   temp-folder") || !strings.Contains(out, "declare: ok in") {
		t.Fatalf("run exited %d: %s", code, out)
	}
}
