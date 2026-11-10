package main

// DTT proposals (dTT-IntegrationImpls Step 1, Revision I; reworked in
// privateRepoOpsImpls Step 4, Revision A).
//
// An interaction's agent may leave DTT tool calls in its slopspace's
// writespaces/dtt-requests; dungeon-keeper records them on the deployment
// when the slopspace is returned and refuses to deploy it again until the
// human decides (dungeon-keeper/pkg/slopspace/proposals.go). In the condoc:
//
//	response N (condoc)   the proposal commit: the reply, "## Proposals N",
//	                      and the documents copied to dtt/N/ beside the step
//	                      file ("response-dtt N")
//	unlock N (pointer)    also sets the hold (phase reviewing): no prompt and
//	                      no Complete until every proposal is answered
//
// Then each proposal, as the human accepts or rejects it, is a sub-iteration
// N.k of its own (see ToolCall):
//
//	lock N.k (pointer)
//	  -> proposal (condoc)  "## DTT N.k": "dtt-read|dtt-assert|dtt-delete N.k",
//	                        or "dtt-reject N.k"
//	  -> accepted: dtt runs the document as soon as that commit is pushed
//	  -> image (condoc)     "## Result N.k" and the image beside the document:
//	                        "dtt-image N.k", or "dtt-failed N.k"
//	  -> unlock N.k (pointer), which the last one makes the hold's release
//
// A write proposal (assert or delete) can also be read before it is decided
// (privateRepoOpsImpls Step 4, Revision B): a sub-iteration of its own like
// the rest -- lock, "dtt-preread N.k", dtt read once that is pushed
// (dungeon-keeper's PreReadProposal, on the document still in dtt-requests),
// the image commit, unlock -- after which the proposal still awaits accept
// or reject.
//
// Asking for changes instead covers every proposal still unanswered: the
// adjustment request is the next interaction, N+1, auto-submitted, and its
// lock commit clears the hold.
//
// The decisions can also be made on local-representative's slopspace tab;
// the tick notices dungeon-keeper has them and runs the same sub-iterations
// (a read accepted there has already run once, as dungeon-keeper does on its
// own; condocv2 runs it again after its proposal commit).

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"dungeon-keeper/pkg/gitauth"
	dkslop "dungeon-keeper/pkg/slopspace"
	dktypes "dungeon-keeper/pkg/types"
)

// proposalsDir is where interaction n's proposals and their images are
// copied in the condoc repo: dtt/<n>/ beside the step file.
func proposalsDir(meta *dktypes.SlopspaceMetadata, spec *PointerSpec, n int) string {
	return filepath.Join(filepath.Dir(slopStepFile(meta, spec)), "dtt", strconv.Itoa(n))
}

// proposalsLink is a document under proposalsDir as the step file links it.
func proposalsLink(n int, rel string) string {
	return path.Join("dtt", strconv.Itoa(n), rel)
}

// copyFileTo copies src to dst, making dst's directory.
func copyFileTo(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0644)
}

// writeProposals is the proposal part of the response commit: interaction
// n's DTT proposals copied from the returned slopspace's dtt-requests into
// the condoc, and the "## Proposals N" section listing them. It reports
// whether there were any.
func writeProposals(meta *dktypes.SlopspaceMetadata, spec *PointerSpec, n int) (bool, error) {
	d := meta.PendingReview()
	if d == nil {
		return false, nil
	}
	requests := filepath.Join(meta.RootPath, dkslop.WriteSpacesDir, dkslop.DTTRequestsDir)
	dir := proposalsDir(meta, spec, n)
	var b strings.Builder
	fmt.Fprintf(&b, "The agent proposed %d DTT tool call(s). Each one is accepted or rejected in a sub-iteration of its own, "+
		"which runs an accepted one, before the next prompt:\n\n", len(d.Pending()))
	for _, p := range d.Pending() {
		if err := copyFileTo(filepath.Join(requests, filepath.FromSlash(p)), filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			return false, fmt.Errorf("copying the proposal %s into the condoc: %v", p, err)
		}
		fmt.Fprintf(&b, "- [`%s`](%s)\n", p, proposalsLink(n, p))
	}
	return true, appendSection(slopStepFile(meta, spec), "Proposals", n, b.String())
}

