package slopspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dungeon-keeper/pkg/types"
)

func newTestManager(t *testing.T) (*Manager, *types.Config) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = filepath.Join(tmpDir, "slopspaces")
	cfg.AgentSlopspaceRoot = filepath.Join(tmpDir, "agent")
	mgr := NewManager(cfg)
	// Never run a real dtt from these tests; TestAcceptedReadsGetImages
	// supplies a fake one.
	mgr.findDTT = func() (string, error) { return "", errors.New("no dtt in tests") }
	return mgr, cfg
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// deployWithProposals deploys a slopspace, has the "agent" propose a read and
// a write, and returns it.
func deployWithProposals(t *testing.T, mgr *Manager, cfg *types.Config) *types.SlopspaceMetadata {
	t.Helper()
	m, err := mgr.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(cfg.DeployPathForAgentType(types.AgentTypeWorker), WriteSpacesDir, DTTRequestsDir)
	writeFile(t, filepath.Join(ws, DTTReadDir, "prs.dtt.yaml"), "read: prs\n")
	writeFile(t, filepath.Join(ws, DTTWriteDir, "repo.dtt.yaml"), "assert: repo\n")
	if err := mgr.Return(m.ID); err != nil {
		t.Fatal(err)
	}
	m, err = mgr.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestReturnRecordsProposalsAndBlocksDeploy(t *testing.T) {
	mgr, cfg := newTestManager(t)
	m := deployWithProposals(t, mgr, cfg)

	d := m.PendingReview()
	if d == nil {
		t.Fatal("expected the proposals to await review")
	}
	want := []string{"read/prs.dtt.yaml", "write/repo.dtt.yaml"}
	if strings.Join(d.Proposals, ",") != strings.Join(want, ",") {
		t.Errorf("proposals = %v, want %v", d.Proposals, want)
	}

	err := mgr.Deploy(m.ID, types.AgentTypeWorker)
	var pending *ReviewPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("deploy with proposals pending: got %v, want a ReviewPendingError", err)
	}
}

func TestReturnWithoutProposalsNeedsNoReview(t *testing.T) {
	mgr, _ := newTestManager(t)
	m, _ := mgr.Create()
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Return(m.ID); err != nil {
		t.Fatal(err)
	}
	m, _ = mgr.Get(m.ID)
	if m.PendingReview() != nil {
		t.Fatal("no proposals, yet a review is pending")
	}
	if _, err := mgr.Review(m.ID, types.DecisionAccept, ""); err == nil {
		t.Error("review with nothing pending should fail")
	}
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Errorf("redeploy: %v", err)
	}
}

func TestDTTRequestsDisabled(t *testing.T) {
	mgr, cfg := newTestManager(t)
	m, _ := mgr.Create()
	if err := mgr.SetDTTRequests(m.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(cfg.DeployPathForAgentType(types.AgentTypeWorker), WriteSpacesDir, DTTRequestsDir)
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Fatalf("dtt-requests deployed with proposals disabled (stat: %v)", err)
	}
	// An agent that makes the dir anyway gets nothing reviewed.
	writeFile(t, filepath.Join(ws, DTTWriteDir, "repo.dtt.yaml"), "assert: repo\n")
	if err := mgr.Return(m.ID); err != nil {
		t.Fatal(err)
	}
	m, _ = mgr.Get(m.ID)
	if m.PendingReview() != nil {
		t.Fatal("proposals disabled, yet a review is pending")
	}
	if _, err := os.Stat(filepath.Join(m.RootPath, WriteSpacesDir, DTTRequestsDir)); !os.IsNotExist(err) {
		t.Errorf("dtt-requests kept with proposals disabled (stat: %v)", err)
	}
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Errorf("redeploy: %v", err)
	}
}

