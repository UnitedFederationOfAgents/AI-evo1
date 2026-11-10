package slopspace

import (
	"fmt"
	"os"
	"path/filepath"
)

// The agent's DTT primer (dTT-IntegrationImpls Step 1, Revision J). An agent
// handed only the folder names can't tell what a DTT is or how to write one,
// but spelling it all out in every prompt would bloat every turn. So the
// prompt carries a two-sentence note (DTTPromptNote) and Deploy drops the
// detail -- what a DTT is, the layout, and a few examples -- into the
// deployed readspaces as DTTGuideFile, for the agent to read when it needs it.
// Readspaces are thrown away on return, so the guide never piles up in the
// slopspace; each deploy writes the current one.

// DTTGuideFile is the guide's name in the deployed readspaces.
const DTTGuideFile = "DTT-GUIDE.md"

const dttGuideIntro = `# DTT guide

A DTT (declarative-tool-tool) document is a YAML file that says which things
to look at -- GitHub repositories, PRs and issues, files and folders, AWS resources --
and, optionally, what state they should be in. You can't run one yourself:
the human runs it with the ` + "`dtt`" + ` tool and keeps the result.

Running a document in one of three modes always returns an **image**, a JSON
record of what was measured:

- ` + "`read`" + `: only measures.
- ` + "`assert`" + `: makes the stated changes, then measures.
- ` + "`delete`" + `: removes what an assert would make (repositories, files,
  PRs by branch and issues are closed, VPCs).

An assert only changes a field that is empty, that matches a ` + "`from.<field>`" + `
value, or that matches the image the human supplies with it (or with force).

## In this slopspace

- ` + "`readspaces/dtt-canvas/read/`, `readspaces/dtt-canvas/write/`" + `:
  documents the human has supplied or accepted. Read-only.
- ` + "`readspaces/dtt-images/`" + `: the images those documents returned, beside
  them by name: ` + "`dtt-canvas/read/prs.dtt.yaml`" + `'s is
  ` + "`dtt-images/read/prs.image.json`" + `. Every read document gets one
  (refreshed only when the human reruns it); a ` + "`.error.txt`" + ` in its place
  says why the read failed. Write documents only have an image if the human
  ran them and kept it.
`

const dttGuideProposals = `- ` + "`writespaces/dtt-requests/read/`, `writespaces/dtt-requests/write/`" + `:
  where you **propose** a document for the human to run. Put it in ` + "`read/`" + `
  if it only reads, in ` + "`write/`" + ` if it asserts or deletes.

## Proposing a document

1. Write one document per file, named ` + "`<what-it-does>.dtt.yaml`" + `, into
   ` + "`writespaces/dtt-requests/read/`" + ` or ` + "`write/`" + `.
2. Start it with ` + "`#`" + ` comments saying what it does and which mode to run
   it in (` + "`read`, `assert` or `delete`" + `).
3. Finish your turn and say what you proposed. The human then accepts it (it
   moves onto the canvas), rejects it, or asks you to change it. An accepted
   read is run straight away, so its image is in ` + "`readspaces/dtt-images/read/`" + `
   on your next turn; an accepted write is run by the human, if at all (a
   condoc runs each accepted write straight away too, in ` + "`assert`" + ` mode
   unless your comments say ` + "`Mode: delete`" + `, and its image is in
   ` + "`readspaces/dtt-images/write/`" + `).

Don't wait for the result in the same turn; it can't arrive until you finish.
`

const dttGuideNoProposals = `
This deployment has no ` + "`writespaces/dtt-requests`" + `, so you can't propose
documents. If you need one run, describe it in your reply instead.
`

