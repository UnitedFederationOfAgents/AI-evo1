package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// The demo tab's proposals sub-tab (PrivateRepoOps Step 5, Revision D; made
// agent-only in Revision H): a demo the worker's agent builds from a name,
// a title and a description of the whole demo.
//
// There are no forms to fill in. demo-proposal-agent-create keeps the
// proposal with just those (and PROPOSAL.md, them in words) and hands the
// worker its creation job: in write mode, in place in the proposal's
// directory, the agent writes the full demo there -- always as a DTT
// document at the directory's top level, <name>.demo.dtt.yaml, shaped like
// the built-in demos -- and beside it anything new the demo needs, and
// optionally an icon (icon.svg or icon.png). It is told to change nothing
// outside the directory.
//
// Once the job is done and the demo document is there, the proposal is
// created (syncProposal): the document is copied into the host's library
// under the proposal's name, so it runs from the demos sub-tab, and the
// tab shows the proposal as a tile -- its icon, if it has one -- whose
// details are its description and the files the agent wrote. The demo's
// declared coverage (its focal: and supporting: lines, and what its
// phases call) picks it out on the protosurf map, as a demo does.
//
// Each proposal is kept under the demo root:
//
//	<root>/proposals/<name>/
//	    proposal.json            DemoProposal
//	    PROPOSAL.md              its title and description, the agent's brief
//	    jobs/<id>.json, <id>.md  the worker's creation job (DemoReview, as reviews are)
//	    <name>.demo.dtt.yaml     the demo the agent wrote
//	    icon.svg | icon.png      its icon, if the agent made one
//	    ...                      whatever else the demo needs, as the agent wrote it

const (
	demoProposalsDir       = "proposals"
	proposalFile           = "proposal.json"
	proposalDocFile        = "PROPOSAL.md"
	proposalJobsDir        = "jobs"
	demoProposalFormat     = "lr-demo-proposal-v2"
	maxProposalDescription = 16 << 10
	maxDemoProposals       = 200
	maxProposalFiles       = 200
	maxProposalIcon        = 32 << 10
)

// proposalIcons are the files a proposal's icon may be, and their types.
var proposalIcons = []struct{ file, mime string }{{"icon.svg", "image/svg+xml"}, {"icon.png", "image/png"}}

