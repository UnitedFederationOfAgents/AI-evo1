package main

// The visual tab's swimlanes, read back from git: one lane per repo -- the
// pointer repo, the condoc repo, then each target -- and a node per commit
// condocv2 made for this condoc, recognized by its "condocv2: <Name> <kind>[
// <n>]" subject (see commitMsg):
//
//	pointer: propose, begin, then lock / unlock around each interaction
//	condoc:  begin, then prompt / response / record for each interaction
//	targets: their branch point (begin), then the commit each interaction made
//
// Events are ordered by slot -- propose, begin, then each interaction's
// lock, prompt, response, commits, record, unlock -- so the frontend can lay
// them out like the hand-drawn diagram. An interaction in flight adds one
// pending node where it goes next.

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
	ID   string `json:"id"`   // "pointer", "condoc", or a target's owner/name
	Repo string `json:"repo"` // owner/name
	Role string `json:"role"` // "pointer", "condoc" or "target"
}

// TimelineEvent is one node: a commit, a branch point, or a pending step.
type TimelineEvent struct {
	Lane        string `json:"lane"`
	Kind        string `json:"kind"` // propose, begin, lock, prompt, response, commit, record, unlock
	Interaction int    `json:"interaction,omitempty"`
	SHA         string `json:"sha,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Time        int64  `json:"time,omitempty"`
	URL         string `json:"url,omitempty"`
	Pending     bool   `json:"pending,omitempty"` // not there yet: where the interaction in flight goes next
	Note        string `json:"note,omitempty"`
}

// Timeline is the visual tab's data.
type Timeline struct {
	Lanes  []TimelineLane  `json:"lanes"`
	Events []TimelineEvent `json:"events"`
	Errors []string        `json:"errors,omitempty"` // lanes that couldn't be read
}

var timelineSubjectRe = regexp.MustCompile(`^condocv2: (\S+) ([a-z-]+)(?: (\d+))?\s*$`)

// timelineKindSlot orders an interaction's events.
var timelineKindSlot = map[string]int{"lock": 0, "prompt": 1, "response": 2, "commit": 3, "record": 4, "unlock": 5}

func eventSlot(e TimelineEvent) int {
	switch e.Kind {
	case "propose":
		return 0
	case "begin":
		return 1
	}
	return 2 + (e.Interaction-1)*len(timelineKindSlot) + timelineKindSlot[e.Kind]
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
	return fmt.Sprintf("https://github.com/%s/commit/%s", repo, sha)
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
		if m == nil || m[1] != name || !want[m[2]] {
			continue
		}
		n, _ := strconv.Atoi(m[3])
		events = append(events, TimelineEvent{
			Lane: lane, Kind: m[2], Interaction: n,
			SHA: c.sha, Subject: c.subject, Time: c.time, URL: commitURL(repo, c.sha),
		})
	}
	return events
}

// pendingEvents is where an interaction in flight goes next, per its stage.
func pendingEvents(spec *PointerSpec) []TimelineEvent {
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
				"propose", "begin", "lock", "unlock")...)
		}
	}

	// The condoc and target lanes: each writespace repo's branch since it
	// began.
	if meta != nil {
		bases := repoShaMap(spec.Bases)
		for _, repo := range spec.Repos() {
			lane, kinds := repo, []string{"commit"}
			if repo == spec.CondocRepo {
				lane, kinds = "condoc", []string{"begin", "prompt", "response", "record"}
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
			if e.Lane == p.Lane && e.Kind == p.Kind && e.Interaction == p.Interaction {
				landed = true
				break
			}
		}
		if !landed {
			tl.Events = append(tl.Events, p)
		}
	}

	sort.SliceStable(tl.Events, func(i, j int) bool {
		si, sj := eventSlot(tl.Events[i]), eventSlot(tl.Events[j])
		if si != sj {
			return si < sj
		}
		return tl.Events[i].Time < tl.Events[j].Time
	})
	return tl
}