func TestReviewAcceptMovesProposalsOntoCanvas(t *testing.T) {
	mgr, cfg := newTestManager(t)
	m := deployWithProposals(t, mgr, cfg)

	if _, err := mgr.Review(m.ID, types.DecisionAccept, ""); err != nil {
		t.Fatal(err)
	}
	canvas := filepath.Join(m.RootPath, ReadSpacesDir, DTTCanvasDir)
	if got := readFile(t, filepath.Join(canvas, DTTWriteDir, "repo.dtt.yaml")); got != "assert: repo\n" {
		t.Errorf("canvas write proposal = %q", got)
	}
	if got := readFile(t, filepath.Join(canvas, DTTReadDir, "prs.dtt.yaml")); got != "read: prs\n" {
		t.Errorf("canvas read proposal = %q", got)
	}
	left, _ := listProposals(filepath.Join(m.RootPath, WriteSpacesDir))
	if len(left) != 0 {
		t.Errorf("dtt-requests should be empty after accepting, has %v", left)
	}
	if _, err := os.Stat(filepath.Join(m.RootPath, WriteSpacesDir, DTTRequestsDir, DTTWriteDir)); err != nil {
		t.Errorf("dtt-requests/write should be recreated: %v", err)
	}
	reviews, _ := os.ReadDir(filepath.Join(m.RootPath, ReviewsDir))
	if len(reviews) != 1 || !strings.HasSuffix(reviews[0].Name(), "-accept") {
		t.Errorf("expected one accepted archive, got %v", reviews)
	}

	m, _ = mgr.Get(m.ID)
	if m.PendingReview() != nil {
		t.Error("still pending after accepting")
	}
	if d := m.Deployments[len(m.Deployments)-1]; d.Decision != types.DecisionAccept || d.DecidedAt == nil {
		t.Errorf("decision not recorded: %+v", d)
	}

	// The accepted canvas reaches the next deployment, and survives its return
	// even if the agent tampers with the deployed copy.
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	deployed := filepath.Join(cfg.DeployPathForAgentType(types.AgentTypeWorker), ReadSpacesDir, DTTCanvasDir, DTTWriteDir, "repo.dtt.yaml")
	if got := readFile(t, deployed); got != "assert: repo\n" {
		t.Errorf("deployed canvas = %q", got)
	}
	writeFile(t, deployed, "tampered\n")
	if err := mgr.Return(m.ID); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(canvas, DTTWriteDir, "repo.dtt.yaml")); got != "assert: repo\n" {
		t.Errorf("canvas after return = %q, want the human's copy", got)
	}
	if _, err := os.Stat(filepath.Join(m.RootPath, KeptReadSpacesDir)); !os.IsNotExist(err) {
		t.Error("the kept copy should be gone after return")
	}
}

func TestReviewRejectSetsProposalsAside(t *testing.T) {
	mgr, cfg := newTestManager(t)
	m := deployWithProposals(t, mgr, cfg)

	if _, err := mgr.Review(m.ID, types.DecisionReject, "not now"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.RootPath, ReadSpacesDir, DTTCanvasDir, DTTWriteDir, "repo.dtt.yaml")); !os.IsNotExist(err) {
		t.Error("a rejected proposal must not reach the canvas")
	}
	reviews, _ := os.ReadDir(filepath.Join(m.RootPath, ReviewsDir))
	if len(reviews) != 1 || !strings.HasSuffix(reviews[0].Name(), "-reject") {
		t.Fatalf("expected one rejected archive, got %v", reviews)
	}
	if got := readFile(t, filepath.Join(m.RootPath, ReviewsDir, reviews[0].Name(), DTTWriteDir, "repo.dtt.yaml")); got != "assert: repo\n" {
		t.Errorf("archived proposal = %q", got)
	}
	// condocv2 holds a condoc again while PendingReview is set (Revision R).
	m, _ = mgr.Get(m.ID)
	if d := m.Deployments[len(m.Deployments)-1]; m.PendingReview() != nil || d.Decision != types.DecisionReject {
		t.Errorf("rejecting all should decide the deployment: %+v", d)
	}
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Errorf("redeploy after rejecting: %v", err)
	}
}

