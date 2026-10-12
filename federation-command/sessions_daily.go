package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// This file keeps federation-command's daily default session in step with
// the calendar day, and carries the session.yaml "follows_from" field (see
// condocs/initialRobotImpls/Step5Prompt.md Revision E).
//
// Once a daily default ID (YYYY-MM-DD-<host>-default) has been resolved it
// is just an ordinary session ID to the rest of the system: FC reports it to
// local-representative as its current session ("fc-session"), and LR hands it
// straight back as --session/FC_SESSION on every relaunch (procman.go's
// fcLaunchSessionID) -- also to brand-new FC instances via session-manager's
// current session. Nothing ever re-resolved it, so a long-running or
// relaunched FC stayed on the day it first started. Two fixes close that:
//
//   - main() treats a handed-in daily default from an earlier day as if no
//     session had been given at all (staleDailyDefault), resolving today's.
//   - a running FC checks every dailyDefaultCheckInterval and, if it is still
//     on an earlier day's default, switches to today's (rolloverDailyDefault).
//     If this instance has run any command since it started or last rolled
//     over, the new default records the old one as its follows_from.
//
// Explicitly chosen sessions are never touched: only IDs that look like a
// daily default roll over, and set-session already refuses to select one.

// sessionFollowsFromKey is the session.yaml field naming the session a
// session follows on from.
const sessionFollowsFromKey = "follows_from"

// dailyDefaultCheckInterval is how often a running FC checks whether its
// daily default has gone stale.
const dailyDefaultCheckInterval = time.Minute

// dailyDefaultDay returns the YYYY-MM-DD day a daily default session ID
// belongs to, and false for any ID that isn't a daily default. Both the
// current YYYY-MM-DD-<host>-default form and the older host-less
// YYYY-MM-DD-default form are recognised.
func dailyDefaultDay(id string) (string, bool) {
	if !strings.HasSuffix(id, "-default") || len(id) < len("2006-01-02-default") || id[10] != '-' {
		return "", false
	}
	day := id[:10]
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return "", false
	}
	return day, true
}

// staleDailyDefault reports whether id is a daily default for a day other
// than now's.
func staleDailyDefault(id string, now time.Time) bool {
	day, ok := dailyDefaultDay(id)
	return ok && day != now.Format("2006-01-02")
}

// resolveDefaultSessionID returns today's daily default session ID, created
// via clauditable get-default-session (which also writes its session.yaml)
// or, when clauditable is unavailable, fallbackDefaultSessionID.
func resolveDefaultSessionID(recordsPath string, now time.Time) string {
	if clauditablePath, findErr := findBinary("clauditable"); findErr == nil {
		getCmd := exec.Command(clauditablePath, "get-default-session")
		getCmd.Env = append(environWithoutClauditableGuard(), EnvAgentRecordsPath+"="+recordsPath)
		if out, runErr := getCmd.Output(); runErr == nil {
			if id := strings.TrimSpace(string(out)); id != "" && id != "default" {
				return id
			}
		}
	}
	return fallbackDefaultSessionID(now, fcHostID)
}

// dailyDefaultTickMsg drives the periodic rollover check.
type dailyDefaultTickMsg struct{}

func dailyDefaultTickCmd() tea.Cmd {
	return tea.Tick(dailyDefaultCheckInterval, func(time.Time) tea.Msg { return dailyDefaultTickMsg{} })
}

