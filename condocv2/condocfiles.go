package main

// Condoc file transforms for slopspace condocs. In the same-repo scheme
// federation-command is the handler and owns these edits (see
// federation-command/condoc.go); in the slopspace scheme condoccer is the
// handler (docs/SlopspaceControlScheme.md, S1), so the subset it needs is
// ported here: the same headings, templates and Human-Prompt conventions,
// so a slopspace condoc reads -- and is detected by detectPhase -- exactly
// like a same-repo one. Only the brief Human-Prompt wording is kept; the UI
// drives these condocs, not a person editing the file.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	cfStepHeadingRe      = regexp.MustCompile(`(?m)^### Step (\d+) - (.+)$`)
	cfPromptBlockRe      = regexp.MustCompile("(?s)```prompt\n(.*?)\n```")
	cfRevisionHeadingRe  = regexp.MustCompile(`(?m)^## Revision ([A-Z])$`)
	cfReplyLetterRe      = regexp.MustCompile(`(?m)^## Reply ([A-Z])$`)
	cfReplyHeadingRe     = regexp.MustCompile(`(?m)^## Reply(?: [A-Z])?$`)
	cfRetryHeadingRe     = regexp.MustCompile(`(?m)^## Retry ([A-Z])(?:\s+\(from\s+(start|[A-Z])\))?$`)
	cfRevertDirectiveRe  = regexp.MustCompile(`(?m)^!REVERT-(\d+)(?:-([A-Z])(?:-([A-Z]))?)?!\s*$`)
	cfNextH2Re           = regexp.MustCompile(`(?m)^## `)
	cfAnsiEscRe          = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b[^[]`)
	cfTimestampFormat    = "Mon Jan 2 03:04:05 PM MST 2006"
	cfHandoffOrCompleted = "When you are done add the '!HANDOFF!' or '!COMPLETED!' directive."
)

func cfOrdinal(n int) string {
	names := []string{"first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth", "tenth"}
	if n >= 1 && n <= len(names) {
		return names[n-1]
	}
	return fmt.Sprintf("step %d", n)
}

// stripANSI removes ANSI escape sequences and collapses CR-overwritten lines
// from captured agent output.
func stripANSI(s string) string {
	s = cfAnsiEscRe.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		parts := strings.Split(line, "\r")
		lines[i] = parts[len(parts)-1]
	}
	return strings.Join(lines, "\n")
}

// slopspaceCondocYAMLHeader is the condoc-yaml header of a slopspace condoc.
// callerPath has no meaning here (the agent runs in the deployed slopspace),
// so pointerRepo takes its place: where this condoc's pointer lives.
func slopspaceCondocYAMLHeader(startTime int64, branch, pointerRepo string) string {
	return fmt.Sprintf("<!--\n```condoc-yaml\ncondoc:\n  startTime: %d\n  controlScheme: slopspace\n  branch: %s\n  pointerRepo: %s\n```\n-->", startTime, branch, pointerRepo)
}

// slopspaceInitialMainFile is the condoc main file as written on accept: the
// proposal (title, header, description) with step 1 already templated --
// federation-command's proposal file plus templateStep, in one go.
func slopspaceInitialMainFile(name string, startTime int64, branch, pointerRepo, description string) string {
	content := "# " + name + "\n\n" +
		slopspaceCondocYAMLHeader(startTime, branch, pointerRepo) + "\n\n" +
		strings.TrimSpace(description) + "\n"
	content = strings.TrimRight(content, "\n") +
		"\n\n\n### Step 1 - <REPLACE-TITLE>\n\n```prompt\n<REPLACE-PROMPT>\n```\n\n"
	return addHumanPrompt(content, fmt.Sprintf(
		"Once you have added the Title and Prompt add the '!HANDOFF!' directive to execute the %s step.", cfOrdinal(1)))
}

// removeHumanPromptSection strips the "## Human-Prompt" section and all
// content after it until EOF, and bare !HANDOFF!/!COMPLETED! lines
// throughout.
func removeHumanPromptSection(content string) string {
	var out []string
	inSection := false
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "## Human-Prompt" {
			inSection = true
			continue
		}
		if inSection || t == "!HANDOFF!" || t == "!COMPLETED!" {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimRight(strings.Join(out, "\n"), " \t\n") + "\n"
}

func addHumanPrompt(content, text string) string {
	return strings.TrimRight(content, "\n") + "\n\n\n## Human-Prompt\n\n" + text + "\n"
}

type parsedStep struct {
	num    int
	title  string
	prompt string
}

// parseLastStep extracts the last "### Step N" heading and its prompt block.
func parseLastStep(content string) (parsedStep, bool) {
	idxs := cfStepHeadingRe.FindAllStringSubmatchIndex(content, -1)
	if len(idxs) == 0 {
		return parsedStep{}, false
	}
	last := idxs[len(idxs)-1]
	num, _ := strconv.Atoi(content[last[2]:last[3]])
	pm := cfPromptBlockRe.FindStringSubmatch(content[last[0]:])
	if pm == nil {
		return parsedStep{}, false
	}
	return parsedStep{num: num, title: strings.TrimSpace(content[last[4]:last[5]]), prompt: strings.TrimSpace(pm[1])}, true
}

func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

func writeFileString(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// templateNextStep adds the next step's template to the main file after a
// step completes.
func templateNextStep(mainPath string, stepNum int) error {
	content, err := readFileString(mainPath)
	if err != nil {
		return err
	}
	content = strings.TrimRight(removeHumanPromptSection(content), "\n") +
		fmt.Sprintf("\n\n\n### Step %d - <REPLACE-TITLE>\n\n```prompt\n<REPLACE-PROMPT>\n```\n\n", stepNum)
	content = addHumanPrompt(content, fmt.Sprintf(
		"Add the Title and Prompt then submit the '!HANDOFF!' directive to execute the %s step,"+
			" or submit the '!COMPLETED!' directive to complete this condoc.", cfOrdinal(stepNum)))
	return writeFileString(mainPath, content)
}

// writeStepFile creates a step file with a backlink to the main file.
func writeStepFile(stepPath, mainPath, prompt string) error {
	if err := os.MkdirAll(filepath.Dir(stepPath), 0755); err != nil {
		return err
	}
	base := strings.TrimSuffix(filepath.Base(mainPath), ".md")
	backLink := fmt.Sprintf("[%s](../%s)", base, filepath.Base(mainPath))
	return writeFileString(stepPath, "# Prompt\n\n"+backLink+"\n\n"+prompt+"\n")
}

// updateMainAfterStepStart links the step file under its heading and points
// the main file's Human-Prompt at the step.
func updateMainAfterStepStart(mainPath string, stepNum int) error {
	content, err := readFileString(mainPath)
	if err != nil {
		return err
	}
	content = removeHumanPromptSection(content)
	linkText := fmt.Sprintf("[Step %d Prompt](%s/Step%dPrompt.md)", stepNum, filepath.Base(implDir(mainPath)), stepNum)
	if !strings.Contains(content, linkText) {
		headingRe := regexp.MustCompile(fmt.Sprintf(`(?m)^### Step %d - .+$`, stepNum))
		content = headingRe.ReplaceAllStringFunc(content, func(match string) string {
			if strings.Contains(match, "<REPLACE-TITLE>") {
				return match
			}
			return match + "\n\n" + linkText
		})
	}
	content = addHumanPrompt(content, fmt.Sprintf("The flow of the condoc is now within the %s step.", cfOrdinal(stepNum)))
	return writeFileString(mainPath, content)
}

