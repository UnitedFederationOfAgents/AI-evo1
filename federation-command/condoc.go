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

	active    bool
	statusMsg string // transient message shown in dynapane
}

// ===== MESSAGES =====

// condocTickMsg fires on the polling interval to check watched files.
type condocTickMsg struct{}

// condocGitDoneMsg is sent when an async git sequence completes.
type condocGitDoneMsg struct{ errStr string }

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

func condocTickCmd() tea.Cmd {
	return tea.Tick(condocPollInterval, func(time.Time) tea.Msg {
		return condocTickMsg{}
	})
}

func deferCondocExec(cmd *exec.Cmd, cb func(error) tea.Msg) tea.Cmd {
	return func() tea.Msg { return condocExecReadyMsg{runCmd: cmd, callback: cb} }
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
	content := "# " + baseName + "\n\n" +
		condocYAMLHeader(cs.startTime, cs.branch, cs.callerPath) + "\n\n" +
		cs.description + "\n\n" +
		"## Human-Prompt\n\n" +
		"This is the proposed document structure and condoc setup for our new condoc.\n\n" +
		"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n" +
		"To accept this condoc proposal add the '!HANDOFF!' directive to the end of the file followed by only whitespace, and the handler will template the first step.\n"
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
	return strings.TrimRight(content, "\n") + "\n\n## Human-Prompt\n\n" + text + "\n"
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
func templateStep(mainFilePath string, stepNum int, prevStepCompleted bool) error {
	b, err := os.ReadFile(mainFilePath)
	if err != nil {
		return err
	}
	content := removeHumanPromptSection(string(b))
	content = strings.TrimRight(content, "\n") +
		fmt.Sprintf("\n\n### Step %d - <REPLACE-TITLE>\n\n```prompt\n<REPLACE-PROMPT>\n```\n\n", stepNum)

	var humanPrompt string
	if !prevStepCompleted {
		humanPrompt = fmt.Sprintf(
			"The proposed document has been accepted and we have templated step %d.\n\n"+
				"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n"+
				"Please replace the title and the prompt with the desired input for our AI.\n\n"+
				"When you are done add the '!HANDOFF!' directive to the end of the file followed by only whitespace,\n"+
				"and the handler will instruct the AI to execute step %d.", stepNum, stepNum)
	} else {
		humanPrompt = fmt.Sprintf(
			"Step %d has been completed and we have templated step %d.\n\n"+
				"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n"+
				"Please replace the title and the prompt with the desired input for our AI.\n\n"+
				"When you are done add the '!HANDOFF!' directive to the end of the file followed by only whitespace,\n"+
				"and the handler will instruct the AI to execute step %d.\n\n"+
				"Alternatively, add the '!COMPLETED!' directive to consider this condoc a success and conclude it.",
			stepNum-1, stepNum, stepNum)
	}
	content = addHumanPrompt(content, humanPrompt)
	return os.WriteFile(mainFilePath, []byte(content), 0644)
}

// writeStepFile creates the initial step child doc with just the prompt.
func writeStepFile(stepFilePath, prompt string) error {
	if err := os.MkdirAll(filepath.Dir(stepFilePath), 0755); err != nil {
		return err
	}
	content := "# Prompt\n\n" + prompt + "\n"
	return os.WriteFile(stepFilePath, []byte(content), 0644)
}

// updateMainAfterStepStart updates the main file to redirect focus to the step file (snp4).
func updateMainAfterStepStart(mainFilePath string) error {
	b, err := os.ReadFile(mainFilePath)
	if err != nil {
		return err
	}
	content := removeHumanPromptSection(string(b))
	humanPrompt := "The flow of the condoc is now within the current step.\n\n" +
		"Please respond to the Human-Prompt in the step file and add the '!HANDOFF!' directive there,\n" +
		"or the '!COMPLETED!' directive when the step is complete."
	content = addHumanPrompt(content, humanPrompt)
	return os.WriteFile(mainFilePath, []byte(content), 0644)
}

// addRevisionTemplate appends the REPLACE-Revision template + Human-Prompt to the step file.
func addRevisionTemplate(stepFilePath string) error {
	b, err := os.ReadFile(stepFilePath)
	if err != nil {
		return err
	}
	content := strings.TrimRight(string(b), "\n") + "\n\n" +
		"## <REPLACE-Revision|Retry> A\n\n" +
		"<REPLACE-PROMPT>\n\n" +
		"## Human-Prompt\n\n" +
		"The AI has responded to the prompt in this step.\n\n" +
		"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n" +
		"To REVISE the work, replace '<REPLACE-Revision|Retry>' with 'Revision' and '<REPLACE-PROMPT>'\n" +
		"with your revision request, then add the '!HANDOFF!' directive.\n\n" +
		"Alternatively, add the '!COMPLETED!' directive to consider this step a success and conclude it.\n"
	return os.WriteFile(stepFilePath, []byte(content), 0644)
}

// addNextRevisionTemplate appends a next-letter revision template to the step file
// after a completed revision cycle.
func addNextRevisionTemplate(stepFilePath, lastLetter string) error {
	b, err := os.ReadFile(stepFilePath)
	if err != nil {
		return err
	}
	nextLetter := string(rune(lastLetter[0] + 1))
	content := removeHumanPromptSection(string(b))
	content = strings.TrimRight(content, "\n") + "\n\n" +
		"## <REPLACE-Revision|Retry> " + nextLetter + "\n\n" +
		"<REPLACE-PROMPT>\n\n" +
		"## Human-Prompt\n\n" +
		"The AI has responded to the revision in this step.\n\n" +
		"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n" +
		"To REVISE further, replace '<REPLACE-Revision|Retry>' with 'Revision' and '<REPLACE-PROMPT>'\n" +
		"with your revision request, then add the '!HANDOFF!' directive.\n\n" +
		"Alternatively, add the '!COMPLETED!' directive to consider this step a success and conclude it.\n"
	return os.WriteFile(stepFilePath, []byte(content), 0644)
}

// finalizeStepFile replaces the Human-Prompt section with a completion timestamp.
func finalizeStepFile(stepFilePath string) error {
	b, err := os.ReadFile(stepFilePath)
	if err != nil {
		return err
	}
	content := removeHumanPromptSection(string(b))
	now := time.Now()
	content = strings.TrimRight(content, "\n") +
		fmt.Sprintf("\n\n## Step Completed\n\nThis step was completed at %d (%s).\n",
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
	content += fmt.Sprintf("\n## Condoc Completed\n\nThis condoc was completed at %d (%s).\n",
		now.Unix(), now.UTC().Format("Mon Jan 2 03:04:05 PM MST 2006"))
	return os.WriteFile(mainFilePath, []byte(content), 0644)
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
			"After completing all tasks, write a brief summary (2-3 sentences) of what you accomplished "+
			"under a new '## Reply' section at the end of the step file. "+
			"Only add the '## Reply' heading and your summary — do not add any other sections.",
		stepFilePath)
}

func buildCondocRevisionPrompt(stepFilePath, revLetter string) string {
	return fmt.Sprintf(
		"You are executing a condoc step revision. The step file is at '%s'. "+
			"Read the step file. You previously completed a task (see '## Reply') and the human has "+
			"requested Revision %s (see '## Revision %s'). "+
			"Execute the revision as described. After completing it, write a brief summary "+
			"under a new '## Reply %s' section at the end of the step file.",
		stepFilePath, revLetter, revLetter, revLetter)
}

// ===== DYNAPANE =====

// CondocDynapane renders condoc status above the prompt.
type CondocDynapane struct {
	active  bool
	session *CondocSession
	tick    int
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

	hintLine := condocHintStyle.Render("ctrl+c to exit condoc mode")

	allLines := []string{titleRow, divider, phaseLine, watchLine, stepLine}
	if cs.statusMsg != "" {
		allLines = append(allLines, condocStatusStyle.Render(cs.statusMsg))
	}
	allLines = append(allLines, divider, hintLine)

	content := strings.Join(allLines, "\n")
	pane := condocBorderStyle.Width(innerWidth).Render(content)
	return pane + "\n"
}

// ===== NewCondocSession =====

// NewCondocSession initialises a new condoc session, writes the proposal file,
// and returns the session ready for the handler to start watching.
func NewCondocSession(filePath, description, cwd string) (*CondocSession, error) {
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(cwd, filePath)
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
	return m, condocTickCmd()
}

// condocAcceptProposal handles !HANDOFF! on the proposal: creates the git branch,
// templates step 1, and commits.
func (m appModel) condocAcceptProposal() (appModel, tea.Cmd) {
	cs := m.condoc
	cs.phase = condocPhaseBranching
	cs.statusMsg = "creating branch and templating step 1…"

	// Pre-emptively update the file (step 1 template will be written after branch creation)
	if err := templateStep(cs.mainFilePath, 1, false); err != nil {
		return m.condocError("templateStep: " + err.Error())
	}

	gitCmds := [][]string{
		{"checkout", "-b", cs.branch},
		{"add", "."},
		{"commit", "-m", "condoc: accept proposal, template step 1"},
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
	if err := writeStepFile(stepPath, step.prompt); err != nil {
		return m.condocError("write step file: " + err.Error())
	}
	if err := updateMainAfterStepStart(cs.mainFilePath); err != nil {
		return m.condocError("update main file: " + err.Error())
	}

	cs.stepNum = step.num
	cs.stepFile = stepPath
	cs.phase = condocPhaseRunningAgent
	cs.statusMsg = "running agent for step " + strconv.Itoa(step.num) + "…"

	prompt := buildCondocStepPrompt(stepPath)
	agentCmd, errMsg := buildAgentPromptCmd(ModeWrite, prompt, m.currentAgent, m.currentModel, m.sessionDir)
	if agentCmd == nil {
		return m.condocError("build agent cmd: " + errMsg)
	}
	agentCmd.Dir = cs.repoRoot

	m.condocDynapane.Deactivate()
	m.blinker.SetState(BlinkerInactive)

	return m, tea.Sequence(
		tea.Println(sessionStyle.Render("condoc: running agent for "+filepath.Base(stepPath)+"…")),
		deferCondocExec(agentCmd, func(err error) tea.Msg {
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

	// Read step file to determine what template to add next.
	b, _ := os.ReadFile(cs.stepFile)
	stepContent := string(b)

	var templateErr error
	if letter := lastReplyLetter(stepContent); letter != "" {
		// Agent completed a lettered reply (e.g. ## Reply A) — offer next revision letter.
		templateErr = addNextRevisionTemplate(cs.stepFile, letter)
	} else {
		// Agent completed the initial step (## Reply, no letter) — offer first revision.
		templateErr = addRevisionTemplate(cs.stepFile)
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

	cs.phase = condocPhaseRunningAgent
	cs.statusMsg = "running agent for revision " + revLetter + "…"

	prompt := buildCondocRevisionPrompt(cs.stepFile, revLetter)
	agentCmd, errMsg := buildAgentPromptCmd(ModeWrite, prompt, m.currentAgent, m.currentModel, m.sessionDir)
	if agentCmd == nil {
		return m.condocError("build agent cmd: " + errMsg)
	}
	agentCmd.Dir = cs.repoRoot

	m.condocDynapane.Deactivate()
	m.blinker.SetState(BlinkerInactive)

	return m, tea.Sequence(
		tea.Println(sessionStyle.Render("condoc: running agent for revision "+revLetter+"…")),
		deferCondocExec(agentCmd, func(err error) tea.Msg {
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
	if err := templateStep(cs.mainFilePath, nextStep, true); err != nil {
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

// parseCondocCommand parses `condoc <filepath> ["<description>"]`.
func parseCondocCommand(line string) (filePath, description string) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "condoc"))
	if rest == "" {
		return "", ""
	}
	args := parseArgs(rest)
	if len(args) == 0 {
		return "", ""
	}
	filePath = args[0]
	if len(args) > 1 {
		description = strings.Join(args[1:], " ")
	}
	return filePath, description
}