// rolloverDailyDefault switches m to today's daily default when m is still
// on an earlier day's. It returns a line to print, or "" when nothing changed.
func (m appModel) rolloverDailyDefault(now time.Time) (appModel, string) {
	if !staleDailyDefault(m.sessionID, now) {
		return m, ""
	}
	oldID := m.sessionID
	newID := resolveDefaultSessionID(m.recordsPath, now)
	if newID == oldID {
		return m, ""
	}
	newDir := filepath.Join(m.recordsPath, newID)
	if err := os.MkdirAll(newDir, 0755); err != nil {
		return m, errorStyle.Render("daily default rollover: " + err.Error())
	}
	if _, err := os.Stat(filepath.Join(newDir, "session.yaml")); os.IsNotExist(err) {
		// The fallback path (no clauditable) doesn't write session.yaml;
		// give it the same "<date> Default <host>" name clauditable would.
		_ = updateSessionName(newDir, now.Format("2006-01-02")+" Default "+fcHostID)
	}
	followed := false
	if m.hadActivity && readSessionYAMLField(newDir, sessionFollowsFromKey) == "" {
		// Several FC instances on this host roll over onto the same default;
		// the first active one to get there sets follows_from.
		if err := setSessionYAMLField(newDir, sessionFollowsFromKey, oldID); err == nil {
			followed = true
		}
	}
	newM, err := m.switchToSession(newID)
	if err != nil {
		return m, errorStyle.Render("daily default rollover: " + err.Error())
	}
	newM.hadActivity = false
	notice := "● new day — switched to daily default session: " + newM.sessionDir
	if followed {
		notice += " (follows " + oldID + ")"
	}
	return newM, sessionStyle.Render(notice)
}

// readSessionYAMLField returns key's value from sessionDir's session.yaml, or
// "" when absent.
func readSessionYAMLField(sessionDir, key string) string {
	for _, f := range readSessionYAMLFields(sessionDir) {
		if f[0] == key {
			return f[1]
		}
	}
	return ""
}

// setSessionYAMLField sets key in sessionDir's session.yaml, replacing an
// existing value or appending a new line. A missing session.yaml is created
// with this host as owner, like updateSessionName.
func setSessionYAMLField(sessionDir, key, value string) error {
	yamlPath := filepath.Join(sessionDir, "session.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		data = []byte(fmt.Sprintf("id: %s\nowner: %s\ncreated: %s\n",
			filepath.Base(sessionDir), fcHostID, time.Now().Format(time.RFC3339)))
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	found := false
	for i, l := range lines {
		if strings.HasPrefix(l, key+": ") {
			lines[i] = key + ": " + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, key+": "+value)
	}
	return os.WriteFile(yamlPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

// parseFollowsFlag splits new-session's arguments into an optional leading
// "-f <session>" and the remaining name. <session> is a single word, or a
// quoted run for a session name containing spaces:
//
//	new-session -f 2026-10-11-box-a1b2-default Robot work
//	new-session -f "Robot work" Robot work, part 2
func parseFollowsFlag(args string) (follows, rest string, err error) {
	args = strings.TrimSpace(args)
	if args != "-f" && !strings.HasPrefix(args, "-f ") {
		return "", args, nil
	}
	args = strings.TrimSpace(strings.TrimPrefix(args, "-f"))
	if args == "" {
		return "", "", fmt.Errorf("-f needs the name or ID of the session the new one follows")
	}
	if q := args[0]; q == '"' || q == '\'' {
		end := strings.IndexByte(args[1:], q)
		if end < 0 {
			return "", "", fmt.Errorf("-f: unterminated quote")
		}
		return args[1 : end+1], strings.TrimSpace(args[end+2:]), nil
	}
	follows, rest, _ = strings.Cut(args, " ")
	return follows, strings.TrimSpace(rest), nil
}

// resolveSessionRef maps a session ID or name to an existing session's ID:
// an ID (directory under recordsPath) wins, then a session.yaml name match.
func resolveSessionRef(recordsPath, ref string) (string, error) {
	if ref == "" || strings.ContainsAny(ref, `/\`) || ref == "." || ref == ".." {
		return "", fmt.Errorf("invalid session %q", ref)
	}
	if info, err := os.Stat(filepath.Join(recordsPath, ref)); err == nil && info.IsDir() {
		return ref, nil
	}
	id, err := findSessionByName(recordsPath, ref)
	if err != nil {
		return "", fmt.Errorf("no session with ID or name %q", ref)
	}
	return id, nil
}

// applyFollowsFrom records followsFrom (if any) on newly created session id.
func applyFollowsFrom(recordsPath, id, followsFrom string) error {
	if followsFrom == "" {
		return nil
	}
	return setSessionYAMLField(filepath.Join(recordsPath, id), sessionFollowsFromKey, followsFrom)
}
