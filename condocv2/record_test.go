package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTimelineRecordRoundTrip(t *testing.T) {
	spec := &PointerSpec{Name: "Foo", CondocRepo: "a/condocs", CondocPath: "condocs/Foo.md", Branch: "condocv2/Foo-42/main"}
	tl := Timeline{
		Lanes: []TimelineLane{
			{ID: "pointer", Repo: "a/pointers", Role: "pointer"},
			{ID: "condoc", Repo: "a/condocs", Role: "condoc"},
			{ID: "a/one", Repo: "a/one", Role: "target"},
		},
		Events: []TimelineEvent{
			{Lane: "pointer", Kind: "propose", SHA: "p1", URL: commitURL("a/pointers", "p1")},
			{Lane: "condoc", Kind: "begin", SHA: "c1", URL: commitURL("a/condocs", "c1")},
			{Lane: "condoc", Kind: "prompt", Interaction: 1, SHA: "c2", Subject: "condocv2: Foo prompt 1", Time: 100, URL: commitURL("a/condocs", "c2")},
			{Lane: "a/one", Kind: "commit", Interaction: 1, SHA: "t1", URL: commitURL("a/one", "t1")},
		},
	}
	interactions := []Interaction{{N: 1, Prompt: "Do </script> & <b>it</b>", Response: "Done.", Record: "- a/one: ..."}}
	rec := newTimelineRecord(spec, tl, interactions, time.Unix(500, 0))
	if !reflect.DeepEqual(rec.Prompts, []Interaction{{N: 1, Prompt: interactions[0].Prompt}}) {
		t.Errorf("prompts = %+v", rec.Prompts)
	}

	page, err := renderRecordHTML(rec)
	if err != nil {
		t.Fatal(err)
	}
	// Every commit is a link, and the prompt can't break out of the page.
	for _, want := range []string{commitURL("a/pointers", "p1"), commitURL("a/condocs", "c2"), commitURL("a/one", "t1"), "Do &lt;/script&gt;"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	got, err := parseRecordHTML(page)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rec) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, rec)
	}

	if recordRepoPath(spec) != "condocs/fooImpls/Record.html" {
		t.Errorf("recordRepoPath = %q", recordRepoPath(spec))
	}
	if _, err := parseRecordHTML("<html></html>"); err == nil {
		t.Error("a page without a model parsed")
	}
}
