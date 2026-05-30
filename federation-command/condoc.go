package main

// condoc.go — condoc (conversational document) handler.
//
// Self-contained: delete this file and remove the corresponding fields and
// switch-cases in main.go and the BlinkerCondoc entries in blinker.go to
// cleanly drop condoc support without affecting anything else.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ===== PHASE =====

type condocPhase int

const (
	condocPhaseProposed     condocPhase = iota // proposal file written; watching main for !HANDOFF!
	condocPhaseBranching                       // git branch creation in progress
	condocPhaseAwaitingStep                    // step templated; waiting for human fill + !HANDOFF!
	condocPhaseRunningAgent                    // agent executing step or revision
	condocPhaseCommitting                      // post-agent git commit in progress
	condocPhaseAwaitingAction                  // step done; watching step file for revision/!COMPLETED!
	condocPhaseDone                            // condoc completed; handler will exit
)

func (p condocPhase) label() string {
	switch p {
	case condocPhaseProposed:
		return "awaiting proposal acceptance"
	case condocPhaseBranching:
		return "creating branch…"
	case condocPhaseAwaitingStep:
		return "awaiting step"
	case condocPhaseRunningAgent:
		return "agent running"
	case condocPhaseCommitting:
		return "committing…"
	case condocPhaseAwaitingAction:
		return "awaiting action"
	case condocPhaseDone:
		return "completed"
	default:
		return "unknown"
	}
}

// ===== SESSION =====

// CondocSession holds all mutable state for an active condoc handler session.
type CondocSession struct {
	mainFilePath string
	description  string
	startTime    int64
	branch       string
	repoRoot     string
	callerPath   string

	phase        condocPhase
	commitTarget condocPhase // phase to transition to when a condocPhaseCommitting git op completes
	stepNum      int         // current step number (1-based; 0 = not yet started)
	stepFile     string      // absolute path to the active child step doc, "" if none

	active          bool
	verbose         bool      // verbose Human-Prompt text if true; brief if false
	statusMsg       string    // transient message shown in dynapane
	lastPullAt      time.Time // last time a pull --rebase was attempted
	replyTmpPath    string    // temp file capturing agent terminal output for the current run
	pendingRevLetter string   // "" for initial step reply, "A"/"B"/... for revision reply
}

// ===== MESSAGES =====

// condocTickMsg fires on the polling interval to check watched files.
type condocTickMsg struct{}

// condocGitDoneMsg is sent when an async git sequence completes.
type condocGitDoneMsg struct{ errStr string }

// condocPullDoneMsg is sent when an async pull --rebase completes.
type condocPullDoneMsg struct{ errStr string }

// condocAgentStepDoneMsg is sent when the agent finishes executing a step or revision.
type condocAgentStepDoneMsg struct {
	exitCode int
	execErr  error
}

// condocExecReadyMsg defers a tea.ExecProcess by one render cycle.
type condocExecReadyMsg struct {
	runCmd   *exec.Cmd
	callback func(error) tea.Msg
}

const condocPollInterval = 1 * time.Second
const condocPullInterval = 30 * time.Second

func condocTickCmd() tea.Cmd {
	return tea.Tick(condocPollInterval, func(time.Time) tea.Msg {
		return condocTickMsg{}
	})
}

func deferCondocExec(cmd *exec.Cmd, cb func(error) tea.Msg) tea.Cmd {
	return func() tea.Msg { return condocExecReadyMsg{runCmd: cmd, callback: cb} }
}

// ===== HELPERS =====

var ansiEscRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b[^[]`)

// stripANSI removes ANSI escape sequences and collapses CR-overwritten lines.
func stripANSI(s string) string {
	s = ansiEscRe.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		parts := strings.Split(line, "\r")
		lines[i] = parts[len(parts)-1]
	}
	return strings.Join(lines, "\n")
}

func ordinal(n int) string {
	switch n {
	case 1:
		return "first"
	case 2:
		return "second"
	case 3:
		return "third"
	case 4:
		return "fourth"
	case 5:
		return "fifth"
	case 6:
		return "sixth"
	case 7:
		return "seventh"
	case 8:
		return "eighth"
	case 9:
		return "ninth"
	case 10:
		return "tenth"
	}
	return fmt.Sprintf("step %d", n)
}

// ===== FILE HELPERS =====

var condocHandoffRe = regexp.MustCompile(`(?m)^!HANDOFF!\s*$`)
var condocCompletedRe = regexp.MustCompile(`(?m)^!COMPLETED!\s*$`)

func condocFileHasHandoff(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && condocHandoffRe.Match(b)
}

func condocFileHasCompleted(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && condocCompletedRe.Match(b)
}

// ===== GIT HELPERS =====

func condocFindGitRoot(filePath string) (string, error) {
	dir := filepath.Dir(filePath)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no git repository found for %s", filePath)
		}
		dir = parent
	}
}

func condocCallerPath(filePath, cwd string) string {
	rel, err := filepath.Rel(filepath.Dir(filePath), cwd)
	if err != nil {
		return ".."
	}
	return rel
}

// runGitSequence runs git commands in sequence inside dir.
// Returns a condocGitDoneMsg — intended for use as a tea.Cmd goroutine.
func runGitSequence(cmds [][]string, dir string) tea.Cmd {
	return func() tea.Msg {
		for _, args := range cmds {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				return condocGitDoneMsg{errStr: fmt.Sprintf("git %s: %v\n%s", args[0], err, string(out))}
			}
		}
		return condocGitDoneMsg{}
	}
}

// runGitPullRebase runs `git pull --rebase origin <branch>` quietly.
func runGitPullRebase(dir, branch string) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("git", "pull", "--rebase", "origin", branch)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return condocPullDoneMsg{errStr: fmt.Sprintf("pull --rebase: %v\n%s", err, string(out))}
		}
		return condocPullDoneMsg{}
	}
}

// ===== PATH HELPERS =====

func condocBaseName(filePath string) string {
	base := filepath.Base(filePath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func condocImplsDirPath(mainFilePath string) string {
	base := condocBaseName(mainFilePath)
	// "Simple" → "simpleImpls"
	var dirName string
	if len(base) > 0 {
		dirName = strings.ToLower(base[:1]) + base[1:] + "Impls"
	} else {
		dirName = "condocImpls"
	}
	return filepath.Join(filepath.Dir(mainFilePath), dirName)
}

func condocStepFilePath(mainFilePath string, stepNum int) string {
	return filepath.Join(condocImplsDirPath(mainFilePath), fmt.Sprintf("Step%dPrompt.md", stepNum))
}

// ===== TEMPLATE HELPERS =====

func condocYAMLHeader(startTime int64, branch, callerPath string) string {
	return fmt.Sprintf("<!--\n```condoc-yaml\ncondoc:\n  startTime: %d\n  controlScheme: same-repo\n  branch: %s\n  callerPath: %s\n```\n-->", startTime, branch, callerPath)
}

// writeProposalFile creates the initial condoc file (snp0 format).
func writeProposalFile(cs *CondocSession) error {
	baseName := condocBaseName(cs.mainFilePath)
	var humanPrompt string
	if cs.verbose {
		humanPrompt = "This is the proposed document structure and condoc setup for our new condoc.\n\n" +
			"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n" +
			"To accept this condoc proposal add the '!HANDOFF!' directive to the end of the file followed by only whitespace, and the handler will template the first step."
	} else {
		humanPrompt = "To accept this condoc proposal add the '!HANDOFF!'."
	}
	content := "# " + baseName + "\n\n" +
		condocYAMLHeader(cs.startTime, cs.branch, cs.callerPath) + "\n\n" +
		cs.description + "\n\n\n" +
		"## Human-Prompt\n\n" +
		humanPrompt + "\n"
	return os.WriteFile(cs.mainFilePath, []byte(content), 0644)
}

// removeHumanPromptSection strips the "## Human-Prompt" section and all content after it
// until EOF. Also removes bare !HANDOFF!/!COMPLETED! lines throughout.
func removeHumanPromptSection(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inSection := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "## Human-Prompt" {
			inSection = true
			continue
		}
		if inSection {
			continue
		}
		if t == "!HANDOFF!" || t == "!COMPLETED!" {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimRight(strings.Join(out, "\n"), " \t\n") + "\n"
}

func addHumanPrompt(content, text string) string {
	return strings.TrimRight(content, "\n") + "\n\n\n## Human-Prompt\n\n" + text + "\n"
}

var condocStepHeadingRe = regexp.MustCompile(`(?m)^### Step (\d+) - (.+)$`)
var condocPromptBlockRe = regexp.MustCompile("(?s)```prompt\n(.*?)\n```")

type parsedCondocStep struct {
	num    int
	title  string
	prompt string
}

// parseLastStep extracts the last ### Step N heading and its prompt block from content.
func parseLastStep(content string) (parsedCondocStep, bool) {
	idxs := condocStepHeadingRe.FindAllStringSubmatchIndex(content, -1)
	if len(idxs) == 0 {
		return parsedCondocStep{}, false
	}
	last := idxs[len(idxs)-1]
	numStr := content[last[2]:last[3]]
	title := content[last[4]:last[5]]
	num, _ := strconv.Atoi(numStr)
	afterHeading := content[last[0]:]
	pm := condocPromptBlockRe.FindStringSubmatch(afterHeading)
	if pm == nil {
		return parsedCondocStep{}, false
	}
	return parsedCondocStep{
		num:    num,
		title:  strings.TrimSpace(title),
		prompt: strings.TrimSpace(pm[1]),
	}, true
}

// templateStep adds a step template to the main file and the appropriate Human-Prompt.
func templateStep(mainFilePath string, stepNum int, prevStepCompleted, verbose bool) error {
	b, err := os.ReadFile(mainFilePath)
	if err != nil {
		return err
	}
	content := removeHumanPromptSection(string(b))
	content = strings.TrimRight(content, "\n") +
		fmt.Sprintf("\n\n\n### Step %d - <REPLACE-TITLE>\n\n```prompt\n<REPLACE-PROMPT>\n```\n\n", stepNum)

	var humanPrompt string
	if !prevStepCompleted {
		if verbose {
			humanPrompt = fmt.Sprintf(
				"The proposed document has been accepted and we have templated step %d.\n\n"+
					"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n"+
					"Please replace the title and the prompt with the desired input for our AI.\n\n"+
					"When you are done add the '!HANDOFF!' directive to the end of the file followed by only whitespace,\n"+
					"and the handler will instruct the AI to execute step %d.", stepNum, stepNum)
		} else {
			humanPrompt = fmt.Sprintf(
				"Once you have added the Title and Prompt add the '!HANDOFF!' directive to execute the %s step.",
				ordinal(stepNum))
		}
	} else {
		if verbose {
			humanPrompt = fmt.Sprintf(
				"Step %d has been completed and we have templated step %d.\n\n"+
					"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n"+
					"Please replace the title and the prompt with the desired input for our AI.\n\n"+
					"When you are done add the '!HANDOFF!' directive to the end of the file followed by only whitespace,\n"+
					"and the handler will instruct the AI to execute step %d.\n\n"+
					"Alternatively, add the '!COMPLETED!' directive to consider this condoc a success and conclude it.",
				stepNum-1, stepNum, stepNum)
		} else {
			humanPrompt = fmt.Sprintf(
				"Add the Title and Prompt then submit the '!HANDOFF!' directive to execute the %s step,"+
					" or submit the '!COMPLETED!' directive to complete this condoc.",
				ordinal(stepNum))
		}
	}
	content = addHumanPrompt(content, humanPrompt)
	return os.WriteFile(mainFilePath, []byte(content), 0644)
}

// writeStepFile creates the initial step child doc with a backlink to the main file and the prompt.
func writeStepFile(stepFilePath, mainFilePath, prompt string) error {
	if err := os.MkdirAll(filepath.Dir(stepFilePath), 0755); err != nil {
		return err
	}
	baseName := condocBaseName(mainFilePath)
	mainFileName := filepath.Base(mainFilePath)
	backLink := fmt.Sprintf("[%s](../%s)", baseName, mainFileName)
	content := "# Prompt\n\n" + backLink + "\n\n" + prompt + "\n"
	return os.WriteFile(stepFilePath, []byte(content), 0644)
}

// insertStepLink inserts a markdown link to the step file right after the filled-in step heading.
func insertStepLink(content, mainFilePath string, stepNum int) string {
	implsDir := filepath.Base(condocImplsDirPath(mainFilePath))
	linkTarget := fmt.Sprintf("%s/Step%dPrompt.md", implsDir, stepNum)
	linkText := fmt.Sprintf("[Step %d Prompt](%s)", stepNum, linkTarget)
	if strings.Contains(content, linkText) {
		return content
	}
	headingRe := regexp.MustCompile(fmt.Sprintf(`(?m)^### Step %d - .+$`, stepNum))
	return headingRe.ReplaceAllStringFunc(content, func(match string) string {
		if strings.Contains(match, "<REPLACE-TITLE>") {
			return match
		}
		return match + "\n\n" + linkText
	})
}

// updateMainAfterStepStart updates the main file to redirect focus to the step file (snp4).
// It inserts a link to the step file and updates the Human-Prompt section.
func updateMainAfterStepStart(mainFilePath string, stepNum int, verbose bool) error {
	b, err := os.ReadFile(mainFilePath)
	if err != nil {
		return err
	}
	content := removeHumanPromptSection(string(b))
	content = insertStepLink(content, mainFilePath, stepNum)
	ord := ordinal(stepNum)
	var humanPrompt string
	if verbose {
		humanPrompt = fmt.Sprintf(
			"The flow of the condoc is now within the %s step.\n\n"+
				"Please respond to the Human-Prompt in the %s step and add the '!HANDOFF!' directive there,\n"+
				"or the '!COMPLETED!' directive when the step is complete.",
			ord, ord)
	} else {
		humanPrompt = fmt.Sprintf("The flow of the condoc is now within the %s step.", ord)
	}
	content = addHumanPrompt(content, humanPrompt)
	return os.WriteFile(mainFilePath, []byte(content), 0644)
}

// addRevisionTemplate appends the REPLACE-Revision template + Human-Prompt to the step file.
func addRevisionTemplate(stepFilePath string, verbose bool) error {
	b, err := os.ReadFile(stepFilePath)
	if err != nil {
		return err
	}
	var humanPrompt string
	if verbose {
		humanPrompt = "The AI has responded to the prompt in this step.\n\n" +
			"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n" +
			"To REVISE the work, replace '<REPLACE-Revision|Retry>' with 'Revision' and '<REPLACE-PROMPT>'\n" +
			"with your revision request, then add the '!HANDOFF!' directive.\n\n" +
			"Alternatively, add the '!COMPLETED!' directive to consider this step a success and conclude it."
	} else {
		humanPrompt = "When you are done add the '!HANDOFF!' or '!COMPLETED!' directive."
	}
	content := strings.TrimRight(string(b), "\n") + "\n\n\n" +
		"## <REPLACE-Revision|Retry> A\n\n" +
		"<REPLACE-PROMPT>\n\n\n" +
		"## Human-Prompt\n\n" +
		humanPrompt + "\n"
	return os.WriteFile(stepFilePath, []byte(content), 0644)
}

// addNextRevisionTemplate appends a next-letter revision template to the step file
// after a completed revision cycle.
func addNextRevisionTemplate(stepFilePath, lastLetter string, verbose bool) error {
	b, err := os.ReadFile(stepFilePath)
	if err != nil {
		return err
	}
	nextLetter := string(rune(lastLetter[0] + 1))
	var humanPrompt string
	if verbose {
		humanPrompt = "The AI has responded to the revision in this step.\n\n" +
			"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n" +
			"To REVISE further, replace '<REPLACE-Revision|Retry>' with 'Revision' and '<REPLACE-PROMPT>'\n" +
			"with your revision request, then add the '!HANDOFF!' directive.\n\n" +
			"Alternatively, add the '!COMPLETED!' directive to consider this step a success and conclude it."
	} else {
		humanPrompt = "When you are done add the '!HANDOFF!' or '!COMPLETED!' directive."
	}
	content := removeHumanPromptSection(string(b))
	content = strings.TrimRight(content, "\n") + "\n\n\n" +
		"## <REPLACE-Revision|Retry> " + nextLetter + "\n\n" +
		"<REPLACE-PROMPT>\n\n\n" +
		"## Human-Prompt\n\n" +
		humanPrompt + "\n"
	return os.WriteFile(stepFilePath, []byte(content), 0644)
}

// removeUnfilledRevisionTemplates strips any ## <REPLACE-Revision|Retry> X blocks
// whose body is still the unfilled <REPLACE-PROMPT> placeholder.
func removeUnfilledRevisionTemplates(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	skip := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## <REPLACE-Revision|Retry>") {
			skip = true
			continue
		}
		if skip {
			if t == "" || t == "<REPLACE-PROMPT>" {
				continue
			}
			skip = false
		}
		out = append(out, line)
	}
	return strings.TrimRight(strings.Join(out, "\n"), " \t\n") + "\n"
}

