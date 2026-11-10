package main

// The visual tab's swimlanes, read back from git: one lane per repo -- the
// pointer repo, the condoc repo, then each target -- and a node per commit
// condocv2 made for this condoc, recognized by its "condocv2: <Name> <kind>[
// <n>]" subject (see commitMsg):
//
//	pointer: propose, begin, then lock / unlock around each interaction N,
//	         and around each of its sub-iterations N.k (one per DTT
//	         proposal, see review.go)
//	condoc:  begin, then prompt / response / record for each interaction,
//	         and dtt / result for each sub-iteration
//	targets: their branch point (begin), then the commit each interaction made
//	read-only repos (readonly.go): a resolve for each new commit pulled --
//	         at begin, and by an interaction's deploy or return -- read from
//	         the pointer's ReadResolutions, not from the repo's history
//	external changes (external.go): the pointer's "external N.e" commit,
//	         and a node in each condoc or target lane whose branch was found
//	         moved from outside, read from the pointer's Externals -- their
//	         own section between interactions N and N+1
//	steps (steps.go): the pointer's and the condoc's "step <path>" and
//	         "step-complete <path>" commits -- entering a step after
//	         interaction N, leaving it after M -- each their own section too
//	resources (resources.go): the condoc's and the pointer's "resource N.R"
//	         commits -- resource R added after interaction N -- a section
//	         of its own too
//
// Every event is marked with the scope it belongs to (TimelineEvent.Step,
// see scopeTimeline), so the visual tab can draw one scope at a time.
//
// Events are ordered by slot -- propose, begin, then each interaction's
// lock, prompt, response, commits, record, unlock, then each of its
// sub-iterations' lock, dtt, result, unlock -- so the frontend can lay them
// out like the hand-drawn diagram (sloppo condocs/privateRepoOpsImpls,
// Step 4's Resource 2). An interaction or sub-iteration in flight adds one
// pending node where it goes next.
//
// Some subjects carry a detail after the kind, which becomes the event's
// note: "response-dtt N" (a response that proposed DTT tool calls),
// "dtt-read|dtt-assert|dtt-delete|dtt-reject|dtt-preread N.k" (kind dtt; a
// preread is a write proposal read before it is decided), "dtt-image"
// and "dtt-failed N.k" (kind result: ok or failed). Condocs held before the
// sub-iterations have "hold N", "review-<decision> N" and "release N"
// instead; they are still drawn. After them come the completion commits
// (complete, redraft, drafted, prs, merged -- see complete.go). The timeline
// record is saved at complete, so after the merge they are added to it from
// the pointer's history and the merged commit's finalization
// (withFinalization), along with each repo's merge commit (kind merged).

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	dktypes "dungeon-keeper/pkg/types"
)

// TimelineLane is one swimlane.
type TimelineLane struct {
	ID   string `json:"id"`   // "pointer", "condoc", or a target's or read-only repo's owner/name
	Repo string `json:"repo"` // owner/name
	Role string `json:"role"` // "pointer", "condoc", "target" or "readonly"
}

