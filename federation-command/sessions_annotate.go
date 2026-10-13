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

// "ufa session note" / "ufa session resource": attach a note (text) or a
// resource (a file) to the current session. Like new-session and archive,
// these run clauditable ("clauditable add-note" / "add-resource", see
// clauditable/annotate.go), the same writer session-manager uses, so the
// entry lands in session.jsonl as a "note"/"resource" record that
// session-manager's transcript shows.

// handleSessionNote is "ufa session note <text>".
func (m appModel) handleSessionNote(args, line string, cmdTime time.Time, deltaMs int64) (appModel, tea.Cmd) {
	text := stripSurroundingQuotes(strings.TrimSpace(args))
	if text == "" {
		m.logRecord(line, cmdTime, deltaMs, 1)
		return m, tea.Println(errorStyle.Render("usage: ufa session note <text>"))
	}
	record, err := runClauditableAnnotate(m.recordsPath, m.sessionID, text, "add-note")
	if err != nil {
		m.logRecord(line, cmdTime, deltaMs, 1)
		return m, tea.Println(errorStyle.Render("ufa session note: " + err.Error()))
	}
	m.logRecord(line, cmdTime, deltaMs, 0)
	return m, tea.Println(successStyle.Render(fmt.Sprintf("note added to %s (record %s)", m.sessionID, record)))
}

// handleSessionResource is "ufa session resource <path> [-name <name>]";
// a relative path is taken from FC's current directory.
func (m appModel) handleSessionResource(args, line string, cmdTime time.Time, deltaMs int64) (appModel, tea.Cmd) {
	path, name, err := parseResourceArgs(args)
	if err != nil {
		m.logRecord(line, cmdTime, deltaMs, 1)
		return m, tea.Println(errorStyle.Render(err.Error()))
	}
	path = resolveUserPath(path, m.cwd)

	cmdArgs := []string{"add-resource"}
	if name != "" {
		cmdArgs = append(cmdArgs, "-name", name)
	}
	cmdArgs = append(cmdArgs, path)
	record, err := runClauditableAnnotate(m.recordsPath, m.sessionID, "", cmdArgs...)
	if err != nil {
		m.logRecord(line, cmdTime, deltaMs, 1)
		return m, tea.Println(errorStyle.Render("ufa session resource: " + err.Error()))
	}
	m.logRecord(line, cmdTime, deltaMs, 0)
	return m, tea.Println(successStyle.Render(fmt.Sprintf("resource %s added to %s (record %s)", filepath.Base(path), m.sessionID, record)))
}

// parseResourceArgs splits "<path> [-name <name>]" (either order; quoted
// names/paths may contain spaces).
func parseResourceArgs(args string) (path, name string, err error) {
	usage := fmt.Errorf("usage: ufa session resource <path> [-name <name>]")
	var paths []string
	parts := parseArgs(args)
	for i := 0; i < len(parts); i++ {
		if parts[i] == "-name" {
			if i+1 >= len(parts) {
				return "", "", usage
			}
			i++
			name = parts[i]
			continue
		}
		paths = append(paths, parts[i])
	}
	if len(paths) != 1 {
		return "", "", usage
	}
	return paths[0], name, nil
}

// resolveUserPath expands a leading "~/" and makes path absolute against cwd.
func resolveUserPath(path, cwd string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		path = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(path, "~"))
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

// runClauditableAnnotate runs "clauditable <args...>" against sessionID with
// stdin (when non-empty) and returns the record ID it prints.
func runClauditableAnnotate(recordsPath, sessionID, stdin string, args ...string) (string, error) {
	clauditablePath, err := findBinary("clauditable")
	if err != nil {
		return "", fmt.Errorf("clauditable not found — %w", err)
	}
	cmd := exec.Command(clauditablePath, args...)
	cmd.Env = append(environWithoutClauditableGuard(),
		EnvAgentRecordsPath+"="+recordsPath,
		EnvAgentSession+"="+sessionID,
		EnvUFAHead+"="+fcHeadID,
	)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
