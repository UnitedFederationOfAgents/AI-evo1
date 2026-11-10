package slopspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dungeon-keeper/pkg/types"
)

// DTT proposals (dTT-IntegrationImpls Step 1, Revision H).
//
// A slopspace's DTT material is split by who may write it:
//
//	readspaces/dtt-canvas/{read,write}/    tool calls the human supplied --
//	                                       added proactively, or proposals
//	                                       the human accepted
//	readspaces/dtt-images/                 the images those tool calls returned
//	readspaces/DTT-GUIDE.md                the agent's primer, written at
//	                                       deploy (see dttguide.go)
//	writespaces/dtt-requests/{read,write}/ tool calls the agent proposes
//
// When a slopspace is returned, whatever is in dtt-requests is recorded on
// that deployment as its proposals, and the slopspace can't be deployed again
// until the human decides (Review):
//
//   - accept moves the proposals onto the canvas, in the same read/ or write/
//     subdir, where the next deployment sees them; accepted reads are run
//     and their images put in dtt-images (see dttimages.go) -- or, through
//     DecideProposals, left for the caller to run (RunCanvasDoc);
//   - reject sets them aside;
//   - modify leaves them where they are for the agent to rework; the caller
//     deploys the slopspace again with the human's note to do that (see
//     dungeon-keeper/slopctl.go).
//
// Accept and reject can also be given one proposal at a time (Revision M,
// ReviewProposals); the slopspace stays held until none are left.
//
// Accepted and rejected proposals are also copied under dtt-reviews/ in the
// slopspace's directory -- outside both spaces, never deployed -- named by
// deployment and decision.
//
// The canvas and images are readspaces, so the deployed copies are thrown
// away on return like the rest. Unlike the rest they're the human's record,
// so Deploy keeps a copy in readspaces-kept/ and Return puts it back.

const (
	ReviewsDir         = "dtt-reviews"
	KeptReadSpacesDir  = "readspaces-kept"
	maxProposalsListed = 200
)

// keptReadSpaces are the readspace dirs that survive a deployment.
var keptReadSpaces = []string{DTTCanvasDir, DTTImagesDir}

// ReviewPendingError is Deploy's refusal while a returned deployment's DTT
// proposals await the human's decision.
type ReviewPendingError struct {
	ID        string
	Proposals []string
}

func (e *ReviewPendingError) Error() string {
	return fmt.Sprintf("slopspace %s returned %d DTT proposal(s) awaiting review (%s): accept, reject or request modification of them before deploying it again",
		shortID(e.ID), len(e.Proposals), summariseProposals(e.Proposals, 3))
}