// addIterationTemplate appends the "## <REPLACE-Revision|Retry> X" template
// and a fresh Human-Prompt to a step or substep file.
func addIterationTemplate(path, letter string) error {
	content, err := readFileString(path)
	if err != nil {
		return err
	}
	content = strings.TrimRight(removeHumanPromptSection(content), "\n") + "\n\n\n" +
		"## <REPLACE-Revision|Retry> " + letter + "\n\n" +
		"<REPLACE-PROMPT>\n\n\n" +
		"## Human-Prompt\n\n" + cfHandoffOrCompleted + "\n"
	return writeFileString(path, content)
}

// removeUnfilledRevisionTemplates strips "## <REPLACE-Revision|Retry> X"
// blocks still holding the unfilled placeholder.
func removeUnfilledRevisionTemplates(content string) string {
	var out []string
	skip := false
	for _, line := range strings.Split(content, "\n") {
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

// finalizeIterFile closes a step or substep file with a "## <heading>"
// completion section ("Step Completed" / "Substep Completed").
func finalizeIterFile(path, heading, noun string) error {
	content, err := readFileString(path)
	if err != nil {
		return err
	}
	content = removeUnfilledRevisionTemplates(removeHumanPromptSection(content))
	now := time.Now()
	content = strings.TrimRight(content, "\n") +
		fmt.Sprintf("\n\n\n## %s\n\nThis %s was completed at %d (%s).\n", heading, noun, now.Unix(), now.UTC().Format(cfTimestampFormat))
	return writeFileString(path, content)
}

// finalizeMainFile adds "## Condoc Completed" and drops an unstarted step
// template.
func finalizeMainFile(mainPath string) error {
	content, err := readFileString(mainPath)
	if err != nil {
		return err
	}
	content = removeHumanPromptSection(content)
	var out []string
	skip := false
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if cfStepHeadingRe.MatchString(t) && strings.Contains(t, "<REPLACE-TITLE>") {
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
		now.Unix(), now.UTC().Format(cfTimestampFormat))
	return writeFileString(mainPath, content)
}

// appendReply writes the agent's reply under "## Reply" / "## Reply X" at
// the end of a step or substep file, with preamble (the per-repo commit
// links) just above the heading.
func appendReply(path, letter, reply, preamble string) error {
	content, err := readFileString(path)
	if err != nil {
		return err
	}
	heading := "## Reply"
	if letter != "" {
		heading += " " + letter
	}
	prefix := ""
	if preamble != "" {
		prefix = preamble + "\n\n"
	}
	content = strings.TrimRight(removeHumanPromptSection(content), "\n") + "\n\n\n" + prefix + heading + "\n\n" + reply + "\n"
	return writeFileString(path, content)
}

// pendingRevisionLetter returns the letter of a Revision with no Reply yet.
func pendingRevisionLetter(content string) string {
	revs := cfRevisionHeadingRe.FindAllStringSubmatch(content, -1)
	replies := cfReplyLetterRe.FindAllStringSubmatch(content, -1)
	if len(revs) > len(replies) {
		return revs[len(revs)-1][1]
	}
	return ""
}

// lastReplyLetter returns the letter of the most recent "## Reply X", or "".
func lastReplyLetter(content string) string {
	m := cfReplyLetterRe.FindAllStringSubmatch(content, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

func nextLetterAfter(letter string) string {
	if letter == "" {
		return "A"
	}
	return string(rune(letter[0] + 1))
}

// pendingRetryLetter returns the letter of a Retry heading with no Reply yet.
func pendingRetryLetter(content string) string {
	retries := cfRetryHeadingRe.FindAllStringSubmatch(content, -1)
	if len(retries) == 0 {
		return ""
	}
	letter := retries[len(retries)-1][1]
	for _, m := range cfReplyLetterRe.FindAllStringSubmatch(content, -1) {
		if m[1] == letter {
			return ""
		}
	}
	return letter
}

// pendingSubstep returns the letter and title of the first substep heading
// in a step file that has a prompt block but no substep file yet.
func pendingSubstep(content, mainPath string, stepNum int) (string, string) {
	for _, m := range substepHeadingRe.FindAllStringSubmatch(content, -1) {
		letter, title := m[1], strings.TrimSpace(m[2])
		if strings.Contains(title, "<REPLACE") {
			continue
		}
		if _, err := os.Stat(substepFilePath(mainPath, stepNum, letter)); !os.IsNotExist(err) {
			continue
		}
		if extractSubstepPrompt(content, letter) != "" {
			return letter, title
		}
	}
	return "", ""
}

// substepSection returns the text under a substep heading up to the next H2,
// and its bounds within content.
func substepSection(content, letter string) (string, int, int, bool) {
	headingRe := regexp.MustCompile(`(?m)^## Substep ` + regexp.QuoteMeta(letter) + ` - .+$`)
	loc := headingRe.FindStringIndex(content)
	if loc == nil {
		return "", 0, 0, false
	}
	start, end := loc[1], len(content)
	if next := cfNextH2Re.FindStringIndex(content[start:]); next != nil {
		end = start + next[0]
	}
	return content[start:end], start, end, true
}

func extractSubstepPrompt(content, letter string) string {
	section, _, _, ok := substepSection(content, letter)
	if !ok {
		return ""
	}
	pm := cfPromptBlockRe.FindStringSubmatch(section)
	if pm == nil {
		return ""
	}
	return strings.TrimSpace(pm[1])
}

// startSubstepFiles writes the substep file and, in the step file, swaps the
// substep's prompt block for a link and marks the substep active.
func startSubstepFiles(stepPath, mainPath string, stepNum int, letter string) (string, error) {
	content, err := readFileString(stepPath)
	if err != nil {
		return "", err
	}
	prompt := extractSubstepPrompt(content, letter)
	if prompt == "" {
		return "", fmt.Errorf("substep %s: no prompt block", letter)
	}
	substepPath := substepFilePath(mainPath, stepNum, letter)
	if err := os.MkdirAll(filepath.Dir(substepPath), 0755); err != nil {
		return "", err
	}
	stepBase := strings.TrimSuffix(filepath.Base(stepPath), ".md")
	backLink := fmt.Sprintf("[%s](%s)", stepBase, filepath.Base(stepPath))
	if err := writeFileString(substepPath, "# Prompt\n\n"+backLink+"\n\n"+prompt+"\n"); err != nil {
		return "", err
	}

	substepName := filepath.Base(substepPath)
	linkText := fmt.Sprintf("[Step %d Substep %s](%s)", stepNum, letter, substepName)
	if section, start, end, ok := substepSection(content, letter); ok && !strings.Contains(content, linkText) {
		newSection := strings.TrimRight(cfPromptBlockRe.ReplaceAllString(section, ""), "\n") + "\n\n" + linkText + "\n\n"
		content = content[:start] + newSection + content[end:]
	}
	content = addHumanPrompt(removeHumanPromptSection(content),
		fmt.Sprintf("Substep %s is now active. Interact with the substep file (%s).", letter, substepName))
	return substepPath, writeFileString(stepPath, content)
}

// resumeStepAfterSubstep restores the step file's iteration template once a
// substep completes.
func resumeStepAfterSubstep(stepPath string) error {
	content, err := readFileString(stepPath)
	if err != nil {
		return err
	}
	return addIterationTemplate(stepPath, nextLetterAfter(lastReplyLetter(removeHumanPromptSection(content))))
}

// ---- agent prompts ----
//
// The same instructions federation-command gives, pointing at the condoc
// files where the agent sees them: inside the deployed slopspace.

const cfDoNotEdit = "IMPORTANT: Do NOT write to or modify the step file or any other condoc files — " +
	"the condoc handler will automatically capture your terminal response as the reply. "

func buildStepPrompt(stepPath string) string {
	return fmt.Sprintf("You are executing a condoc step. The step file is at '%s'. "+
		"Read the step file. Execute the task described in the '# Prompt' section. "+cfDoNotEdit+
		"When you have finished, provide a brief summary (2-3 sentences) of what you accomplished as your terminal response.", stepPath)
}

func buildRevisionPrompt(path, letter string, substep bool) string {
	kind := "step"
	if substep {
		kind = "substep"
	}
	return fmt.Sprintf("You are executing a condoc %s revision. The %s file is at '%s'. "+
		"Read the %s file. You previously completed a task (see '## Reply') and the human has "+
		"requested Revision %s (see '## Revision %s'). Execute the revision as described. "+cfDoNotEdit+
		"When you have finished, provide a brief summary (2-3 sentences) of what you changed as your terminal response.",
		kind, kind, path, kind, letter, letter)
}

func buildSubstepPrompt(stepPath, substepPath string) string {
	return fmt.Sprintf("You are executing a condoc substep. The parent step file is at '%s'. "+
		"The substep file is at '%s'. Read the substep file. Execute the task described in the '# Prompt' section. "+
		"You may also read the parent step file for additional context. "+cfDoNotEdit+
		"When you have finished, provide a brief summary (2-3 sentences) of what you accomplished as your terminal response.",
		stepPath, substepPath)
}

// slopspaceRepoNote tells the agent where the condoc's repos sit inside the
// deployed slopspace, appended to every turn's prompt.
func slopspaceRepoNote(deployRepos string, targets []string, condocRepo string) string {
	return fmt.Sprintf("\n\nThis condoc runs in a slopspace. The repos you may change are checked out under '%s': %s. "+
		"The condoc files live in %s (under the same directory) -- read them but do not edit them. "+
		"Do not commit or push; the handler commits and pushes every repo you changed after you finish.",
		deployRepos, strings.Join(targets, ", "), condocRepo)
}

// agentResourcesNote tells the agent it may hand back files to be linked
// into the condoc as a resource (see addAgentResources), appended to every
// turn's prompt after slopspaceRepoNote.
func agentResourcesNote(dir string) string {
	return fmt.Sprintf("\n\nIf this interaction calls for a resource worth keeping with the condoc (a report, diagram, log, "+
		"screenshot, data file, or a .zip of several), write it as a file into '%s'. After you finish, the handler copies "+
		"every file there into the condoc's folder and links it under a new \"## Resource N (agent)\" heading after your "+
		"reply. Do not link it or copy it into the condoc files yourself; you may describe what you added in your terminal "+
		"response.", dir)
}
