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
	// agentOpsRespMax keeps the end of a finished call's response, where an
	// agent's final answer is.
	agentOpsRespMax = 16 << 10
	// Separators in clauditable's processed record (clauditable/pkg/records):
	// the raw file follows the first, its response the second.
	agentOpsWrittenSep  = "\n----------WRITTEN_RAW----------\n"
	agentOpsResponseSep = "\n\n----------RESPONSE----------\n\n"
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
	// Response is the finished call's redacted response, from its
	// -processed.txt (last agentOpsRespMax bytes); "" while running.
	Response string `json:"response,omitempty"`
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
	ended := []string{}
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
				ended = append(ended, id)
				changed = true
			}
		case now.Sub(time.Unix(op.EndedAt, 0)) > agentOpsHold:
			delete(w.ops, id)
			changed = true
		}
	}
	w.mu.Unlock()
	// clauditable writes -processed.txt before removing the writing file, so
	// a finished call's response is already there.
	for _, id := range ended {
		resp := readAgentOpResponse(w.root, id)
		w.mu.Lock()
		if op, ok := w.ops[id]; ok {
			op.Response = resp
			w.ops[id] = op
		}
		w.mu.Unlock()
	}
	if changed && w.notify != nil {
		w.notify()
	}
}

// readAgentOpResponse reads a finished call's response from its processed
// record: -processed.txt, or -s-processed.txt for a secondary not yet folded.
func readAgentOpResponse(root, id string) string {
	session, prefix, _ := strings.Cut(id, "/")
	var data []byte
	for _, suffix := range []string{"-processed.txt", "-s-processed.txt"} {
		var err error
		if data, err = os.ReadFile(filepath.Join(root, session, prefix+suffix)); err == nil {
			break
		}
	}
	s := string(data)
	if i := strings.Index(s, agentOpsWrittenSep); i >= 0 {
		s = s[i+len(agentOpsWrittenSep):]
	}
	_, resp, ok := strings.Cut(s, agentOpsResponseSep)
	if !ok {
		return ""
	}
	resp = strings.TrimSpace(resp)
	if len(resp) > agentOpsRespMax {
		resp = "…" + strings.ToValidUTF8(resp[len(resp)-agentOpsRespMax:], "")
	}
	return resp
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