// holdForProposals is the tick's fallback for a condoc unlocked with
// proposals awaiting review but no hold -- one unlocked before the unlock
// set it. The hold is only written locally: it changes nothing visible, and
// the first sub-iteration's lock commits it. The caller holds the claim.
func (s *Server) holdForProposals(spec *PointerSpec) error {
	if spec.Hold != nil || spec.Lock != nil || spec.Phase != PhaseAwaitingPrompt || spec.SlopspaceID == "" {
		return nil
	}
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return err
	}
	d := meta.PendingReview()
	if d == nil {
		return nil
	}
	n := spec.Interactions
	// A proposal that already had its sub-iteration is dungeon-keeper failing
	// to record the decision (as it did before its Revision R); never hold
	// for it again.
	content, _ := readFileString(slopStepFile(meta, spec))
	var left []string
	for _, p := range d.Pending() {
		if !proposalTaken(content, n, p) {
			left = append(left, p)
		}
	}
	if len(left) == 0 {
		log.Printf("condocv2 %s: slopspace %s still reports interaction %d's DTT proposals pending after their sub-iterations; not holding again",
			spec.Stem(), spec.SlopspaceID, n)
		return nil
	}
	spec.Hold = &ProposalHold{Interaction: n, Proposals: left}
	spec.Phase = PhaseReviewing
	return s.writeSpec(spec, "")
}

// proposalTaken reports whether the step file has a sub-iteration of
// interaction n for proposal p.
func proposalTaken(content string, n int, p string) bool {
	for _, it := range parseInteractions(content) {
		if it.N != n {
			continue
		}
		for _, t := range it.Tools {
			if !strings.HasPrefix(t.DTT, preReadMark) && strings.Contains(t.DTT, "`"+p+"`") {
				return true
			}
		}
	}
	return false
}

func validDecision(decision string) bool {
	switch dktypes.ProposalDecision(decision) {
	case dktypes.DecisionAccept, dktypes.DecisionReject, dktypes.DecisionModify:
		return true
	}
	return false
}

// slopReview is the human's decision on a held condoc's proposals: accept or
// reject proposal (every one still unanswered when it's empty), or modify
// them all with note as the adjustment request. An accept or reject is only
// recorded with dungeon-keeper here; the tick then runs the sub-iterations,
// one at a time (advanceHold). read (decisionPreRead) isn't a decision: it
// starts a sub-iteration reading the write proposal before it is decided.
func (s *Server) slopReview(stem, decision, note, proposal string) error {
	decision = strings.TrimSpace(decision)
	note = strings.TrimSpace(note)
	proposal = strings.TrimSpace(proposal)
	if decision == decisionPreRead {
		return s.preRead(stem, proposal)
	}
	if !validDecision(decision) {
		return fmt.Errorf("unknown decision %q (accept, reject, modify or read)", decision)
	}
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	h := spec.Hold
	if h == nil {
		return fmt.Errorf("%s has no DTT proposals awaiting review", spec.Name)
	}
	pending := h.pending()
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return err
	}
	var undecided []string
	if d := meta.PendingReview(); d != nil {
		undecided = d.Pending()
	}

	if decision == string(dktypes.DecisionModify) {
		if note == "" {
			return fmt.Errorf("say what to change: an adjustment request needs a prompt")
		}
		if h.Call != nil || len(undecided) != len(pending) {
			return fmt.Errorf("wait for the accepted and rejected proposals' sub-iterations to finish before asking for changes to the rest")
		}
		if _, err := s.slop.ss.Review(spec.SlopspaceID, dktypes.DecisionModify, note); err != nil {
			return err
		}
		return s.modifyHeld(&spec, note)
	}

	only := undecided
	if proposal != "" {
		ok := false
		for _, p := range undecided {
			ok = ok || p == proposal
		}
		if !ok {
			return fmt.Errorf("%q isn't a DTT proposal awaiting your decision", proposal)
		}
		only = []string{proposal}
	}
	if len(only) == 0 {
		return fmt.Errorf("every proposal is decided already; their sub-iterations are running")
	}
	_, err = s.slop.ss.DecideProposals(spec.SlopspaceID, dktypes.ProposalDecision(decision), only)
	return err
}

// preRead starts a sub-iteration reading the write proposal before it is
// decided: lock N.k is committed here, and the tick runs the rest
// (advanceHold).
func (s *Server) preRead(stem, proposal string) error {
	if proposal == "" {
		return fmt.Errorf("say which proposal to read")
	}
	if dkslop.DTTDocMode(proposal, nil) == "read" {
		return fmt.Errorf("%s is a read: accept it, which reads it", proposal)
	}
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	h := spec.Hold
	if h == nil {
		return fmt.Errorf("%s has no DTT proposals awaiting review", spec.Name)
	}
	if h.Call != nil {
		return fmt.Errorf("wait for sub-iteration %s to finish", sectionLabel(h.Interaction, h.Call.Sub))
	}
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return err
	}
	waiting := false
	if d := meta.PendingReview(); d != nil {
		for _, p := range d.Pending() {
			waiting = waiting || p == proposal
		}
	}
	if !waiting {
		return fmt.Errorf("%q isn't a DTT proposal awaiting your decision", proposal)
	}
	h.Call = &ToolCall{Sub: h.nextSub(), Proposal: proposal, Decision: decisionPreRead, Mode: "read", Stage: toolLocked}
	return s.writeSpec(&spec, subCommitMsg(&spec, "lock", h.Interaction, h.Call.Sub))
}

