package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Agent operations (condocv2 Step1 Revision J): the system tab's "agent
// operations" section, below "agents & models" -- the AI agent calls running
// on this host right now. clauditable, which wraps every ambiguous-agent
// call, writes a {ts}[-<host>][-s]-writing.txt into the call's session
// directory under AGENT_RECORDS_PATH when it dispatches and removes it once
// the call's record is written, so a writing file's lifetime is the call's.
// LR polls for them; a call whose file has gone is kept, as finished, for
// agentOpsHold, then dropped.

const (
	agentOpsPollInterval = 3 * time.Second
	agentOpsHold         = 5 * time.Minute
	// agentOpsDirWindow skips session directories untouched for this long --
	// creating a writing file touches its directory, so only a call running
	// longer than this would be missed.
	agentOpsDirWindow = 24 * time.Hour
	agentOpsCmdMax    = 300
)

// AgentOp is one agent call. EndedAt is 0 while it runs.
type AgentOp struct {
	ID          string `json:"id"` // <session>/<record prefix>
	Session     string `json:"session"`
	SessionName string `json:"session_name,omitempty"`
	Agent       string `json:"agent"`
	Model       string `json:"model,omitempty"`
	Command     string `json:"command"` // first agentOpsCmdMax bytes
	StartedAt   int64  `json:"started_at"`
	EndedAt     int64  `json:"ended_at,omitempty"`
}

type agentOpsWatch struct {
	root   string
	notify func()

	mu  sync.Mutex
	ops map[string]AgentOp
}

func newAgentOpsWatch(root string, notify func()) *agentOpsWatch {
	return &agentOpsWatch{root: root, notify: notify, ops: map[string]AgentOp{}}
}

// snapshot lists the calls newest first; nil-safe, never null.
func (w *agentOpsWatch) snapshot() []AgentOp {
	out := []AgentOp{}
	if w == nil {
		return out
	}
	w.mu.Lock()
	for _, op := range w.ops {
		out = append(out, op)
	}
	w.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt > out[j].StartedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (w *agentOpsWatch) watchLoop() {
	for {
		w.scan(time.Now())
		time.Sleep(agentOpsPollInterval)
	}
}

// scan reconciles w.ops with the writing files present at now, notifying
// when anything started, finished or was dropped.
func (w *agentOpsWatch) scan(now time.Time) {
	seen := map[string]string{} // id -> writing file
	sessions, _ := os.ReadDir(w.root)
	for _, s := range sessions {
		if !s.IsDir() {
			continue
		}
		if info, err := s.Info(); err != nil || now.Sub(info.ModTime()) > agentOpsDirWindow {
			continue
		}
		dir := filepath.Join(w.root, s.Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			prefix, ok := strings.CutSuffix(f.Name(), "-writing.txt")
			if !ok {
				continue
			}
			// A secondary's file is renamed to -s-writing.txt 200ms in; it's
			// the same call.
			prefix = strings.TrimSuffix(prefix, "-s")
			seen[s.Name()+"/"+prefix] = filepath.Join(dir, f.Name())
		}
	}

	changed := false
	w.mu.Lock()
	for id, path := range seen {
		if _, known := w.ops[id]; known {
			continue
		}
		op := readAgentOp(id, path)
		if op.Agent == "none" {
			continue // a plain command federation-command wrapped
		}
		op.SessionName = readSessionYAMLName(filepath.Dir(path))
		w.ops[id] = op
		changed = true
	}
	for id, op := range w.ops {
		switch {
		case op.EndedAt == 0:
			if _, running := seen[id]; !running {
				op.EndedAt = now.Unix()
				w.ops[id] = op
				changed = true
			}
		case now.Sub(time.Unix(op.EndedAt, 0)) > agentOpsHold:
			delete(w.ops, id)
			changed = true
		}
	}
	w.mu.Unlock()
	if changed && w.notify != nil {
		w.notify()
	}
}

// readAgentOp parses a writing file: "<ts> <agent> <model>\n<command>\n".
// An older clauditable wrote only "<ts>", so agent may be "".
func readAgentOp(id, path string) AgentOp {
	session, prefix, _ := strings.Cut(id, "/")
	op := AgentOp{ID: id, Session: session}
	if ts, err := strconv.ParseInt(strings.SplitN(prefix, "-", 2)[0], 10, 64); err == nil {
		op.StartedAt = ts
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return op
	}
	head, cmd, _ := strings.Cut(string(data), "\n")
	if fields := strings.Fields(head); len(fields) == 3 {
		op.Agent = strings.TrimPrefix(fields[1], "-")
		op.Model = strings.TrimPrefix(fields[2], "-")
	}
	cmd = strings.TrimSpace(cmd)
	if len(cmd) > agentOpsCmdMax {
		cmd = strings.ToValidUTF8(cmd[:agentOpsCmdMax], "") + "…"
	}
	op.Command = cmd
	return op
}
