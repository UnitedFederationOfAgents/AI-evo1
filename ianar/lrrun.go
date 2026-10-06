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
// LR's files area instead and the result names it.

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
	return compileSequence(q, actions, nil, clock())
}

// handleRobotRun starts the run in raw ("__robot:run <json>") in the
// background, so the representable read loop it was called from keeps going.
func (s *Server) handleRobotRun(arg string) {
	var req RobotRunRequest
	if err := json.Unmarshal([]byte(arg), &req); err != nil {
		log.Printf("repr: bad run request: %v", err)
		return
	}
	go s.runForLR(req)
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

func (s *Server) runForLR(req RobotRunRequest) {
	q, err := compileRobotRun(req)
	if err != nil {
		s.sendToLR("robot-run-result", RobotRunMsg{Run: req.Run, Step: -1, Status: "error", Error: err.Error()})
		return
	}
	log.Printf("robot: running %d step(s) for local-representative (run %s)", len(q.steps), req.Run)
	res := runSequence(q, func(p SequenceProgressMsg) {
		s.sendToLR("robot-run-progress", RobotRunMsg{Run: req.Run, Step: p.Step, Status: p.Status, Message: p.Message})
	})

	out := RobotRunMsg{
		Run:         req.Run,
		Step:        res.FailedStep,
		Success:     res.Success,
		Error:       res.Error,
		DurationMs:  res.DurationMs,
		KeyboardVia: res.KeyboardVia,
	}
	out.Status = "success"
	if !res.Success {
		out.Status = "error"
	}
	for i, st := range res.Steps {
		label := ""
		if i < len(q.steps) {
			label = q.steps[i].Label
		}
		out.Steps = append(out.Steps, RobotRunStepResult{Label: label, Status: st.Status, Message: st.Message, DurationMs: st.DurationMs})
	}
	if res.Recording != nil {
		id := s.artifacts.keep(sequenceArtifact(q.def(), res))
		if req.Save {
			if f, err := s.saveArtifact(id); err != nil {
				out.SaveError = err.Error()
			} else {
				out.SavedAs = f.Name
			}
		}
	}
	s.sendToLR("robot-run-result", out)
}