// finalizeStepFile replaces the Human-Prompt section with a completion timestamp.
func finalizeStepFile(stepFilePath string) error {
	b, err := os.ReadFile(stepFilePath)
	if err != nil {
		return err
	}
	content := removeHumanPromptSection(string(b))
	content = removeUnfilledRevisionTemplates(content)
	now := time.Now()
	content = strings.TrimRight(content, "\n") +
		fmt.Sprintf("\n\n\n## Step Completed\n\nThis step was completed at %d (%s).\n",
			now.Unix(), now.UTC().Format("Mon Jan 2 03:04:05 PM MST 2006"))
	return os.WriteFile(stepFilePath, []byte(content), 0644)
}

// finalizeMainFile adds the completion section, removes unstarted step templates.
func finalizeMainFile(mainFilePath string) error {
	b, err := os.ReadFile(mainFilePath)
	if err != nil {
		return err
	}
	content := removeHumanPromptSection(string(b))

	// Remove any unstarted step template block (### Step N - <REPLACE-TITLE> ... ```)
	lines := strings.Split(content, "\n")
	var out []string
	skip := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if condocStepHeadingRe.MatchString(t) && strings.Contains(t, "<REPLACE-TITLE>") {
			skip = true
			continue
		}
		if skip {
			if t == "" || t == "```prompt" || t == "<REPLACE-PROMPT>" || t == "```" {
				continue
			}
			skip = false
		}
		out = append(out, line)
	}
	content = strings.TrimRight(strings.Join(out, "\n"), " \t\n") + "\n"

	now := time.Now()
	content += fmt.Sprintf("\n\n## Condoc Completed\n\nThis condoc was completed at %d (%s).\n",
		now.Unix(), now.UTC().Format("Mon Jan 2 03:04:05 PM MST 2006"))
	return os.WriteFile(mainFilePath, []byte(content), 0644)
}