func summariseProposals(ps []string, n int) string {
	if len(ps) <= n {
		return strings.Join(ps, ", ")
	}
	return strings.Join(ps[:n], ", ") + fmt.Sprintf(", and %d more", len(ps)-n)
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// keepReadSpaces copies the canvas and images aside before a deploy moves the
// readspaces out.
func keepReadSpaces(root string) error {
	kept := filepath.Join(root, KeptReadSpacesDir)
	if err := os.RemoveAll(kept); err != nil {
		return fmt.Errorf("failed to clear %s: %w", kept, err)
	}
	for _, d := range keptReadSpaces {
		src := filepath.Join(root, ReadSpacesDir, d)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		if err := copyDirContents(src, filepath.Join(kept, d)); err != nil {
			return fmt.Errorf("failed to keep %s: %w", d, err)
		}
	}
	return nil
}

// restoreReadSpaces puts the copy keepReadSpaces made back into the (freshly
// recreated) readspaces. A slopspace deployed before the copy was made has
// none, and keeps the empty dirs.
func restoreReadSpaces(root string) error {
	kept := filepath.Join(root, KeptReadSpacesDir)
	for _, d := range keptReadSpaces {
		src := filepath.Join(kept, d)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		dst := filepath.Join(root, ReadSpacesDir, d)
		os.RemoveAll(dst)
		if err := os.Rename(src, dst); err != nil {
			if err := copyDirContents(src, dst); err != nil {
				return fmt.Errorf("failed to restore %s: %w", d, err)
			}
		}
	}
	return os.RemoveAll(kept)
}

// listProposals lists the files under writeSpaces/dtt-requests, relative to
// it and sorted, at most maxProposalsListed of them.
func listProposals(writeSpaces string) ([]string, error) {
	base := filepath.Join(writeSpaces, DTTRequestsDir)
	var out []string
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || len(out) >= maxProposalsListed {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list DTT proposals: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// Review records the human's decision on a returned slopspace's pending DTT
// proposals and acts on it (see the top of this file). note is kept with the
// decision; modify needs one, since it is what the agent is asked to do.
// It returns a one-line description of what happened.
func (m *Manager) Review(id string, decision types.ProposalDecision, note string) (string, error) {
	return m.ReviewProposals(id, decision, note, nil)
}

// ReviewProposals is Review for only some of the pending proposals (Revision
// M): each one named in only is accepted onto the canvas or rejected on its
// own, and the rest stay pending. Once none are left the deployment's
// Decision is set -- to accept or reject if every answer was the same, mixed
// otherwise -- and the slopspace can be deployed again. Requesting
// modification always covers every proposal still pending, so only must be
// empty for it. An empty only is Review: every pending proposal.
func (m *Manager) ReviewProposals(id string, decision types.ProposalDecision, note string, only []string) (string, error) {
	return m.reviewProposals(id, decision, note, only, true)
}

// DecideProposals is ReviewProposals, accept or reject only, for a caller
// that runs the accepted documents itself: they go onto the canvas, but even
// the reads aren't run. condocv2 commits each accepted proposal to its condoc
// before running it (RunCanvasDoc), then commits the image.
func (m *Manager) DecideProposals(id string, decision types.ProposalDecision, only []string) (string, error) {
	if decision != types.DecisionAccept && decision != types.DecisionReject {
		return "", fmt.Errorf("DecideProposals is for accept or reject, not %q", decision)
	}
	return m.reviewProposals(id, decision, "", only, false)
}

func (m *Manager) reviewProposals(id string, decision types.ProposalDecision, note string, only []string, runReads bool) (string, error) {
	metadata, err := m.Get(id)
	if err != nil {
		return "", err
	}
	if metadata.Deployed {
		return "", fmt.Errorf("slopspace %s is deployed; its proposals are reviewed once it's returned", shortID(id))
	}
	d := metadata.PendingReview()
	if d == nil {
		return "", fmt.Errorf("slopspace %s has no DTT proposals awaiting review", shortID(id))
	}
	note = strings.TrimSpace(note)
	pending := d.Pending()

	requests := filepath.Join(metadata.RootPath, WriteSpacesDir, DTTRequestsDir)
	archive := filepath.Join(metadata.RootPath, ReviewsDir,
		fmt.Sprintf("%03d-%s-%s", len(metadata.Deployments), d.ReturnedAt.Format("20060102T150405"), decision))
	canvas := filepath.Join(metadata.RootPath, ReadSpacesDir, DTTCanvasDir)
	now := time.Now()
	var detail string
	switch decision {
	case types.DecisionAccept, types.DecisionReject:
		verb := "accepted"
		if decision == types.DecisionReject {
			verb = "rejected"
		}
		if len(only) > 0 {
			waiting := make(map[string]bool, len(pending))
			for _, p := range pending {
				waiting[p] = true
			}
			for _, p := range only {
				if !waiting[p] {
					return "", fmt.Errorf("%q isn't a DTT proposal awaiting review in slopspace %s", p, shortID(id))
				}
			}
			for _, p := range only {
				if err := decideProposal(requests, archive, canvas, p, decision == types.DecisionAccept); err != nil {
					return "", err
				}
				d.Decided = append(d.Decided, types.ProposalVerdict{Proposal: p, Decision: decision, DecidedAt: now})
			}
			detail = fmt.Sprintf("%s %s", verb, summariseProposals(only, 3))
		} else {
			if err := copyDirContents(requests, archive); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("failed to archive the proposals: %w", err)
			}
			if decision == types.DecisionAccept {
				if err := copyDirContents(requests, canvas); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return "", fmt.Errorf("failed to move the proposals onto the canvas: %w", err)
				}
			}
			if err := os.RemoveAll(requests); err != nil {
				return "", fmt.Errorf("failed to clear dtt-requests: %w", err)
			}
			// Once some were answered on their own, keep the rest's answers
			// with them, so the deployment's Decision can say whether they
			// all went the same way.
			if len(d.Decided) > 0 {
				for _, p := range pending {
					d.Decided = append(d.Decided, types.ProposalVerdict{Proposal: p, Decision: decision, DecidedAt: now})
				}
			}
			detail = fmt.Sprintf("%s %d proposal(s)", verb, len(pending))
		}
		if decision == types.DecisionAccept {
			detail += " onto the DTT canvas"
			// Accepted reads are run straight away, so their images are
			// waiting for the agent at the next deploy (dttimages.go) --
			// unless the caller runs them itself (DecideProposals).
			accepted := only
			if len(accepted) == 0 {
				accepted = pending
			}
			if !runReads {
				accepted = nil
			}
			if s := m.readCanvasImages(metadata.RootPath, accepted); s != "" {
				detail += "; " + s
			}
		}
		if err := makeLayout("", filepath.Join(metadata.RootPath, WriteSpacesDir)); err != nil {
			return "", err
		}
	case types.DecisionModify:
		if len(only) > 0 {
			return "", errors.New("requesting modification covers every proposal still awaiting review; accept or reject single ones instead")
		}
		if note == "" {
			return "", errors.New("say what to change: requesting modification needs a note for the agent")
		}
		detail = fmt.Sprintf("requested modification of %d proposal(s)", len(pending))
	default:
		return "", fmt.Errorf("unknown decision %q (accept, reject or modify)", decision)
	}

	// A whole-set answer (no only) decides every pending proposal, even when
	// none were answered on their own and so have no verdict for Pending to
	// count; without it the deployment stayed pending and condocv2 held the
	// condoc again straight after releasing it (Revision R).
	if decision == types.DecisionModify || len(only) == 0 || len(d.Pending()) == 0 {
		d.Decision = decision
		if decision != types.DecisionModify {
			d.Decision = overallDecision(d.Decided, decision)
		}
		d.DecidedAt = &now
		d.Note = note
	}
	if left := len(d.Pending()); left > 0 {
		detail += fmt.Sprintf("; %d still awaiting review", left)
	}
	if err := m.writeMetadata(metadata.RootPath, metadata); err != nil {
		return "", fmt.Errorf("failed to update metadata: %w", err)
	}
	return detail, nil
}

// decideProposal acts on one proposal, p (relative to requests): it is
// archived, copied onto the canvas when accepted, then removed from requests
// along with any directories that leaves empty.
func decideProposal(requests, archive, canvas, p string, accept bool) error {
	rel := filepath.FromSlash(p)
	src := filepath.Join(requests, rel)
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return nil // gone already; nothing left to move
	}
	dsts := []string{filepath.Join(archive, rel)}
	if accept {
		dsts = append(dsts, filepath.Join(canvas, rel))
	}
	for _, dst := range dsts {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("failed to place %s: %w", p, err)
		}
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("failed to copy %s: %w", p, err)
		}
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("failed to remove %s from dtt-requests: %w", p, err)
	}
	for dir := filepath.Dir(src); dir != requests && strings.HasPrefix(dir, requests); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			break // not empty
		}
	}
	return nil
}

