package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// set-session / "ufa session set" join an existing session. A session that
// isn't on this host is pulled in from the other hosts first (through
// local-representative), and one that can't be found anywhere is only
// created after the user confirms it. Before, set-session made an empty
// directory for any ID it was given; on a host that didn't have the session
// yet that became a second, empty session with the same ID, which then
// overwrote the real one's index when the two were synced.

// sessionCreateConfirmMsg reports the answer to set-session's "create it
// here?" prompt (see buildCreateSessionConfirmScript).
type sessionCreateConfirmMsg struct {
	id        string
	confirmed bool
	line      string
	cmdTime   time.Time
	deltaMs   int64
}

// validSessionIDArg reports whether id can name a session directory.
func validSessionIDArg(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.HasPrefix(id, ".") && !strings.ContainsAny(id, `/\`)
}

// findRemoteSessionByName returns the ID of the one session a connected
// host reports under name, or "" when none or several do.
func (m appModel) findRemoteSessionByName(name string) string {
	found := ""
	for _, r := range m.discoverRemoteSessions() {
		if r.Name != name {
			continue
		}
		if found != "" && found != r.ID {
			return ""
		}
		found = r.ID
	}
	return found
}

// handleSetSession is set-session/"ufa session set" (cmdName is which, for
// messages). args is "<id>" or "-n <name>".
func (m appModel) handleSetSession(args, cmdName, line string, cmdTime time.Time, deltaMs int64) (appModel, tea.Cmd) {
	fail := func(msg string) (appModel, tea.Cmd) {
		m.logRecord(line, cmdTime, deltaMs, 1)
		return m, tea.Println(errorStyle.Render(msg))
	}
	args = strings.TrimSpace(args)
	id := args
	if args == "-n" || strings.HasPrefix(args, "-n ") {
		name := strings.TrimSpace(strings.TrimPrefix(args, "-n"))
		if name == "" {
			return fail("usage: " + cmdName + " -n <name>")
		}
		foundID, findErr := findSessionByName(m.recordsPath, name)
		if findErr != nil {
			if foundID = m.findRemoteSessionByName(name); foundID == "" {
				return fail("set-session: " + findErr.Error() + " here or on a connected host")
			}
		}
		id = foundID
	}
	if id == "" {
		return fail("usage: " + cmdName + " <id> | " + cmdName + " -n <name>")
	}
	if strings.HasSuffix(id, "-default") {
		return fail("set-session: session IDs ending in '-default' are reserved for daily defaults")
	}
	if !validSessionIDArg(id) {
		return fail(fmt.Sprintf("set-session: invalid session ID %q", id))
	}

	pulled, reachedAll := false, true
	if !sessionExistsLocally(m.recordsPath, id) {
		reachedAll = m.pullSessionFromPeers(id)
		pulled = true
	}
	if sessionExistsLocally(m.recordsPath, id) {
		newM, err := m.switchToSession(id)
		if err != nil {
			return fail("set-session: " + err.Error())
		}
		newM.logRecord(line, cmdTime, deltaMs, 0)
		msg := "session set to: " + newM.sessionDir
		if pulled {
			msg += " (pulled in from another host)"
		}
		if !isSessionOwner(newM.sessionDir, fcHostID) {
			msg += " -- owned by " + readSessionYAMLField(newM.sessionDir, "owner") + "; this host's records go to its own log until the owner merges them"
		}
		return newM, tea.Println(successStyle.Render(msg))
	}

	// Nowhere to be found: only create it if the user says so.
	script := buildCreateSessionConfirmScript(id, reachedAll, time.Now())
	return m, tea.ExecProcess(exec.Command("bash", "-c", script), func(err error) tea.Msg {
		return sessionCreateConfirmMsg{id: id, confirmed: extractExitCode(err) == 0, line: line, cmdTime: cmdTime, deltaMs: deltaMs}
	})
}

// finishSessionCreateConfirm handles sessionCreateConfirmMsg in Update.
func (m appModel) finishSessionCreateConfirm(msg sessionCreateConfirmMsg) (appModel, tea.Cmd) {
	exitCode := 1
	var out tea.Cmd
	if msg.confirmed {
		if err := createSessionWithID(m.recordsPath, msg.id, msg.id); err != nil {
			out = tea.Println(errorStyle.Render("set-session: " + err.Error()))
		} else if newM, err := m.switchToSession(msg.id); err != nil {
			out = tea.Println(errorStyle.Render("set-session: " + err.Error()))
		} else {
			m = newM
			exitCode = 0
			out = tea.Println(successStyle.Render("created session: " + m.sessionDir))
		}
	}
	m.lastExitCode = exitCode
	setPromptWidth(&m.input, buildPrompt(m.cwd, m.currentAgent, m.currentModel, exitCode, m.devMode), m.windowWidth)
	m.logRecord(msg.line, msg.cmdTime, msg.deltaMs, exitCode)
	if !m.blinker.IsRemoteControlActive() {
		m.blinker.SetState(BlinkerIdle)
	}
	m.prevInputLen = 0
	return m, tea.Batch(out, m.blinker.ResetTick())
}
