package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	dkslop "dungeon-keeper/pkg/slopspace"
	dktypes "dungeon-keeper/pkg/types"
)

// ---- Agent-created resources (Revision B of
// condocs/slopspaceSlopspaceCondocsImpls/Step1Prompt.md) ----
//
// A slopspace turn's agent can hand back files to be linked into the condoc
// as a resource. It writes them into agentResourcesDir in the deployed
// slopspace's writespaces -- outside every repo, so nothing commits them
// there -- and never touches the condoc files itself. Once the slopspace is
// returned and the reply written, collectAgentResources moves each file into
// the condoc's Impls folder, where the human-driven Add Resources sources put
// theirs, and a "## Resource N (agent)" block linking them goes in just after
// the reply. The marker is what the UI uses to set agent resources apart.

// agentResourcesDir is the staging folder's name inside a slopspace's
// writespaces dir.
const agentResourcesDir = "condoc-resources"

// agentResourceMarker follows the number in an agent resource's heading:
// "## Resource N (agent)" or "## Resource N (agent) -- <name>".
const agentResourceMarker = " (agent)"

// slopAgentResourcesDir is the staging folder wherever the slopspace's
// writespaces are right now: the deploy location while deployed, else its
// root under the slopspaces dir.
func slopAgentResourcesDir(meta *dktypes.SlopspaceMetadata) string {
	base := meta.RootPath
	if meta.Deployed {
		base = meta.DeployPath
	}
	return filepath.Join(base, dkslop.WriteSpacesDir, agentResourcesDir)
}

// resetAgentResources empties the staging folder at the start of a turn, so
// only files this turn's agent wrote get linked.
func resetAgentResources(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0755)
}

// collectAgentResources moves every regular file in srcDir into destDir under
// a collision-free "<8-hex>_<name>" filename, as an Upload does, unzipping
// zips. Hidden files, directories, symlinks and files over
// maxResourceUploadSize are left behind; skipped lists each with why. A file
// that fails to copy is skipped the same way, so one bad file doesn't lose
// the rest.
func collectAgentResources(srcDir, destDir string) (links []resourceLink, skipped []string, err error) {
	entries, err := os.ReadDir(srcDir)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !e.Type().IsRegular() {
			skipped = append(skipped, name+" (not a regular file)")
			continue
		}
		info, err := e.Info()
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", name, err))
			continue
		}
		if info.Size() > maxResourceUploadSize {
			skipped = append(skipped, fmt.Sprintf("%s (over %s)", name, formatBytes(maxResourceUploadSize)))
			continue
		}
		link, err := moveAgentResource(filepath.Join(srcDir, name), destDir)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", name, err))
			continue
		}
		links = append(links, link)
	}
	return links, skipped, nil
}

// moveAgentResource copies one staged file into destDir, unzips it if it's a
// zip, and removes the staged copy.
func moveAgentResource(src, destDir string) (resourceLink, error) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return resourceLink{}, err
	}
	f, err := os.Open(src)
	if err != nil {
		return resourceLink{}, err
	}
	link, err := saveResourceFrom(filepath.Base(src), f, destDir)
	f.Close()
	if err != nil {
		return resourceLink{}, err
	}
	note, err := extractZipResource(destDir, link.Filename)
	if err != nil {
		note = "couldn't unzip: " + err.Error()
	}
	link.Note = note
	os.Remove(src)
	return link, nil
}

// addAgentResources links whatever the turn's agent staged in srcDir into
// the step/substep file at path, as a "## Resource N (agent)" block above its
// pending placeholder, after copying the files into the condoc's Impls folder
// (destDir). replyLabel ("Reply", "Reply B") names the reply the files came
// with. Nothing staged means no block.
func addAgentResources(path, srcDir, destDir, replyLabel string) error {
	links, skipped, err := collectAgentResources(srcDir, destDir)
	if err != nil {
		return err
	}
	if len(links) == 0 && len(skipped) == 0 {
		return nil
	}
	desc := fmt.Sprintf("Created by the agent with %s.", replyLabel)
	if len(skipped) > 0 {
		desc += "\n\nNot linked: " + strings.Join(skipped, ", ") + "."
	}
	return insertResourceBlockAs(path, "", desc, links, true)
}
