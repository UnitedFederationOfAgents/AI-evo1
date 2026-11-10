package main

// The visual tab's right-hand column: the local operations behind a condoc
// -- its agent invocations and its DTT executions (dTT-IntegrationImpls Step
// 1, Revision I). None of this is kept in the condoc's model (the pointer,
// the condoc files, the timeline record): it is read fresh from the records
// this host already has, and matched to the timeline by time.
//
//	agent  each work signal condocv2 wrote for the condoc (role
//	       "condocv2-<stem>", in ongoing/ or complete/), with what its
//	       slopspace deployment came back with
//	dtt    each image in the slopspace's readspaces/dtt-images: what a DTT
//	       tool call on the canvas returned
//	aside  each aside asked of the agent (aside.go), from this host's
//	       aside history
//
// Each hangs off the last commit before it started -- an interaction's agent
// off its prompt, a DTT run off its sub-iteration's proposal commit.

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	dkslop "dungeon-keeper/pkg/slopspace"
	dktypes "dungeon-keeper/pkg/types"
	"dungeon-keeper/pkg/worksignal"
)

const maxDTTOps = 100

// LocalOperation is one box in the right-hand column.
type LocalOperation struct {
	Kind   string `json:"kind"` // "agent", "dtt" or "aside"
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Status string `json:"status,omitempty"` // agent: pending, running, completed, failed
	Start  int64  `json:"start"`
	End    int64  `json:"end,omitempty"`
	Path   string `json:"path,omitempty"` // the record it was read from

	// The timeline node it hangs off (see anchorOps).
	AnchorLane  string `json:"anchorLane,omitempty"`
	AnchorKind  string `json:"anchorKind,omitempty"`
	Interaction int    `json:"interaction,omitempty"`
	Sub         int    `json:"sub,omitempty"`

	// aside: its number and the scope it ran in, which places it on the
	// visual even when the node it hangs off is another scope's.
	Number int    `json:"number,omitempty"`
	Step   string `json:"step,omitempty"`

	// agent: what the worker tab's visual finds its work submission by
	// (Swimlanes.tsx's openWork) -- the slopspace it ran in, the signal's
	// role and file name. Read from this host's records like the rest, so
	// never part of the condoc either.
	Slopspace string `json:"slopspace,omitempty"`
	Role      string `json:"role,omitempty"`
	Signal    string `json:"signal,omitempty"`
}

var sessionRe = regexp.MustCompile(`session: (\S+)`)

// localOperations reads the condoc's operations and anchors them on tl.
func (s *Server) localOperations(spec *PointerSpec, meta *dktypes.SlopspaceMetadata, tl Timeline, asides []Aside) []LocalOperation {
	ops := s.agentOps(spec, meta)
	if meta != nil {
		ops = append(ops, dttOps(meta)...)
	}
	ops = append(ops, asideOps(asides)...)
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].Start < ops[j].Start })
	anchorOps(ops, tl)
	return ops
}

// agentOps is one operation per work signal the condoc's turns wrote. A
// completed signal never changes, so its summary is cached.
func (s *Server) agentOps(spec *PointerSpec, meta *dktypes.SlopspaceMetadata) []LocalOperation {
	role := "condocv2-" + spec.Stem()
	var paths []string
	for _, list := range []func() ([]string, error){s.slop.signals.ListOngoing, s.slop.signals.ListComplete} {
		ps, _ := list()
		for _, p := range ps {
			if name, _, _, err := worksignal.ParseFilename(p); err == nil && name == role {
				paths = append(paths, p)
			}
		}
	}
	var ops []LocalOperation
	for _, p := range paths {
		_, _, complete, _ := worksignal.ParseFilename(p)
		if complete {
			s.slop.mu.Lock()
			op, ok := s.slop.opCache[p]
			s.slop.mu.Unlock()
			if ok {
				ops = append(ops, withDeployment(op, meta))
				continue
			}
		}
		signal, events, err := s.slop.signals.Read(p)
		if err != nil {
			continue
		}
		op := agentOp(p, signal, events)
		if complete {
			s.slop.mu.Lock()
			s.slop.opCache[p] = op
			s.slop.mu.Unlock()
		}
		ops = append(ops, withDeployment(op, meta))
	}
	return ops
}

func agentOp(path string, signal *dktypes.WorkSignal, events []dktypes.WorkEvent) LocalOperation {
	op := LocalOperation{
		Kind:   "agent",
		Title:  "agent",
		Status: string(signal.Status),
		Start:  signal.CreatedAt.Unix(),
		Path:   path,
		Role:   signal.Role,
		Signal: filepath.Base(path),
	}
	if signal.WorkType != dktypes.WorkTypeSlopspace {
		op.Title = "agent (PR drafting)"
	}
	if signal.CompletedAt != nil {
		op.End = signal.CompletedAt.Unix()
	}
	var detail []string
	if who := strings.Trim(signal.Agent+" "+signal.Model, " "); who != "" {
		detail = append(detail, who)
	}
	for _, e := range events {
		if m := sessionRe.FindStringSubmatch(stripANSI(e.Output)); m != nil {
			detail = append(detail, "session "+m[1])
			break
		}
	}
	op.Detail = strings.Join(detail, "\n")
	return op
}