// DemoProposal is a demo the worker's agent builds, as proposal.json keeps
// it: its name, title and description, the creation job (Job, the
// DemoReview's ID) and the demo put into the host's library from it
// (Demo). Brief is the older name of Description, only read. The fields
// after UpdatedAt aren't kept, but filled in when it's read (syncProposal).
type DemoProposal struct {
	Format      string `json:"format,omitempty"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Brief       string `json:"brief,omitempty"`
	Job         string `json:"job,omitempty"`
	Demo        string `json:"demo,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at,omitempty"`

	Dir string `json:"dir,omitempty"`
	// Status is creating (the job is with the worker), created (its demo
	// document is there) or failed (Error says why).
	Status   string        `json:"status,omitempty"`
	Error    string        `json:"error,omitempty"`
	DemoFile string        `json:"demo_file,omitempty"` // the demo document, relative to Dir
	Files    []string      `json:"files,omitempty"`     // what's in Dir, relative to it, but LR's own files
	Icon     string        `json:"icon,omitempty"`      // a data: URL
	Coverage *DemoCoverage `json:"coverage,omitempty"`  // the demo's declared coverage
}

func proposalsRoot(root string) string { return filepath.Join(root, demoProposalsDir) }

// proposalDir is proposal name's directory under root.
func proposalDir(root, name string) (string, error) {
	name = strings.TrimSpace(name)
	if !demoNameRe.MatchString(name) {
		return "", fmt.Errorf("%q: proposal names are lowercase letters, digits and '-', starting with a letter", name)
	}
	return filepath.Join(proposalsRoot(root), name), nil
}

func proposalJobsPath(dir string) string { return filepath.Join(dir, proposalJobsDir) }

// normalizeProposal trims p and checks its name and description.
func normalizeProposal(p *DemoProposal) error {
	p.Name = strings.TrimSpace(p.Name)
	if !demoNameRe.MatchString(p.Name) {
		return fmt.Errorf("%q: proposal names are lowercase letters, digits and '-', starting with a letter", p.Name)
	}
	p.Title = oneLineText(p.Title, 200)
	if p.Title == "" {
		p.Title = p.Name
	}
	p.Description = strings.TrimSpace(p.Description)
	if len(p.Description) > maxProposalDescription {
		return fmt.Errorf("the description is too long (the limit is %d KiB)", maxProposalDescription>>10)
	}
	return nil
}

// writeProposal keeps p in dir: proposal.json, and PROPOSAL.md from it.
func writeProposal(dir string, p DemoProposal) error {
	p.Format, p.Brief = demoProposalFormat, ""
	p.Dir, p.Status, p.Error, p.DemoFile, p.Files, p.Icon, p.Coverage = "", "", "", "", nil, "", nil
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, proposalFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, proposalFile)); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, proposalDocFile), []byte(proposalDoc(p)), 0o644)
}

// readProposal reads the proposal kept in dir, as kept.
func readProposal(dir string) (DemoProposal, error) {
	var p DemoProposal
	b, err := os.ReadFile(filepath.Join(dir, proposalFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return p, fmt.Errorf("there's no proposal called %s", filepath.Base(dir))
		}
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("proposal %s: %w", filepath.Base(dir), err)
	}
	if p.Description == "" {
		p.Description = p.Brief
	}
	p.Brief = ""
	p.Dir = dir
	return p, nil
}

// proposalDoc is p in words: PROPOSAL.md, the agent's brief.
func proposalDoc(p DemoProposal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Demo proposal: %s\n\n", p.Title)
	fmt.Fprintf(&b, "- name: %s\n", p.Name)
	fmt.Fprintf(&b, "- the demo: %s%s, at the top level of this directory\n", p.Name, demoSuffix)
	b.WriteString("- kept by local-representative's demo tab (proposals); this file is written from proposal.json\n")
	b.WriteString("\n## Description\n\n")
	if p.Description == "" {
		b.WriteString("(none -- go by the title)\n")
	} else {
		b.WriteString(p.Description + "\n")
	}
	return b.String()
}

// proposalDemoFile is the demo document the agent wrote at the top level of
// the proposal in dir: <name>.demo.dtt.yaml, else the first other
// *.demo.dtt.yaml there; "" if there's none yet.
func proposalDemoFile(dir, name string) string {
	if info, err := os.Stat(filepath.Join(dir, name+demoSuffix)); err == nil && info.Mode().IsRegular() {
		return name + demoSuffix
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), demoSuffix) {
			return e.Name()
		}
	}
	return ""
}

// proposalFiles is what's in the proposal's directory dir, relative to it
// and sorted, but for LR's own proposal.json and jobs/: PROPOSAL.md, the
// demo and what the agent wrote beside it.
func proposalFiles(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			if rel == proposalJobsDir || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == proposalFile || rel == proposalFile+".tmp" {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		if len(out) >= maxProposalFiles {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// proposalIcon is the proposal in dir's icon as a data: URL, "" if it has
// none (or one too big to send).
func proposalIcon(dir string) string {
	for _, ic := range proposalIcons {
		info, err := os.Stat(filepath.Join(dir, ic.file))
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxProposalIcon {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, ic.file)); err == nil {
			return "data:" + ic.mime + ";base64," + base64.StdEncoding.EncodeToString(b)
		}
	}
	return ""
}

// syncProposalJobs brings the creation jobs of the proposal in dir up to
// date from the worker's signals.
func syncProposalJobs(dir string, signals []WorkerSignalInfo) []DemoReview {
	jobs := proposalJobsPath(dir)
	return syncWorkerJobs(jobs, dir, demoProposalRole, signals, func(r DemoReview) {
		writeWorkerJob(jobs, r, fmt.Sprintf("Creation %s of proposal %s", r.ID, r.Record))
	})
}

// syncProposal reads the proposal in dir, its creation job synced from the
// worker's signals, with where it stands: creating while the job is with
// the worker; once it's done, created if the agent left a demo document at
// the top level, else failed. The first time it's created, its demo is
// copied into the host's library (installProposalDemo). Callers hold
// reviewMu.
func (w *toolsWatch) syncProposal(dir string, signals []WorkerSignalInfo) (DemoProposal, error) {
	p, err := readProposal(dir)
	if err != nil {
		return p, err
	}
	jobs := syncProposalJobs(dir, signals)
	i := slices.IndexFunc(jobs, func(j DemoReview) bool { return j.ID == p.Job })
	p.DemoFile = proposalDemoFile(dir, p.Name)
	switch {
	case i >= 0 && !jobs[i].finished():
		p.Status = "creating"
	case p.DemoFile != "":
		p.Status = "created"
	default:
		p.Status, p.Error = "failed", fmt.Sprintf("the agent finished without writing %s%s here", p.Name, demoSuffix)
		if p.Job == "" {
			p.Error = "made before the worker's agent built proposals' demos -- delete it and create it again"
		} else if i < 0 {
			p.Error = "its creation job is gone"
		} else if jobs[i].Error != "" {
			p.Error = jobs[i].Error
		} else if out := []rune(strings.TrimSpace(jobs[i].Output)); len(out) > 0 {
			// What the agent said instead: the end of its reply, its summary.
			if len(out) > 300 {
				out = append([]rune("…"), out[len(out)-300:]...)
			}
			p.Error += " -- its reply ended: " + oneLineText(string(out), 320)
		}
	}
	p.Files, p.Icon = proposalFiles(dir), proposalIcon(dir)
	if p.DemoFile == "" {
		return p, nil
	}
	src, err := os.ReadFile(filepath.Join(dir, p.DemoFile))
	if err != nil {
		return p, nil
	}
	d := parseDemo(p.Name, string(src), demoOriginConfigured, filepath.Join(dir, p.DemoFile))
	p.Coverage = d.Coverage
	if d.Error != "" {
		p.Error = fmt.Sprintf("%s: %s", p.DemoFile, d.Error)
	}
	if p.Status == "created" && p.Demo == "" {
		if err := w.installProposalDemo(&p, string(src)); err != nil {
			p.Error = err.Error()
		}
	}
	return p, nil
}

// installProposalDemo copies created proposal p's demo document, src, into
// the host's library as <name>.demo.dtt.yaml -- so a proposal's demo is a
// configured one, never embedded (Revision N) -- and keeps that p's demo is
// there. A demo already called that is left as it is, and p says so.
func (w *toolsWatch) installProposalDemo(p *DemoProposal, src string) error {
	root := w.demoDir()
	if root == "" {
		return errors.New("there's no directory on this host to keep demos in")
	}
	if slices.ContainsFunc(w.demoLibrary(), func(d DemoInfo) bool { return d.Name == p.Name }) {
		return fmt.Errorf("there's a demo called %s in the library already -- %s stays in the proposal's directory", p.Name, p.DemoFile)
	}
	lib := filepath.Join(root, demoLibraryDir)
	if err := os.MkdirAll(lib, 0o755); err != nil {
		return err
	}
	if p.DemoFile != p.Name+demoSuffix {
		src = strings.ReplaceAll(src, p.DemoFile, p.Name+demoSuffix)
	}
	path := filepath.Join(lib, p.Name+demoSuffix)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(src); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	p.Demo, p.UpdatedAt = p.Name, time.Now().UnixMilli()
	return writeProposal(p.Dir, *p)
}

// demoProposals lists the proposals under root, most recently changed
// first, each synced (syncProposal). Callers hold reviewMu.
func (w *toolsWatch) demoProposals(root string) []DemoProposal {
	out := []DemoProposal{}
	if root == "" {
		return out
	}
	entries, err := os.ReadDir(proposalsRoot(root))
	if err != nil {
		return out
	}
	signals := w.currentWorkerSignals()
	for _, e := range entries {
		if !e.IsDir() || !demoNameRe.MatchString(e.Name()) {
			continue
		}
		p, err := w.syncProposal(filepath.Join(proposalsRoot(root), e.Name()), signals)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	at := func(p DemoProposal) int64 { return max(p.UpdatedAt, p.CreatedAt) }
	sort.SliceStable(out, func(i, j int) bool { return at(out[i]) > at(out[j]) })
	if len(out) > maxDemoProposals {
		out = out[:maxDemoProposals]
	}
	return out
}

// proposalCreatePrompt is what the worker's agent is asked to build p
// with. It leads with the one file the job must leave and where (absolute),
// and says plainly that the agent writes the demo's script and doesn't give
// the demo: a description reads as things to do -- "create a new repo",
// "do a couple of AI invocations" -- and an agent that took it that way
// finished without the file (PrivateRepoOps Step 5, Revision J). Then: the
// description, a skeleton of the document, the demos and examples there are
// to build on, and the steps, ending with checking the file is there. The
// skeleton's compose phase is written out, references and clip shapes as
// the built-in demos have them, and a stand-in subject is ruled out: an
// agent left with "title cards and each phase's render" guessed
// ${{ridealong.render}}, and called an unrelated example for want of one
// (Revision P).
func proposalCreatePrompt(p DemoProposal, demos []DemoInfo, project string) string {
	file := p.Name + demoSuffix
	target := filepath.Join(p.Dir, file)
	var b strings.Builder
	fmt.Fprintf(&b, "Your job is to write ONE file: %s\n\n", target)
	fmt.Fprintf(&b, "That file is a demo's script -- a DTT document that local-representative runs later, when someone presses Run demo. "+
		"You are writing the script, not giving the demo. Do not do what the description below describes: do not run dtt, create or delete repositories, "+
		"start apps, open browsers or invoke agents. The description says what the demo will do when it is run; your file is what will make it do that. "+
		"The job counts as failed unless %s exists when you finish, so write it even if you're unsure of parts -- "+
		"if something the description wants isn't possible with what the project has, say what's missing in its leading comment.\n\n", target)
	fmt.Fprintf(&b, "Your working directory, %s, is the only place you may create or change files. Leave proposal.json, PROPOSAL.md and jobs/ there as they are. "+
		"Read anything in the project; change nothing in it.\n\n", p.Dir)

	fmt.Fprintf(&b, "## The demo to write\n\nName: %s\nTitle: %s\n", p.Name, p.Title)
	if p.Description != "" {
		fmt.Fprintf(&b, "Description (what it shows when run; also in PROPOSAL.md):\n\n%s\n", p.Description)
	} else {
		b.WriteString("There's no description beyond the title: write the demo the title suggests.\n")
	}

	b.WriteString("\n## What the file looks like\n\n" +
		"A demo is a DTT composition: phases, each calling one existing dtt example (dtt.calls), in this order -- a general lead-in (localrep-demo-prepare.dtt.yaml), " +
		"a lead-in for the subject area, the subject (the demo's meat, and its primary video), the area's tear-down, the general tear-down (localrep-demo-shutdown.dtt.yaml) -- " +
		"then a compose phase, where composer cuts their renders into one video with a chapter card before each. A phase's role comes from its name: lead-in or <area>-lead-in, " +
		"tear-down or <area>-tear-down, compose, and anything else is the subject. The robot (ianar) drives and records the screens. Its shape, to fill in:\n\n")
	fmt.Fprintf(&b, "```yaml\n# Demo -- %s\n#\n# <what it shows, phase by phase, and what it needs on the node>\n#\n"+
		"#   focal: <project paths it is about; a directory ends in />\n#   supporting: <project paths it needs on the way>\n#\n"+
		"#   dtt assert %s --var node=<node>\n\nvariables:\n  node:\n    default: \"\"\n    help: the node to give the demo on; empty for this node\n\n"+
		"timestamp:\n  select: now\n  alias: the-time\n\ndtt.calls:\n  select:\n    document: localrep-demo-prepare.dtt.yaml\n  alias: prep\n  phase: lead-in\n  assert:\n"+
		"    variables:\n      node: ${{var.node}}\n      at: ${{the-time.unix}}\n\n# ... the area's lead-in, the subject, the area's tear-down, each a dtt.calls like that ...\n\n"+
		"dtt.calls:\n  select:\n    document: localrep-demo-shutdown.dtt.yaml\n  alias: shut\n  phase: tear-down\n  assert:\n    variables:\n      node: ${{var.node}}\n      at: ${{the-time.unix}}\n\n"+
		"localrep.compositions:\n  select:\n    id: demo-%s-${{the-time.unix}}\n  alias: demo-comp\n  phase: compose\n  assert:\n    composition: |\n      {\n"+
		"        \"name\": \"%s on ${{prep.by_alias.prep-seq.node}} -- ${{the-time.human-readable}}\",\n        \"width\": 1280,\n        \"height\": 720,\n        \"video\": [\n"+
		"          {\"kind\": \"title\", \"duration\": 3, \"text\": \"<the demo's title>\", \"color\": \"#ffffff\", \"background\": \"#1a1a1a\"},\n"+
		"          {\"kind\": \"title\", \"duration\": 3, \"text\": \"<a chapter card for the lead-in>\", \"color\": \"#ffffff\", \"background\": \"#333333\"},\n"+
		"          {\"kind\": \"media\", \"file_id\": \"${{prep.by_alias.prep-comp.render.file_id}}\", \"file_name\": \"${{prep.by_alias.prep-comp.render.name}}\"},\n"+
		"          ... a chapter card and a media clip like that for every other phase, in order ...\n"+
		"          {\"kind\": \"title\", \"duration\": 2, \"text\": \"<a closing card>\", \"color\": \"#1a1a1a\", \"background\": \"#e0e0e0\"}\n"+
		"        ],\n        \"audio\": []\n      }\n"+
		"    rendered: true\n    renderer: auto\n    wait: 600\n```\n\n", p.Title, file, p.Name, p.Title)
	b.WriteString("References (${{...}}) are where demos most often go wrong, so don't guess them -- copy them from a built-in demo and check each against the document it calls:\n" +
		"- A dtt.calls phase has no fields of its own. What it holds is the called document's objects, by their aliases there: ${{<phase alias>.by_alias.<alias in the called document>.<field>}}.\n" +
		"- A phase's video is its called document's localrep.compositions render: open that document, find its localrep.compositions alias (prep-comp in localrep-demo-prepare, shut-comp in localrep-demo-shutdown), " +
		"and use ${{<phase alias>.by_alias.<that alias>.render.file_id}} and ${{<phase alias>.by_alias.<that alias>.render.name}}. ${{<phase alias>.render}} does not exist.\n" +
		"- Every clip has a kind: a media clip is {\"kind\": \"media\", \"file_id\": ..., \"file_name\": ...}; a title card is {\"kind\": \"title\", \"duration\", \"text\", \"color\", \"background\"}. The composition needs width and height.\n\n")

	b.WriteString("## What to build on\n\n")
	if project != "" {
		fmt.Fprintf(&b, "The project is at %s. Copy the closest built-in demo and adapt it rather than starting from nothing:\n", project)
		fmt.Fprintf(&b, "- %s: the built-in demos (<name>.demo.dtt.yaml). Read the one nearest yours first.\n", filepath.Join(project, "local-representative", "demos"))
		fmt.Fprintf(&b, "- %s: the documents demos call, and in resources/ there the control sequences (<x>.control.yaml) they run.\n", filepath.Join(project, "dtt", "examples"))
		if ex := demoExamples(filepath.Join(project, "dtt", "examples")); len(ex) > 0 {
			fmt.Fprintf(&b, "  The examples a demo phase can call: %s.\n", strings.Join(ex, ", "))
		}
		b.WriteString("- dtt/README.md there describes every statement a document can make.\n")
	} else {
		b.WriteString("Copy the closest demo below and adapt it rather than starting from nothing; the project's local-representative/demos/ and dtt/examples/compositions/ hold them and the examples they call, where you can reach them.\n")
	}
	if len(demos) > 0 {
		b.WriteString("\nThe demos there are now:\n")
		for _, d := range demos {
			var phases []string
			for _, ph := range d.Phases {
				s := ph.Name
				if len(ph.Calls) > 0 {
					s += " (" + strings.Join(ph.Calls, ", ") + ")"
				}
				phases = append(phases, s)
			}
			fmt.Fprintf(&b, "- %s: %s -- phases %s\n", d.Name, d.Title, strings.Join(phases, ", "))
		}
	}

	b.WriteString("\n## Steps\n\n")
	b.WriteString("1. Read the nearest built-in demo, and the examples it calls, to see how the pieces fit.\n")
	fmt.Fprintf(&b, "2. Write %s: the whole demo, as one DTT document shaped as above. local-representative copies it into the host's demo library once you're done.\n", target)
	fmt.Fprintf(&b, "3. The subject phase must show what the description is about. Never call an unrelated example as a stand-in -- a demo that runs but shows something else is a failed demo. "+
		"If no example does what the subject needs, write one, with its control sequence, under %s (examples/, resources/), copied from the nearest example and named as the demo calls it, "+
		"and say in the demo's leading comment that it is new and needs putting beside dtt's own before the demo will run.\n", p.Dir)
	b.WriteString("4. Check every ${{...}} in the demo against the document it points into, as described under References.\n")
	fmt.Fprintf(&b, "5. Optionally, write an icon beside it: %s (a small square SVG, no scripts or external references) or icon.png, under %d KiB.\n", filepath.Join(p.Dir, "icon.svg"), maxProposalIcon>>10)
	fmt.Fprintf(&b, "6. Check %s exists (list the directory). If it doesn't, write it now.\n", target)
	b.WriteString("7. Reply with a short summary: what the demo shows when run, and each file you wrote.\n")
	return b.String()
}