const dttGuideFormat = `
## Writing a document

Each top-level key is a statement. A path starts with the tool: ` + "`github`" + `,
` + "`filesystem`" + ` or ` + "`aws`" + `. ` + "`select`" + ` picks objects, ` + "`alias`" + ` names
one for later statements, ` + "`assert`" + ` gives the wanted state, and
` + "`${{timestamp.date}}`" + `-style references fill in values. Use spaces, not tabs.

### Read a repository and its open PRs

` + "```yaml" + `
# Read the AI-sandboxing repository and its PRs. Mode: read.
github.users.user-1.repos.AI-sandboxing:
  read: *
` + "```" + `

For an organization's repositories the path is ` + "`github.orgs.<org>.repos`" + `.

### Write a file

` + "```yaml" + `
# Create /tmp/my-json.json. Mode: assert.
filesystem.items:
  select:
    abspath: "/tmp/"
  alias: temp-folder

temp-folder:
  assert:
    items:
      my-json.json:
        type: json
        content:
          name: "My Json File."
` + "```" + `

### Open or update a PR

` + "```yaml" + `
# Open a PR from condoc/blah1/main into main, or set its body. Mode: assert.
timestamp:
  select: now

github.users.user-1.repos.AI-sandboxing:
  read: *
  alias: the-repo

the-repo:
  assert:
    prs:
      select:
        from: condoc/blah1/main
        to: main
        title: Merge one branch
      state:
        exists: true
        body: |
          This PR will reintegrate a branch.
          ${{timestamp.human-readable}}
` + "```" + `

### Open an issue

` + "```yaml" + `
# Open an issue titled "Check-in <date>", labelled. Mode: assert.
timestamp:
  select: now

github.users.user-1.repos.AI-sandboxing:
  assert:
    issues:
      select:
        title: Check-in ${{timestamp.date}}
      state:
        body: Opened by dtt.
        labels:
          has: [dtt]
` + "```" + `

An issue is found by its title among the open ones; delete mode closes it.

### Create a repository named by the date

` + "```yaml" + `
# Create scratch-<date>; run in delete mode to remove it. Mode: assert.
timestamp:
  select: now

github.users.user-1.repos:
  assert:
    repos:
      select:
        name: scratch-${{timestamp.date}}
      state:
        private: true
        auto_init: true
        approve_delete: user-1/scratch-${{timestamp.date}}
` + "```" + `

More examples, and every field each tool supports, are in the AI-evo1
repository under ` + "`dtt/examples/compositions/`" + ` and ` + "`dtt/README.md`" + `, if it is checked
out under ` + "`readspaces/repos`" + ` or ` + "`writespaces/repos`" + `.
`

// DTTGuide is the guide's text: the proposing section only when the
// deployment has writespaces/dtt-requests.
func DTTGuide(proposals bool) string {
	if proposals {
		return dttGuideIntro + dttGuideProposals + dttGuideFormat
	}
	return dttGuideIntro + dttGuideNoProposals + dttGuideFormat
}

// writeDTTGuide puts the guide into a deployment's readspaces.
func writeDTTGuide(readSpaces string, proposals bool) error {
	if err := os.WriteFile(filepath.Join(readSpaces, DTTGuideFile), []byte(DTTGuide(proposals)), 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", DTTGuideFile, err)
	}
	return nil
}

// DTTPromptNote is the short note on DTTs appended to a slopspace prompt;
// deployPath is where the slopspace is deployed. It points at the guide
// rather than repeating it.
func DTTPromptNote(deployPath string, proposals bool) string {
	guide := filepath.Join(deployPath, ReadSpacesDir, DTTGuideFile)
	canvas := filepath.Join(deployPath, ReadSpacesDir, DTTCanvasDir)
	if !proposals {
		return fmt.Sprintf("\n\nDTT (declarative-tool-tool) documents the human has supplied, if any, are in '%s' (read/ and write/), "+
			"with the images they returned beside it in dtt-images; treat them as read-only. If you need to understand one, "+
			"read '%s'.", canvas, guide)
	}
	requests := filepath.Join(deployPath, WriteSpacesDir, DTTRequestsDir)
	return fmt.Sprintf("\n\nDTT (declarative-tool-tool) documents are YAML files the human runs to read or change GitHub, files "+
		"or AWS; you can't run them yourself. Ones the human has supplied are in '%s' (read/ and write/), with the images they "+
		"returned in dtt-images; treat them as read-only. If this work calls for a DTT read or change, write the document as a "+
		"proposal into '%s/read' or '%s/write' for the human to review after you finish. Before writing or reading one, "+
		"read '%s': it explains the format and has examples.", canvas, requests, requests, guide)
}

// DTTProposalsEnabled reports whether the slopspace deployed at deployPath
// offers somewhere to propose DTT documents.
func DTTProposalsEnabled(deployPath string) bool {
	fi, err := os.Stat(filepath.Join(deployPath, WriteSpacesDir, DTTRequestsDir))
	return err == nil && fi.IsDir()
}
