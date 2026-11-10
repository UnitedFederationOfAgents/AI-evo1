package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResourceSpecRoundTrip(t *testing.T) {
	spec := PointerSpec{Name: "Foo", StartTime: 1, ControlScheme: condocControlScheme, Branch: "condocv2/x", CondocRepo: "o/c",
		Phase: PhaseAwaitingPrompt, Interactions: 4,
		Resources: []Resource{
			{Number: 1, Interaction: 2, Title: "Design: the sketch", Source: resourceFromUpload, Files: []string{"a.png", "notes: one.md"}, Time: 100},
			{Number: 2, Interaction: 4, Step: "1", Source: resourceFromText, Files: []string{"resource.md"}},
		}}
	got, err := parsePointerSpec(spec.render())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Resources, spec.Resources) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got.Resources, spec.Resources)
	}
}

func TestResourceFiles(t *testing.T) {
	cache, store := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(cache, "0123abcd_a.txt"), []byte("cached"), 0o644)
	os.WriteFile(filepath.Join(cache, ".manifest_0123abcd_a.txt.yaml"), []byte("held: false"), 0o644)
	os.WriteFile(filepath.Join(store, "89abcdef_a.txt"), []byte("stored"), 0o644)
	s := &Server{hostCacheDir: cache, hostStoreDir: store}

	listed := s.hostFiles()
	if len(listed) != 2 {
		t.Fatalf("host files %+v: want the two files, not the manifest", listed)
	}
	for _, f := range listed {
		if f.Name != "a.txt" {
			t.Errorf("host file %s named %q", f.ID, f.Name)
		}
	}

	// From the host's files: both named a.txt, so the second is renamed.
	files, err := s.resourceFiles(ResourceRequest{Source: resourceFromFiles, HostFiles: []string{"0123abcd_a.txt", "89abcdef_a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if files[0].name != "a.txt" || files[1].name != "a-2.txt" || string(files[1].data) != "stored" {
		t.Errorf("host files read as %q/%q, %q", files[0].name, files[1].name, files[1].data)
	}
	for _, bad := range []string{"../etc/passwd", ".manifest_0123abcd_a.txt.yaml", "missing"} {
		if _, err := s.resourceFiles(ResourceRequest{Source: resourceFromFiles, HostFiles: []string{bad}}); err == nil {
			t.Errorf("host file %q was read", bad)
		}
	}

	// Uploaded: base64, and a name that tries to leave its directory.
	files, err = s.resourceFiles(ResourceRequest{Source: resourceFromUpload, Uploads: []ResourceUpload{
		{Name: "../../x/.secret.md", Data: base64.StdEncoding.EncodeToString([]byte("hi"))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if files[0].name != "secret.md" || string(files[0].data) != "hi" {
		t.Errorf("upload read as %q: %q", files[0].name, files[0].data)
	}

	// Text: one file, named resource.md unless told otherwise.
	files, err = s.resourceFiles(ResourceRequest{Source: resourceFromText, Text: "the text"})
	if err != nil || len(files) != 1 || files[0].name != "resource.md" {
		t.Errorf("text read as %+v, %v", files, err)
	}

	// Voice: dictated text, one file, named dictation.md unless told otherwise.
	files, err = s.resourceFiles(ResourceRequest{Source: resourceFromVoice, Text: "spoken words"})
	if err != nil || len(files) != 1 || files[0].name != "dictation.md" || string(files[0].data) != "spoken words" {
		t.Errorf("voice read as %+v, %v", files, err)
	}

	for _, req := range []ResourceRequest{
		{Source: resourceFromText, Text: "  "},
		{Source: resourceFromVoice},
		{Source: resourceFromHighlighted}, // not connected to local-representative
		{Source: resourceFromUpload},
		{Source: "url"},
		{Source: resourceFromUpload, Uploads: []ResourceUpload{{Name: "x", Data: "not base64!"}}},
	} {
		if _, err := s.resourceFiles(req); err == nil {
			t.Errorf("%+v was taken", req)
		}
	}
}

func TestResourceSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Step1.md")
	if err := writeFileString(path, initialStepFile("Foo")+"\n## Prompt 1\n\none\n\n## Record 1\n\n- o/t: (no changes)\n"); err != nil {
		t.Fatal(err)
	}
	r := Resource{Number: 1, Interaction: 1, Title: "## Prompt 9", Source: resourceFromUpload, Files: []string{"a.png"}}
	// A retried resource commit replaces the section the failed one left.
	for _, body := range []string{"stale", resourceBody(r, map[string]int{"a.png": 2048})} {
		if err := setResourceSection(path, 1, body); err != nil {
			t.Fatal(err)
		}
	}
	content, _ := readFileString(path)
	if strings.Count(content, "## Resource 1\n") != 1 || strings.Contains(content, "stale") {
		t.Errorf("content:\n%s", content)
	}
	if !strings.Contains(content, "- [a.png](resources/1/a.png) (2.0 KB)") {
		t.Errorf("no link to the file:\n%s", content)
	}
	// The title can't forge an interaction, and the resource ends the record
	// before it.
	its := parseInteractions(content + "\n## Prompt 2\n\ntwo\n")
	if len(its) != 2 || its[0].Record != "- o/t: (no changes)" || its[1].Prompt != "two" {
		t.Errorf("interactions %+v", its)
	}
}

func TestResourceTimeline(t *testing.T) {
	spec := stepSpec()
	spec.Name = "Foo"
	spec.Resources = []Resource{{Number: 1, Interaction: 3, Step: "1", Title: "the sketch", Source: resourceFromUpload, Files: []string{"a.png"}, Time: 350}}
	commits := []gitCommit{
		{sha: "a", time: 350, subject: "condocv2: Foo resource 3.1"},
		{sha: "b", time: 250, subject: "condocv2: Foo prompt 3"},
	}
	tl := spec.scopeTimeline(Timeline{
		Lanes: []TimelineLane{{ID: "pointer", Role: "pointer"}, {ID: "condoc", Role: "condoc"}},
		Events: append(timelineEvents("Foo", "condoc", "o/c", commits, "resource", "prompt"),
			timelineEvents("Foo", "pointer", "o/p", commits[:1], "resource")...),
	})
	sortTimeline(tl.Events)
	for _, e := range tl.Events {
		if e.Kind != "resource" {
			continue
		}
		if e.Interaction != 3 || e.Seq != 1 || e.Sub != 0 || e.Step != "1" || e.Note != "the sketch" {
			t.Errorf("resource event %+v", e)
		}
	}
	if k := tl.Events[len(tl.Events)-1].Kind; k != "resource" {
		t.Errorf("the resource isn't after interaction 3: last event %s", k)
	}
	layout := newSwimLayout(tl.Events, nil)
	if row := layout.resources[1]; row <= layout.base[3] {
		t.Errorf("resource 1 at row %d, interaction 3 at %d", row, layout.base[3])
	}
	svg := swimlaneSVG(tl, nil)
	for _, want := range []string{"resource 1", "the sketch"} {
		if !strings.Contains(svg, want) {
			t.Errorf("svg has no %q", want)
		}
	}
}