// demoExamples are the dtt examples in dir a demo phase can call: the
// localrep-demo-* documents, by file name.
func demoExamples(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if n := e.Name(); e.Type().IsRegular() && strings.HasPrefix(n, "localrep-demo-") && strings.HasSuffix(n, ".dtt.yaml") {
			out = append(out, n)
		}
	}
	return out
}

// checkProposalCreate is why proposal p couldn't be handed to the worker,
// found before anything is kept.
func (w *toolsWatch) checkProposalCreate(root string, p DemoProposal) error {
	if w.proposalSubmit == nil {
		return errors.New("there's no worker here to create demos")
	}
	if p.Title == p.Name && p.Description == "" {
		return errors.New("give the agent something to go on: a title or a description")
	}
	dir, err := proposalDir(root, p.Name)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("proposal %s isn't at an absolute path (%s)", p.Name, dir)
	}
	if slices.ContainsFunc(w.demoLibrary(), func(d DemoInfo) bool { return d.Name == p.Name }) {
		return fmt.Errorf("there's a demo called %s already -- the new demo takes the proposal's name", p.Name)
	}
	return nil
}

// agentCreateProposal keeps req.Proposal as a new proposal and hands the
// worker its creation job, in write mode in the proposal's directory. If
// the job can't be handed over, nothing is kept.
func (w *toolsWatch) agentCreateProposal(root string, req ToolsRequest) (DemoProposal, error) {
	if req.Proposal == nil {
		return DemoProposal{}, errors.New("there's no proposal to create")
	}
	p := DemoProposal{Name: req.Proposal.Name, Title: req.Proposal.Title, Description: req.Proposal.Description}
	if err := normalizeProposal(&p); err != nil {
		return p, err
	}
	if err := w.checkProposalCreate(root, p); err != nil {
		return p, err
	}
	dir, _ := proposalDir(root, p.Name)
	if err := os.MkdirAll(proposalsRoot(root), 0o755); err != nil {
		return p, err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return p, fmt.Errorf("there's a proposal called %s already", p.Name)
		}
		return p, err
	}
	project := ""
	if w.protosurfRoot != nil {
		if r, err := w.protosurfRoot(); err == nil {
			project = r
		}
	}
	now := time.Now()
	p.Dir, p.CreatedAt, p.UpdatedAt = dir, now.UnixMilli(), now.UnixMilli()
	j := DemoReview{ID: strings.ReplaceAll("create-"+now.Format("20060102-150405.000"), ".", "-"), Record: p.Name,
		Agent: strings.TrimSpace(req.Agent), Model: strings.TrimSpace(req.Model), Status: "submitted", RequestedAt: now.UnixMilli()}
	j.Prompt = proposalCreatePrompt(p, w.demoLibrary(), project)
	p.Job = j.ID
	err := writeProposal(dir, p)
	if err == nil {
		err = w.proposalSubmit(j.Prompt, dir, j.Agent, j.Model)
	}
	if err == nil {
		err = writeWorkerJob(proposalJobsPath(dir), j, fmt.Sprintf("Creation %s of proposal %s", j.ID, p.Name))
	}
	if err != nil {
		os.RemoveAll(dir) // don't leave its name taken
		return p, err
	}
	return p, nil
}

