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
	condocPhaseProposed         condocPhase = iota // proposal file written; watching main for !HANDOFF!
	condocPhaseBranching                           // git branch creation in progress
	condocPhaseAwaitingStep                        // step templated; waiting for human fill + !HANDOFF!
	condocPhaseStepStarting                        // step file committed; about to launch agent
	condocPhaseRunningAgent                        // agent executing step or revision
	condocPhaseCommitting                          // post-agent git commit in progress
	condocPhaseAwaitingAction                      // step done; watching step file for revision/!COMPLETED!
	condocPhaseDone                                // condoc completed; handler will exit
	condocPhaseHumanCommitting                     // committing stripped human prompt; pending agent will follow
)

func (p condocPhase) label() string {
	switch p {
	case condocPhaseProposed:
		return "awaiting proposal acceptance"
	case condocPhaseBranching:
		return "creating branch…"
	case condocPhaseAwaitingStep:
		return "awaiting step"
	case condocPhaseStepStarting:
		return "starting step…"
	case condocPhaseRunningAgent:
		return "agent running"
	case condocPhaseCommitting:
		return "committing…"
	case condocPhaseAwaitingAction:
		return "awaiting action"
	case condocPhaseDone:
		return "completed"
	case condocPhaseHumanCommitting:
		return "committing prompt…"
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

	active           bool
	verbose          bool      // verbose Human-Prompt text if true; brief if false
	statusMsg        string    // transient message shown in dynapane
	lastPullAt       time.Time // last time a pull --rebase was attempted
	replyTmpPath     string    // temp file capturing agent terminal output for the current run
	pendingRevLetter string    // "" for initial step reply, "A"/"B"/... for revision/retry reply
	takeCounter      int       // number of retries executed so far (increments on each Retry)
	stepStartHash    string    // git commit hash of "step N started" commit; anchor for retry-from-start resets

	substepFile      string    // absolute path to active substep doc, "" if none
	substepLetter    string    // letter of the active substep ("A", "B", …)
	substepStartHash string    // git commit hash of substep-N-started commit; anchor for substep retry-from-start

	humanPromptHash string    // HEAD preamble captured when HANDOFF was detected (before human prompt commit)
	pendingIsRetry  bool      // true when the current agent run was triggered by a Retry heading
	pendingAgentCmd *exec.Cmd // pre-built agent command waiting for human-prompt commit to finish
	pendingReplyTmp string    // temp path for pendingAgentCmd output
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

// condocRetryReadyMsg is sent when the retry git operations complete and the agent is ready to start.
type condocRetryReadyMsg struct {
	runCmd       *exec.Cmd
	replyTmpPath string
	errStr       string
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
var condocYAMLHeaderRe = regexp.MustCompile("(?s)<!--\\s*```condoc-yaml\\n(.*?)```\\s*-->")
var condocCompletedMarkerRe = regexp.MustCompile(`(?m)^## Condoc Completed$`)

// parseCondocYAML extracts startTime, branch, and callerPath from a condoc-yaml block.
// Returns ok=false if the block is absent or branch cannot be parsed.
func parseCondocYAML(content string) (startTime int64, branch, callerPath string, ok bool) {
	m := condocYAMLHeaderRe.FindStringSubmatch(content)
	if m == nil {
		return 0, "", "", false
	}
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "startTime:"):
			val, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "startTime:")), 10, 64)
			if err == nil {
				startTime = val
			}
		case strings.HasPrefix(line, "branch:"):
			branch = strings.TrimSpace(strings.TrimPrefix(line, "branch:"))
		case strings.HasPrefix(line, "callerPath:"):
			callerPath = strings.TrimSpace(strings.TrimPrefix(line, "callerPath:"))
		}
	}
	return startTime, branch, callerPath, branch != ""
}