func TestReviewModifyKeepsProposals(t *testing.T) {
	mgr, cfg := newTestManager(t)
	m := deployWithProposals(t, mgr, cfg)

	if _, err := mgr.Review(m.ID, types.DecisionModify, "  "); err == nil {
		t.Error("modify without a note should fail")
	}
	if _, err := mgr.Review(m.ID, types.DecisionModify, "use a dated name"); err != nil {
		t.Fatal(err)
	}
	left, _ := listProposals(filepath.Join(m.RootPath, WriteSpacesDir))
	if len(left) != 2 {
		t.Errorf("modify should leave the proposals for the agent, has %v", left)
	}
	m, _ = mgr.Get(m.ID)
	if d := m.Deployments[len(m.Deployments)-1]; d.Note != "use a dated name" {
		t.Errorf("note = %q", d.Note)
	}
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatalf("redeploy after modify: %v", err)
	}
	// Returned without changes, they await review again.
	if err := mgr.Return(m.ID); err != nil {
		t.Fatal(err)
	}
	m, _ = mgr.Get(m.ID)
	if m.PendingReview() == nil {
		t.Error("the reworked proposals should await review again")
	}
}

func TestReviewOneProposalAtATime(t *testing.T) {
	mgr, cfg := newTestManager(t)
	m := deployWithProposals(t, mgr, cfg)
	canvas := filepath.Join(m.RootPath, ReadSpacesDir, DTTCanvasDir)

	if _, err := mgr.ReviewProposals(m.ID, types.DecisionAccept, "", []string{"write/nope.dtt.yaml"}); err == nil {
		t.Error("accepting a proposal that isn't pending should fail")
	}
	if _, err := mgr.ReviewProposals(m.ID, types.DecisionModify, "change it", []string{"write/repo.dtt.yaml"}); err == nil {
		t.Error("modify of a single proposal should fail")
	}

	if _, err := mgr.ReviewProposals(m.ID, types.DecisionAccept, "", []string{"write/repo.dtt.yaml"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(canvas, DTTWriteDir, "repo.dtt.yaml")); got != "assert: repo\n" {
		t.Errorf("canvas write proposal = %q", got)
	}
	if _, err := os.Stat(filepath.Join(canvas, DTTReadDir, "prs.dtt.yaml")); !os.IsNotExist(err) {
		t.Error("the other proposal must stay off the canvas until it's answered")
	}
	left, _ := listProposals(filepath.Join(m.RootPath, WriteSpacesDir))
	if strings.Join(left, ",") != "read/prs.dtt.yaml" {
		t.Errorf("dtt-requests after accepting one = %v", left)
	}
	m, _ = mgr.Get(m.ID)
	d := m.PendingReview()
	if d == nil || strings.Join(d.Pending(), ",") != "read/prs.dtt.yaml" {
		t.Fatalf("pending after accepting one = %+v", d)
	}
	var pending *ReviewPendingError
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); !errors.As(err, &pending) || len(pending.Proposals) != 1 {
		t.Fatalf("deploy with one proposal still pending: got %v", err)
	}

	if _, err := mgr.ReviewProposals(m.ID, types.DecisionReject, "", []string{"read/prs.dtt.yaml"}); err != nil {
		t.Fatal(err)
	}
	m, _ = mgr.Get(m.ID)
	if m.PendingReview() != nil {
		t.Error("still pending after answering every proposal")
	}
	if d := m.Deployments[len(m.Deployments)-1]; d.Decision != types.DecisionMixed || d.DecidedAt == nil || len(d.Decided) != 2 {
		t.Errorf("decision = %+v, want mixed with two answers", d)
	}
	reviews, _ := os.ReadDir(filepath.Join(m.RootPath, ReviewsDir))
	if len(reviews) != 2 {
		t.Errorf("expected an accepted and a rejected archive, got %v", reviews)
	}
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Errorf("redeploy after answering every proposal: %v", err)
	}
}

