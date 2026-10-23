package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dungeon-keeper/pkg/types"
	"dungeon-keeper/pkg/worksignal"

	"representable"
	ufaversion "ufa-version"
)

// --auto-connect tuning. dungeon-keeper is a long-running daemon managed by
// local-representative (see local-representative/procman.go's "worker"
// entry), so unlike the-conversationalist it never gives up: it keeps
// retrying until LR answers, and again after every drop.
const (
	autoConnectInterval    = 10 * time.Second
	autoConnectDialTimeout = 3 * time.Second
)

// Limits on what one "worker-state" push carries. The full output stays in
// the signal file; LR only needs enough to show it. maxStateBytes keeps the
// whole push under representable's 64 KiB line limit -- see fitState.
const (
	maxStateCompleted  = 20
	maxStateOutput     = 32 * 1024
	maxStatePrompt     = 4 * 1024
	maxStateBytes      = 48 * 1024
	maxStateSlopspaces = 50
)

// submitRole is the role recorded on work submitted from LR's worker tab;
// commandRole on prompts from the slopspace manual control's command entry.
const (
	submitRole  = "worker-tab"
	commandRole = "slopspace-command"
)

// WorkerStateMsg is the "worker-state" data payload pushed to
// local-representative, which shows it on its worker tab.
type WorkerStateMsg struct {
	WorkerID  string             `json:"worker_id"`
	AgentType string             `json:"agent_type"`
	Busy      bool               `json:"busy"`
	Signals   []WorkerSignalInfo `json:"signals"` // ongoing first, then the most recent completed, newest first within each

	// The worker tab's slopspace view: every slopspace on this host, in use
	// first, then deployed, then newest first -- at most maxStateSlopspaces,
	// with how many more were left out.
	Slopspaces        []SlopspaceInfo `json:"slopspaces"`
	SlopspacesOmitted int             `json:"slopspaces_omitted,omitempty"`
}

// SlopspaceInfo summarises one slopspace for the worker tab.
type SlopspaceInfo struct {
	ID         string   `json:"id"`
	CreatedAt  int64    `json:"created_at"` // unix ms
	Iteration  int      `json:"iteration"`  // deployment cycles so far
	Deployed   bool     `json:"deployed"`
	AgentType  string   `json:"agent_type,omitempty"`  // the agent type it's deployed to (or was, last)
	InUse      bool     `json:"in_use"`                // deployed, and a local agent of that type is working a slopspace signal right now
	Repos      []string `json:"repos,omitempty"`       // writespace repos, owner/name
	Path       string   `json:"path"`                  // its directory under the slopspaces dir: metadata, plus its spaces while stored
	DeployPath string   `json:"deploy_path,omitempty"` // while deployed, where its readspaces and writespaces are
}

// WorkerSignalInfo summarises one work signal for the worker tab.
type WorkerSignalInfo struct {
	ID            string `json:"id"`
	File          string `json:"file"`
	Role          string `json:"role"`
	Prompt        string `json:"prompt"`
	Agent         string `json:"agent,omitempty"`
	Model         string `json:"model,omitempty"`
	Mode          string `json:"mode,omitempty"`
	WorkType      string `json:"work_type"`
	WorkLocation  string `json:"work_location,omitempty"`
	Status        string `json:"status"`
	CreatedAt     int64  `json:"created_at"` // unix ms
	StartedAt     int64  `json:"started_at,omitempty"`
	CompletedAt   int64  `json:"completed_at,omitempty"`
	AgentType     string `json:"agent_type,omitempty"`
	SlopspaceID   string `json:"slopspace_id,omitempty"` // slopspace work: the slopspace it ran in
	Comment       string `json:"comment,omitempty"`      // the last event's comment, e.g. why it failed
	Output        string `json:"output,omitempty"`
	OutputTrimmed bool   `json:"output_trimmed,omitempty"`
}

// SubmitRequest is the JSON carried by "__worker:submit <json>".
type SubmitRequest struct {
	Prompt string `json:"prompt"`
	Agent  string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"`
}

// connectLoop keeps this worker connected to local-representative. Runs in
// its own goroutine; never returns.
func (w *Worker) connectLoop(host, port, name string) {
	addr := net.JoinHostPort(host, port)
	log.Printf("[%s] auto-connect enabled: dialing local-representative at %s as %q", w.workerID, addr, name)
	for {
		client, err := representable.Connect(addr, name, representable.Mode(w.config.DevMode), autoConnectDialTimeout)
		if err != nil {
			time.Sleep(autoConnectInterval)
			continue
		}
		log.Printf("[%s] connected to local-representative at %s", w.workerID, addr)
		client.SetCommandHandler(w.handleReprCommand)
		w.reprMu.Lock()
		w.repr = client
		w.lastPushed = "" // a fresh connection always gets the current state
		w.reprMu.Unlock()
		client.SendData("version", struct {
			Version string `json:"version"`
		}{ufaversion.Version})
		w.pushState()

		<-client.DisconnectCh()

		w.reprMu.Lock()
		if w.repr == client {
			w.repr = nil
		}
		w.reprMu.Unlock()
		log.Printf("[%s] disconnected from local-representative at %s; retrying", w.workerID, addr)
		time.Sleep(autoConnectInterval)
	}
}