// loadCondocSession reconstructs a CondocSession from an existing condoc file.
// Returns an error if the file is not a valid condoc or is already completed.
func loadCondocSession(filePath string, verbose bool, cwd string) (*CondocSession, error) {
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(cwd, filePath)
	}
	b, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("condoc: cannot read file: %w", err)
	}
	content := string(b)

	startTime, branch, callerPath, ok := parseCondocYAML(content)
	if !ok {
		return nil, fmt.Errorf("condoc: file exists but is not a condoc (no condoc-yaml header): %s", filePath)
	}
	if condocCompletedMarkerRe.MatchString(content) {
		return nil, fmt.Errorf("condoc: already completed: %s", filePath)
	}

	repoRoot, err := condocFindGitRoot(filePath)
	if err != nil {
		return nil, fmt.Errorf("condoc: %w", err)
	}

	cs := &CondocSession{
		mainFilePath: filePath,
		startTime:    startTime,
		branch:       branch,
		repoRoot:     repoRoot,
		callerPath:   callerPath,
		active:       true,
		verbose:      verbose,
	}

	stepMatches := condocStepHeadingRe.FindAllStringSubmatch(content, -1)
	if len(stepMatches) == 0 {
		// No step headings — still in the proposal phase.
		cs.phase = condocPhaseProposed
		return cs, nil
	}

	lastMatch := stepMatches[len(stepMatches)-1]
	lastStepNum, _ := strconv.Atoi(lastMatch[1])

	if strings.Contains(lastMatch[2], "<REPLACE-TITLE>") {
		// Template present but not yet filled in.
		cs.phase = condocPhaseAwaitingStep
		cs.stepNum = lastStepNum
		return cs, nil
	}

	// Step heading is filled — the step file must exist to resume.
	stepPath := condocStepFilePath(filePath, lastStepNum)
	if _, err := os.Stat(stepPath); err != nil {
		return nil, fmt.Errorf("condoc: step %d was started but its file is missing — condoc is in an inconsistent state: %s", lastStepNum, stepPath)
	}

	cs.phase = condocPhaseAwaitingAction
	cs.stepNum = lastStepNum
	cs.stepFile = stepPath

	// Try to recover the step-start commit hash so retry-from-start works correctly.
	hashCmd := exec.Command("git", "log", "--format=%H",
		"--grep=condoc: step "+strconv.Itoa(lastStepNum)+" started")
	hashCmd.Dir = repoRoot
	if hashOut, err := hashCmd.Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(hashOut)), "\n")
		if len(lines) > 0 && lines[0] != "" {
			cs.stepStartHash = lines[0]
		}
	}

	return cs, nil
}

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
		humanPrompt = "The AI has responded to the first prompt in this step.\n\n" +
			"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n" +
			"To REVISE the work here replace the '<REPLACE-Revision|Retry>' with 'Revision', to incorporate the AI's current work and add to it.\n\n" +
			"To RETRY from a previous point replace the '<REPLACE-Revision|Retry>' with 'Retry'. By default this retries from the\n" +
			"previous increment. To retry from further back add '(from start)' or '(from X)' where X is a revision letter,\n" +
			"for example '## Retry A (from start)'.\n\n" +
			"To add a SUBSTEP replace '<REPLACE-Revision|Retry>' with 'Substep A - Your Title' and add a ```prompt block beneath it.\n\n" +
			"Replace the '<REPLACE-PROMPT>' with the new prompt you wish the agent to follow.\n\n" +
			"When you are done add the '!HANDOFF!' directive to the end of the file followed by only whitespace.\n\n" +
			"Alternatively, add the '!COMPLETED!' directive to the end of the file to consider this step a success and conclude it."
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
		humanPrompt = "The AI has responded to the next increment in this step.\n\n" +
			"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n" +
			"To REVISE further, replace '<REPLACE-Revision|Retry>' with 'Revision' to incorporate the AI's current work and add to it.\n\n" +
			"To RETRY from a previous point replace the '<REPLACE-Revision|Retry>' with 'Retry'. By default this retries from the\n" +
			"previous increment. To retry from further back add '(from start)' or '(from X)' where X is a revision letter,\n" +
			"for example '## Retry C (from A)'.\n\n" +
			"To add a SUBSTEP replace '<REPLACE-Revision|Retry>' with 'Substep X - Your Title' (using the next letter) and add a ```prompt block.\n\n" +
			"Replace the '<REPLACE-PROMPT>' with the new prompt you wish the agent to follow.\n\n" +
			"When you are done add the '!HANDOFF!' directive to the end of the file followed by only whitespace.\n\n" +
			"Alternatively, add the '!COMPLETED!' directive to the end of the file to consider this step a success and conclude it."
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
			if t == "" || t == "<REPLACE-PROMPT>" || t == "```prompt" || t == "```" {
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
// at the end of the step file. commitPreamble is a markdown link to the HEAD commit inserted
// immediately before the heading (pass "" to omit). Called by the handler — agents must never
// write to step files.
func appendReplyToStepFile(stepFilePath, letter, replyText, commitPreamble string) error {
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
	prefix := ""
	if commitPreamble != "" {
		prefix = commitPreamble + "\n\n"
	}
	content := strings.TrimRight(cleaned, "\n") +
		"\n\n\n" + prefix + heading + "\n\n" + replyText + "\n"
	return os.WriteFile(stepFilePath, []byte(content), 0644)
}

// ===== REVISION DETECTION =====

var condocRevisionHeadingRe = regexp.MustCompile(`(?m)^## Revision ([A-Z])$`)
var condocReplyLetterRe = regexp.MustCompile(`(?m)^## Reply ([A-Z])$`)
var condocInitialReplyRe = regexp.MustCompile(`(?m)^## Reply$`)
var condocRetryHeadingRe = regexp.MustCompile(`(?m)^## Retry ([A-Z])(?:\s+\(from\s+(start|[A-Z])\))?$`)
var condocSubstepHeadingRe = regexp.MustCompile(`(?m)^## Substep ([A-Z]) - (.+)$`)
var condocSubstepCompletedRe = regexp.MustCompile(`(?m)^## Substep Completed$`)
var condocRevertRe = regexp.MustCompile(`(?m)^!REVERT-(\d+)(?:-([A-Z])(?:-([A-Z]))?)?!\s*$`)

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

// pendingRetryLetterAndFrom returns the letter and (from X) reference of a Retry heading that has
// no corresponding Reply heading, or ("", "") if no pending retry exists.
func pendingRetryLetterAndFrom(content string) (letter, fromRef string) {
	retries := condocRetryHeadingRe.FindAllStringSubmatch(content, -1)
	if len(retries) == 0 {
		return "", ""
	}
	last := retries[len(retries)-1]
	retryLetter := last[1]
	if len(last) > 2 {
		fromRef = last[2]
	}
	// Check if there is already a Reply with this letter (retry already processed).
	for _, m := range condocReplyLetterRe.FindAllStringSubmatch(content, -1) {
		if m[1] == retryLetter {
			return "", ""
		}
	}
	return retryLetter, fromRef
}

// retryText extracts the human guidance text under a "## Retry X" heading.
func retryText(content, letter string) string {
	re := regexp.MustCompile(`(?m)^## Retry ` + regexp.QuoteMeta(letter) + `(?:\s+\([^)]+\))?$`)
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

// retryFromStepsBack computes how many commits to revert for a retry.
// fromRef is "start", a letter "A"-"Z", or "" (default = 1 step back).
func retryFromStepsBack(content, fromRef string) int {
	if fromRef == "" {
		return 1
	}
	if fromRef == "start" {
		total := 0
		if condocInitialReplyRe.MatchString(content) {
			total++
		}
		total += len(condocReplyLetterRe.FindAllStringSubmatch(content, -1))
		return total
	}
	// fromRef is a letter: count Reply sections with a later letter (those come after fromRef).
	stepsBack := 0
	for _, m := range condocReplyLetterRe.FindAllStringSubmatch(content, -1) {
		if m[1] > fromRef {
			stepsBack++
		}
	}
	return stepsBack
}

// ===== SUBSTEP HELPERS =====

func condocSubstepFilePath(mainFilePath string, stepNum int, substepLetter string) string {
	return filepath.Join(condocImplsDirPath(mainFilePath),
		fmt.Sprintf("Step%dSubstep%sPrompt.md", stepNum, substepLetter))
}

// pendingSubstepLetterAndTitle returns the letter and title of the first unstarted substep
// found in the step file content (one whose substep file does not yet exist).
func pendingSubstepLetterAndTitle(content, mainFilePath string, stepNum int) (letter, title string) {
	for _, m := range condocSubstepHeadingRe.FindAllStringSubmatch(content, -1) {
		l, t := m[1], strings.TrimSpace(m[2])
		if strings.Contains(t, "<REPLACE") {
			continue
		}
		substepPath := condocSubstepFilePath(mainFilePath, stepNum, l)
		if _, err := os.Stat(substepPath); os.IsNotExist(err) {
			// Verify a prompt block follows this heading (before the next ## heading).
			headingRe := regexp.MustCompile(`(?m)^## Substep ` + regexp.QuoteMeta(l) + ` - .+$`)
			loc := headingRe.FindStringIndex(content)
			if loc == nil {
				continue
			}
			after := content[loc[1]:]
			nextH2 := regexp.MustCompile(`(?m)^## `).FindStringIndex(after)
			section := after
			if nextH2 != nil {
				section = after[:nextH2[0]]
			}
			if condocPromptBlockRe.MatchString(section) {
				return l, t
			}
		}
	}
	return "", ""
}

// extractSubstepPrompt extracts the text inside the ```prompt block of a substep heading section.
func extractSubstepPrompt(content, substepLetter string) string {
	headingRe := regexp.MustCompile(`(?m)^## Substep ` + regexp.QuoteMeta(substepLetter) + ` - .+$`)
	loc := headingRe.FindStringIndex(content)
	if loc == nil {
		return ""
	}
	after := content[loc[1]:]
	nextH2 := regexp.MustCompile(`(?m)^## `).FindStringIndex(after)
	section := after
	if nextH2 != nil {
		section = after[:nextH2[0]]
	}
	pm := condocPromptBlockRe.FindStringSubmatch(section)
	if pm == nil {
		return ""
	}
	return strings.TrimSpace(pm[1])
}

// writeSubstepFile creates the initial substep child doc with a backlink to the parent step file.
func writeSubstepFile(substepFilePath, stepFilePath, prompt string) error {
	if err := os.MkdirAll(filepath.Dir(substepFilePath), 0755); err != nil {
		return err
	}
	stepBaseName := condocBaseName(stepFilePath)
	stepFileName := filepath.Base(stepFilePath)
	backLink := fmt.Sprintf("[%s](%s)", stepBaseName, stepFileName)
	content := "# Prompt\n\n" + backLink + "\n\n" + prompt + "\n"
	return os.WriteFile(substepFilePath, []byte(content), 0644)
}

// replaceSubstepPromptWithLink replaces the ```prompt block under a substep heading with a
// markdown link to the substep file, and removes the Human-Prompt section.
func replaceSubstepPromptWithLink(content, mainFilePath string, stepNum int, substepLetter string) string {
	substepFileName := filepath.Base(condocSubstepFilePath(mainFilePath, stepNum, substepLetter))
	linkText := fmt.Sprintf("[Step %d Substep %s](%s)", stepNum, substepLetter, substepFileName)
	if strings.Contains(content, linkText) {
		return content
	}
	headingRe := regexp.MustCompile(`(?m)^## Substep ` + regexp.QuoteMeta(substepLetter) + ` - .+$`)
	loc := headingRe.FindStringIndex(content)
	if loc == nil {
		return content
	}
	headingEnd := loc[1]
	afterHeading := content[headingEnd:]
	nextH2 := regexp.MustCompile(`(?m)^## `).FindStringIndex(afterHeading)
	sectionEnd := len(content)
	sectionText := afterHeading
	if nextH2 != nil {
		sectionText = afterHeading[:nextH2[0]]
		sectionEnd = headingEnd + nextH2[0]
	}
	newSection := condocPromptBlockRe.ReplaceAllString(sectionText, "")
	newSection = strings.TrimRight(newSection, "\n") + "\n\n" + linkText + "\n\n"
	return content[:headingEnd] + newSection + content[sectionEnd:]
}

// markSubstepInStepFile replaces the substep prompt block with a link and updates Human-Prompt.
func markSubstepInStepFile(stepFilePath, mainFilePath string, stepNum int, substepLetter string) error {
	b, err := os.ReadFile(stepFilePath)
	if err != nil {
		return err
	}
	content := replaceSubstepPromptWithLink(string(b), mainFilePath, stepNum, substepLetter)
	content = removeHumanPromptSection(content)
	substepFileName := filepath.Base(condocSubstepFilePath(mainFilePath, stepNum, substepLetter))
	humanPrompt := fmt.Sprintf("Substep %s is now active. Interact with the substep file (%s).",
		substepLetter, substepFileName)
	content = addHumanPrompt(content, humanPrompt)
	return os.WriteFile(stepFilePath, []byte(content), 0644)
}

// finalizeSubstepFile marks the substep as completed (mirrors finalizeStepFile).
func finalizeSubstepFile(substepFilePath string) error {
	b, err := os.ReadFile(substepFilePath)
	if err != nil {
		return err
	}
	content := removeHumanPromptSection(string(b))
	content = removeUnfilledRevisionTemplates(content)
	now := time.Now()
	content = strings.TrimRight(content, "\n") +
		fmt.Sprintf("\n\n\n## Substep Completed\n\nThis substep was completed at %d (%s).\n",
			now.Unix(), now.UTC().Format("Mon Jan 2 03:04:05 PM MST 2006"))
	return os.WriteFile(substepFilePath, []byte(content), 0644)
}

// addSubstepRevisionTemplate appends a revision template to the substep file; the Human-Prompt
// mentions that !COMPLETED! returns to the parent step.
func addSubstepRevisionTemplate(substepFilePath string, verbose bool) error {
	b, err := os.ReadFile(substepFilePath)
	if err != nil {
		return err
	}
	var humanPrompt string
	if verbose {
		humanPrompt = "The AI has responded to the substep prompt.\n\n" +
			"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n" +
			"To REVISE replace '<REPLACE-Revision|Retry>' with 'Revision' to add to the AI's work.\n\n" +
			"To RETRY replace '<REPLACE-Revision|Retry>' with 'Retry' to restart.\n\n" +
			"When done add the '!HANDOFF!' directive.\n\n" +
			"Alternatively, add the '!COMPLETED!' directive to complete this substep and return to the parent step."
	} else {
		humanPrompt = "When done add '!HANDOFF!' or '!COMPLETED!' to return to the parent step."
	}
	content := strings.TrimRight(string(b), "\n") + "\n\n\n" +
		"## <REPLACE-Revision|Retry> A\n\n" +
		"<REPLACE-PROMPT>\n\n\n" +
		"## Human-Prompt\n\n" +
		humanPrompt + "\n"
	return os.WriteFile(substepFilePath, []byte(content), 0644)
}

// updateStepFileAfterSubstepComplete restores the revision template and Human-Prompt in the step
// file after a substep completes, using the next available revision letter.
func updateStepFileAfterSubstepComplete(stepFilePath string, verbose bool) error {
	b, err := os.ReadFile(stepFilePath)
	if err != nil {
		return err
	}
	content := removeHumanPromptSection(string(b))
	letter := lastReplyLetter(content)
	var nextLetter string
	if letter == "" {
		nextLetter = "A"
	} else {
		nextLetter = string(rune(letter[0] + 1))
	}
	var humanPrompt string
	if verbose {
		humanPrompt = "The substep has been completed.\n\n" +
			"You may now Revise, Retry, add another Substep, or Complete this step.\n\n" +
			"Replace '<REPLACE-Revision|Retry>' with 'Revision', 'Retry', or 'Substep X - Title'.\n\n" +
			"Add the '!HANDOFF!' directive when ready, or '!COMPLETED!' to complete this step."
	} else {
		humanPrompt = "Add the '!HANDOFF!' or '!COMPLETED!' directive."
	}
	content = strings.TrimRight(content, "\n") + "\n\n\n" +
		"## <REPLACE-Revision|Retry> " + nextLetter + "\n\n" +
		"<REPLACE-PROMPT>\n\n\n" +
		"## Human-Prompt\n\n" +
		humanPrompt + "\n"
	return os.WriteFile(stepFilePath, []byte(content), 0644)
}

// condocBranchIdentifier extracts the identifier segment from a condoc branch name.
// e.g. "condoc/Simple-1779734463/main" → "Simple-1779734463"
func condocBranchIdentifier(branch string) string {
	parts := strings.Split(branch, "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return branch
}

// condocGitHubCommitURL attempts to build a GitHub commit URL from the repo remote and a full hash.
// Returns "" if the remote is not a GitHub URL or cannot be determined.
func condocGitHubCommitURL(repoRoot, fullHash string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	remote := strings.TrimSpace(string(out))
	remote = strings.TrimSuffix(remote, ".git")
	if strings.HasPrefix(remote, "git@github.com:") {
		remote = "https://github.com/" + strings.TrimPrefix(remote, "git@github.com:")
	}
	if strings.Contains(remote, "github.com") {
		return remote + "/commit/" + fullHash
	}
	return ""
}

// condocCommitPreamble returns a markdown link to the current HEAD commit for insertion before
// a ## Reply heading. Returns "" if the commit hash cannot be determined.
func condocCommitPreamble(repoRoot string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	fullHash := strings.TrimSpace(string(out))
	if len(fullHash) < 7 {
		return ""
	}
	shortHash := fullHash[:7]
	url := condocGitHubCommitURL(repoRoot, fullHash)
	if url != "" {
		return fmt.Sprintf("[`%s`](%s)", shortHash, url)
	}
	return fmt.Sprintf("`%s`", shortHash)
}

// condocDualPreamble builds a combined preamble that records both the commit hash at the time
// of human input (promptHash) and at the time the agent responded (replyParentHash).
// Either may be "" if unavailable, in which case only the available hash is included.
func condocDualPreamble(promptHash, replyParentHash string) string {
	if promptHash == "" && replyParentHash == "" {
		return ""
	}
	if promptHash == "" {
		return replyParentHash
	}
	if replyParentHash == "" || promptHash == replyParentHash {
		return promptHash
	}
	return "prompt: " + promptHash + " → reply: " + replyParentHash
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

func buildCondocRetryPrompt(stepFilePath, retryLetter, retryGuidance string) string {
	base := fmt.Sprintf(
		"You are executing a condoc step retry. The step file is at '%s'. "+
			"Read the step file — it contains the full history of previous attempts on this step. "+
			"The project files have been reset to their state before those attempts, "+
			"so you are starting fresh. Execute the task described in the '# Prompt' section.",
		stepFilePath)
	if retryGuidance != "" {
		base += fmt.Sprintf(
			" The human has provided this additional guidance for Retry %s: %q.",
			retryLetter, retryGuidance)
	}
	return base + " " +
		"IMPORTANT: Do NOT write to or modify the step file or any other condoc files — " +
		"the condoc handler will automatically capture your terminal response as the reply. " +
		"When you have finished, provide a brief summary (2-3 sentences) of what you accomplished " +
		"as your terminal response."
}

func buildCondocSubstepPrompt(stepFilePath, substepFilePath string) string {
	return fmt.Sprintf(
		"You are executing a condoc substep. The parent step file is at '%s'. "+
			"The substep file is at '%s'. "+
			"Read the substep file. Execute the task described in the '# Prompt' section. "+
			"You may also read the parent step file for additional context. "+
			"IMPORTANT: Do NOT write to or modify the step file, substep file, or any other condoc files — "+
			"the condoc handler will automatically capture your terminal response as the reply. "+
			"When you have finished, provide a brief summary (2-3 sentences) of what you accomplished "+
			"as your terminal response.",
		stepFilePath, substepFilePath)
}

func buildCondocSubstepRevisionPrompt(substepFilePath, revLetter string) string {
	return fmt.Sprintf(
		"You are executing a condoc substep revision. The substep file is at '%s'. "+
			"Read the substep file. You previously completed the substep task (see '## Reply') and the human has "+
			"requested Revision %s (see '## Revision %s'). "+
			"Execute the revision as described. "+
			"IMPORTANT: Do NOT write to or modify the substep file or any other condoc files — "+
			"the condoc handler will automatically capture your terminal response as the reply. "+
			"When you have finished, provide a brief summary (2-3 sentences) of what you changed "+
			"as your terminal response.",
		substepFilePath, revLetter, revLetter)
}

func buildCondocSubstepRetryPrompt(substepFilePath, retryLetter, retryGuidance string) string {
	base := fmt.Sprintf(
		"You are executing a condoc substep retry. The substep file is at '%s'. "+
			"Read the substep file — it contains the full history of previous attempts on this substep. "+
			"The project files have been reset to their state before those attempts, "+
			"so you are starting fresh. Execute the task described in the '# Prompt' section.",
		substepFilePath)
	if retryGuidance != "" {
		base += fmt.Sprintf(
			" The human has provided this additional guidance for Retry %s: %q.",
			retryLetter, retryGuidance)
	}
	return base + " " +
		"IMPORTANT: Do NOT write to or modify the substep file or any other condoc files — " +
		"the condoc handler will automatically capture your terminal response as the reply. " +
		"When you have finished, provide a brief summary (2-3 sentences) of what you accomplished " +
		"as your terminal response."
}

// runCondocRetryGitSequence pushes the current branch state to a take branch, resets HEAD
// back to resetHash (for retry-from-start) or stepsBack commits (for other retries), restores
// the step file with its full pre-reset history, writes the diff file, commits both, then
// pushes. On success it returns a condocRetryReadyMsg carrying the pre-built agent command.
//
// resetHash should be the "step N started" commit hash when fromRef=="start"; passing a hash
// avoids the stepsBack overcounting that occurs after a prior retry-from-start has already
// accumulated reply sections in the step file without a matching git commit for each.
//
// Restoring the step file after the reset is the key invariant of a retry: project files go
// back to the earlier state, but the step document keeps the full visible history of every
// previous attempt (including the Retry heading the human just wrote).
func runCondocRetryGitSequence(repoRoot, mainBranch, takeBranch, diffFilePath, stepFilePath string, stepsBack, takeN int, runCmd *exec.Cmd, replyTmpPath, retryLetter, resetHash string, stepNum int, substepLetter string) tea.Cmd {
	return func() tea.Msg {
		// 1. Capture the diff of the commits we are about to abandon.
		var logCmd *exec.Cmd
		if resetHash != "" {
			logCmd = exec.Command("git", "log", "-p", resetHash+"..HEAD")
		} else {
			logCmd = exec.Command("git", "log", "-p", fmt.Sprintf("-n%d", stepsBack))
		}
		logCmd.Dir = repoRoot
		diffOut, err := logCmd.Output()
		if err != nil {
			return condocRetryReadyMsg{errStr: "git log: " + err.Error()}
		}

		// 2. Save the step file content before the reset so we can restore it.
		// Strip the Human-Prompt section; the retry agent does not need it.
		stepRaw, err := os.ReadFile(stepFilePath)
		if err != nil {
			return condocRetryReadyMsg{errStr: "read step file: " + err.Error()}
		}
		stepContent := removeHumanPromptSection(string(stepRaw))

		// 3. Push current HEAD to the take branch.
		pushTakeCmd := exec.Command("git", "push", "origin",
			fmt.Sprintf("HEAD:refs/heads/%s", takeBranch))
		pushTakeCmd.Dir = repoRoot
		if out, err := pushTakeCmd.CombinedOutput(); err != nil {
			return condocRetryReadyMsg{errStr: fmt.Sprintf("push take branch: %v\n%s", err, string(out))}
		}

		// 4. Reset main branch HEAD to the target. For retry-from-start, resetHash is the exact
		// "step N started" commit, which is correct regardless of how many prior retries have
		// accumulated reply sections in the step file. For other retries, fall back to HEAD~stepsBack.
		var resetTarget string
		if resetHash != "" {
			resetTarget = resetHash
		} else {
			resetTarget = fmt.Sprintf("HEAD~%d", stepsBack)
		}
		resetCmd := exec.Command("git", "reset", "--hard", resetTarget)
		resetCmd.Dir = repoRoot
		if out, err := resetCmd.CombinedOutput(); err != nil {
			return condocRetryReadyMsg{errStr: fmt.Sprintf("git reset: %v\n%s", err, string(out))}
		}

		// 5. Restore the step file with its full pre-reset history. The reset reverted
		// the working tree, so without this the agent would see an older file that is
		// missing all previous attempts and the Retry heading.
		if err := os.MkdirAll(filepath.Dir(stepFilePath), 0755); err != nil {
			return condocRetryReadyMsg{errStr: "create step dir: " + err.Error()}
		}
		if err := os.WriteFile(stepFilePath, []byte(stepContent), 0644); err != nil {
			return condocRetryReadyMsg{errStr: "restore step file: " + err.Error()}
		}

		// 6. Write the diff file alongside the step file. MkdirAll is a safety net
		// in case the directory was somehow not part of the reset target commit.
		if err := os.MkdirAll(filepath.Dir(diffFilePath), 0755); err != nil {
			return condocRetryReadyMsg{errStr: "create diff dir: " + err.Error()}
		}
		if err := os.WriteFile(diffFilePath, diffOut, 0644); err != nil {
			return condocRetryReadyMsg{errStr: "write diff file: " + err.Error()}
		}

		// 7. Commit the diff file and restored step file so both survive on the reset branch.
		addCmd := exec.Command("git", "add", diffFilePath, stepFilePath)
		addCmd.Dir = repoRoot
		if out, err := addCmd.CombinedOutput(); err != nil {
			return condocRetryReadyMsg{errStr: fmt.Sprintf("git add: %v\n%s", err, string(out))}
		}
		var commitMsg string
		if substepLetter != "" {
			commitMsg = fmt.Sprintf("condoc: step %d substep %s retry %s prompt (take%d snapshot)", stepNum, substepLetter, retryLetter, takeN)
		} else {
			commitMsg = fmt.Sprintf("condoc: step %d retry %s prompt (take%d snapshot)", stepNum, retryLetter, takeN)
		}
		commitCmd := exec.Command("git", "commit", "-m", commitMsg)
		commitCmd.Dir = repoRoot
		if out, err := commitCmd.CombinedOutput(); err != nil {
			return condocRetryReadyMsg{errStr: fmt.Sprintf("git commit: %v\n%s", err, string(out))}
		}

		// 8. Force-push the reset + snapshot commit to origin. History was rewritten by the
		// reset so a regular push would be rejected; the take branch already preserves
		// the old state.
		pushMainCmd := exec.Command("git", "push", "--force", "origin", mainBranch)
		pushMainCmd.Dir = repoRoot
		if out, err := pushMainCmd.CombinedOutput(); err != nil {
			return condocRetryReadyMsg{errStr: fmt.Sprintf("push main branch: %v\n%s", err, string(out))}
		}

		return condocRetryReadyMsg{runCmd: runCmd, replyTmpPath: replyTmpPath}
	}
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
	if cs.substepFile != "" && cs.phase == condocPhaseAwaitingAction {
		watchLine = condocStatusStyle.Render("watching: " + filepath.Base(cs.substepFile))
	} else if cs.stepFile != "" && cs.phase == condocPhaseAwaitingAction {
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
		return loadCondocSession(filePath, verbose, cwd)
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
		if cs.substepFile != "" {
			// Watching substep file.
			if b, err := os.ReadFile(cs.substepFile); err == nil {
				if m2 := condocRevertRe.FindStringSubmatch(string(b)); m2 != nil {
					return m.condocRevert(cs.substepFile, m2)
				}
			}
			if condocFileHasCompleted(cs.substepFile) {
				return m.condocCompleteSubstep()
			}
			if condocFileHasHandoff(cs.substepFile) {
				b, err := os.ReadFile(cs.substepFile)
				if err != nil {
					return m.condocError("read substep file: " + err.Error())
				}
				if letter, _ := pendingRetryLetterAndFrom(string(b)); letter != "" {
					return m.condocRunRetry()
				}
				return m.condocRunRevision()
			}
		} else if cs.stepFile != "" {
			// Watching step file.
			if b, err := os.ReadFile(cs.stepFile); err == nil {
				if m2 := condocRevertRe.FindStringSubmatch(string(b)); m2 != nil {
					return m.condocRevert(cs.stepFile, m2)
				}
			}
			if condocFileHasCompleted(cs.stepFile) {
				return m.condocCompleteStep()
			}
			if condocFileHasHandoff(cs.stepFile) {
				b, err := os.ReadFile(cs.stepFile)
				if err != nil {
					return m.condocError("read step file: " + err.Error())
				}
				content := string(b)
				if letter, _ := pendingRetryLetterAndFrom(content); letter != "" {
					return m.condocRunRetry()
				}
				if substepLetter, substepTitle := pendingSubstepLetterAndTitle(content, cs.mainFilePath, cs.stepNum); substepLetter != "" {
					return m.condocStartSubstep(substepLetter, substepTitle)
				}
				return m.condocRunRevision()
			}
		}
	}
	// Also check for revert on main/step files in proposal/awaiting-step phases.
	switch cs.phase {
	case condocPhaseProposed, condocPhaseAwaitingStep:
		if b, err := os.ReadFile(cs.mainFilePath); err == nil {
			if m2 := condocRevertRe.FindStringSubmatch(string(b)); m2 != nil {
				return m.condocRevert(cs.mainFilePath, m2)
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

	case condocPhaseStepStarting:
		// Committed — capture hash for retry-from-start, then launch the appropriate agent.
		revCmd := exec.Command("git", "rev-parse", "HEAD")
		revCmd.Dir = cs.repoRoot
		if hashOut, err := revCmd.Output(); err == nil {
			hash := strings.TrimSpace(string(hashOut))
			if cs.substepFile != "" {
				cs.substepStartHash = hash
			} else {
				cs.stepStartHash = hash
			}
		}
		if cs.substepFile != "" {
			return m.condocStartSubstepAgent()
		}
		return m.condocStartStepAgent()

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

	case condocPhaseHumanCommitting:
		// Human prompt commit landed — now launch the pre-built agent command.
		runCmd := cs.pendingAgentCmd
		cs.replyTmpPath = cs.pendingReplyTmp
		cs.pendingAgentCmd = nil
		cs.pendingReplyTmp = ""
		if runCmd == nil {
			return m.condocError("human commit done but no pending agent command")
		}
		cs.phase = condocPhaseRunningAgent
		cs.statusMsg = "running agent…"
		m.condocDynapane.Deactivate()
		m.blinker.SetState(BlinkerInactive)
		return m, tea.Sequence(
			tea.Println(sessionStyle.Render("condoc: prompt committed, running agent…")),
			deferCondocExec(runCmd, func(err error) tea.Msg {
				return condocAgentStepDoneMsg{exitCode: extractExitCode(err), execErr: err}
			}),
		)
	}
	return m, condocTickCmd()
}

// condocStartStep handles !HANDOFF! on the main file when a step is ready to run.
// It creates the step file, updates the main file, then commits before starting the agent.
// Committing first ensures a clean git state that retry can reset to.
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
	cs.pendingIsRetry = false
	cs.humanPromptHash = condocCommitPreamble(cs.repoRoot) // HEAD before the step-started commit
	cs.phase = condocPhaseStepStarting
	cs.statusMsg = "committing step " + strconv.Itoa(step.num) + " start…"

	gitCmds := [][]string{
		{"add", "."},
		{"commit", "-m", fmt.Sprintf("condoc: step %d started", step.num)},
		{"push", "origin", cs.branch},
	}
	return m, tea.Batch(
		tea.Println(sessionStyle.Render("condoc: step "+strconv.Itoa(step.num)+" started, committing…")),
		m.condocDynapane.Activate(cs),
		runGitSequence(gitCmds, cs.repoRoot),
	)
}

// condocStartStepAgent launches the agent after the step-started commit completes.
func (m appModel) condocStartStepAgent() (appModel, tea.Cmd) {
	cs := m.condoc
	cs.phase = condocPhaseRunningAgent
	cs.statusMsg = "running agent for step " + strconv.Itoa(cs.stepNum) + "…"

	prompt := buildCondocStepPrompt(cs.stepFile)
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
		tea.Println(sessionStyle.Render("condoc: running agent for "+filepath.Base(cs.stepFile)+"…")),
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
	isRetry := cs.pendingIsRetry

	// Build dual preamble: parent hash at human input time + parent hash at reply time.
	humanHash := cs.humanPromptHash
	replyParentHash := condocCommitPreamble(cs.repoRoot) // HEAD before the reply commit
	commitPreamble := condocDualPreamble(humanHash, replyParentHash)

	// Reset pending flags.
	cs.humanPromptHash = ""
	cs.pendingIsRetry = false

	// Write to substep file if a substep is active; otherwise write to the step file.
	activeFile := cs.stepFile
	if cs.substepFile != "" {
		activeFile = cs.substepFile
	}
	if err := appendReplyToStepFile(activeFile, revLetter, replyText, commitPreamble); err != nil {
		return m.condocError("append reply to file: " + err.Error())
	}

	var templateErr error
	if cs.substepFile != "" {
		if revLetter != "" {
			templateErr = addNextRevisionTemplate(cs.substepFile, revLetter, cs.verbose)
		} else {
			templateErr = addSubstepRevisionTemplate(cs.substepFile, cs.verbose)
		}
	} else {
		if revLetter != "" {
			templateErr = addNextRevisionTemplate(cs.stepFile, revLetter, cs.verbose)
		} else {
			templateErr = addRevisionTemplate(cs.stepFile, cs.verbose)
		}
	}
	if templateErr != nil {
		return m.condocError("add revision template: " + templateErr.Error())
	}

	cs.phase = condocPhaseCommitting
	cs.commitTarget = condocPhaseAwaitingAction
	cs.statusMsg = "committing agent reply…"
	m.blinker.SetState(BlinkerCondoc)

	// Build a descriptive commit message indicating step/substep, iteration type and letter.
	var commitMsg string
	if cs.substepFile != "" {
		switch {
		case isRetry:
			commitMsg = fmt.Sprintf("condoc: step %d substep %s retry %s reply", cs.stepNum, cs.substepLetter, revLetter)
		case revLetter != "":
			commitMsg = fmt.Sprintf("condoc: step %d substep %s revision %s reply", cs.stepNum, cs.substepLetter, revLetter)
		default:
			commitMsg = fmt.Sprintf("condoc: step %d substep %s initial reply", cs.stepNum, cs.substepLetter)
		}
	} else {
		switch {
		case isRetry:
			commitMsg = fmt.Sprintf("condoc: step %d retry %s reply", cs.stepNum, revLetter)
		case revLetter != "":
			commitMsg = fmt.Sprintf("condoc: step %d revision %s reply", cs.stepNum, revLetter)
		default:
			commitMsg = fmt.Sprintf("condoc: step %d initial reply", cs.stepNum)
		}
	}
	gitCmds := [][]string{
		{"add", "."},
		{"commit", "-m", commitMsg},
		{"push", "origin", cs.branch},
	}
	return m, tea.Batch(
		statusPrint,
		m.condocDynapane.Activate(cs),
		m.blinker.ResetTick(),
		runGitSequence(gitCmds, cs.repoRoot),
	)
}

// condocRunRevision handles !HANDOFF! on the active file (step or substep) for a revision.
// It strips the !HANDOFF! directive and commits the human prompt before launching the agent.
func (m appModel) condocRunRevision() (appModel, tea.Cmd) {
	cs := m.condoc
	activeFile := cs.stepFile
	if cs.substepFile != "" {
		activeFile = cs.substepFile
	}
	b, err := os.ReadFile(activeFile)
	if err != nil {
		return m.condocError("read active file: " + err.Error())
	}
	content := string(b)
	revLetter := pendingRevisionLetter(content)
	if revLetter == "" {
		return m, condocTickCmd()
	}

	revText := revisionText(content, revLetter)

	// Strip HANDOFF directive and Human-Prompt section before committing the human prompt.
	stripped := removeHumanPromptSection(content)
	if err := os.WriteFile(activeFile, []byte(stripped), 0644); err != nil {
		return m.condocError("strip handoff from active file: " + err.Error())
	}

	// Capture parent hash at time of human input (before the prompt commit).
	cs.humanPromptHash = condocCommitPreamble(cs.repoRoot)
	cs.pendingRevLetter = revLetter
	cs.pendingIsRetry = false

	// Build agent command now so we can store it; it will be launched after the commit lands.
	var agentPrompt string
	if cs.substepFile != "" {
		agentPrompt = buildCondocSubstepRevisionPrompt(cs.substepFile, revLetter)
	} else {
		agentPrompt = buildCondocRevisionPrompt(cs.stepFile, revLetter)
	}
	agentCmd, errMsg := buildAgentPromptCmd(ModeWrite, agentPrompt, m.currentAgent, m.currentModel, m.sessionDir)
	if agentCmd == nil {
		return m.condocError("build agent cmd: " + errMsg)
	}
	agentCmd.Dir = cs.repoRoot
	agentCmd.Env = append(agentCmd.Env, "AA_QUIET=true")

	replyTmpPath := filepath.Join(os.TempDir(), fmt.Sprintf("condoc-reply-%d.txt", time.Now().UnixNano()))
	cs.pendingAgentCmd = teeCommand(agentCmd, replyTmpPath)
	cs.pendingReplyTmp = replyTmpPath

	cs.phase = condocPhaseHumanCommitting
	cs.statusMsg = "committing revision " + revLetter + " prompt…"

	var commitMsg string
	if cs.substepFile != "" {
		commitMsg = fmt.Sprintf("condoc: step %d substep %s revision %s prompt", cs.stepNum, cs.substepLetter, revLetter)
	} else {
		commitMsg = fmt.Sprintf("condoc: step %d revision %s prompt", cs.stepNum, revLetter)
	}
	gitCmds := [][]string{
		{"add", "."},
		{"commit", "-m", commitMsg},
		{"push", "origin", cs.branch},
	}

	m.condocDynapane.Activate(cs)
	m.blinker.SetState(BlinkerCondoc)

	return m, tea.Batch(
		tea.Println(sessionStyle.Render("condoc: revision "+revLetter+" — "+revText)),
		m.condocDynapane.Activate(cs),
		m.blinker.ResetTick(),
		runGitSequence(gitCmds, cs.repoRoot),
	)
}

// condocRunRetry handles !HANDOFF! on the active file (step or substep) when a Retry is requested.
// Retry is only valid after at least one reply exists (not for the entry iteration).
func (m appModel) condocRunRetry() (appModel, tea.Cmd) {
	cs := m.condoc
	activeFile := cs.stepFile
	if cs.substepFile != "" {
		activeFile = cs.substepFile
	}
	b, err := os.ReadFile(activeFile)
	if err != nil {
		return m.condocError("read active file: " + err.Error())
	}
	content := string(b)

	retryLetter, fromRef := pendingRetryLetterAndFrom(content)
	if retryLetter == "" {
		return m, condocTickCmd()
	}

	// Retry is only valid once at least one reply exists; the entry iteration cannot be retried.
	hasReply := condocInitialReplyRe.MatchString(content) || len(condocReplyLetterRe.FindAllString(content, -1)) > 0
	if !hasReply {
		return m.condocError("retry: no reply exists yet — retry is only valid after the entry iteration")
	}

	retryGuidance := retryText(content, retryLetter)
	stepsBack := retryFromStepsBack(content, fromRef)
	if stepsBack == 0 {
		return m.condocError("retry: computed 0 steps back — nothing to revert")
	}

	var resetHash string
	if fromRef == "start" {
		if cs.substepFile != "" {
			resetHash = cs.substepStartHash
		} else {
			resetHash = cs.stepStartHash
		}
	}

	// Capture parent hash at time of human input (before history is rewritten).
	cs.humanPromptHash = condocCommitPreamble(cs.repoRoot)
	cs.pendingIsRetry = true

	cs.takeCounter++
	takeN := cs.takeCounter
	identifier := condocBranchIdentifier(cs.branch)
	takeBranch := fmt.Sprintf("condoc/%s/take%d", identifier, takeN)

	var diffFileName string
	if cs.substepFile != "" {
		diffFileName = fmt.Sprintf("take%d%sstep%dsubstep%sdiff.txt", takeN, identifier, cs.stepNum, cs.substepLetter)
	} else {
		diffFileName = fmt.Sprintf("take%d%sdiff.txt", takeN, identifier)
	}
	diffFilePath := filepath.Join(filepath.Dir(activeFile), diffFileName)

	cs.pendingRevLetter = retryLetter
	cs.phase = condocPhaseRunningAgent
	cs.statusMsg = fmt.Sprintf("retry %s: saving take%d…", retryLetter, takeN)

	var agentPrompt string
	if cs.substepFile != "" {
		agentPrompt = buildCondocSubstepRetryPrompt(cs.substepFile, retryLetter, retryGuidance)
	} else {
		agentPrompt = buildCondocRetryPrompt(cs.stepFile, retryLetter, retryGuidance)
	}
	agentCmd, errMsg := buildAgentPromptCmd(ModeWrite, agentPrompt, m.currentAgent, m.currentModel, m.sessionDir)
	if agentCmd == nil {
		return m.condocError("build agent cmd: " + errMsg)
	}
	agentCmd.Dir = cs.repoRoot
	agentCmd.Env = append(agentCmd.Env, "AA_QUIET=true")

	replyTmpPath := filepath.Join(os.TempDir(), fmt.Sprintf("condoc-reply-%d.txt", time.Now().UnixNano()))
	cs.replyTmpPath = replyTmpPath
	runCmd := teeCommand(agentCmd, replyTmpPath)

	fromDisplay := fromRef
	if fromDisplay == "" {
		fromDisplay = "previous"
	}

	m.condocDynapane.Deactivate()
	m.blinker.SetState(BlinkerInactive)

	return m, tea.Sequence(
		tea.Println(sessionStyle.Render(fmt.Sprintf("condoc: retry %s (from %s) — saving take%d…", retryLetter, fromDisplay, takeN))),
		runCondocRetryGitSequence(cs.repoRoot, cs.branch, takeBranch, diffFilePath, activeFile, stepsBack, takeN, runCmd, replyTmpPath, retryLetter, resetHash, cs.stepNum, cs.substepLetter),
	)
}

// handleCondocRetryReady processes the result of the retry git sequence and starts the agent.
func (m appModel) handleCondocRetryReady(msg condocRetryReadyMsg) (appModel, tea.Cmd) {
	if msg.errStr != "" {
		return m.condocError("retry: " + msg.errStr)
	}
	cs := m.condoc
	if cs == nil {
		return m, nil
	}
	cs.replyTmpPath = msg.replyTmpPath
	return m, tea.Sequence(
		tea.Println(sessionStyle.Render("condoc: take saved, running agent for retry…")),
		deferCondocExec(msg.runCmd, func(err error) tea.Msg {
			return condocAgentStepDoneMsg{exitCode: extractExitCode(err), execErr: err}
		}),
	)
}

// condocStartSubstep handles detection of an unstarted substep heading in the step file.
func (m appModel) condocStartSubstep(letter, title string) (appModel, tea.Cmd) {
	cs := m.condoc
	b, err := os.ReadFile(cs.stepFile)
	if err != nil {
		return m.condocError("read step file: " + err.Error())
	}
	prompt := extractSubstepPrompt(string(b), letter)
	if prompt == "" {
		return m.condocError(fmt.Sprintf("substep %s: could not extract prompt block", letter))
	}

	substepPath := condocSubstepFilePath(cs.mainFilePath, cs.stepNum, letter)
	if err := writeSubstepFile(substepPath, cs.stepFile, prompt); err != nil {
		return m.condocError("write substep file: " + err.Error())
	}
	if err := markSubstepInStepFile(cs.stepFile, cs.mainFilePath, cs.stepNum, letter); err != nil {
		return m.condocError("mark substep in step file: " + err.Error())
	}

	cs.substepFile = substepPath
	cs.substepLetter = letter
	cs.pendingRevLetter = ""
	cs.pendingIsRetry = false
	cs.humanPromptHash = condocCommitPreamble(cs.repoRoot) // HEAD before the substep-started commit
	cs.phase = condocPhaseStepStarting
	cs.statusMsg = fmt.Sprintf("starting substep %s…", letter)

	gitCmds := [][]string{
		{"add", "."},
		{"commit", "-m", fmt.Sprintf("condoc: step %d substep %s started", cs.stepNum, letter)},
		{"push", "origin", cs.branch},
	}
	return m, tea.Batch(
		tea.Println(sessionStyle.Render(fmt.Sprintf("condoc: substep %s started, committing…", letter))),
		m.condocDynapane.Activate(cs),
		runGitSequence(gitCmds, cs.repoRoot),
	)
}

// condocStartSubstepAgent launches the agent for a substep after the substep-started commit.
func (m appModel) condocStartSubstepAgent() (appModel, tea.Cmd) {
	cs := m.condoc
	cs.phase = condocPhaseRunningAgent
	cs.statusMsg = fmt.Sprintf("running agent for substep %s…", cs.substepLetter)

	prompt := buildCondocSubstepPrompt(cs.stepFile, cs.substepFile)
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
		tea.Println(sessionStyle.Render(fmt.Sprintf("condoc: running agent for substep %s…", cs.substepLetter))),
		deferCondocExec(runCmd, func(err error) tea.Msg {
			return condocAgentStepDoneMsg{exitCode: extractExitCode(err), execErr: err}
		}),
	)
}

// condocCompleteSubstep handles !COMPLETED! on the substep file.
func (m appModel) condocCompleteSubstep() (appModel, tea.Cmd) {
	cs := m.condoc
	if err := finalizeSubstepFile(cs.substepFile); err != nil {
		return m.condocError("finalize substep file: " + err.Error())
	}
	if err := updateStepFileAfterSubstepComplete(cs.stepFile, cs.verbose); err != nil {
		return m.condocError("update step file after substep: " + err.Error())
	}

	substepLetter := cs.substepLetter
	cs.substepFile = ""
	cs.substepLetter = ""
	cs.substepStartHash = ""
	cs.pendingRevLetter = ""

	cs.phase = condocPhaseCommitting
	cs.commitTarget = condocPhaseAwaitingAction
	cs.statusMsg = fmt.Sprintf("substep %s completed, returning to step…", substepLetter)

	gitCmds := [][]string{
		{"add", "."},
		{"commit", "-m", fmt.Sprintf("condoc: step %d substep %s completed", cs.stepNum, substepLetter)},
		{"push", "origin", cs.branch},
	}
	return m, tea.Batch(
		tea.Println(successStyle.Render(fmt.Sprintf("condoc: substep %s completed", substepLetter))),
		m.condocDynapane.Activate(cs),
		runGitSequence(gitCmds, cs.repoRoot),
	)
}

// trimContentBeforeIterationReply removes everything from the "## Reply letter" heading
// (including any preceding preamble link lines) onwards, but preserves the user-created
// Revision/Retry heading and text for that letter so the human can modify and resubmit.
// If no Reply heading for that letter exists the content is returned unchanged.
func trimContentBeforeIterationReply(content, letter string) string {
	re := regexp.MustCompile(`(?m)^## Reply ` + regexp.QuoteMeta(letter) + `\s*$`)
	loc := re.FindStringIndex(content)
	if loc == nil {
		return content
	}
	// Cut at the reply heading; also strip any preamble link lines immediately before it.
	before := strings.TrimRight(content[:loc[0]], "\n")
	lines := strings.Split(before, "\n")
	for len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		// Remove trailing blank lines and commit-hash preamble lines.
		if last == "" || strings.HasPrefix(last, "prompt: ") || strings.HasPrefix(last, "[`") || strings.HasPrefix(last, "`") {
			lines = lines[:len(lines)-1]
		} else {
			break
		}
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// condocRevert handles a !REVERT-N[-X[-Y]]! directive found in the given file.
// match is the result of condocRevertRe.FindStringSubmatch.
//
// Three forms:
//   !REVERT-N!      → reset git to "step N started"; strip all iterations from step file.
//   !REVERT-N-X!    → reset git to "step N started"; keep step file content up to before
//                     iteration X (first Revision/Retry/Substep heading with letter X).
//   !REVERT-N-X-Y!  → reset git to "step N substep X started"; keep substep file content
//                     up to before iteration Y; step file restored by git reset.
func (m appModel) condocRevert(sourceFile string, match []string) (appModel, tea.Cmd) {
	cs := m.condoc
	stepNumStr := match[1]
	iterLetter := ""
	substepIterLetter := ""
	if len(match) > 2 {
		iterLetter = match[2]
	}
	if len(match) > 3 {
		substepIterLetter = match[3]
	}
	stepNum, err := strconv.Atoi(stepNumStr)
	if err != nil {
		return m.condocError("revert: invalid step number: " + stepNumStr)
	}

	// Determine git target hash and substep file path.
	var targetHash string
	var substepFilePath string

	if substepIterLetter != "" {
		// iterLetter is the substep letter; substepIterLetter is the iteration within that substep.
		grepPattern := fmt.Sprintf("condoc: step %d substep %s started", stepNum, iterLetter)
		logCmd := exec.Command("git", "log", "--format=%H", "--grep="+grepPattern)
		logCmd.Dir = cs.repoRoot
		hashOut, err := logCmd.Output()
		if err != nil || strings.TrimSpace(string(hashOut)) == "" {
			return m.condocError(fmt.Sprintf("revert: could not find 'step %d substep %s started' commit", stepNum, iterLetter))
		}
		targetHash = strings.Split(strings.TrimSpace(string(hashOut)), "\n")[0]
		substepFilePath = condocSubstepFilePath(cs.mainFilePath, stepNum, iterLetter)
	} else {
		grepPattern := fmt.Sprintf("condoc: step %d started", stepNum)
		logCmd := exec.Command("git", "log", "--format=%H", "--grep="+grepPattern)
		logCmd.Dir = cs.repoRoot
		hashOut, err := logCmd.Output()
		if err != nil || strings.TrimSpace(string(hashOut)) == "" {
			return m.condocError(fmt.Sprintf("revert: could not find 'step %d started' commit", stepNum))
		}
		targetHash = strings.Split(strings.TrimSpace(string(hashOut)), "\n")[0]
	}

	cs.takeCounter++
	takeN := cs.takeCounter
	identifier := condocBranchIdentifier(cs.branch)
	takeBranch := fmt.Sprintf("condoc/%s/take%d", identifier, takeN)

	var diffFileSuffix string
	switch {
	case substepIterLetter != "":
		diffFileSuffix = fmt.Sprintf("take%d%sstep%dsubstep%srevert%sdiff.txt", takeN, identifier, stepNum, iterLetter, substepIterLetter)
	case iterLetter != "":
		diffFileSuffix = fmt.Sprintf("take%d%sstep%drevert%sdiff.txt", takeN, identifier, stepNum, iterLetter)
	default:
		diffFileSuffix = fmt.Sprintf("take%d%sstep%drevertdiff.txt", takeN, identifier, stepNum)
	}
	diffFilePath := filepath.Join(condocImplsDirPath(cs.mainFilePath), diffFileSuffix)

	targetStepFile := condocStepFilePath(cs.mainFilePath, stepNum)

	// Clear substep state; handleCondocRevertDone will restore it if reverting into a substep.
	cs.substepFile = ""
	cs.substepLetter = ""
	cs.substepStartHash = ""
	cs.pendingRevLetter = ""
	cs.stepNum = stepNum
	cs.stepFile = targetStepFile
	cs.phase = condocPhaseRunningAgent // placeholder while git ops run

	var statusLabel string
	switch {
	case substepIterLetter != "":
		statusLabel = fmt.Sprintf("reverting to step %d substep %s iteration %s…", stepNum, iterLetter, substepIterLetter)
	case iterLetter != "":
		statusLabel = fmt.Sprintf("reverting to step %d before iteration %s…", stepNum, iterLetter)
	default:
		statusLabel = fmt.Sprintf("reverting to step %d…", stepNum)
	}
	cs.statusMsg = statusLabel

	m.condocDynapane.Deactivate()
	m.blinker.SetState(BlinkerInactive)

	return m, tea.Sequence(
		tea.Println(sessionStyle.Render(fmt.Sprintf("condoc: %s — saving take%d…", statusLabel, takeN))),
		runCondocRevertGitSequence(cs.repoRoot, cs.branch, takeBranch, diffFilePath, targetStepFile, substepFilePath, targetHash, takeN, stepNum, iterLetter, substepIterLetter, cs.verbose),
	)
}

// revertGitDoneMsg is sent when the async revert git sequence completes.
type revertGitDoneMsg struct {
	stepNum         int
	stepFile        string
	errStr          string
	substepFile     string // non-empty when reverting into a substep (!REVERT-N-X-Y!)
	substepLetter   string // substep letter for the restored substep
	substepStartHash string // targetHash used for substep revert; needed for future substep retry-from-start
}

// runCondocRevertGitSequence performs the git operations for a revert: saves diff, pushes take
// branch, resets to targetHash, restores step/substep file content, commits, force-pushes.
//
//   iterLetter=""       → strip all iterations from step file (revert to step start)
//   iterLetter="X"      → keep step file content before iteration X heading
//   substepIterLetter   → iterLetter is the substep letter; reset to substep-start commit;
//                         keep substep file content before iteration substepIterLetter heading
func runCondocRevertGitSequence(repoRoot, mainBranch, takeBranch, diffFilePath, stepFilePath, substepFilePath string, targetHash string, takeN, stepNum int, iterLetter, substepIterLetter string, verbose bool) tea.Cmd {
	return func() tea.Msg {
		// 1. Capture diff of commits being abandoned.
		logCmd := exec.Command("git", "log", "-p", targetHash+"..HEAD")
		logCmd.Dir = repoRoot
		diffOut, err := logCmd.Output()
		if err != nil {
			return revertGitDoneMsg{errStr: "git log: " + err.Error()}
		}

		// 1b. If reverting within a substep, save the substep file content before the reset.
		var substepContentSaved string
		if substepIterLetter != "" && substepFilePath != "" {
			if raw, readErr := os.ReadFile(substepFilePath); readErr == nil {
				substepContentSaved = removeHumanPromptSection(string(raw))
			}
		}

		// 1c. If reverting to a step iteration, save the step file content before the reset.
		// (The reset will restore the file to its "step N started" state, erasing all iterations.)
		var stepContentSaved string
		if iterLetter != "" && substepIterLetter == "" && stepFilePath != "" {
			if raw, readErr := os.ReadFile(stepFilePath); readErr == nil {
				stepContentSaved = removeHumanPromptSection(string(raw))
			}
		}

		// 2. Push current state to take branch.
		pushTakeCmd := exec.Command("git", "push", "origin",
			fmt.Sprintf("HEAD:refs/heads/%s", takeBranch))
		pushTakeCmd.Dir = repoRoot
		if out, err := pushTakeCmd.CombinedOutput(); err != nil {
			return revertGitDoneMsg{errStr: fmt.Sprintf("push take branch: %v\n%s", err, string(out))}
		}

		// 3. Reset to target commit.
		resetCmd := exec.Command("git", "reset", "--hard", targetHash)
		resetCmd.Dir = repoRoot
		if out, err := resetCmd.CombinedOutput(); err != nil {
			return revertGitDoneMsg{errStr: fmt.Sprintf("git reset: %v\n%s", err, string(out))}
		}

		// 4. Update file(s) with trimmed content + Human-Prompt.
		switch {
		case substepIterLetter != "":
			// Revert within a substep: restore substep file trimmed to before iteration Y.
			// The step file is automatically at its substep-started state after the git reset.
			if substepContentSaved != "" {
				trimmed := trimContentBeforeIterationReply(substepContentSaved, substepIterLetter)
				var hp string
				if verbose {
					hp = fmt.Sprintf(
						"The substep has been reverted to before iteration %s.\n\n"+
							"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n"+
							"Add the '!HANDOFF!' directive to re-run the agent for this substep.",
						substepIterLetter)
				} else {
					hp = fmt.Sprintf("Reverted substep %s to before iteration %s. Add '!HANDOFF!' to re-run.", iterLetter, substepIterLetter)
				}
				_ = os.WriteFile(substepFilePath, []byte(addHumanPrompt(trimmed, hp)), 0644)
			}

		case iterLetter != "":
			// Revert to before iteration X in the step file.
			// Use the pre-reset snapshot; reading from disk here would return the post-reset (bare) state.
			if stepContentSaved != "" {
				stepContent := trimContentBeforeIterationReply(stepContentSaved, iterLetter)
				var hp string
				if verbose {
					hp = fmt.Sprintf(
						"The step has been reverted to before iteration %s.\n\n"+
							"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n"+
							"Add the '!HANDOFF!' directive to re-run the agent.",
						iterLetter)
				} else {
					hp = fmt.Sprintf("Reverted step %d to before iteration %s. Add '!HANDOFF!' to re-run.", stepNum, iterLetter)
				}
				_ = os.WriteFile(stepFilePath, []byte(addHumanPrompt(stepContent, hp)), 0644)
			}

		default:
			// Revert to step start: strip all iterations from the step file.
			if _, statErr := os.Stat(stepFilePath); statErr == nil {
				stepRaw, readErr := os.ReadFile(stepFilePath)
				if readErr == nil {
					stepContent := removeHumanPromptSection(string(stepRaw))
					firstReply := regexp.MustCompile(`(?m)^## (Reply|Revision|Retry)`).FindStringIndex(stepContent)
					if firstReply != nil {
						stepContent = strings.TrimRight(stepContent[:firstReply[0]], "\n") + "\n"
					}
					var hp string
					if verbose {
						hp = fmt.Sprintf(
							"The condoc has been reverted to step %d.\n\n"+
								"Note that everything after the 'Human-Prompt' header will be removed for our next interaction.\n\n"+
								"Add the '!HANDOFF!' directive to re-run the agent for this step.",
							stepNum)
					} else {
						hp = fmt.Sprintf("Reverted to step %d. Add '!HANDOFF!' to re-run the agent.", stepNum)
					}
					_ = os.WriteFile(stepFilePath, []byte(addHumanPrompt(stepContent, hp)), 0644)
				}
			}
		}

		// 5. Write diff file.
		if err := os.MkdirAll(filepath.Dir(diffFilePath), 0755); err != nil {
			return revertGitDoneMsg{errStr: "create diff dir: " + err.Error()}
		}
		if err := os.WriteFile(diffFilePath, diffOut, 0644); err != nil {
			return revertGitDoneMsg{errStr: "write diff file: " + err.Error()}
		}

		// 6. Commit diff file and updated file(s).
		addCmd := exec.Command("git", "add", ".")
		addCmd.Dir = repoRoot
		if out, err := addCmd.CombinedOutput(); err != nil {
			return revertGitDoneMsg{errStr: fmt.Sprintf("git add: %v\n%s", err, string(out))}
		}
		var commitMsg string
		switch {
		case substepIterLetter != "":
			commitMsg = fmt.Sprintf("condoc: revert step %d substep %s to before %s, take%d snapshot saved", stepNum, iterLetter, substepIterLetter, takeN)
		case iterLetter != "":
			commitMsg = fmt.Sprintf("condoc: revert step %d to before %s, take%d snapshot saved", stepNum, iterLetter, takeN)
		default:
			commitMsg = fmt.Sprintf("condoc: revert to step %d, take%d snapshot saved", stepNum, takeN)
		}
		commitCmd := exec.Command("git", "commit", "-m", commitMsg)
		commitCmd.Dir = repoRoot
		if out, err := commitCmd.CombinedOutput(); err != nil {
			return revertGitDoneMsg{errStr: fmt.Sprintf("git commit: %v\n%s", err, string(out))}
		}

		// 7. Force-push.
		pushMainCmd := exec.Command("git", "push", "--force", "origin", mainBranch)
		pushMainCmd.Dir = repoRoot
		if out, err := pushMainCmd.CombinedOutput(); err != nil {
			return revertGitDoneMsg{errStr: fmt.Sprintf("push main branch: %v\n%s", err, string(out))}
		}

		// substepLetter and substepStartHash are only meaningful for case 3.
		retSubstepLetter := ""
		retSubstepStartHash := ""
		if substepFilePath != "" {
			retSubstepLetter = iterLetter
			retSubstepStartHash = targetHash
		}
		return revertGitDoneMsg{
			stepNum:          stepNum,
			stepFile:         stepFilePath,
			substepFile:      substepFilePath,
			substepLetter:    retSubstepLetter,
			substepStartHash: retSubstepStartHash,
		}
	}
}

// handleCondocRevertDone processes the result of a revert git sequence.
func (m appModel) handleCondocRevertDone(msg revertGitDoneMsg) (appModel, tea.Cmd) {
	cs := m.condoc
	if cs == nil {
		return m, nil
	}
	if msg.errStr != "" {
		return m.condocError("revert: " + msg.errStr)
	}
	cs.stepNum = msg.stepNum
	cs.stepFile = msg.stepFile
	cs.substepFile = msg.substepFile
	cs.substepLetter = msg.substepLetter
	cs.substepStartHash = msg.substepStartHash
	cs.phase = condocPhaseAwaitingAction
	var statusMsg string
	if msg.substepFile != "" {
		statusMsg = fmt.Sprintf("reverted substep %s in step %d", msg.substepLetter, msg.stepNum)
	} else {
		statusMsg = fmt.Sprintf("reverted to step %d", msg.stepNum)
	}
	cs.statusMsg = statusMsg
	m.blinker.SetState(BlinkerCondoc)
	return m, tea.Batch(
		tea.Println(successStyle.Render("condoc: "+statusMsg)),
		m.condocDynapane.Activate(cs),
		m.blinker.ResetTick(),
		condocTickCmd(),
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
	m.sendCondocState()
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