// appendReplyToStepFile writes the captured agent reply under "## Reply" (or "## Reply X")
// at the end of the step file. Called by the handler — agents must never write to step files.
func appendReplyToStepFile(stepFilePath, letter, replyText string) error {
	b, err := os.ReadFile(stepFilePath)
	if err != nil {
		return err
	}
	heading := "## Reply"
	if letter != "" {
		heading = "## Reply " + letter
	}
	// Strip any Human-Prompt section (and HANDOFF directive) before appending — for revision
	// runs the file still has ## Human-Prompt + !HANDOFF! at EOF when this is called, so
	// without stripping, addNextRevisionTemplate's removeHumanPromptSection call would
	// delete the reply we just wrote.
	cleaned := removeHumanPromptSection(string(b))
	content := strings.TrimRight(cleaned, "\n") +
		"\n\n\n" + heading + "\n\n" + replyText + "\n"
	return os.WriteFile(stepFilePath, []byte(content), 0644)
}

// ===== REVISION DETECTION =====

var condocRevisionHeadingRe = regexp.MustCompile(`(?m)^## Revision ([A-Z])$`)
var condocReplyLetterRe = regexp.MustCompile(`(?m)^## Reply ([A-Z])$`)

// pendingRevisionLetter returns the letter of a Revision heading that has no
// corresponding Reply heading, or "" if all revisions have replies.
func pendingRevisionLetter(content string) string {
	revs := condocRevisionHeadingRe.FindAllStringSubmatch(content, -1)
	replies := condocReplyLetterRe.FindAllStringSubmatch(content, -1)
	if len(revs) > len(replies) {
		return revs[len(revs)-1][1]
	}
	return ""
}

// revisionText extracts the human text under "## Revision X" in the step file content.
func revisionText(content, letter string) string {
	re := regexp.MustCompile(`(?m)^## Revision ` + regexp.QuoteMeta(letter) + `$`)
	loc := re.FindStringIndex(content)
	if loc == nil {
		return ""
	}
	after := content[loc[1]:]
	endRe := regexp.MustCompile(`(?m)^## `)
	if endLoc := endRe.FindStringIndex(after); endLoc != nil {
		after = after[:endLoc[0]]
	}
	return strings.TrimSpace(after)
}