// handleReprCommand handles commands local-representative sends down. Only
// the "__worker:" namespace is acted on:
//
//	__worker:submit {"prompt": "...", "agent": "...", "model": "..."}
//	__worker:refresh
//	__worker:slopspace {"op": "...", "id": "...", ...}  (see slopctl.go)
func (w *Worker) handleReprCommand(raw string) {
	if !strings.HasPrefix(raw, "__worker:") {
		return
	}
	rest := strings.TrimSpace(strings.TrimPrefix(raw, "__worker:"))
	verb, arg, _ := strings.Cut(rest, " ")
	switch verb {
	case "submit":
		var req SubmitRequest
		if err := json.Unmarshal([]byte(arg), &req); err != nil {
			log.Printf("[%s] repr: bad submit payload: %v", w.workerID, err)
			return
		}
		if _, err := w.submit(req); err != nil {
			log.Printf("[%s] repr: submit failed: %v", w.workerID, err)
		}
	case "slopspace":
		w.handleSlopspaceCommand(arg)
	case "refresh":
		w.reprMu.Lock()
		w.lastPushed = ""
		w.reprMu.Unlock()
		w.pushState()
	default:
		log.Printf("[%s] repr: ignoring unrecognised command %q", w.workerID, raw)
	}
}

// submit creates a prompt-only, in-place work signal for this worker's agent
// type, run in the temp directory, and wakes the watch loop so it starts
// right away. Returns the new signal's path.
func (w *Worker) submit(req SubmitRequest) (string, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("empty prompt")
	}
	signal := &types.WorkSignal{
		WorkType:     types.WorkTypeInPlace,
		WorkLocation: os.TempDir(),
		AgentType:    w.config.AgentType,
		Role:         submitRole,
		Prompt:       prompt,
		Agent:        req.Agent,
		Model:        req.Model,
		Mode:         "prompt",
		CreatedAt:    time.Now(),
	}
	path, err := w.createSignal(signal)
	if err != nil {
		return "", err
	}
	log.Printf("[%s] submitted work from local-representative: %s", w.workerID, path)
	w.pushState()
	return path, nil
}