// modifyHeld ends the hold with an adjustment request for the proposals
// still unanswered: it is the next interaction, sent straight to the agent,
// whose lock commit clears the hold. The caller holds the claim.
func (s *Server) modifyHeld(spec *PointerSpec, note string) error {
	proposals := spec.Hold.pending()
	// Whatever was pushed from outside since comes in first (external.go).
	if err := s.refreshBeforeLock(spec); err != nil {
		return err
	}
	spec.Hold = nil
	spec.Phase = PhaseAwaitingPrompt
	return s.startInteraction(spec, dkslop.ModifyPrompt(proposals, note), true)
}

// nextDecided is the first proposal no sub-iteration has taken that
// dungeon-keeper has an answer for, and the answer: its own, else the
// whole set's.
func nextDecided(h *ProposalHold, d *dktypes.Deployment) (string, string) {
	if d == nil {
		return "", ""
	}
	for _, p := range h.pending() {
		for _, v := range d.Decided {
			if v.Proposal == p {
				return p, string(v.Decision)
			}
		}
		if d.Decision == dktypes.DecisionAccept || d.Decision == dktypes.DecisionReject {
			return p, string(d.Decision)
		}
	}
	return "", ""
}

// docMode is the mode an accepted proposal runs in, read from its copy in
// the condoc, else from the canvas.
func docMode(meta *dktypes.SlopspaceMetadata, spec *PointerSpec, n int, p string) string {
	content, err := os.ReadFile(filepath.Join(proposalsDir(meta, spec, n), filepath.FromSlash(p)))
	if err != nil {
		content, _ = os.ReadFile(filepath.Join(meta.RootPath, dkslop.ReadSpacesDir, dkslop.DTTCanvasDir, filepath.FromSlash(p)))
	}
	return dkslop.DTTDocMode(p, content)
}

