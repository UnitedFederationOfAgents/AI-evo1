package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The demo tab's review sub-tab (PrivateRepoOps Step 5): point at a demo
// record and have the worker's agent analyse it, read-only, in place. The
// review is a work signal like any other -- dungeon-keeper's submit, role
// demo-review, mode read, its work location the record's own directory
// (Server.submitDemoReview) -- so it also shows on the worker tab. The
// agent reads the record's files there, starting from MAP.md: record.json,
// demo.dtt.yaml, artifacts/ (summary, image, outputs, log, screenshots) and
// analysis/, the frames pulled from the videos that stand in for them
// (demoanalysis.go).
//
// Each review is kept with the record, in reviews/: <id>.json (DemoReview)
// from when it's asked for, and <id>.md with the agent's reply once the
// worker says it's done. The worker's state is matched back to a review by
// its role and work location (syncDemoReviews), whenever that state
// changes (workerStateChanged) and whenever the reviews are listed.

const (
	demoReviewsDir    = "reviews"
	maxDemoQuestion   = 4 << 10
	maxRelayedReview  = 8 << 10 // a review's output, through agent-coordinator
	reviewClaimWindow = 10 * time.Second
	// reviewUnclaimedAfter is how long a review waits for the worker to
	// list its signal before it's taken as refused.
	reviewUnclaimedAfter = 2 * time.Minute
)

// DemoReview is one review of a demo record. Status is submitted (handed to
// the worker, which hasn't listed it yet), pending, processing, completed
// or failed -- the work signal's, once it has one (SignalID).
type DemoReview struct {
	ID            string `json:"id"`
	Record        string `json:"record"`
	Question      string `json:"question,omitempty"`
	Agent         string `json:"agent,omitempty"`
	Model         string `json:"model,omitempty"`
	Status        string `json:"status"`
	SignalID      string `json:"signal_id,omitempty"`
	SignalFile    string `json:"signal_file,omitempty"`
	RequestedAt   int64  `json:"requested_at"`
	StartedAt     int64  `json:"started_at,omitempty"`
	FinishedAt    int64  `json:"finished_at,omitempty"`
	Comment       string `json:"comment,omitempty"`
	Output        string `json:"output,omitempty"`
	OutputTrimmed bool   `json:"output_trimmed,omitempty"`
	Error         string `json:"error,omitempty"`
	Prompt        string `json:"prompt,omitempty"`
}

func (r DemoReview) finished() bool { return r.Status == "completed" || r.Status == "failed" }