// lastReplyLetter returns the letter of the most recent "## Reply X" section, or "".
func lastReplyLetter(content string) string {
	matches := condocReplyLetterRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

// ===== AGENT PROMPT BUILDERS =====

func buildCondocStepPrompt(stepFilePath string) string {
	return fmt.Sprintf(
		"You are executing a condoc step. The step file is at '%s'. "+
			"Read the step file. Execute the task described in the '# Prompt' section. "+
			"IMPORTANT: Do NOT write to or modify the step file or any other condoc files — "+
			"the condoc handler will automatically capture your terminal response as the reply. "+
			"When you have finished, provide a brief summary (2-3 sentences) of what you accomplished "+
			"as your terminal response.",
		stepFilePath)
}

func buildCondocRevisionPrompt(stepFilePath, revLetter string) string {
	return fmt.Sprintf(
		"You are executing a condoc step revision. The step file is at '%s'. "+
			"Read the step file. You previously completed a task (see '## Reply') and the human has "+
			"requested Revision %s (see '## Revision %s'). "+
			"Execute the revision as described. "+
			"IMPORTANT: Do NOT write to or modify the step file or any other condoc files — "+
			"the condoc handler will automatically capture your terminal response as the reply. "+
			"When you have finished, provide a brief summary (2-3 sentences) of what you changed "+
			"as your terminal response.",
		stepFilePath, revLetter, revLetter)
}

// ===== DYNAPANE =====

// CondocDynapane renders condoc status above the prompt.
type condocMenuAction int

const (
	condocMenuExit        condocMenuAction = iota
	condocMenuPlaceholder
)

const condocMenuItemCount = 2

type CondocDynapane struct {
	active    bool
	session   *CondocSession
	tick      int
	menuIndex int
}

func (cd *CondocDynapane) MenuUp() {
	if cd.menuIndex > 0 {
		cd.menuIndex--
	}
}

func (cd *CondocDynapane) MenuDown() {
	if cd.menuIndex < condocMenuItemCount-1 {
		cd.menuIndex++
	}
}

func (cd *CondocDynapane) MenuAction() condocMenuAction {
	if cd.menuIndex == 1 {
		return condocMenuPlaceholder
	}
	return condocMenuExit
}

// CondocDynapaneTickMsg drives the animation in the dynapane.
type CondocDynapaneTickMsg struct{}

const condocDynapaneTickInterval = 80 * time.Millisecond

func condocDynapaneTickCmd() tea.Cmd {
	return tea.Tick(condocDynapaneTickInterval, func(time.Time) tea.Msg {
		return CondocDynapaneTickMsg{}
	})
}

func (cd *CondocDynapane) Activate(cs *CondocSession) tea.Cmd {
	cd.active = true
	cd.session = cs
	cd.tick = 0
	return condocDynapaneTickCmd()
}

func (cd *CondocDynapane) Deactivate() {
	cd.active = false
	cd.session = nil
}

func (cd *CondocDynapane) IsActive() bool { return cd.active }

func (cd *CondocDynapane) Tick() tea.Cmd {
	if !cd.active {
		return nil
	}
	cd.tick++
	return condocDynapaneTickCmd()
}

var (
	condocBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("34"))

	condocTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("82")).
				Bold(true)

	condocFileStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Italic(true)

	condocDividerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("34"))

	condocPhaseStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("220")).
				Bold(true)

	condocStatusStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243"))

	condocHintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Italic(true)

	condocMenuSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("82")).
				Bold(true)

	condocMenuStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))
)

// condocAnimColors cycles green→yellow for the animated indicator.
var condocAnimColors = []lipgloss.Color{
	"34", "40", "46", "82", "118", "154", "190", "226", "220", "214",
}

func renderCondocIndicator(tick int) string {
	idx := (tick / 3) % len(condocAnimColors)
	style := lipgloss.NewStyle().Foreground(condocAnimColors[idx]).Bold(true)
	return style.Render("◈")
}

func (cd *CondocDynapane) View(windowWidth int) string {
	if !cd.active || cd.session == nil {
		return ""
	}
	if windowWidth <= 0 {
		windowWidth = 100
	}
	innerWidth := windowWidth - 4
	if innerWidth < 20 {
		innerWidth = 20
	}

	cs := cd.session

	indicator := renderCondocIndicator(cd.tick)
	title := condocTitleStyle.Render(" condoc")
	sep := condocDividerStyle.Render("  ---  ")
	fileName := condocFileStyle.Render(filepath.Base(cs.mainFilePath))
	titleRow := indicator + title + sep + fileName
	if w := lipgloss.Width(titleRow); w < innerWidth {
		titleRow += strings.Repeat(" ", innerWidth-w)
	}

	divider := condocDividerStyle.Render(strings.Repeat("─", innerWidth))
	phaseLine := condocPhaseStyle.Render("phase: " + cs.phase.label())

	var watchLine string
	if cs.stepFile != "" && cs.phase == condocPhaseAwaitingAction {
		watchLine = condocStatusStyle.Render("watching: " + filepath.Base(cs.stepFile))
	} else {
		watchLine = condocStatusStyle.Render("watching: " + filepath.Base(cs.mainFilePath))
	}

	var stepLine string
	if cs.stepNum > 0 {
		stepLine = condocStatusStyle.Render(fmt.Sprintf("step: %d", cs.stepNum))
	} else {
		stepLine = condocStatusStyle.Render("step: —")
	}

	menuLabels := []string{"exit", "placeholder"}
	allLines := []string{titleRow, divider, phaseLine, watchLine, stepLine}
	if cs.statusMsg != "" {
		allLines = append(allLines, condocStatusStyle.Render(cs.statusMsg))
	}
	allLines = append(allLines, divider)
	for i, label := range menuLabels {
		if cd.menuIndex == i {
			allLines = append(allLines, condocMenuSelectedStyle.Render("◈ "+label))
		} else {
			allLines = append(allLines, condocMenuStyle.Render("  "+label))
		}
	}
	allLines = append(allLines, condocHintStyle.Render("  ↑↓ navigate  enter to select"))

	content := strings.Join(allLines, "\n")
	pane := condocBorderStyle.Width(innerWidth).Render(content)
	return pane + "\n"
}

// ===== NewCondocSession =====