// advanceHold runs a held condoc's sub-iterations, one per decided proposal,
// stage by stage, saving after each so an error or restart resumes it. With
// nothing decided it waits; a modify decided on the slopspace tab starts the
// next interaction. The caller holds the claim.
func (s *Server) advanceHold(stem string) error {
	spec, err := s.readHeldSpec(stem)
	if err != nil || spec.Error != "" || spec.Hold == nil || isFinalPhase(spec.Phase) {
		return nil
	}
	for {
		h := spec.Hold
		if h == nil {
			return nil
		}
		n := h.Interaction
		meta, err := s.slop.ss.Get(spec.SlopspaceID)
		if err != nil {
			return err
		}
		if meta.Deployed {
			return fmt.Errorf("slopspace %s is deployed while %s is held for its DTT proposals", spec.SlopspaceID, spec.Name)
		}
		var d *dktypes.Deployment
		if len(meta.Deployments) > 0 {
			d = &meta.Deployments[len(meta.Deployments)-1]
		}

		c := h.Call
		if c == nil {
			p, decision := nextDecided(h, d)
			if p == "" {
				if d != nil && d.Decision == dktypes.DecisionModify && h.pending() != nil {
					return s.modifyHeld(&spec, d.Note) // asked on the slopspace tab
				}
				return nil // waiting for the human
			}
			c = &ToolCall{Sub: h.nextSub(), Proposal: p, Decision: decision, Stage: toolLocked}
			if decision == string(dktypes.DecisionAccept) {
				c.Mode = docMode(meta, &spec, n, p)
			}
			h.Call = c
			if err := s.writeSpec(&spec, subCommitMsg(&spec, "lock", n, c.Sub)); err != nil {
				return err
			}
			continue
		}

		switch c.Stage {
		case toolLocked:
			// The proposal commit: once it is pushed the document runs.
			dir := proposalsDir(meta, &spec, n)
			doc := filepath.Join(dir, filepath.FromSlash(c.Proposal))
			if !fileExists(doc) {
				// Proposed before the response commit copied them: still in
				// dtt-requests, or on the canvas once accepted.
				if copyFileTo(filepath.Join(meta.RootPath, dkslop.WriteSpacesDir, dkslop.DTTRequestsDir, filepath.FromSlash(c.Proposal)), doc) != nil {
					copyFileTo(filepath.Join(meta.RootPath, dkslop.ReadSpacesDir, dkslop.DTTCanvasDir, filepath.FromSlash(c.Proposal)), doc)
				}
			}
			if err := appendSubSection(slopStepFile(meta, &spec), "DTT", n, c.Sub, toolCallBody(c, n, fileExists(doc))); err != nil {
				return err
			}
			kind := "dtt-reject"
			switch c.Decision {
			case string(dktypes.DecisionAccept):
				kind = "dtt-" + c.Mode
			case decisionPreRead:
				kind = "dtt-preread"
			}
			if err := s.commitCondocRepo(&spec, subCommitMsg(&spec, kind, n, c.Sub)); err != nil {
				return err
			}
			c.Stage = toolProposed

		case toolProposed:
			if !c.runs() {
				if err := s.unlockToolCall(&spec); err != nil {
					return err
				}
				continue
			}
			var image string
			if c.Decision == decisionPreRead {
				image, _, err = s.slop.ss.PreReadProposal(spec.SlopspaceID, c.Proposal)
			} else {
				_, image, _, err = s.slop.ss.RunCanvasDoc(spec.SlopspaceID, c.Proposal)
			}
			c.Failed = ""
			if err != nil {
				c.Failed = err.Error()
			}
			c.Image = ""
			if image != "" && fileExists(image) {
				c.Image = image
			}
			c.Stage = toolRan

		case toolRan:
			// The image commit.
			link := ""
			if c.Image != "" {
				rel := path.Join(path.Dir(c.Proposal), filepath.Base(c.Image))
				if err := copyFileTo(c.Image, filepath.Join(proposalsDir(meta, &spec, n), filepath.FromSlash(rel))); err != nil {
					return fmt.Errorf("copying the image into the condoc: %v", err)
				}
				link = proposalsLink(n, rel)
			}
			if err := appendSubSection(slopStepFile(meta, &spec), "Result", n, c.Sub, toolResultBody(c, link)); err != nil {
				return err
			}
			kind := "dtt-image"
			if c.Failed != "" {
				kind = "dtt-failed"
			}
			if err := s.commitCondocRepo(&spec, subCommitMsg(&spec, kind, n, c.Sub)); err != nil {
				return err
			}
			c.Stage = toolImaged

		case toolImaged:
			if err := s.unlockToolCall(&spec); err != nil {
				return err
			}
			continue

		default:
			return fmt.Errorf("unknown tool call stage %q", c.Stage)
		}
		if err := s.writeSpec(&spec, ""); err != nil {
			return err
		}
	}
}

// unlockToolCall is unlock N.k, which ends the sub-iteration; after the last
// proposal's it is also the hold's release. A pre-read leaves its proposal
// awaiting a decision.
func (s *Server) unlockToolCall(spec *PointerSpec) error {
	h := spec.Hold
	n, k := h.Interaction, h.Call.Sub
	if h.Call.Decision == decisionPreRead {
		h.PreReads = append(h.PreReads, h.Call.Proposal)
	} else {
		h.Done = append(h.Done, h.Call.Proposal)
	}
	h.Call = nil
	if len(h.pending()) == 0 {
		spec.Hold = nil
		spec.Phase = PhaseAwaitingPrompt
	}
	return s.writeSpec(spec, subCommitMsg(spec, "unlock", n, k))
}

// preReadMark opens a pre-read's "## DTT N.k" -- which isn't an answer to its
// proposal (see proposalTaken).
const preReadMark = "**Read before deciding**"

// toolCallBody is "## DTT N.k": the decision on one proposal, or its
// pre-read.
func toolCallBody(c *ToolCall, n int, linked bool) string {
	doc := "`" + c.Proposal + "`"
	if linked {
		doc = fmt.Sprintf("[`%s`](%s)", c.Proposal, proposalsLink(n, c.Proposal))
	}
	if c.Decision == decisionPreRead {
		return fmt.Sprintf("%s %s: `dtt read` runs it once this commit is pushed, to see what it would change; it still awaits accept or reject.\n", preReadMark, doc)
	}
	if c.Decision != string(dktypes.DecisionAccept) {
		return fmt.Sprintf("**Rejected** %s: set aside, never run.\n", doc)
	}
	return fmt.Sprintf("**Accepted** %s onto the slopspace's DTT canvas; `dtt %s` runs it once this commit is pushed.\n", doc, c.Mode)
}

