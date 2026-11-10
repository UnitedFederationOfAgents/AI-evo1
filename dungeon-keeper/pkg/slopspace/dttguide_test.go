package slopspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dungeon-keeper/pkg/types"
)

func TestDeployWritesDTTGuide(t *testing.T) {
	for _, proposals := range []bool{true, false} {
		mgr, cfg := newTestManager(t)
		m, _ := mgr.Create()
		if err := mgr.SetDTTRequests(m.ID, proposals); err != nil {
			t.Fatal(err)
		}
		if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
			t.Fatal(err)
		}
		deployPath := cfg.DeployPathForAgentType(types.AgentTypeWorker)
		guide := readFile(t, filepath.Join(deployPath, ReadSpacesDir, DTTGuideFile))
		if got := strings.Contains(guide, "## Proposing a document"); got != proposals {
			t.Errorf("proposals=%v: guide has the proposing section = %v", proposals, got)
		}
		if got := DTTProposalsEnabled(deployPath); got != proposals {
			t.Errorf("proposals=%v: DTTProposalsEnabled = %v", proposals, got)
		}
		note := DTTPromptNote(deployPath, proposals)
		if !strings.Contains(note, DTTGuideFile) {
			t.Errorf("proposals=%v: prompt note doesn't point at the guide: %q", proposals, note)
		}
		if got := strings.Contains(note, DTTRequestsDir); got != proposals {
			t.Errorf("proposals=%v: prompt note offers dtt-requests = %v", proposals, got)
		}

		// The guide goes with the readspaces on return.
		if err := mgr.Return(m.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(m.RootPath, ReadSpacesDir, DTTGuideFile)); !os.IsNotExist(err) {
			t.Errorf("proposals=%v: guide kept after return (stat: %v)", proposals, err)
		}
	}
}
