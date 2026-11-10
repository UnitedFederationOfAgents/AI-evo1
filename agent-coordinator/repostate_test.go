package main

import (
	"encoding/json"
	"testing"
)

// TestRepoStateUfabuildRoundTrips guards against a Revision K-shaped bug
// (Step5Prompt.md Revision O/P): AC's own RepoStateMsg, used to decode LR's
// "repo-state" payload, falling out of sync with
// local-representative/repowatch.go's, so json.Unmarshal silently drops a
// field and repoStateMsg() never forwards it into "lr-repo-state". Here, the
// .ufabuild request fields the repo-watch panel shows.
func TestRepoStateUfabuildRoundTrips(t *testing.T) {
	// Mirrors what local-representative's RepoStateMsg actually serializes to.
	raw := []byte(`{"watched":true,"dirty":false,"rebuild_ready":true,"ufabuild_request":"1a2b3c4","ufabuild_note":"note"}`)

	var rs RepoStateMsg
	if err := json.Unmarshal(raw, &rs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(repoStateMsg("host-1", &rs))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(out, &roundTripped); err != nil {
		t.Fatalf("unmarshal round-tripped output: %v", err)
	}
	if roundTripped["ufabuild_request"] != "1a2b3c4" || roundTripped["ufabuild_note"] != "note" || roundTripped["rebuild_ready"] != true {
		t.Fatalf("re-encoded LRRepoStateMsg dropped the .ufabuild fields: %v", roundTripped)
	}
}

// TestRepoStateBranchRoundTrips is the same guard for the dev-branch fields
// (Step1SubstepCPrompt.md Revision B).
func TestRepoStateBranchRoundTrips(t *testing.T) {
	raw := []byte(`{"watched":true,"branch":"main","branches":["main","other"],"switch_error":"nope"}`)

	var rs RepoStateMsg
	if err := json.Unmarshal(raw, &rs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg := repoStateMsg("host-1", &rs)
	if msg.Branch != "main" || len(msg.Branches) != 2 || msg.Branches[1] != "other" || msg.SwitchError != "nope" {
		t.Fatalf("repoStateMsg dropped dev-branch fields: %+v", msg)
	}
}

// TestRepoStateExternalsRoundTrips is the same guard for the external paths
// (privateRepoOpsImpls Step6Prompt.md Revision E), relayed as-is.
func TestRepoStateExternalsRoundTrips(t *testing.T) {
	raw := []byte(`{"watched":true,"externals":[{"path":"../HyenaExternalUfaPlugin","plugin":true,"app_id":"hyena-external-ufa-plugin","building":true}],"external_error":"nope"}`)

	var rs RepoStateMsg
	if err := json.Unmarshal(raw, &rs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(repoStateMsg("host-1", &rs))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var msg struct {
		Externals []struct {
			Path     string `json:"path"`
			AppID    string `json:"app_id"`
			Building bool   `json:"building"`
		} `json:"externals"`
		ExternalError string `json:"external_error"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatalf("unmarshal lr-repo-state: %v", err)
	}
	if len(msg.Externals) != 1 || msg.Externals[0].AppID != "hyena-external-ufa-plugin" || !msg.Externals[0].Building || msg.ExternalError != "nope" {
		t.Fatalf("repoStateMsg dropped the external paths: %s", out)
	}
}

func TestExternalWatchCommand(t *testing.T) {
	cases := []struct {
		typ, payload, want string
		ok                 bool
	}{
		{"lr-watch-external", `{"path":"../x"}`, "__system:watch-external ../x", true},
		{"lr-watch-external", `{"path":" ../my dir "}`, "__system:watch-external ../my dir", true},
		{"lr-watch-external", `{"path":"../x","plugin":true}`, "__system:watch-external-plugin ../x", true},
		{"lr-watch-external", `{"path":"../x","plugin":true,"app_id":"x-plugin"}`, "__system:watch-external-plugin ../x x-plugin", true},
		{"lr-watch-external", `{"path":"../my dir","plugin":true}`, "", false},
		{"lr-watch-external", `{"path":"../x","plugin":true,"app_id":"a b"}`, "", false},
		{"lr-unwatch-external", `{"path":"../x"}`, "__system:unwatch-external ../x", true},
		{"lr-build-external", `{"path":"../x"}`, "__system:build-external ../x", true},
		{"lr-build-external", `{"path":"  "}`, "", false},
		{"lr-watch-external", `{"path":"../x\n__system:restart"}`, "", false},
	}
	for _, c := range cases {
		got, ok := externalWatchCommand(c.typ, json.RawMessage(c.payload))
		if got != c.want || ok != c.ok {
			t.Errorf("externalWatchCommand(%s, %s) = %q, %v; want %q, %v", c.typ, c.payload, got, ok, c.want, c.ok)
		}
	}
}
