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
// the signal file; LR only needs enough to show it.
const (
	maxStateCompleted = 20
	maxStateOutput    = 64 * 1024
)

// submitRole is the role recorded on work submitted from LR's worker tab.
const submitRole = "worker-tab"

// WorkerStateMsg is the "worker-state" data payload pushed to
// local-representative, which shows it on its worker tab.
type WorkerStateMsg struct {
	WorkerID  string             `json:"worker_id"`
	AgentType string             `json:"agent_type"`
	Busy      bool               `json:"busy"`
	Signals   []WorkerSignalInfo `json:"signals"` // ongoing first, then the most recent completed, newest first within each
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
	CompletedAt   int64  `json:"completed_at,omitempty"`
	Comment       string `json:"comment,omitempty"` // the last event's comment, e.g. why it failed
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
	// Signal files are named by role and creation second, so two submits in
	// the same second would share a name and the second would overwrite the
	// first. Move to the next free second instead.
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
	log.Printf("[%s] submitted work from local-representative: %s", w.workerID, path)
	select {
	case w.wake <- struct{}{}:
	default:
	}
	w.pushState()
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
	return st
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
		}
		if signal.CompletedAt != nil {
			info.CompletedAt = signal.CompletedAt.UnixMilli()
		}
		for _, e := range events {
			if e.Output != "" {
				info.Output = e.Output
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