// dttFilePrefix serves the condoc's copies of its DTT proposals and their
// images (privateRepoOpsImpls Step 4, Revision B): GET
// /api/dtt/<stem>/<N>/<rel>, rel as under dtt/<N>/ beside the step file --
// a proposal (write/repo.dtt.yaml) or an image (write/repo.image.json) --
// for the UI to show. A merged condoc's slopspace is gone, so it redirects
// to the file in the merged condoc repo instead.
const dttFilePrefix = "/api/dtt/"

func (s *Server) handleDTTFile(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, dttFilePrefix), "/", 3)
	if len(parts) != 3 || parts[2] == "" {
		http.Error(w, "want "+dttFilePrefix+"<stem>/<interaction>/<file>", http.StatusBadRequest)
		return
	}
	stem, rel := parts[0], path.Clean(parts[2])
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 1 || rel == "." || path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		http.Error(w, "bad DTT file", http.StatusBadRequest)
		return
	}
	spec, err := s.readSpec(stem)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var b []byte
	if spec.merged() || spec.SlopspaceID == "" {
		stepRel := path.Dir(filepath.ToSlash(stepFilePath(spec.CondocPath)))
		file := path.Join(stepRel, proposalsLink(n, rel))
		if gitauth.IsLocal(spec.CondocRepo) {
			// No GitHub to send the browser to: read it from the local repo,
			// on the kept branch, else the one it was merged into.
			if b, err = localFileContent(spec.CondocRepo, spec.Branch, file); err != nil {
				b, err = localFileContent(spec.CondocRepo, "", file)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
		} else if prefix := strings.TrimSuffix(spec.CondocURL, spec.CondocPath); prefix != spec.CondocURL {
			http.Redirect(w, r, prefix+file, http.StatusFound)
			return
		} else {
			http.Error(w, "the condoc's slopspace is gone", http.StatusNotFound)
			return
		}
	} else {
		meta, err := s.slop.ss.Get(spec.SlopspaceID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if b, err = os.ReadFile(filepath.Join(proposalsDir(meta, &spec, n), filepath.FromSlash(rel))); err != nil {
			http.Error(w, "no such DTT file in the condoc", http.StatusNotFound)
			return
		}
	}
	if strings.HasSuffix(rel, ".json") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

// toolResultBody is "## Result N.k": what dtt returned.
func toolResultBody(c *ToolCall, link string) string {
	var b strings.Builder
	if c.Failed == "" {
		fmt.Fprintf(&b, "✓ `dtt %s` returned [its image](%s).\n", c.Mode, link)
		return b.String()
	}
	fmt.Fprintf(&b, "✗ `dtt %s` failed:\n\n", c.Mode)
	for _, line := range strings.Split(c.Failed, "\n") {
		b.WriteString("> " + line + "\n")
	}
	if link != "" {
		fmt.Fprintf(&b, "\nIt still returned [an image](%s).\n", link)
	}
	return b.String()
}

// tickReview is the tick's part: hold a condoc whose proposals came back
// without a hold, and run a held condoc's sub-iterations as proposals are
// decided -- in the background, as dtt can take minutes. It reports whether
// the condoc was a review's to handle.
func (s *Server) tickReview(stem string, spec PointerSpec) bool {
	pendingHold := spec.Hold == nil && spec.Lock == nil && spec.Phase == PhaseAwaitingPrompt && spec.SlopspaceID != ""
	if spec.Hold == nil && !pendingHold {
		return false
	}
	if pendingHold {
		meta, err := s.slop.ss.Get(spec.SlopspaceID)
		if err != nil || meta.Deployed || meta.PendingReview() == nil {
			return false
		}
	} else if spec.Hold.Call == nil {
		// Nothing in flight: only go on once something is decided.
		meta, err := s.slop.ss.Get(spec.SlopspaceID)
		if err != nil || len(meta.Deployments) == 0 {
			return true
		}
		d := &meta.Deployments[len(meta.Deployments)-1]
		if p, _ := nextDecided(spec.Hold, d); p == "" && d.Decision != dktypes.DecisionModify {
			return true
		}
	}
	if !s.claim(stem) {
		return true
	}
	if pendingHold {
		defer s.release(stem)
		spec, err := s.readHeldSpec(stem)
		if err != nil || spec.Error != "" {
			return true
		}
		if err := s.holdForProposals(&spec); err != nil {
			log.Printf("condocv2 %s: hold: %v", stem, err)
		}
		return true
	}
	go func() {
		defer s.release(stem)
		if err := s.advanceHold(stem); err != nil {
			log.Printf("condocv2 %s: DTT sub-iteration: %v", stem, err)
			s.setSpecError(stem, err)
		}
	}()
	return true
}
