package main

import (
	"encoding/json"
	"testing"
)

// TestRepoStateCondocLockedRoundTrips guards against a Revision K-shaped bug
// found while investigating Step5Prompt.md Revision O/P: AC's own
// RepoStateMsg (used to decode LR's "repo-state" payload, see the
// "repo-state" case in the representable message switch in main.go) had
// fallen out of sync with local-representative/repowatch.go's RepoStateMsg
// and was missing the CondocLocked field entirely, so json.Unmarshal
// silently dropped it, and repoStateMsg() never forwarded it into the
// "lr-repo-state" broadcast either -- the repo-watch panel's "condoc" label
// (see docs/DevMode.md) could never surface through agent-coordinator,
// unlike local-representative's own dashboard. See
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision O/P.
func TestRepoStateCondocLockedRoundTrips(t *testing.T) {
	// Mirrors what local-representative's RepoStateMsg actually serializes to.
	raw := []byte(`{"watched":true,"dirty":false,"condoc_locked":true}`)

	var rs RepoStateMsg
	if err := json.Unmarshal(raw, &rs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !rs.CondocLocked {
		t.Fatal("expected CondocLocked=true after decoding a payload with condoc_locked:true")
	}

	msg := repoStateMsg("host-1", &rs)
	if !msg.CondocLocked {
		t.Fatal("repoStateMsg dropped CondocLocked when building the lr-repo-state broadcast")
	}

	out, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(out, &roundTripped); err != nil {
		t.Fatalf("unmarshal round-tripped output: %v", err)
	}
	if v, ok := roundTripped["condoc_locked"]; !ok || v != true {
		t.Fatalf("re-encoded LRRepoStateMsg missing/false condoc_locked: %v", roundTripped)
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