// demoReviewPrompt is what the worker's agent is asked about rec: where
// everything is in its directory, what the run says of itself, and what
// to report.
func demoReviewPrompt(rec DemoRecord, question string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Review the record of a demo run, read-only. Your working directory is the record's directory, %s. "+
		"Do not create, change or delete any file anywhere: read, look and think, then give your analysis as your reply.\n\n", rec.Dir)
	b.WriteString("What is in the directory:\n")
	b.WriteString("- MAP.md: the record's map -- read it first. It lists every file lightest first, with sizes, and has tables of the frames pulled from the videos: each one's time, the step it shows and that step's result. Use it to choose what to read next rather than reading everything.\n")
	b.WriteString("- analysis/: frames from the videos, standing in for them. Look at the contact sheets (analysis/contact-sheet.jpg, and analysis/failure/*/contact-sheet.jpg when the demo failed) before single frames; open single frames (analysis/steps/, scenes/, failure/) only where a step needs a closer look.\n")
	b.WriteString("- artifacts/summary.md: the record in words.\n")
	b.WriteString("- record.json: the run as local-representative kept it (status, phases, videos, artifacts).\n")
	b.WriteString("- demo.dtt.yaml: the DTT composition that ran. Its phases call dtt's examples (lead-in, subject, tear-down), then compose a video.\n")
	b.WriteString("- artifacts/image.json: the image dtt returned -- every object's state, phase by phase, with each call's own image nested in it. It's large: search it rather than read it whole.\n")
	b.WriteString("- artifacts/outputs.txt: what each control sequence run printed, and its error if it had one.\n")
	b.WriteString("- artifacts/log.txt: the end of dtt's log.\n")
	b.WriteString("- artifacts/screenshots/: screenshots the robot took during the demo.\n")
	b.WriteString("- videos/: the videos the run rendered. You can't watch them; the frames in analysis/ stand in for them.\n")
	if p := primaryRender(rec.Renders); p != nil {
		where := "it wasn't kept with the record"
		if p.Kept != "" {
			where = "videos/" + p.Kept
		}
		fmt.Fprintf(&b, "  The primary video, the demo's subject without lead-in and tear-down, is %s (%s).\n", where, p.Composition)
	}
	fmt.Fprintf(&b, "\nThe run: demo %s (%q), mode %s, status %s, on %s.", rec.Demo, rec.Title, rec.Mode, rec.Status, rec.Node)
	if rec.Suite != "" {
		fmt.Fprintf(&b, " One demo of suite %s (run %s).", rec.Suite, rec.SuiteRecord)
	}
	if rec.Error != "" {
		fmt.Fprintf(&b, " Its error: %s", rec.Error)
	}
	b.WriteString("\n\nReport, briefly and with evidence from the files:\n")
	b.WriteString("1. Whether the demo did what it set out to do -- above all its subject phase.\n")
	b.WriteString("2. Anything that failed or looks wrong, and its most likely cause.\n")
	b.WriteString("3. What would make the demo or its sequences more robust.\n")
	b.WriteString("4. What a viewer of the primary video sees, judging from its frames, and whether each step's frame matches the result the step claimed.\n")
	if q := strings.TrimSpace(question); q != "" {
		fmt.Fprintf(&b, "\nThe person who asked for this review also asks:\n%s\n", q)
	}
	return b.String()
}

func reviewsDir(recDir string) string { return filepath.Join(recDir, demoReviewsDir) }

func writeDemoReview(recDir string, r DemoReview) error {
	return writeWorkerJob(reviewsDir(recDir), r, fmt.Sprintf("Review %s of %s", r.ID, r.Record))
}

// readDemoReviews lists recDir's reviews, newest first.
func readDemoReviews(recDir string) []DemoReview { return readWorkerJobs(reviewsDir(recDir)) }

// syncDemoReviews brings recDir's unfinished reviews up to date from the
// worker's signals (syncWorkerJobs).
func syncDemoReviews(recDir string, signals []WorkerSignalInfo) []DemoReview {
	return syncWorkerJobs(reviewsDir(recDir), recDir, demoReviewRole, signals, func(r DemoReview) { writeDemoReview(recDir, r) })
}

// ---- Read-only work handed to the worker ----
//
// A review of a record and a draft of a proposal (demoproposals.go) are
// both kept the same way: a DemoReview per job, <id>.json in a directory of
// its own, with <id>.md -- headed heading -- holding the agent's reply once
// it's done; the worker's signals are matched back to them by role and
// work location.

func writeWorkerJob(dir string, r DemoReview, heading string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, r.ID+".json.tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, r.ID+".json")); err != nil {
		return err
	}
	if r.finished() && strings.TrimSpace(r.Output+r.Comment) != "" {
		var md strings.Builder
		fmt.Fprintf(&md, "# %s\n\n", heading)
		fmt.Fprintf(&md, "- status: %s\n", r.Status)
		if r.Question != "" {
			fmt.Fprintf(&md, "- question: %s\n", r.Question)
		}
		if r.Comment != "" {
			fmt.Fprintf(&md, "- worker: %s\n", r.Comment)
		}
		if r.OutputTrimmed {
			fmt.Fprintf(&md, "- the reply is trimmed here; the work signal %s has all of it\n", r.SignalFile)
		}
		md.WriteString("\n" + r.Output + "\n")
		os.WriteFile(filepath.Join(dir, r.ID+".md"), []byte(md.String()), 0o644)
	}
	return nil
}

// readWorkerJobs lists the jobs kept in dir, newest first.
func readWorkerJobs(dir string) []DemoReview {
	out := []DemoReview{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r DemoReview
		if json.Unmarshal(b, &r) == nil && r.ID != "" {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RequestedAt > out[j].RequestedAt })
	return out
}