// NewCondocSession initialises a new condoc session, writes the proposal file,
// and returns the session ready for the handler to start watching.
func NewCondocSession(filePath, description string, verbose bool, cwd string) (*CondocSession, error) {
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(cwd, filePath)
	}
	if !strings.HasSuffix(filePath, ".md") {
		return nil, fmt.Errorf("condoc: file path must have a .md extension")
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return nil, fmt.Errorf("condoc: cannot create directory: %w", err)
	}
	if _, err := os.Stat(filePath); err == nil {
		return nil, fmt.Errorf("condoc: file already exists: %s", filePath)
	}

	repoRoot, err := condocFindGitRoot(filePath)
	if err != nil {
		return nil, fmt.Errorf("condoc: %w", err)
	}

	startTime := time.Now().Unix()
	baseName := condocBaseName(filePath)
	branch := fmt.Sprintf("condoc/%s-%d/main", baseName, startTime)
	callerPath := condocCallerPath(filePath, cwd)

	cs := &CondocSession{
		mainFilePath: filePath,
		description:  description,
		startTime:    startTime,
		branch:       branch,
		repoRoot:     repoRoot,
		callerPath:   callerPath,
		phase:        condocPhaseProposed,
		active:       true,
		verbose:      verbose,
	}
	if err := writeProposalFile(cs); err != nil {
		return nil, fmt.Errorf("condoc: cannot write proposal file: %w", err)
	}
	return cs, nil
}

// ===== UPDATE HANDLERS (methods on appModel, defined here to keep condoc self-contained) =====

// handleCondocTick is called on each condocTickMsg.
// It polls watched files and fires state transitions when events are detected.
func (m appModel) handleCondocTick() (appModel, tea.Cmd) {
	cs := m.condoc
	if cs == nil || !cs.active {
		return m, nil
	}

	// Attempt periodic pull --rebase while waiting (branch must already exist).
	var pullCmd tea.Cmd
	if cs.phase == condocPhaseAwaitingStep || cs.phase == condocPhaseAwaitingAction {
		if time.Since(cs.lastPullAt) >= condocPullInterval {
			cs.lastPullAt = time.Now()
			pullCmd = runGitPullRebase(cs.repoRoot, cs.branch)
		}
	}

	switch cs.phase {
	case condocPhaseProposed:
		if condocFileHasHandoff(cs.mainFilePath) {
			return m.condocAcceptProposal()
		}
	case condocPhaseAwaitingStep:
		if condocFileHasCompleted(cs.mainFilePath) {
			return m.condocCompleteCondoc()
		}
		if condocFileHasHandoff(cs.mainFilePath) {
			return m.condocStartStep()
		}
	case condocPhaseAwaitingAction:
		if cs.stepFile != "" {
			if condocFileHasCompleted(cs.stepFile) {
				return m.condocCompleteStep()
			}
			if condocFileHasHandoff(cs.stepFile) {
				return m.condocRunRevision()
			}
		}
	}
	return m, tea.Batch(condocTickCmd(), pullCmd)
}

// handleCondocPullDone processes the result of a background pull --rebase.
func (m appModel) handleCondocPullDone(msg condocPullDoneMsg) (appModel, tea.Cmd) {
	if msg.errStr != "" && m.condoc != nil {
		m.condoc.statusMsg = "pull: " + msg.errStr
		return m, m.condocDynapane.Activate(m.condoc)
	}
	return m, nil
}

// condocAcceptProposal handles !HANDOFF! on the proposal: creates the git branch,
// templates step 1, and commits.
func (m appModel) condocAcceptProposal() (appModel, tea.Cmd) {
	cs := m.condoc
	cs.phase = condocPhaseBranching
	cs.statusMsg = "creating branch and templating step 1…"

	// Pre-emptively update the file (step 1 template will be written after branch creation)
	if err := templateStep(cs.mainFilePath, 1, false, cs.verbose); err != nil {
		return m.condocError("templateStep: " + err.Error())
	}

	gitCmds := [][]string{
		{"checkout", "-b", cs.branch},
		{"add", "."},
		{"commit", "-m", "condoc: accept proposal, template step 1"},
		{"push", "--set-upstream", "origin", cs.branch},
	}
	return m, tea.Batch(
		m.condocDynapane.Activate(cs),
		runGitSequence(gitCmds, cs.repoRoot),
	)
}

// handleCondocGitDone processes the result of an async git sequence.
func (m appModel) handleCondocGitDone(msg condocGitDoneMsg) (appModel, tea.Cmd) {
	cs := m.condoc
	if cs == nil {
		return m, nil
	}
	if msg.errStr != "" {
		cs.statusMsg = "git error: " + msg.errStr
		cs.phase = condocPhaseAwaitingStep // let user sort it out
		return m, tea.Batch(
			tea.Println(errorStyle.Render("condoc git: "+msg.errStr)),
			m.condocDynapane.Activate(cs),
			condocTickCmd(),
		)
	}

	switch cs.phase {
	case condocPhaseBranching:
		// Branch created + step 1 committed — now wait for human to fill the step.
		cs.phase = condocPhaseAwaitingStep
		cs.stepNum = 1
		cs.statusMsg = ""
		m.blinker.SetState(BlinkerCondoc)
		return m, tea.Batch(
			tea.Println(successStyle.Render("condoc: branch created, step 1 templated")),
			m.condocDynapane.Activate(cs),
			m.blinker.ResetTick(),
			condocTickCmd(),
		)

	case condocPhaseCommitting:
		// Commit done — advance to whichever phase was requested.
		target := cs.commitTarget
		if target == 0 {
			target = condocPhaseAwaitingAction // safe default
		}
		cs.phase = target
		cs.statusMsg = ""
		m.blinker.SetState(BlinkerCondoc)
		return m, tea.Batch(
			m.condocDynapane.Activate(cs),
			m.blinker.ResetTick(),
			condocTickCmd(),
		)
	}
	return m, condocTickCmd()
}

