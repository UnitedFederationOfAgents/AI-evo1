package main

// To notes: the "To Notes" buttons beside the notepad and the transcription
// window (condocs/initialRobotImpls/Step5SubstepGPrompt.md, Revision C)
// append that panel's current text to a plain notes file on this host --
// ~/notes-input.txt unless --notes-file says otherwise. The panel itself is
// left as it was; the frontend disables its button until the text changes.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// defaultNotesFile is the notes file used when --notes-file isn't given:
// notes-input.txt in the home directory of the user running this process.
func defaultNotesFile() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "notes-input.txt"
	}
	return filepath.Join(home, "notes-input.txt")
}

// NotesResultMsg is the "notes-result" WebSocket payload answering an
// "append-notes" request. Source echoes the request's source ("notepad" or
// "transcript") so the frontend knows which panel's button it answers.
type NotesResultMsg struct {
	Source  string `json:"source"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Path    string `json:"path,omitempty"`
}

// appendNotes appends text to the notes file, followed by a newline if it
// doesn't already end in one, so consecutive appends start on fresh lines.
// notesMu serializes appends from different tabs so their text can't
// interleave.
func (s *Server) appendNotes(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("nothing to append")
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	f, err := os.OpenFile(s.notesFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// handleAppendNotes appends text to the notes file on behalf of client c and
// reports the outcome back to c alone.
func (s *Server) handleAppendNotes(c *wsClient, source, text string) {
	res := NotesResultMsg{Source: source, Path: s.notesFile}
	if err := s.appendNotes(text); err != nil {
		res.Error = err.Error()
	} else {
		res.Success = true
	}
	s.sendToClient(c, "notes-result", res)
}