// syncWorkerJobs brings the unfinished jobs kept in dir up to date from
// the worker's signals, writing each one that changes: one without a signal
// yet claims the oldest signal of role in workLocation made since it was
// asked for that no other job has; one with a signal takes its status and
// output.
func syncWorkerJobs(dir, workLocation, role string, signals []WorkerSignalInfo, write func(DemoReview)) []DemoReview {
	reviews := readWorkerJobs(dir)
	claimed := map[string]bool{}
	for _, r := range reviews {
		if r.SignalID != "" {
			claimed[r.SignalID] = true
		}
	}
	var ours []WorkerSignalInfo
	for _, s := range signals {
		if s.Role == role && filepath.Clean(s.WorkLocation) == filepath.Clean(workLocation) {
			ours = append(ours, s)
		}
	}
	sort.SliceStable(ours, func(i, j int) bool { return ours[i].CreatedAt < ours[j].CreatedAt })
	// Oldest request first, so each claims the signal made for it.
	for i := len(reviews) - 1; i >= 0; i-- {
		r := &reviews[i]
		if r.finished() {
			continue
		}
		before := *r
		if r.SignalID == "" {
			for _, s := range ours {
				if !claimed[s.ID] && s.CreatedAt >= r.RequestedAt-reviewClaimWindow.Milliseconds() {
					r.SignalID, r.SignalFile = s.ID, s.File
					claimed[s.ID] = true
					break
				}
			}
		}
		// The submit is fire-and-forget: a job the worker never listed (it
		// refused it, or wasn't there) is given up on.
		if r.SignalID == "" && time.Since(time.UnixMilli(r.RequestedAt)) > reviewUnclaimedAfter {
			r.Status, r.FinishedAt = "failed", time.Now().UnixMilli()
			r.Error = "the worker never listed this -- see dungeon-keeper's log for why it refused it"
		}
		for _, s := range ours {
			if s.ID != r.SignalID || r.SignalID == "" {
				continue
			}
			r.Status, r.StartedAt, r.FinishedAt = s.Status, s.StartedAt, s.CompletedAt
			r.Comment, r.Output, r.OutputTrimmed = s.Comment, s.Output, s.OutputTrimmed
			if s.File != "" {
				r.SignalFile = s.File
			}
		}
		if *r != before {
			write(*r)
		}
	}
	return reviews
}

// workerStateChanged syncs the reviews of each record the worker's
// demo-review signals point into, and each proposal its demo-proposal
// signals do (demoproposals.go, which copies a created proposal's demo into
// the library) -- only records and proposals
// under this host's demo root, so a signal can't have LR write anywhere
// else.
func (w *toolsWatch) workerStateChanged(st WorkerStateMsg) {
	root := w.demoDir()
	if root == "" {
		return
	}
	records := filepath.Clean(filepath.Join(root, demoRecordsDir))
	proposals := filepath.Clean(filepath.Join(root, demoProposalsDir))
	seen := map[string]bool{}
	for _, s := range st.Signals {
		if (s.Role != demoReviewRole && s.Role != demoProposalRole) || seen[s.Role+"\x00"+s.WorkLocation] {
			continue
		}
		seen[s.Role+"\x00"+s.WorkLocation] = true
		dir := filepath.Clean(s.WorkLocation)
		switch {
		case s.Role == demoReviewRole && filepath.Dir(dir) == records && demoRecordIDRe.MatchString(filepath.Base(dir)):
			w.reviewMu.Lock()
			syncDemoReviews(dir, st.Signals)
			w.reviewMu.Unlock()
		case s.Role == demoProposalRole && filepath.Dir(dir) == proposals && demoNameRe.MatchString(filepath.Base(dir)):
			w.reviewMu.Lock()
			w.syncProposal(dir, st.Signals)
			w.reviewMu.Unlock()
		}
	}
}

func (w *toolsWatch) currentWorkerSignals() []WorkerSignalInfo {
	if w.workerSignals == nil {
		return nil
	}
	return w.workerSignals()
}