// condocStartStep handles !HANDOFF! on the main file when a step is ready to run.
func (m appModel) condocStartStep() (appModel, tea.Cmd) {
	cs := m.condoc
	b, err := os.ReadFile(cs.mainFilePath)
	if err != nil {
		return m.condocError("read main file: " + err.Error())
	}
	step, ok := parseLastStep(string(b))
	if !ok {
		return m.condocError("could not parse step from main file — fill in the title and prompt first")
	}

	stepPath := condocStepFilePath(cs.mainFilePath, step.num)
	if err := writeStepFile(stepPath, cs.mainFilePath, step.prompt); err != nil {
		return m.condocError("write step file: " + err.Error())
	}
	if err := updateMainAfterStepStart(cs.mainFilePath, step.num, cs.verbose); err != nil {
		return m.condocError("update main file: " + err.Error())
	}

	cs.stepNum = step.num
	cs.stepFile = stepPath
	cs.pendingRevLetter = ""
	cs.phase = condocPhaseRunningAgent
	cs.statusMsg = "running agent for step " + strconv.Itoa(step.num) + "…"

	prompt := buildCondocStepPrompt(stepPath)
	agentCmd, errMsg := buildAgentPromptCmd(ModeWrite, prompt, m.currentAgent, m.currentModel, m.sessionDir)
	if agentCmd == nil {
		return m.condocError("build agent cmd: " + errMsg)
	}
	agentCmd.Dir = cs.repoRoot
	agentCmd.Env = append(agentCmd.Env, "AA_QUIET=true")

	replyTmpPath := filepath.Join(os.TempDir(), fmt.Sprintf("condoc-reply-%d.txt", time.Now().UnixNano()))
	cs.replyTmpPath = replyTmpPath
	runCmd := teeCommand(agentCmd, replyTmpPath)

	m.condocDynapane.Deactivate()
	m.blinker.SetState(BlinkerInactive)

	return m, tea.Sequence(
		tea.Println(sessionStyle.Render("condoc: running agent for "+filepath.Base(stepPath)+"…")),
		tea.Println(condocHintStyle.Render("prompt: "+step.prompt)),
		deferCondocExec(runCmd, func(err error) tea.Msg {
			return condocAgentStepDoneMsg{exitCode: extractExitCode(err), execErr: err}
		}),
	)
}

// handleCondocAgentDone processes the result of an agent step/revision execution.
func (m appModel) handleCondocAgentDone(msg condocAgentStepDoneMsg) (appModel, tea.Cmd) {
	cs := m.condoc
	if cs == nil {
		return m, nil
	}

	var statusPrint tea.Cmd
	if msg.execErr != nil {
		if _, ok := msg.execErr.(*exec.ExitError); ok {
			statusPrint = tea.Println(errorStyle.Render(fmt.Sprintf("condoc agent exited: %d", msg.exitCode)))
		} else {
			statusPrint = tea.Println(errorStyle.Render("condoc agent error: " + msg.execErr.Error()))
		}
	} else {
		statusPrint = tea.Println(successStyle.Render("condoc: agent completed"))
	}

	// Read and strip the captured terminal reply from the temp file, then write it to
	// the step file. The handler owns all condoc file writes — agents must not touch them.
	replyText := "(no reply captured)"
	if cs.replyTmpPath != "" {
		if raw, err := os.ReadFile(cs.replyTmpPath); err == nil {
			cleaned := strings.TrimSpace(stripANSI(string(raw)))
			if cleaned != "" {
				replyText = cleaned
			}
		}
		_ = os.Remove(cs.replyTmpPath)
		cs.replyTmpPath = ""
	}

	revLetter := cs.pendingRevLetter
	if err := appendReplyToStepFile(cs.stepFile, revLetter, replyText); err != nil {
		return m.condocError("append reply to step file: " + err.Error())
	}

	var templateErr error
	if revLetter != "" {
		templateErr = addNextRevisionTemplate(cs.stepFile, revLetter, cs.verbose)
	} else {
		templateErr = addRevisionTemplate(cs.stepFile, cs.verbose)
	}
	if templateErr != nil {
		return m.condocError("add revision template: " + templateErr.Error())
	}

	cs.phase = condocPhaseCommitting
	cs.commitTarget = condocPhaseAwaitingAction
	cs.statusMsg = "committing step output…"
	m.blinker.SetState(BlinkerCondoc)

	gitCmds := [][]string{
		{"add", "."},
		{"commit", "-m", fmt.Sprintf("condoc: step %d agent output", cs.stepNum)},
		{"push", "origin", cs.branch},
	}
	return m, tea.Batch(
		statusPrint,
		m.condocDynapane.Activate(cs),
		m.blinker.ResetTick(),
		runGitSequence(gitCmds, cs.repoRoot),
	)
}