// createSignal writes signal into ongoing/ and wakes the watch loop so it
// starts right away. Signal files are named by role and creation second, so
// two submits in the same second would share a name and the second would
// overwrite the first. Move to the next free second instead.
func (w *Worker) createSignal(signal *types.WorkSignal) (string, error) {
	for {
		name := worksignal.GenerateFilename(signal.Role, signal.CreatedAt, false)
		if _, err := os.Stat(filepath.Join(w.config.OngoingWorkDir(), name)); os.IsNotExist(err) {
			break
		}
		signal.CreatedAt = signal.CreatedAt.Add(time.Second)
	}
	path, err := w.workSignalMgr.Create(signal)
	if err != nil {
		return "", err
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return path, nil
}

func (w *Worker) setBusy(busy bool) {
	w.reprMu.Lock()
	w.busy = busy
	w.reprMu.Unlock()
	w.pushState()
}

// pushState sends the current worker state to local-representative if it
// has changed since the last push. No-op when not connected. Pushes are
// serialised so an older snapshot can't be sent after a newer one.
func (w *Worker) pushState() {
	w.pushMu.Lock()
	defer w.pushMu.Unlock()
	w.reprMu.Lock()
	client, busy := w.repr, w.busy
	w.reprMu.Unlock()
	if client == nil {
		return
	}
	st := w.state(busy)
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	w.reprMu.Lock()
	if w.repr != client || w.lastPushed == string(b) {
		w.reprMu.Unlock()
		return
	}
	w.lastPushed = string(b)
	w.reprMu.Unlock()
	client.SendData("worker-state", st)
}

// state builds the worker-state snapshot from the signal directories.
func (w *Worker) state(busy bool) WorkerStateMsg {
	st := WorkerStateMsg{
		WorkerID:  w.workerID,
		AgentType: string(w.config.AgentType),
		Busy:      busy,
		Signals:   []WorkerSignalInfo{},
	}
	ongoing, _ := w.workSignalMgr.ListOngoing()
	complete, _ := w.workSignalMgr.ListComplete()
	st.Signals = append(st.Signals, w.summarise(ongoing, 0)...)
	st.Signals = append(st.Signals, w.summarise(complete, maxStateCompleted)...)
	st.Slopspaces, st.SlopspacesOmitted = w.slopspaces(ongoing)
	fitState(&st)
	return st
}

// slopspaces summarises every slopspace on this host for the worker tab.
// Whether one is in use comes from the ongoing signals: a deployed slopspace
// is in use while an agent of the type it's deployed to is processing
// slopspace work -- at most one slopspace is deployed per agent type, so
// that work is running in it. That agent may be this worker or any other
// dungeon-keeper on the host.
func (w *Worker) slopspaces(ongoing []string) ([]SlopspaceInfo, int) {
	all, err := w.slopspaceMgr.List()
	if err != nil {
		return []SlopspaceInfo{}, 0
	}
	working := map[types.AgentType]bool{}
	for _, p := range ongoing {
		signal, _, err := w.workSignalMgr.Read(p)
		if err != nil {
			continue
		}
		if signal.Status == types.WorkStatusProcessing && signal.WorkType == types.WorkTypeSlopspace {
			working[signal.AgentType] = true
		}
	}

	out := make([]SlopspaceInfo, 0, len(all))
	for _, m := range all {
		info := SlopspaceInfo{
			ID:        m.ID,
			CreatedAt: m.CreatedAt.UnixMilli(),
			Iteration: m.Iteration,
			Deployed:  m.Deployed,
			AgentType: string(m.DeployedAgentType),
			InUse:     m.Deployed && working[m.DeployedAgentType],
			Path:      m.RootPath,
		}
		if m.Deployed {
			info.DeployPath = m.DeployPath
		}
		info.Repos, _ = w.slopspaceMgr.WritespaceRepos(m.ID)
		out = append(out, info)
	}
	rank := func(s SlopspaceInfo) int {
		switch {
		case s.InUse:
			return 0
		case s.Deployed:
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	omitted := 0
	if len(out) > maxStateSlopspaces {
		omitted = len(out) - maxStateSlopspaces
		out = out[:maxStateSlopspaces]
	}
	return out, omitted
}

// fitState trims st until it marshals to at most maxStateBytes: representable
// sends each message as one line and its reader (bufio.Scanner) drops the
// connection on a line over 64 KiB -- on LR's link from here, and again on
// LR's link up to agent-coordinator, which relays worker-state as-is. Older
// signals lose their output first; then prompts are shortened; then the
// newest's output is cut to its end. The full text stays in the signal files.
func fitState(st *WorkerStateMsg) {
	size := func() int {
		b, _ := json.Marshal(st)
		return len(b)
	}
	if size() <= maxStateBytes {
		return
	}
	// Signals are newest first: drop the oldest outputs until it fits or only
	// the newest's is left.
	for i := len(st.Signals) - 1; i > 0 && size() > maxStateBytes; i-- {
		if st.Signals[i].Output != "" {
			st.Signals[i].Output = ""
			st.Signals[i].OutputTrimmed = true
		}
	}
	for n := maxStatePrompt; n >= 64 && size() > maxStateBytes; n /= 2 {
		for i := range st.Signals {
			if p := st.Signals[i].Prompt; len(p) > n {
				st.Signals[i].Prompt = p[:n] + "…"
			}
		}
	}
	if len(st.Signals) > 0 {
		newest := &st.Signals[0]
		for newest.Output != "" && size() > maxStateBytes {
			newest.Output = newest.Output[len(newest.Output)/2:]
			newest.OutputTrimmed = true
			if len(newest.Output) < 64 {
				newest.Output = ""
			}
		}
	}
}

// summarise reads the given signal files, newest first, keeping at most
// limit of them (0 for all). The filenames carry the creation time, so only
// the files that are kept get read.
func (w *Worker) summarise(paths []string, limit int) []WorkerSignalInfo {
	created := func(p string) int64 {
		_, ts, _, err := worksignal.ParseFilename(p)
		if err != nil {
			return 0
		}
		return ts
	}
	sort.Slice(paths, func(i, j int) bool { return created(paths[i]) > created(paths[j]) })
	if limit > 0 && len(paths) > limit {
		paths = paths[:limit]
	}

	var out []WorkerSignalInfo
	for _, p := range paths {
		signal, events, err := w.workSignalMgr.Read(p)
		if err != nil {
			continue
		}
		info := WorkerSignalInfo{
			ID:           signal.ID,
			File:         filepath.Base(p),
			Role:         signal.Role,
			Prompt:       signal.Prompt,
			Agent:        signal.Agent,
			Model:        signal.Model,
			Mode:         signal.Mode,
			WorkType:     string(signal.WorkType),
			WorkLocation: signal.WorkLocation,
			Status:       string(signal.Status),
			CreatedAt:    signal.CreatedAt.UnixMilli(),
			AgentType:    string(signal.AgentType),
		}
		if signal.StartedAt != nil {
			info.StartedAt = signal.StartedAt.UnixMilli()
		}
		if signal.CompletedAt != nil {
			info.CompletedAt = signal.CompletedAt.UnixMilli()
		}
		for _, e := range events {
			if e.Output != "" {
				info.Output = e.Output
			}
			if e.SlopspaceID != "" {
				info.SlopspaceID = e.SlopspaceID
			}
			if e.Comment != "" {
				info.Comment = e.Comment
			}
		}
		if len(info.Output) > maxStateOutput {
			info.Output = info.Output[len(info.Output)-maxStateOutput:]
			info.OutputTrimmed = true
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}
