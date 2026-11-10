package main

// Polly over HTTP (condocs/dTT-IntegrationImpls/Step1Prompt.md, Revision
// V): what the notepad's Polly button does with save checked, for programs
// -- dtt's localrep provider (dtt/declare_localrep.go) makes clips through
// it, by way of local-representative's /convo/ proxy:
//
//	POST /api/polly  {"text": "...", "name": "robot-hello", "config": "neural", "play": true}
//	  -> {"file_id", "saved_as", "voice", "size",
//	      "clip", "config", "engine", "region", "characters", "cost_usd", "cost", "reused",
//	      "played", "played_on" | "play_error"}
//	GET  /api/polly/configs  -> {"active", "configs", "prices", "engines"}
//	GET  /api/polly/clips    -> {"clips"}: the recent clips, newest first
//
// The clip is always saved to local-representative's files area (that's
// the point of asking over HTTP), as "polly-<name>.mp3" when a name is
// given -- so a caller can find it again by name -- or pollyClipName's
// "polly-<voice>-<first words>-<stamp>.mp3" when not.
//
// config names a Polly configuration (Step3Prompt.md, pollyconfig.go); the
// active one is used without it. text's [[...]] fields are filled in for
// it. A recent clip already saying the same text with the same voice and
// engine is reused (reused: true) rather than asking Polly again; its
// cost_usd is still what it cost to make.
//
// play (Revision B) also plays the clip out loud on this host, in a TC tab
// open there, after any it's still playing: see pollyplay.go. played says
// whether a tab took it; with none open the clip is made all the same, and
// play_error says why it wasn't played.

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var pollyNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

// pollyNamedClip is the file name of a clip saved under name.
func pollyNamedClip(name string) string {
	return "polly-" + strings.TrimPrefix(name, "polly-") + ".mp3"
}

func pollyReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func pollyFail(w http.ResponseWriter, status int, err error) {
	pollyReply(w, status, map[string]string{"error": err.Error()})
}

// handlePollyConfigsAPI is GET /api/polly/configs.
func (s *Server) handlePollyConfigsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		pollyFail(w, http.StatusMethodNotAllowed, errors.New("GET"))
		return
	}
	st := s.pollyState()
	pollyReply(w, http.StatusOK, map[string]any{"active": st.Active, "configs": st.Configs, "prices": st.Prices, "engines": st.Engines})
}

// handlePollyClipsAPI is GET /api/polly/clips.
func (s *Server) handlePollyClipsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		pollyFail(w, http.StatusMethodNotAllowed, errors.New("GET"))
		return
	}
	pollyReply(w, http.StatusOK, map[string]any{"clips": s.pollyState().Clips})
}

func (s *Server) handlePollyAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		pollyFail(w, http.StatusMethodNotAllowed, errors.New("POST {text, name, config}"))
		return
	}
	var req struct {
		Text   string `json:"text"`
		Name   string `json:"name"`
		Config string `json:"config"`
		Play   bool   `json:"play"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		pollyFail(w, http.StatusBadRequest, errors.New("the request should be JSON: {text, name, config, play}"))
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name != "" && !pollyNameRe.MatchString(req.Name) {
		pollyFail(w, http.StatusBadRequest, errors.New("name should be lower-case letters, digits, '.', '_' and '-'"))
		return
	}
	if _, err := s.pollyConfig(strings.TrimSpace(req.Config)); err != nil {
		pollyFail(w, http.StatusBadRequest, err)
		return
	}
	base, err := s.lrFilesBase()
	if err != nil {
		pollyFail(w, http.StatusServiceUnavailable, errors.New("can't save the clip: "+err.Error()))
		return
	}
	spoken, err := s.speak(req.Text, strings.TrimSpace(req.Config), "api")
	if err != nil {
		log.Printf("polly: %v", err)
		pollyFail(w, http.StatusBadGateway, err)
		return
	}
	c := spoken.clip
	name := pollyClipName(c.Voice, c.Text, time.Now())
	if req.Name != "" {
		name = pollyNamedClip(req.Name)
	}
	uploaded, err := uploadToFiles(base, name, c.audio)
	if err != nil {
		log.Printf("polly: saving clip: %v", err)
		pollyFail(w, http.StatusBadGateway, err)
		return
	}
	s.noteClipSaved(c, uploaded.Name, uploaded.ID)
	ans := map[string]any{
		"file_id": uploaded.ID, "saved_as": uploaded.Name, "voice": c.Voice, "size": len(c.audio),
		"clip": c.ID, "config": c.Config, "engine": c.Engine, "region": c.Region, "text": c.Text,
		"characters": c.Characters, "cost_usd": c.CostUSD, "cost": c.Cost, "reused": spoken.reused,
	}
	if req.Play {
		on, err := s.playOnHost(c, spoken.reused, uploaded.Name)
		ans["played"] = err == nil
		if err != nil {
			ans["play_error"] = err.Error()
		} else {
			ans["played_on"] = on
		}
	}
	pollyReply(w, http.StatusOK, ans)
}