// condocRunRevision handles !HANDOFF! on the step file (revision requested).
func (m appModel) condocRunRevision() (appModel, tea.Cmd) {
	cs := m.condoc
	b, err := os.ReadFile(cs.stepFile)
	if err != nil {
		return m.condocError("read step file: " + err.Error())
	}
	revLetter := pendingRevisionLetter(string(b))
	if revLetter == "" {
		// No pending revision — shouldn't happen, but treat as no-op.
		return m, condocTickCmd()
	}

	revText := revisionText(string(b), revLetter)

	cs.pendingRevLetter = revLetter
	cs.phase = condocPhaseRunningAgent
	cs.statusMsg = "running agent for revision " + revLetter + "…"

	prompt := buildCondocRevisionPrompt(cs.stepFile, revLetter)
	agentCmd, errMsg := buildAgentPromptCmd(ModeWrite, prompt, m.currentAgent, m.currentModel, m.sessionDir)
	if agentCmd == nil {
		return m.condocError("build agent cmd: " + errMsg)
	}
	agentCmd.Dir = cs.repoRoot
	agentCmd.Env = append(agentCmd.Env, "AA_QUIET=true")

	replyTmpPath := filepath.Join(os.TempDir(), fmt.Sprintf("condoc-reply-%d.txt", time.Now().UnixNano()))
	cs.replyTmpPath = replyTmpPath
	runCmd := teeCommand(agentCmd, replyTmpPath)

	m.condocDynapane.Deactivate()
	m.blinker.SetState(BlinkerInactive)

	return m, tea.Sequence(
		tea.Println(sessionStyle.Render("condoc: running agent for revision "+revLetter+"…")),
		tea.Println(condocHintStyle.Render("revision: "+revText)),
		deferCondocExec(runCmd, func(err error) tea.Msg {
			return condocAgentStepDoneMsg{exitCode: extractExitCode(err), execErr: err}
		}),
	)
}

// condocCompleteStep handles !COMPLETED! on the step file.
func (m appModel) condocCompleteStep() (appModel, tea.Cmd) {
	cs := m.condoc
	if err := finalizeStepFile(cs.stepFile); err != nil {
		return m.condocError("finalize step file: " + err.Error())
	}
	nextStep := cs.stepNum + 1
	if err := templateStep(cs.mainFilePath, nextStep, true, cs.verbose); err != nil {
		return m.condocError("template next step: " + err.Error())
	}

	cs.phase = condocPhaseCommitting
	cs.commitTarget = condocPhaseAwaitingStep
	cs.stepFile = ""
	cs.stepNum = nextStep
	cs.statusMsg = fmt.Sprintf("step %d completed, templating step %d…", nextStep-1, nextStep)

	gitCmds := [][]string{
		{"add", "."},
		{"commit", "-m", fmt.Sprintf("condoc: step %d completed, template step %d", nextStep-1, nextStep)},
		{"push", "origin", cs.branch},
	}
	// After commit, condocPhaseCommitting handler will set phase → condocPhaseAwaitingStep.
	return m, tea.Batch(
		tea.Println(successStyle.Render(fmt.Sprintf("condoc: step %d completed", nextStep-1))),
		m.condocDynapane.Activate(cs),
		runGitSequence(gitCmds, cs.repoRoot),
	)
}

// condocCompleteCondoc handles !COMPLETED! on the main file.
func (m appModel) condocCompleteCondoc() (appModel, tea.Cmd) {
	cs := m.condoc
	if err := finalizeMainFile(cs.mainFilePath); err != nil {
		return m.condocError("finalize main file: " + err.Error())
	}
	cs.phase = condocPhaseDone
	cs.active = false

	gitCmds := [][]string{
		{"add", "."},
		{"commit", "-m", "condoc: completed"},
		{"push", "origin", cs.branch},
	}
	m.condocDynapane.Deactivate()
	m.condoc = nil
	m.blinker.SetState(BlinkerIdle)
	m.input.Focus()
	m.input.Prompt = buildPrompt(m.cwd, m.currentAgent, m.currentModel, m.lastExitCode)
	m.prevInputLen = 0

	return m, tea.Batch(
		tea.Println(successStyle.Render("condoc: completed — "+filepath.Base(cs.mainFilePath))),
		runGitSequence(gitCmds, cs.repoRoot),
		m.blinker.ResetTick(),
	)
}

// exitCondoc exits condoc mode, leaving files untouched.
func (m appModel) exitCondoc() (appModel, tea.Cmd) {
	if m.condoc != nil {
		m.condoc.active = false
	}
	m.condoc = nil
	m.condocDynapane.Deactivate()
	m.blinker.SetState(BlinkerIdle)
	m.input.SetValue("")
	m.input.Focus()
	m.input.Prompt = buildPrompt(m.cwd, m.currentAgent, m.currentModel, m.lastExitCode)
	m.prevInputLen = 0
	return m, tea.Batch(
		tea.Println(sessionStyle.Render("condoc mode exited")),
		m.blinker.ResetTick(),
	)
}

// condocError prints an error and suspends polling so the user can investigate.
func (m appModel) condocError(msg string) (appModel, tea.Cmd) {
	if m.condoc != nil {
		m.condoc.statusMsg = "error: " + msg
	}
	return m, tea.Batch(
		tea.Println(errorStyle.Render("condoc: "+msg)),
		m.condocDynapane.Activate(m.condoc),
		condocTickCmd(),
	)
}

// parseCondocCommand parses `condoc [-v|--verbose] <filepath> ["<description>"]`.
func parseCondocCommand(line string) (filePath, description string, verbose bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "condoc"))
	if rest == "" {
		return "", "", false
	}
	args := parseArgs(rest)
	if len(args) == 0 {
		return "", "", false
	}
	var remaining []string
	for _, arg := range args {
		if arg == "-v" || arg == "--verbose" {
			verbose = true
		} else {
			remaining = append(remaining, arg)
		}
	}
	if len(remaining) == 0 {
		return "", "", verbose
	}
	filePath = remaining[0]
	if len(remaining) > 1 {
		description = strings.Join(remaining[1:], " ")
	}
	return filePath, description, verbose
}
