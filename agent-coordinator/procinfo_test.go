package main

import (
	"encoding/json"
	"testing"
)

// TestProcInfoAutoUpdateRoundTrips guards against the Revision K bug: AC's
// own ProcInfo (used to decode LR's "system-state" payload, see the
// "system-state" case in the representable message switch in main.go) had
// fallen out of sync with local-representative/procman.go's ProcInfo and was
// missing the AutoUpdate field entirely, so json.Unmarshal silently dropped
// it -- the "auto-update" checkbox could be toggled, but the resulting
// lr-system-state broadcast to the browser always reported it back off. See
// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision K.
func TestProcInfoAutoUpdateRoundTrips(t *testing.T) {
	// Mirrors what local-representative's ProcInfo actually serializes to.
	raw := []byte(`{"name":"lr","loader_managed":true,"auto_update":true}`)

	var proc ProcInfo
	if err := json.Unmarshal(raw, &proc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proc.AutoUpdate {
		t.Fatal("expected AutoUpdate=true after decoding a payload with auto_update:true")
	}

	out, err := json.Marshal(proc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(out, &roundTripped); err != nil {
		t.Fatalf("unmarshal round-tripped output: %v", err)
	}
	if v, ok := roundTripped["auto_update"]; !ok || v != true {
		t.Fatalf("re-encoded ProcInfo missing/false auto_update: %v", roundTripped)
	}
}
