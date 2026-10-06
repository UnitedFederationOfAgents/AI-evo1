package main

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// This file lets local-representative record this node's screen across a
// whole control sequence (condocs/initialRobotImpls/Step3Prompt.md,
// Revision A), rather than one robot run at a time:
//
//	__robot:record-start {"rec": "<id>", "name": "<what for>"}
//	__robot:record-stop  {"rec": "<id>", "save": true}
//
// Each is answered with a "robot-record-result" data message: status
// "recording" once started, then "saved" (or "error") once stopped, with
// the recording uploaded into LR's files area when save is set. A recording
// left running stops itself after lrRecordMax. While one runs it holds
// clipMu, so Native Clip waits; LR's robot runs for the same sequence ask
// not to record themselves (RobotRunRequest.NoRecord).

const lrRecordMax = 10 * time.Minute

// RobotRecordRequest is the JSON after "__robot:record-start " and
// "__robot:record-stop ".
type RobotRecordRequest struct {
	Rec  string `json:"rec"`            // LR's id for the recording, echoed back
	Name string `json:"name,omitempty"` // part of the saved file's name
	Save bool   `json:"save,omitempty"` // record-stop: upload it to LR's files area
}

// RobotRecordMsg is the payload of "robot-record-result".
type RobotRecordMsg struct {
	Rec        string `json:"rec"`
	Status     string `json:"status"` // "recording" | "saved" | "error"
	Error      string `json:"error,omitempty"`
	Via        string `json:"via,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	SavedAs    string `json:"saved_as,omitempty"`
	SavedID    string `json:"saved_id,omitempty"` // its id in LR's files area, for playing it back
	SaveError  string `json:"save_error,omitempty"`
}

// lrRecording is a recording LR started.
type lrRecording struct {
	name string
	stop func() ClipResultMsg
	once sync.Once
	res  ClipResultMsg
}

// finish stops the recording (once) and releases clipMu.
func (r *lrRecording) finish() ClipResultMsg {
	r.once.Do(func() {
		r.res = r.stop()
		clipMu.Unlock()
	})
	return r.res
}

var (
	lrRecMu sync.Mutex
	lrRecs  = map[string]*lrRecording{}
)

func (s *Server) handleRobotRecord(verb, arg string) {
	var req RobotRecordRequest
	if err := json.Unmarshal([]byte(arg), &req); err != nil || req.Rec == "" {
		log.Printf("repr: bad %s request %q: %v", verb, arg, err)
		return
	}
	switch verb {
	case "record-start":
		go s.sendToLR("robot-record-result", startLRRecording(req))
	case "record-stop":
		go s.sendToLR("robot-record-result", s.stopLRRecording(req))
	}
}

// startLRRecording starts recording the screen for req.
func startLRRecording(req RobotRecordRequest) RobotRecordMsg {
	out := RobotRecordMsg{Rec: req.Rec}
	if !clipMu.TryLock() {
		out.Status, out.Error = "error", "a native clip or sequence recording is already under way"
		return out
	}
	rec := &lrRecording{name: req.Name, stop: startNativeRecording(lrRecordMax, seqFrameInterval)}
	lrRecMu.Lock()
	lrRecs[req.Rec] = rec
	lrRecMu.Unlock()
	log.Printf("robot: recording the screen for local-representative (%s)", req.Rec)
	// Never leave the recorder (and clipMu) held by a sequence that never
	// asks to stop.
	time.AfterFunc(lrRecordMax, func() {
		lrRecMu.Lock()
		_, live := lrRecs[req.Rec]
		lrRecMu.Unlock()
		if live {
			log.Printf("robot: recording %s ran past %s; stopping it", req.Rec, lrRecordMax)
			rec.finish()
		}
	})
	out.Status = "recording"
	return out
}

// stopLRRecording stops the recording for req and, if asked, saves it.
func (s *Server) stopLRRecording(req RobotRecordRequest) RobotRecordMsg {
	out := RobotRecordMsg{Rec: req.Rec}
	lrRecMu.Lock()
	rec := lrRecs[req.Rec]
	delete(lrRecs, req.Rec)
	lrRecMu.Unlock()
	if rec == nil {
		out.Status, out.Error = "error", "no recording "+req.Rec+" is under way"
		return out
	}
	clip := rec.finish()
	if !clip.Success {
		out.Status, out.Error = "error", clip.Error
		return out
	}
	out.Status, out.Via, out.DurationMs = "saved", clip.Via, clip.DurationMs
	id := s.artifacts.keep(recordingArtifact(rec.name, clip))
	if req.Save {
		if f, err := s.saveArtifact(id); err != nil {
			out.SaveError = err.Error()
		} else {
			out.SavedAs, out.SavedID = f.Name, f.ID
		}
	}
	return out
}

// recordingArtifact is a recording LR asked for, named for what it recorded.
func recordingArtifact(name string, clip ClipResultMsg) artifact {
	base := "ianar-recording-"
	if name != "" {
		base += fileSafe(name) + "-"
	}
	return namedClipArtifact(base+stamp(), clip)
}

// fileSafe reduces s to lower-case letters, digits and single dashes.
func fileSafe(s string) string {
	b := make([]rune, 0, len(s))
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b, dash = append(b, r), false
		case r >= 'A' && r <= 'Z':
			b, dash = append(b, r-'A'+'a'), false
		default:
			if !dash && len(b) > 0 {
				b, dash = append(b, '-'), true
			}
		}
	}
	if dash {
		b = b[:len(b)-1]
	}
	return string(b)
}