// overallDecision is a deployment's Decision once all its proposals are
// answered: the one answer they all got, or mixed. With no per-proposal
// answers it is last, the whole-set decision.
func overallDecision(decided []types.ProposalVerdict, last types.ProposalDecision) types.ProposalDecision {
	for _, v := range decided {
		if v.Decision != last {
			return types.DecisionMixed
		}
	}
	return last
}

// SetDTTRequests turns a slopspace's DTT proposals on or off. Off, it is
// deployed without writespaces/dtt-requests and Return records no proposals
// (dropping anything the agent wrote there anyway), so it never waits for a
// review. It takes effect at the next deploy; proposals already awaiting
// review still have to be answered.
func (m *Manager) SetDTTRequests(id string, enabled bool) error {
	metadata, err := m.Get(id)
	if err != nil {
		return err
	}
	if metadata.DTTRequestsDisabled == !enabled {
		return nil
	}
	metadata.DTTRequestsDisabled = !enabled
	if err := m.writeMetadata(metadata.RootPath, metadata); err != nil {
		return fmt.Errorf("failed to update metadata: %w", err)
	}
	return nil
}

// ModifyPrompt is the prompt a modify review queues for the agent once the
// slopspace is deployed again.
func ModifyPrompt(proposals []string, note string) string {
	return "The human reviewed the DTT tool calls you proposed in writespaces/" + DTTRequestsDir + "/ (" +
		summariseProposals(proposals, 20) + ") and asked for changes:\n\n" + note + "\n\n" +
		"Revise the proposals in writespaces/" + DTTRequestsDir + "/read and /write accordingly. Proposals that read " +
		"go in read/, ones that change something go in write/. The human will accept, reject or ask for further " +
		"changes once you're done."
}