// handleDemoReview answers demo-review (ask the worker to review
// req.Record) and demo-reviews (list its reviews); both answer the
// record's reviews, newest first.
func (w *toolsWatch) handleDemoReview(req ToolsRequest, fromAC bool, root string, reply ToolsReply) ToolsReply {
	fail := func(err error) ToolsReply {
		reply.Error = err.Error()
		return reply
	}
	rec, err := w.demoRecord(root, strings.TrimSpace(req.Record))
	if err != nil {
		return fail(err)
	}
	rec.LogTail = ""
	reply.DemoRecord = &rec
	w.reviewMu.Lock()
	defer w.reviewMu.Unlock()
	if req.Op == "demo-review" {
		if err := w.requestDemoReview(rec, req); err != nil {
			return fail(err)
		}
	}
	reply.OK, reply.DemoReviews = true, syncDemoReviews(rec.Dir, w.currentWorkerSignals())
	reply.Detail = fmt.Sprintf("%d review(s) of %s", len(reply.DemoReviews), rec.ID)
	if req.Op == "demo-review" {
		reply.Detail = fmt.Sprintf("asked the worker to review %s, read-only, in %s", rec.ID, rec.Dir)
	}
	for i := range reply.DemoReviews {
		reply.DemoReviews[i].Prompt = "" // in the review's file
	}
	if fromAC {
		fitReviewsReply(&reply)
	}
	return reply
}

// requestDemoReview hands the worker a review of rec, keeping it in
// reviews/ whether or not the worker could take it.
func (w *toolsWatch) requestDemoReview(rec DemoRecord, req ToolsRequest) error {
	if w.reviewSubmit == nil {
		return errors.New("there's no worker here to review records")
	}
	switch {
	case rec.Status == "running":
		return fmt.Errorf("demo record %s is still running -- review it once it ends", rec.ID)
	case !filepath.IsAbs(rec.Dir):
		return fmt.Errorf("demo record %s isn't at an absolute path (%s)", rec.ID, rec.Dir)
	case len(req.Question) > maxDemoQuestion:
		return fmt.Errorf("the question is too long (the limit is %d KiB)", maxDemoQuestion>>10)
	}
	now := time.Now()
	r := DemoReview{ID: "review-" + now.Format("20060102-150405.000"), Record: rec.ID, Question: strings.TrimSpace(req.Question),
		Agent: strings.TrimSpace(req.Agent), Model: strings.TrimSpace(req.Model), Status: "submitted", RequestedAt: now.UnixMilli()}
	r.ID = strings.ReplaceAll(r.ID, ".", "-")
	// The map is written again, so it counts the reviews so far -- and so a
	// record from before maps had one to start from.
	writeDemoMap(rec)
	r.Prompt = demoReviewPrompt(rec, r.Question)
	if err := w.reviewSubmit(r.Prompt, rec.Dir, r.Agent, r.Model); err != nil {
		r.Status, r.Error, r.FinishedAt = "failed", err.Error(), now.UnixMilli()
		writeDemoReview(rec.Dir, r)
		return err
	}
	return writeDemoReview(rec.Dir, r)
}

// fitReviewsReply cuts a reviews reply to fit the relay to
// agent-coordinator: each output to maxRelayedReview, then the oldest
// reviews.
func fitReviewsReply(reply *ToolsReply) {
	fits := func() bool {
		b, err := json.Marshal(reply)
		return err == nil && len(b) <= maxRelayedTools
	}
	if fits() {
		return
	}
	for i := range reply.DemoReviews {
		if r := &reply.DemoReviews[i]; len(r.Output) > maxRelayedReview {
			r.Output, r.OutputTrimmed = r.Output[:maxRelayedReview]+"…", true
		}
	}
	for !fits() && len(reply.DemoReviews) > 1 {
		reply.DemoReviews = reply.DemoReviews[:len(reply.DemoReviews)-1]
	}
	if reply.DemoRecord != nil && !fits() {
		reply.DemoRecord.Artifacts = nil
	}
	reply.Detail += " (cut to fit the relay -- the record's reviews/ on that host has all of it)"
}
