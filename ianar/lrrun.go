package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
)

// This file lets local-representative drive IANAR: LR's control tab
// (local-representative/control.go, condocs/initialRobotImpls/
// Step3Prompt.md) sequences actions across sub-apps, and for the steps that
// need the robot it sends "__robot:run <json>" down the representable link.
// The request lists steps, each made of the same instructions the
// sequence-v2 definer uses (seqv2_ops.go), so a run goes through the same
// engine and is recorded the same way as one started from the runner tab.
// Progress and the result go back up as "robot-run-progress" and
// "robot-run-result" data messages. Those carry no images or recording --
// representable lines are capped well below a screenshot's size -- so with
// save set the run's zip (report, step images, recording) is uploaded into
// LR's files area instead and the result names it. LR cancelling its control
// sequence sends "__robot:cancel {"run": "<id>"}", which stops the run before
// its next instruction (or ends a wait it is in) and reports it cancelled.

// RobotRunStep is one step of a run LR asked for.
type RobotRunStep struct {
	Label string        `json:"label"`
	Do    []Instruction `json:"do"`
}

// RobotRunRequest is the JSON after "__robot:run ".
type RobotRunRequest struct {
	Run   string         `json:"run"`            // LR's id for the run, echoed back
	Name  string         `json:"name,omitempty"` // shown in the recording's report
	Steps []RobotRunStep `json:"steps"`
	Save  bool           `json:"save,omitempty"` // upload the run's zip to LR's files area
	// NoRecord leaves the screen recording out of the run: LR is already
	// recording the whole control sequence (see lrrecord.go).
	NoRecord bool `json:"no_record,omitempty"`
}