func TestReviewRestAfterOne(t *testing.T) {
	mgr, cfg := newTestManager(t)
	m := deployWithProposals(t, mgr, cfg)

	if _, err := mgr.ReviewProposals(m.ID, types.DecisionAccept, "", []string{"read/prs.dtt.yaml"}); err != nil {
		t.Fatal(err)
	}
	// The whole-set accept now covers only what's left; every answer was
	// accept, so the deployment's decision is too.
	if _, err := mgr.Review(m.ID, types.DecisionAccept, ""); err != nil {
		t.Fatal(err)
	}
	m, _ = mgr.Get(m.ID)
	if d := m.Deployments[len(m.Deployments)-1]; d.Decision != types.DecisionAccept || len(d.Decided) != 2 {
		t.Errorf("decision = %+v, want accept with two answers", d)
	}
	canvas := filepath.Join(m.RootPath, ReadSpacesDir, DTTCanvasDir)
	for _, p := range []string{filepath.Join(DTTReadDir, "prs.dtt.yaml"), filepath.Join(DTTWriteDir, "repo.dtt.yaml")} {
		if _, err := os.Stat(filepath.Join(canvas, p)); err != nil {
			t.Errorf("%s should be on the canvas: %v", p, err)
		}
	}
}

func TestReviewRefusedWhileDeployed(t *testing.T) {
	mgr, _ := newTestManager(t)
	m, _ := mgr.Create()
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Review(m.ID, types.DecisionAccept, ""); err == nil {
		t.Error("review while deployed should fail")
	}
}

func TestBrowse(t *testing.T) {
	mgr, cfg := newTestManager(t)
	m, _ := mgr.Create()
	writeFile(t, filepath.Join(m.RootPath, WriteSpacesDir, FilesDir, "notes.txt"), "hello\n")
	writeFile(t, filepath.Join(m.RootPath, WriteSpacesDir, FilesDir, "blob.bin"), "a\x00b")
	writeFile(t, filepath.Join(m.RootPath, MetadataFile+".extra"), "outside the spaces")

	top, err := mgr.Browse(m.ID, "")
	if err != nil || !top.Dir || len(top.Entries) != 2 {
		t.Fatalf("top = %+v, %v", top, err)
	}
	dir, err := mgr.Browse(m.ID, "writespaces/files")
	if err != nil || !dir.Dir || len(dir.Entries) != 2 || dir.Entries[0].Name != "blob.bin" || dir.Entries[1].Size != 6 {
		t.Fatalf("files dir = %+v, %v", dir, err)
	}
	f, err := mgr.Browse(m.ID, "writespaces/files/notes.txt")
	if err != nil || f.Dir || f.Content != "hello\n" {
		t.Fatalf("notes = %+v, %v", f, err)
	}
	b, err := mgr.Browse(m.ID, "writespaces/files/blob.bin")
	if err != nil || !b.Binary || b.Content != "" {
		t.Fatalf("blob = %+v, %v", b, err)
	}
	for _, bad := range []string{"SLOPSPACE.json", "../" + m.ID + "/SLOPSPACE.json", "writespaces/../SLOPSPACE.json.extra"} {
		if _, err := mgr.Browse(m.ID, bad); err == nil {
			t.Errorf("browsing %q should be refused", bad)
		}
	}
	if err := os.Symlink(filepath.Join(m.RootPath, MetadataFile), filepath.Join(m.RootPath, WriteSpacesDir, "meta")); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Browse(m.ID, "writespaces/meta"); err == nil {
		t.Error("a symlink out of the spaces should be refused")
	}

	// While deployed it looks at the deploy location.
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	f, err = mgr.Browse(m.ID, "writespaces/files/notes.txt")
	if err != nil || f.Root != cfg.DeployPathForAgentType(types.AgentTypeWorker) || f.Content != "hello\n" {
		t.Fatalf("deployed notes = %+v, %v", f, err)
	}
}