// deleteProposal removes proposal name's directory, everything the agent
// wrote there with it; a demo copied into the library stays.
func deleteProposal(root, name string) error {
	dir, err := proposalDir(root, name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, proposalFile)); err != nil {
		return fmt.Errorf("there's no proposal called %s", name)
	}
	return os.RemoveAll(dir)
}

// handleProposal answers the proposals sub-tab's ops (see ToolsRequest):
// each but demo-proposal-delete answers the proposal in full.
func (w *toolsWatch) handleProposal(req ToolsRequest, fromAC bool, root string, reply ToolsReply) ToolsReply {
	fail := func(err error) ToolsReply {
		reply.Error = err.Error()
		return reply
	}
	if root == "" {
		return fail(errors.New("there's no directory on this host to keep demo proposals in"))
	}
	w.reviewMu.Lock()
	defer w.reviewMu.Unlock()
	name := strings.TrimSpace(req.Name)
	switch req.Op {
	case "demo-proposal-agent-create":
		p, err := w.agentCreateProposal(root, req)
		if err != nil {
			return fail(err)
		}
		name = p.Name
		reply.Detail = fmt.Sprintf("asked the worker to build demo %s in %s -- its tile is ready once the agent has written %s%s there", p.Name, p.Dir, p.Name, demoSuffix)
	case "demo-proposal-delete":
		if err := deleteProposal(root, name); err != nil {
			return fail(err)
		}
		reply.OK, reply.Detail = true, fmt.Sprintf("deleted proposal %s", name)
		return reply
	}
	dir, err := proposalDir(root, name)
	if err != nil {
		return fail(err)
	}
	p, err := w.syncProposal(dir, w.currentWorkerSignals())
	if err != nil {
		return fail(err)
	}
	reply.OK, reply.DemoProposal = true, &p
	if reply.Detail == "" {
		reply.Detail = fmt.Sprintf("proposal %s: %s", p.Name, p.Status)
	}
	if fromAC {
		fitProposalReply(&reply)
	}
	return reply
}

// fitProposalReply cuts a proposal reply to fit the relay to
// agent-coordinator: its icon, then its files, then its description.
func fitProposalReply(reply *ToolsReply) {
	fits := func() bool {
		b, err := json.Marshal(reply)
		return err == nil && len(b) <= maxRelayedTools
	}
	p := reply.DemoProposal
	if fits() || p == nil {
		return
	}
	p.Icon = ""
	for len(p.Files) > 20 && !fits() {
		p.Files = p.Files[:len(p.Files)/2]
	}
	if !fits() {
		p.Description = oneLineText(p.Description, 2000)
	}
	reply.Detail += " (cut to fit the relay -- the proposal's directory on that host has all of it)"
}