// RobotRunStepResult is one step's outcome in a "robot-run-result".
type RobotRunStepResult struct {
	Label      string `json:"label"`
	Status     string `json:"status"` // "success" | "error" | "skipped"
	Message    string `json:"message,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// RobotRunMsg is the payload of "robot-run-progress" (Step, Status, Message
// set) and "robot-run-result" (Success, Error, Steps and the rest set).
type RobotRunMsg struct {
	Run         string               `json:"run"`
	Step        int                  `json:"step"`
	Status      string               `json:"status,omitempty"`
	Message     string               `json:"message,omitempty"`
	Success     bool                 `json:"success"`
	Error       string               `json:"error,omitempty"`
	Cancelled   bool                 `json:"cancelled,omitempty"` // stopped by "__robot:cancel"
	Steps       []RobotRunStepResult `json:"steps,omitempty"`
	DurationMs  int64                `json:"duration_ms,omitempty"`
	KeyboardVia string               `json:"keyboard_via,omitempty"`
	SavedAs     string               `json:"saved_as,omitempty"`   // the run's zip in LR's files area
	SaveError   string               `json:"save_error,omitempty"` // why it couldn't be saved
}

// compileRobotRun turns a request into a sequence the engine can run: each
// step becomes a one-off action holding its instructions.
func compileRobotRun(req RobotRunRequest) (sequence, error) {
	if len(req.Steps) == 0 {
		return sequence{}, errors.New("no steps given")
	}
	name := req.Name
	if name == "" {
		name = "local-representative run " + req.Run
	}
	q := SequenceV2{ID: "lr-" + req.Run, Name: name}
	actions := map[string]ActionDef{}
	for i, st := range req.Steps {
		if len(st.Do) == 0 {
			return sequence{}, fmt.Errorf("step %d (%s): no instructions", i+1, st.Label)
		}
		for j, in := range st.Do {
			if err := validateInstruction(in); err != nil {
				return sequence{}, fmt.Errorf("step %d (%s), instruction %d: %v", i+1, st.Label, j+1, err)
			}
		}
		id := fmt.Sprintf("lr-step-%d", i+1)
		actions[id] = ActionDef{ID: id, Name: st.Label, Do: st.Do}
		q.Steps = append(q.Steps, StepRef{Action: id, Label: st.Label})
	}
	seq, err := compileSequence(q, actions, nil, clock())
	seq.noRecord = req.NoRecord
	return seq, err
}

// handleRobotRun starts the run in raw ("__robot:run <json>") in the
// background, so the representable read loop it was called from keeps going.
// The run is registered before that, so a cancel right behind it finds it.
func (s *Server) handleRobotRun(arg string) {
	var req RobotRunRequest
	if err := json.Unmarshal([]byte(arg), &req); err != nil {
		log.Printf("repr: bad run request: %v", err)
		return
	}
	cancel := make(chan struct{})
	s.lrRunsMu.Lock()
	if s.lrRuns == nil {
		s.lrRuns = make(map[string]chan struct{})
	}
	s.lrRuns[req.Run] = cancel
	s.lrRunsMu.Unlock()
	go func() {
		defer func() {
			s.lrRunsMu.Lock()
			delete(s.lrRuns, req.Run)
			s.lrRunsMu.Unlock()
		}()
		s.runForLR(req, cancel)
	}()
}

// RobotCancelRequest is the JSON after "__robot:cancel ".
type RobotCancelRequest struct {
	Run string `json:"run"` // the id LR's "__robot:run" gave
}

// handleRobotCancel cancels the LR run named in raw ("__robot:cancel
// <json>"), if it is still going.
func (s *Server) handleRobotCancel(arg string) {
	var req RobotCancelRequest
	if err := json.Unmarshal([]byte(arg), &req); err != nil {
		log.Printf("repr: bad cancel request: %v", err)
		return
	}
	if !s.cancelLRRun(req.Run) {
		log.Printf("robot: no run %s for local-representative to cancel", req.Run)
	}
}

// cancelLRRun cancels LR's run id, reporting whether it was still going.
func (s *Server) cancelLRRun(id string) bool {
	s.lrRunsMu.Lock()
	defer s.lrRunsMu.Unlock()
	ch, ok := s.lrRuns[id]
	if !ok {
		return false
	}
	delete(s.lrRuns, id) // so a second cancel doesn't close it again
	close(ch)
	log.Printf("robot: local-representative cancelled run %s", id)
	return true
}

// sendToLR sends a data message to local-representative, if connected.
func (s *Server) sendToLR(dataType string, payload interface{}) {
	s.reprMu.Lock()
	client := s.reprClient
	s.reprMu.Unlock()
	if client != nil {
		client.SendData(dataType, payload)
	}
}

// runForLR carries out req; closing cancel stops it (see cancelLRRun).
func (s *Server) runForLR(req RobotRunRequest, cancel <-chan struct{}) {
	q, err := compileRobotRun(req)
	if err != nil {
		s.sendToLR("robot-run-result", RobotRunMsg{Run: req.Run, Step: -1, Status: "error", Error: err.Error()})
		return
	}
	q.cancel = cancel
	log.Printf("robot: running %d step(s) for local-representative (run %s)", len(q.steps), req.Run)
	res := runSequence(q, func(p SequenceProgressMsg) {
		s.sendToLR("robot-run-progress", RobotRunMsg{Run: req.Run, Step: p.Step, Status: p.Status, Message: p.Message})
	})

	out := RobotRunMsg{
		Run:         req.Run,
		Step:        res.FailedStep,
		Success:     res.Success,
		Error:       res.Error,
		Cancelled:   res.Cancelled,
		DurationMs:  res.DurationMs,
		KeyboardVia: res.KeyboardVia,
	}
	out.Status = "success"
	switch {
	case res.Cancelled:
		out.Status = "cancelled"
	case !res.Success:
		out.Status = "error"
	}
	for i, st := range res.Steps {
		label := ""
		if i < len(q.steps) {
			label = q.steps[i].Label
		}
		out.Steps = append(out.Steps, RobotRunStepResult{Label: label, Status: st.Status, Message: st.Message, DurationMs: st.DurationMs})
	}
	// Saved with or without a recording: what each step saw is worth keeping
	// on its own, a failed step's most of all.
	id := s.artifacts.keep(sequenceArtifact(q.def(), res))
	if req.Save {
		if f, err := s.saveArtifact(id); err != nil {
			out.SaveError = err.Error()
		} else {
			out.SavedAs = f.Name
		}
	}
	s.sendToLR("robot-run-result", out)
}
