package main

import (
	"strings"
	"testing"
)

func TestPRDrafts(t *testing.T) {
	spec := PointerSpec{Name: "Foo", Targets: []string{"a/one", "a/two"}, CondocRepo: "a/condocs"}
	repos := []string{"a/one", "a/condocs"} // a/two has nothing to merge

	// The fallback draft parses and covers exactly the repos.
	drafts, err := parsePRTemplate(defaultPRTemplate(&spec, repos))
	if err != nil {
		t.Fatalf("default template: %v", err)
	}
	if _, err := orderPRDrafts(repos, drafts); err != nil {
		t.Fatalf("default template: %v", err)
	}

	// Drafts come back in repos' order (targets first, the condoc repo last)
	// with the repos' own spelling.
	agent := "prs:\n  - repo: A/Condocs\n    title: Condoc\n    body: |\n      - one\n      - two\n" +
		"  - repo: a/one\n    title: One\n"
	drafts, err = parsePRTemplate(agent)
	if err != nil {
		t.Fatal(err)
	}
	ordered, err := orderPRDrafts(repos, drafts)
	if err != nil {
		t.Fatal(err)
	}
	if ordered[0].Repo != "a/one" || ordered[1].Repo != "a/condocs" || ordered[1].Body != "- one\n- two" {
		t.Errorf("ordered: %+v", ordered)
	}

	// Empty, untitled, missing a/condocs, a/two (nothing to merge), a/one twice.
	for _, bad := range []string{
		"prs: []\n",
		"prs:\n  - repo: a/one\n    title: \"\"\n",
		"prs:\n  - repo: a/one\n    title: One\n",
		"prs:\n  - repo: a/one\n    title: One\n  - repo: a/condocs\n    title: C\n  - repo: a/two\n    title: Two\n",
		"prs:\n  - repo: a/one\n    title: One\n  - repo: a/one\n    title: Again\n  - repo: a/condocs\n    title: C\n",
	} {
		drafts, err := parsePRTemplate(bad)
		if err == nil {
			_, err = orderPRDrafts(repos, drafts)
		}
		if err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestDraftPromptCarriesGuidance(t *testing.T) {
	p := draftPrompt("/ss/pr-draft-1", []string{"a/one", "a/condocs"}, false)
	for _, want := range []string{"/ss/pr-draft-1", "guidance.txt", prTemplateFile, "a/one, a/condocs"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q: %s", want, p)
		}
	}
	if strings.Contains(p, "previous-prs.yaml") {
		t.Error("a first draft mentions previous drafts")
	}
	if !strings.Contains(draftPrompt("/d", nil, true), "previous-prs.yaml") {
		t.Error("a redraft doesn't mention the previous drafts")
	}
	if !strings.Contains(defaultPRGuidance, "extremely concise") {
		t.Error("the default PR guidance isn't asking for extreme concision")
	}
}
