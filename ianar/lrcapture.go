package main

import (
	"bytes"
	"encoding/json"
	"image/png"
	"log"
)

// This file lets local-representative have the robot take a native capture
// -- what the robot tab's capture-native button takes -- and save it into
// LR's files area as a screenshot (condocs/initialRobotImpls/Step3Prompt.md,
// Revision F: the control tab's node-capture op, which may be run for
// another node's LR through agent-coordinator):
//
//	__robot:capture {"req": "<id>", "name": "<what for>"}
//
// It's answered with a "robot-capture-result" data message. The image never
// crosses the representable link (its lines are capped well below a
// screenshot's size): it's uploaded into LR's files area, and the result
// names it.

// RobotCaptureRequest is the JSON after "__robot:capture ".
type RobotCaptureRequest struct {
	Req  string `json:"req"`            // LR's id for the capture, echoed back
	Name string `json:"name,omitempty"` // part of the saved file's name
}

// RobotCaptureMsg is the payload of "robot-capture-result".
type RobotCaptureMsg struct {
	Req     string `json:"req"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	SavedAs string `json:"saved_as,omitempty"` // the screenshot in LR's files area
	SavedID string `json:"saved_id,omitempty"`
}

func (s *Server) handleRobotCapture(arg string) {
	var req RobotCaptureRequest
	if err := json.Unmarshal([]byte(arg), &req); err != nil || req.Req == "" {
		log.Printf("repr: bad capture request %q: %v", arg, err)
		return
	}
	go s.sendToLR("robot-capture-result", captureForLR(req, s.lrBaseURL))
}

// captureForLR captures the native display and uploads it into the files
// area of the LR at lrBase.
func captureForLR(req RobotCaptureRequest, lrBase func() (string, error)) RobotCaptureMsg {
	out := RobotCaptureMsg{Req: req.Req}
	data, err := captureNativeDisplay()
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(data)); err == nil {
		out.Width, out.Height = cfg.Width, cfg.Height
	}
	base, err := lrBase()
	if err != nil {
		out.Error = "captured the screen but couldn't save it: " + err.Error()
		return out
	}
	f, err := uploadToFiles(base, lrCaptureName(req.Name), data)
	if err != nil {
		out.Error = "captured the screen but couldn't save it: " + err.Error()
		return out
	}
	log.Printf("robot: saved a native capture for local-representative (%s) as %s", req.Req, f.Name)
	out.Success, out.SavedAs, out.SavedID = true, f.Name, f.ID
	return out
}

// lrCaptureName names a capture LR asked for, like the capture-native
// button's (captureArtifact), with what it was for.
func lrCaptureName(name string) string {
	base := "ianar-capture-native-"
	if n := fileSafe(name); n != "" {
		base += n + "-"
	}
	return base + stamp() + ".png"
}
