package main

// Spoken announcements (condocs/dTT-IntegrationImpls/Step3Prompt.md,
// Revision A). Saying "application running" on every start or restart
// (initialRobotImpls/Step5Prompt.md, Revision D) is now off by default:
//
//   - The "announce start" toggle in TC's sidebar turns it back on. It's one
//     setting for the whole instance, kept in --settings-file so it outlasts
//     the restarts it's there to announce. While it's off, TC doesn't report
//     its started_at, in self-info or in convo-state to local-representative,
//     and the started_at is all any UI announces from (each frontend's
//     tcannounce.ts). So TC's own page, LR's and AC's all stay quiet with no
//     change of their own. Turning it on doesn't announce the start that
//     already happened: tcannounce.ts only speaks for a start it saw happen.
//   - The "announce self" button says "hello" in the tab that clicked it,
//     whatever the toggle says (App.tsx).

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	ufaversion "ufa-version"
)

// convoSettings is what --settings-file holds.
type convoSettings struct {
	AnnounceStart bool `json:"announce_start"`
}

func defaultSettingsFile() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ufa", "convo-settings.json")
}

// loadSettings applies what s.settingsFile holds, if anything.
func (s *Server) loadSettings() {
	if s.settingsFile == "" {
		return
	}
	data, err := os.ReadFile(s.settingsFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("settings: reading %s: %v", s.settingsFile, err)
		}
		return
	}
	var st convoSettings
	if err := json.Unmarshal(data, &st); err != nil {
		log.Printf("settings: %s: %v", s.settingsFile, err)
		return
	}
	s.settingsMu.Lock()
	s.announceStart = st.AnnounceStart
	s.settingsMu.Unlock()
}

// setAnnounceStart turns the start announcement on or off, keeps it in
// s.settingsFile and tells every tab and local-representative.
func (s *Server) setAnnounceStart(on bool) {
	s.settingsMu.Lock()
	s.announceStart = on
	file := s.settingsFile
	s.settingsMu.Unlock()
	if file != "" {
		data, _ := json.MarshalIndent(convoSettings{AnnounceStart: on}, "", "  ")
		err := os.MkdirAll(filepath.Dir(file), 0o755)
		if err == nil {
			err = os.WriteFile(file, append(data, '\n'), 0o644)
		}
		if err != nil {
			log.Printf("settings: saving %s: %v", file, err)
		}
	}
	msg := s.marshalMsg("self-info", s.selfInfo())
	s.mu.RLock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
	s.mu.RUnlock()
	s.pushConvoState()
}

func (s *Server) announcing() bool {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	return s.announceStart
}

// reportedStartedAt is the started_at UIs announce from: this process's
// start (unix ms) while announce start is on, else 0 (left out).
func (s *Server) reportedStartedAt() int64 {
	if !s.announcing() {
		return 0
	}
	return s.startedAt.UnixMilli()
}

func (s *Server) selfInfo() SelfInfoMsg {
	return SelfInfoMsg{
		DevMode:       s.devMode,
		Version:       ufaversion.Version,
		StartedAt:     s.reportedStartedAt(),
		AnnounceStart: s.announcing(),
	}
}