// fakeDTT is a dtt that answers "<read|assert|delete> --json <doc>" with an
// image naming the mode and document, or fails when the document says "fail".
func fakeDTT(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dtt")
	writeFile(t, p, `#!/bin/sh
case "$1" in read|assert|delete) ;; *) echo "unexpected: $*" >&2; exit 2 ;; esac
[ "$2" = --json ] || { echo "unexpected: $*" >&2; exit 2; }
if grep -q fail "$3"; then
  echo '{"ok": false, "error": "line 1: no such tool"}'
  exit 2
fi
printf '{"ok": true, "format": "dtt-image-v1", "mode": "%s", "doc": "%s"}\n' "$1" "$(basename "$3")"
`)
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAcceptedReadsGetImages(t *testing.T) {
	mgr, cfg := newTestManager(t)
	bin := fakeDTT(t)
	mgr.findDTT = func() (string, error) { return bin, nil }
	m := deployWithProposals(t, mgr, cfg)

	detail, err := mgr.Review(m.ID, types.DecisionAccept, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "read 1 into dtt-images") {
		t.Errorf("detail = %q", detail)
	}
	images := filepath.Join(m.RootPath, ReadSpacesDir, DTTImagesDir)
	if got := readFile(t, filepath.Join(images, DTTReadDir, "prs.image.json")); !strings.Contains(got, `"doc": "prs.dtt.yaml"`) {
		t.Errorf("read image = %q", got)
	}
	// Writes change things; accepting one never runs it.
	if _, err := os.Stat(filepath.Join(images, DTTWriteDir, "repo.image.json")); !os.IsNotExist(err) {
		t.Error("an accepted write must not be run")
	}

	// The image reaches the agent, and survives the return.
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	readFile(t, filepath.Join(cfg.DeployPathForAgentType(types.AgentTypeWorker), ReadSpacesDir, DTTImagesDir, DTTReadDir, "prs.image.json"))
	if err := mgr.Return(m.ID); err != nil {
		t.Fatal(err)
	}
	readFile(t, filepath.Join(images, DTTReadDir, "prs.image.json"))
}

func TestDeployFillsMissingImages(t *testing.T) {
	mgr, cfg := newTestManager(t)
	m, err := mgr.Create()
	if err != nil {
		t.Fatal(err)
	}
	canvas := filepath.Join(m.RootPath, ReadSpacesDir, DTTCanvasDir)
	images := filepath.Join(m.RootPath, ReadSpacesDir, DTTImagesDir)
	// Accepted before reads were run, or put there by the human.
	writeFile(t, filepath.Join(canvas, DTTReadDir, "repo.dtt.yaml"), "read: repo\n")
	writeFile(t, filepath.Join(canvas, DTTReadDir, "bad.dtt.yaml"), "fail\n")
	writeFile(t, filepath.Join(canvas, DTTWriteDir, "pr.dtt.yaml"), "assert: pr\n")

	// Without dtt the deploy still goes ahead, leaving an error file.
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	deployedImages := filepath.Join(cfg.DeployPathForAgentType(types.AgentTypeWorker), ReadSpacesDir, DTTImagesDir)
	if got := readFile(t, filepath.Join(deployedImages, DTTReadDir, "repo.error.txt")); !strings.Contains(got, "no dtt in tests") {
		t.Errorf("error file = %q", got)
	}
	if err := mgr.Return(m.ID); err != nil {
		t.Fatal(err)
	}

	bin := fakeDTT(t)
	mgr.findDTT = func() (string, error) { return bin, nil }
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	readFile(t, filepath.Join(deployedImages, DTTReadDir, "repo.image.json"))
	if _, err := os.Stat(filepath.Join(deployedImages, DTTReadDir, "repo.error.txt")); !os.IsNotExist(err) {
		t.Error("a successful read should clear the old error file")
	}
	if got := readFile(t, filepath.Join(deployedImages, DTTReadDir, "bad.error.txt")); !strings.Contains(got, "no such tool") {
		t.Errorf("bad read's error file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(deployedImages, DTTWriteDir, "pr.image.json")); !os.IsNotExist(err) {
		t.Error("a canvas write must not be run at deploy")
	}
	if err := mgr.Return(m.ID); err != nil {
		t.Fatal(err)
	}
	// An image that's there isn't taken again.
	writeFile(t, filepath.Join(images, DTTReadDir, "repo.image.json"), "kept\n")
	if err := mgr.Deploy(m.ID, types.AgentTypeWorker); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(deployedImages, DTTReadDir, "repo.image.json")); got != "kept\n" {
		t.Errorf("existing image was replaced: %q", got)
	}
}

// condocv2's way: decide one proposal at a time without running anything,
// then run each accepted document in its own mode.
func TestDecideThenRunCanvasDoc(t *testing.T) {
	mgr, cfg := newTestManager(t)
	bin := fakeDTT(t)
	mgr.findDTT = func() (string, error) { return bin, nil }
	m := deployWithProposals(t, mgr, cfg)
	images := filepath.Join(m.RootPath, ReadSpacesDir, DTTImagesDir)

	if _, err := mgr.DecideProposals(m.ID, types.DecisionModify, nil); err == nil {
		t.Error("DecideProposals must refuse modify")
	}
	for _, p := range []string{"read/prs.dtt.yaml", "write/repo.dtt.yaml"} {
		if _, err := mgr.DecideProposals(m.ID, types.DecisionAccept, []string{p}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(images, DTTReadDir, "prs.image.json")); !os.IsNotExist(err) {
		t.Error("DecideProposals must not run the accepted read")
	}
	if got, _ := mgr.Get(m.ID); got.PendingReview() != nil || got.Deployments[len(got.Deployments)-1].Decision != types.DecisionAccept {
		t.Errorf("deployment still pending after every proposal was decided: %+v", got.Deployments)
	}

	for _, c := range []struct{ rel, mode string }{{"read/prs.dtt.yaml", "read"}, {"write/repo.dtt.yaml", "assert"}} {
		mode, image, _, err := mgr.RunCanvasDoc(m.ID, c.rel)
		if err != nil || mode != c.mode {
			t.Fatalf("%s: mode %q, err %v", c.rel, mode, err)
		}
		if got := readFile(t, image); !strings.Contains(got, `"mode": "`+c.mode+`"`) {
			t.Errorf("%s image = %q", c.rel, got)
		}
	}

	if got := DTTDocMode("write/x.dtt.yaml", []byte("# Close the PR. Mode: delete.\nselect: x\n")); got != "delete" {
		t.Errorf("DTTDocMode = %q, want delete", got)
	}
	if got := DTTDocMode("write/x.dtt.yaml", []byte("select: x\n# Mode: delete\n")); got != "assert" {
		t.Errorf("DTTDocMode read past the leading comments: %q", got)
	}
}

// A write proposal can be read before it is decided: dtt read on its copy in
// dtt-requests, beside where the assert's image would go, and it stays
// pending.
func TestPreReadProposal(t *testing.T) {
	mgr, cfg := newTestManager(t)
	bin := fakeDTT(t)
	mgr.findDTT = func() (string, error) { return bin, nil }
	m := deployWithProposals(t, mgr, cfg)
	images := filepath.Join(m.RootPath, ReadSpacesDir, DTTImagesDir)

	image, _, err := mgr.PreReadProposal(m.ID, "write/repo.dtt.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(images, DTTWriteDir, "repo.read.image.json"); image != want {
		t.Errorf("image = %q, want %q", image, want)
	}
	if got := readFile(t, image); !strings.Contains(got, `"mode": "read"`) {
		t.Errorf("pre-read image = %q", got)
	}
	got, _ := mgr.Get(m.ID)
	if d := got.PendingReview(); d == nil || len(d.Pending()) != 2 {
		t.Errorf("a pre-read must leave the proposals pending: %+v", got.Deployments)
	}

	// The assert's own image doesn't replace it.
	if _, err := mgr.DecideProposals(m.ID, types.DecisionAccept, []string{"write/repo.dtt.yaml"}); err != nil {
		t.Fatal(err)
	}
	if _, assertImage, _, err := mgr.RunCanvasDoc(m.ID, "write/repo.dtt.yaml"); err != nil || assertImage == image {
		t.Fatalf("assert image %q, err %v", assertImage, err)
	}
	readFile(t, image)

	// Only a proposal still awaiting a decision can be.
	if _, _, err := mgr.PreReadProposal(m.ID, "write/repo.dtt.yaml"); err == nil {
		t.Error("PreReadProposal must refuse a decided proposal")
	}
}