// withDeployment adds the slopspace the agent ran in and what its deployment
// came back with: the last deployment made before its signal.
func withDeployment(op LocalOperation, meta *dktypes.SlopspaceMetadata) LocalOperation {
	if meta == nil || op.Title != "agent" {
		return op
	}
	op.Slopspace = meta.ID
	var dep *dktypes.Deployment
	for i := range meta.Deployments {
		if d := &meta.Deployments[i]; d.DeployedAt.Unix() <= op.Start {
			dep = d
		}
	}
	if dep == nil || len(dep.Proposals) == 0 {
		return op
	}
	line := fmt.Sprintf("%d DTT proposal(s)", len(dep.Proposals))
	if dep.Decision != "" {
		line += ": " + string(dep.Decision)
	} else {
		line += ": awaiting review"
	}
	if op.Detail != "" {
		op.Detail += "\n"
	}
	op.Detail += line
	return op
}

// asideOps is one operation per aside (aside.go), read from its history file
// rather than its work signal: one still queued has none yet. It starts when
// its agent did, or, queued, when it was asked.
func asideOps(asides []Aside) []LocalOperation {
	var ops []LocalOperation
	for _, a := range asides {
		status := a.Status
		if status == asideQueued {
			status = "pending"
		}
		op := LocalOperation{
			Kind:   "aside",
			Title:  "aside",
			Status: status,
			Start:  a.AskedAt,
			End:    a.CompletedAt,
			Number: a.Number,
			Step:   a.Step,
			Role:   a.Role,
		}
		if a.StartedAt != 0 {
			op.Start = a.StartedAt
		}
		if a.Role != "" {
			op.Signal = worksignal.GenerateFilename(a.Role, time.Unix(a.SignalAt, 0), a.CompletedAt != 0)
		}
		detail := []string{fmt.Sprintf("aside %d (read-only)", a.Number)}
		if who := strings.Trim(a.Agent+" "+a.Model, " "); who != "" {
			detail = append(detail, who)
		}
		detail = append(detail, firstLine(a.Prompt))
		op.Detail = strings.Join(detail, "\n")
		ops = append(ops, op)
	}
	return ops
}

// dttOps is one operation per image in the slopspace's dtt-images -- in the
// copy kept beside it while it is deployed.
func dttOps(meta *dktypes.SlopspaceMetadata) []LocalOperation {
	dir := filepath.Join(meta.RootPath, dkslop.ReadSpacesDir, dkslop.DTTImagesDir)
	if meta.Deployed {
		dir = filepath.Join(meta.RootPath, dkslop.KeptReadSpacesDir, dkslop.DTTImagesDir)
	}
	var ops []LocalOperation
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || len(ops) >= maxDTTOps {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		ops = append(ops, LocalOperation{
			Kind:   "dtt",
			Title:  "dtt " + filepath.ToSlash(rel),
			Detail: "image " + info.ModTime().UTC().Format(time.RFC3339),
			Start:  info.ModTime().Unix(),
			Path:   filepath.Join("readspaces", dkslop.DTTImagesDir, filepath.ToSlash(rel)),
		})
		return nil
	})
	return ops
}

// anchorOps hangs each operation off the last pointer or condoc node at or
// before its start, or the first node when it started before them all.
func anchorOps(ops []LocalOperation, tl Timeline) {
	var nodes []TimelineEvent
	for _, e := range tl.Events {
		// An external change (external.go), a step (steps.go) or a resource
		// (resources.go) is nothing condocv2 ran.
		if !e.Pending && e.Time > 0 && !isBetweenKind(e.Kind) && (e.Lane == "pointer" || e.Lane == "condoc") {
			nodes = append(nodes, e)
		}
	}
	if len(nodes) == 0 {
		return
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Time != nodes[j].Time {
			return nodes[i].Time < nodes[j].Time
		}
		return eventSlot(nodes[i]) < eventSlot(nodes[j])
	})
	for i := range ops {
		at, found := nodes[0], false
		for _, n := range nodes {
			if n.Time > ops[i].Start {
				break
			}
			// An aside hangs off its own scope's last node (aside.go).
			if ops[i].Kind == "aside" && n.Step != ops[i].Step {
				continue
			}
			at, found = n, true
		}
		if !found && ops[i].Kind == "aside" {
			continue // none yet in its scope: the visual places it unhung
		}
		// A pointer lock's condoc commit follows within seconds; hang the
		// operation off that node, as the sketch draws it: an agent off its
		// prompt, a DTT run off its sub-iteration's proposal. A run's image
		// is written before its result commit, maybe within the same second.
		switch {
		case at.Lane == "pointer" && at.Kind == "lock" && at.Sub > 0, at.Kind == "result":
			at.Lane, at.Kind = "condoc", "dtt"
		case at.Lane == "pointer" && at.Kind == "lock":
			at.Lane, at.Kind = "condoc", "prompt"
		}
		ops[i].AnchorLane, ops[i].AnchorKind, ops[i].Interaction, ops[i].Sub = at.Lane, at.Kind, at.Interaction, at.Sub
	}
}