// TimelineEvent is one node: a commit, a branch point, or a pending step.
type TimelineEvent struct {
	Lane        string `json:"lane"`
	Kind        string `json:"kind"` // propose, begin, lock, prompt, response, commit, record, unlock, dtt, result, resolve, external, step, step-complete, resource (and hold, review, release)
	Interaction int    `json:"interaction,omitempty"`
	Sub         int    `json:"sub,omitempty"`     // k of sub-iteration N.k: a lock, dtt, result or unlock of one DTT proposal
	Seq         int    `json:"seq,omitempty"`     // e of external change N.e (see external.go), R of resource N.R (resources.go)
	Commits     int    `json:"commits,omitempty"` // external: how many commits came in
	SHA         string `json:"sha,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Time        int64  `json:"time,omitempty"`
	URL         string `json:"url,omitempty"`
	Pending     bool   `json:"pending,omitempty"` // not there yet: where the interaction in flight goes next
	Note        string `json:"note,omitempty"`
	Path        string `json:"path,omitempty"` // step, step-complete: the step entered or left ("1", "1-1", ...)
	Step        string `json:"step,omitempty"` // the scope it belongs to ("" the main scope, see steps.go)
}

// Timeline is the visual tab's data.
type Timeline struct {
	Lanes  []TimelineLane  `json:"lanes"`
	Events []TimelineEvent `json:"events"`
	Errors []string        `json:"errors,omitempty"` // lanes that couldn't be read

	// Source is "record" once the slopspace is gone and the timeline is the
	// one saved on completion (see record.go), at RecordURL.
	Source    string `json:"source,omitempty"`
	RecordURL string `json:"recordUrl,omitempty"`
}

// A target's commit carries the condoc tag before the name (see
// targetCommitMsg); older ones don't. A step's commits carry its path
// ("step 1-2", see steps.go) where the others carry N or N.k.
var timelineSubjectRe = regexp.MustCompile(`^condocv2: (?:\[condocv2-[0-9a-z]{4}\] )?(\S+) ([a-z-]+)(?: (\d+(?:-\d+)*)(?:\.(\d+))?)?\s*$`)

func isStepKind(kind string) bool {
	return kind == "step" || kind == "step-complete"
}

// isBetweenKind is an event that is a section of its own between
// interactions: an external change, a step entered or left, or a resource.
func isBetweenKind(kind string) bool {
	return kind == "external" || kind == "resource" || isStepKind(kind)
}

// timelineKindSlot orders an interaction's events.
var timelineKindSlot = map[string]int{
	"lock": 0, "prompt": 1, "response": 2, "commit": 3, "record": 4, "unlock": 5,
	"hold": 6, "review": 7, "release": 8,
}

// toolKindSlot orders a sub-iteration's events, after its interaction's.
var toolKindSlot = map[string]int{"lock": 0, "dtt": 1, "result": 2, "unlock": 3}

const (
	interactionSlots = 1000 // slots per interaction: its own, then its sub-iterations'
	toolSlotsFrom    = 100
)

// timelineKind splits a subject's kind into the event's kind and note (see
// the top of this file).
func timelineKind(kind string) (string, string) {
	switch {
	case kind == "response-dtt":
		return "response", "dtt"
	case kind == "dtt-image":
		return "result", "ok"
	case kind == "dtt-failed":
		return "result", "failed"
	case strings.HasPrefix(kind, "dtt-"):
		return "dtt", strings.TrimPrefix(kind, "dtt-")
	case strings.HasPrefix(kind, "review-"):
		return "review", strings.TrimPrefix(kind, "review-")
	}
	return kind, ""
}

// completionKindSlot orders the completion commits, after every interaction
// (see complete.go).
var completionKindSlot = map[string]int{"complete": 0, "redraft": 1, "drafted": 2, "prs": 3, "merged": 4}

func eventSlot(e TimelineEvent) int {
	switch e.Kind {
	case "propose":
		return 0
	case "begin":
		return 1
	}
	if s, ok := completionKindSlot[e.Kind]; ok {
		return 1<<30 + s
	}
	if e.Kind == "resolve" && e.Interaction == 0 {
		return 1 // begin's
	}
	if isBetweenKind(e.Kind) {
		// After interaction N and its sub-iterations, before N+1's lock.
		return 1 + e.Interaction*interactionSlots
	}
	base :=2 + (e.Interaction-1)*interactionSlots
	if e.Kind == "resolve" {
		// A deploy's commit is what the response saw; a return's comes after,
		// as does a configure's (made after interaction N, see configure.go).
		if e.Note == readAtReturn || e.Note == readAtConfigure {
			return base + timelineKindSlot["record"]
		}
		return base + timelineKindSlot["response"]
	}
	if e.Sub > 0 {
		return base + toolSlotsFrom + e.Sub*len(toolKindSlot) + toolKindSlot[e.Kind]
	}
	return base + timelineKindSlot[e.Kind]
}

type gitCommit struct {
	sha     string
	time    int64
	subject string
}

const gitLogFormat = "--format=%H%x1f%ct%x1f%s"

// parseGitLog reads "git log" output in gitLogFormat.
func parseGitLog(out []byte) []gitCommit {
	var commits []gitCommit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		t, _ := strconv.ParseInt(parts[1], 10, 64)
		commits = append(commits, gitCommit{sha: parts[0], time: t, subject: parts[2]})
	}
	return commits
}

func commitURL(repo, sha string) string {
	return repoCommitURL(repo, sha)
}

// timelineEvents turns a lane's commits into events, keeping the subjects
// of kinds that belong on it.
func timelineEvents(name, lane, repo string, commits []gitCommit, kinds ...string) []TimelineEvent {
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	var events []TimelineEvent
	for _, c := range commits {
		m := timelineSubjectRe.FindStringSubmatch(c.subject)
		if m == nil || m[1] != name {
			continue
		}
		kind, note := timelineKind(m[2])
		if !want[kind] {
			continue
		}
		n, _ := strconv.Atoi(m[3])
		k, _ := strconv.Atoi(m[4])
		e := TimelineEvent{
			Lane: lane, Kind: kind, Interaction: n, Sub: k, Note: note,
			SHA: c.sha, Subject: c.subject, Time: c.time, URL: commitURL(repo, c.sha),
		}
		switch {
		case kind == "external" || kind == "resource":
			e.Sub, e.Seq = 0, k // N.e and N.R are no sub-iterations
		case isStepKind(kind):
			// The step's path; scopeTimeline places it after an interaction.
			e.Path, e.Interaction, e.Sub = m[3], 0, 0
		}
		events = append(events, e)
	}
	return events
}

// pendingEvents is where an interaction in flight goes next, per its stage.
func pendingEvents(spec *PointerSpec) []TimelineEvent {
	if isFinalPhase(spec.Phase) {
		return nil // an abandoned condoc's lock goes nowhere
	}
	if h := spec.Hold; h != nil {
		// Waiting for the human is drawn on the interaction's record node
		// ("?"); a sub-iteration in flight goes on.
		c := h.Call
		if c == nil {
			return nil
		}
		one := func(lane, kind, note string) []TimelineEvent {
			return []TimelineEvent{{Lane: lane, Kind: kind, Interaction: h.Interaction, Sub: c.Sub, Pending: true, Note: note}}
		}
		switch c.Stage {
		case toolLocked:
			return one("condoc", "dtt", "committing the proposal")
		case toolProposed:
			if !c.runs() {
				return one("pointer", "unlock", "unlocking")
			}
			return one("condoc", "result", "running dtt "+c.Mode)
		case toolRan:
			return one("condoc", "result", "committing the image")
		case toolImaged:
			return one("pointer", "unlock", "unlocking")
		}
		return nil
	}
	l := spec.Lock
	if l == nil {
		return nil
	}
	n := l.Interaction
	one := func(lane, kind, note string) []TimelineEvent {
		return []TimelineEvent{{Lane: lane, Kind: kind, Interaction: n, Pending: true, Note: note}}
	}
	switch l.Stage {
	case stageLocked:
		return one("condoc", "prompt", "inserting the prompt")
	case stagePrompted:
		return one("condoc", "response", "waiting for Submit")
	case stageQueued:
		return one("condoc", "response", "queued for the agent")
	case stageDeployed, stageSignalled:
		return one("condoc", "response", "agent running")
	case stageReturned:
		return one("condoc", "response", "collecting the response")
	case stageResponded:
		var out []TimelineEvent
		for _, t := range spec.Targets {
			out = append(out, TimelineEvent{Lane: t, Kind: "commit", Interaction: n, Pending: true, Note: "committing"})
		}
		return out
	case stageMapped:
		return one("condoc", "record", "recording the commits")
	case stageRecorded:
		return one("pointer", "unlock", "unlocking")
	}
	return nil
}

// buildTimeline reads the condoc's lanes. meta is nil before the condoc has
// begun (only the pointer lane has anything then).
func (s *Server) buildTimeline(spec *PointerSpec, meta *dktypes.SlopspaceMetadata) Timeline {
	tl := Timeline{Lanes: []TimelineLane{}, Events: []TimelineEvent{}}

	pointerRepo := ""
	if cfg, _, err := s.pointerReadyPath(); err == nil {
		pointerRepo = normalizeRepo(cfg.Repo)
	}
	tl.Lanes = append(tl.Lanes,
		TimelineLane{ID: "pointer", Repo: pointerRepo, Role: "pointer"},
		TimelineLane{ID: "condoc", Repo: spec.CondocRepo, Role: "condoc"})
	for _, t := range spec.Targets {
		tl.Lanes = append(tl.Lanes, TimelineLane{ID: t, Repo: t, Role: "target"})
	}
	for _, r := range spec.ReadOnly {
		tl.Lanes = append(tl.Lanes, TimelineLane{ID: r.Repo, Repo: r.Repo, Role: "readonly"})
	}
	tl.Events = append(tl.Events, readResolutionEvents(spec)...)
	tl.Events = append(tl.Events, externalEvents(spec)...)

	// The pointer lane: the history of this condoc's pointer file.
	if pointerRepo != "" {
		_, path := s.acquirePointerOp()
		out, err := exec.Command("git", "-C", path, "log", "--reverse", gitLogFormat, "--",
			pointerDir+"/"+spec.Stem()+".yaml").Output()
		s.endPointerOp()
		if err != nil {
			tl.Errors = append(tl.Errors, fmt.Sprintf("pointer: git log: %v", err))
		} else {
			tl.Events = append(tl.Events, timelineEvents(spec.Name, "pointer", pointerRepo, parseGitLog(out),
				"propose", "begin", "lock", "unlock", "hold", "release", "external", "step", "step-complete", "resource",
				"complete", "redraft", "drafted", "prs", "merged")...)
		}
	}

	// The condoc and target lanes: each writespace repo's branch since it
	// began.
	if meta != nil {
		bases := repoShaMap(spec.Bases)
		for _, repo := range spec.Repos() {
			lane, kinds := repo, []string{"commit"}
			if repo == spec.CondocRepo {
				lane, kinds = "condoc", []string{"begin", "prompt", "response", "record", "dtt", "result", "review", "step", "step-complete", "resource", "complete", "prs"}
			}
			rangeArg := "HEAD"
			base := bases[repo]
			if base != "" {
				rangeArg = base + "..HEAD"
			}
			if lane != "condoc" && base != "" {
				// A target has no begin commit: its branch begins where it
				// was cut.
				tl.Events = append(tl.Events, TimelineEvent{
					Lane: lane, Kind: "begin", SHA: base, URL: commitURL(repo, base), Note: "branch point",
				})
			}
			out, err := exec.Command("git", "--git-dir", slopGitDir(meta, repo), "log", "--reverse", "--first-parent",
				gitLogFormat, rangeArg).Output()
			if err != nil {
				tl.Errors = append(tl.Errors, fmt.Sprintf("%s: git log: %v", repo, err))
				continue
			}
			tl.Events = append(tl.Events, timelineEvents(spec.Name, lane, repo, parseGitLog(out), kinds...)...)
		}
	}

	// Where the interaction in flight goes next, unless its commit has
	// landed already and only the stage write is behind.
	for _, p := range pendingEvents(spec) {
		landed := false
		for _, e := range tl.Events {
			if e.Lane == p.Lane && e.Kind == p.Kind && e.Interaction == p.Interaction && e.Sub == p.Sub {
				landed = true
				break
			}
		}
		if !landed {
			tl.Events = append(tl.Events, p)
		}
	}

	tl = spec.scopeTimeline(tl)
	sortTimeline(tl.Events)
	return tl
}

// readResolutionEvents is a resolve node for each new commit a read-only
// repo was pulled at, in its lane (see readonly.go).
func readResolutionEvents(spec *PointerSpec) []TimelineEvent {
	readOnly := map[string]ReadOnlyRepo{}
	for _, r := range spec.ReadOnly {
		readOnly[strings.ToLower(r.Repo)] = r
	}
	var events []TimelineEvent
	for _, r := range spec.ReadResolutions {
		ro, ok := readOnly[strings.ToLower(r.Repo)]
		if !ok {
			continue // no lane for it
		}
		// The ref it followed then (resolutions recorded before refs could be
		// configured don't say: it was the one it follows now).
		lane, ref := ro.Repo, refName(ro.Ref)
		if r.Ref != "" {
			ref = r.Ref
		}
		events = append(events, TimelineEvent{
			Lane: lane, Kind: "resolve", Interaction: r.Interaction, Note: r.At,
			SHA: r.SHA, Time: r.Time, URL: commitURL(r.Repo, r.SHA),
			Subject: fmt.Sprintf("read-only %s: %s resolved to a new commit (%s)", r.Repo, ref, r.At),
		})
	}
	return events
}

// sortTimeline orders events by slot, then time.
func sortTimeline(events []TimelineEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		si, sj := eventSlot(events[i]), eventSlot(events[j])
		if si != sj {
			return si < sj
		}
		return events[i].Time < events[j].Time
	})
}

// withFinalization adds to a merged condoc's timeline (its record's, which
// stops before complete) the completion that followed: the pointer's
// completion commits, from its history, and the other lanes' -- the condoc's
// complete and prs, each repo's merge -- kept by the merged commit
// (spec.Finalization).
func (s *Server) withFinalization(spec *PointerSpec, tl Timeline) Timeline {
	// A record's timeline is cached (see timelineRecord): don't touch it.
	tl.Events = append([]TimelineEvent{}, tl.Events...)
	tl.Errors = append([]string(nil), tl.Errors...)
	have := map[string]bool{}
	key := func(e TimelineEvent) string { return e.Lane + "|" + e.Kind + "|" + e.SHA }
	for _, e := range tl.Events {
		have[key(e)] = true
	}
	add := func(e TimelineEvent) {
		if !have[key(e)] {
			have[key(e)] = true
			tl.Events = append(tl.Events, e)
		}
	}
	pointer := s.buildTimeline(spec, nil)
	tl.Errors = append(tl.Errors, pointer.Errors...)
	for _, e := range pointer.Events {
		if _, ok := completionKindSlot[e.Kind]; ok && e.Lane == "pointer" {
			add(e)
		}
	}
	if f := spec.Finalization; f != nil {
		for _, fe := range f.Events {
			e := TimelineEvent{Lane: fe.Lane, Kind: fe.Kind, SHA: fe.SHA, Time: fe.Time}
			if fe.SHA != "" {
				e.URL = commitURL(fe.Repo, fe.SHA)
			}
			if fe.PR != "" {
				e.Note = "merged " + fe.PR
			}
			add(e)
		}
	}
	sortTimeline(tl.Events)
	return tl
}
