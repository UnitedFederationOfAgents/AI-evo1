package main

// The condocv2 condoc files. A v2 condoc is explicitly incompatible with v1:
// its main file carries a condocv2-yaml header (federation-command and
// condoccer only recognize condoc-yaml, so neither ever picks one up), and
// its interaction is a single linear step file of numbered sections --
//
//	## Prompt N     the human's text (the prompt commit)
//	## Response N   the agent's reply (the response commit)
//	## Record N     each target repo's commit for it (the record commit)
//
// -- rather than v1's Reply/Revision/Retry/Substep iterations. There are no
// multi-steps or substeps yet, and no Human-Prompt or directives: the UI and
// the pointer's lock drive everything.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var (
	// condocV2HeaderRe matches the commented-out condocv2-yaml header.
	condocV2HeaderRe = regexp.MustCompile("(?s)<!--\\s*```condocv2-yaml\\n(.*?)```\\s*-->")
	// interactionHeadingRe matches a section heading of the step file.
	interactionHeadingRe = regexp.MustCompile(`(?m)^## (Prompt|Response|Record) (\d+)[ \t]*$`)
	ansiEscRe            = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b[^[]`)
)

// stripANSI removes ANSI escape sequences and collapses CR-overwritten lines
// from captured agent output.
func stripANSI(s string) string {
	s = ansiEscRe.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		parts := strings.Split(line, "\r")
		lines[i] = parts[len(parts)-1]
	}
	return strings.Join(lines, "\n")
}

func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

func writeFileString(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// implDir returns the directory beside a condoc main file holding its step
// file, e.g. Simple.md -> simpleImpls/.
func implDir(mainPath string) string {
	dir := filepath.Dir(mainPath)
	base := strings.TrimSuffix(filepath.Base(mainPath), ".md")
	if base == "" {
		return filepath.Join(dir, "Impls")
	}
	runes := []rune(base)
	return filepath.Join(dir, string(unicode.ToLower(runes[0]))+string(runes[1:])+"Impls")
}

// stepFilePath is the condoc's one (linear) step file.
func stepFilePath(mainPath string) string {
	return filepath.Join(implDir(mainPath), "Step1.md")
}

// condocV2Header is the main file's condocv2-yaml header.
func condocV2Header(startTime int64, branch, pointerRepo string) string {
	return fmt.Sprintf("<!--\n```condocv2-yaml\ncondoc:\n  version: %s\n  startTime: %d\n  controlScheme: %s\n  branch: %s\n  pointerRepo: %s\n```\n-->",
		condocVersion, startTime, condocControlScheme, branch, pointerRepo)
}

// initialMainFile is the condoc main file as written when the condoc begins.
func initialMainFile(name string, startTime int64, branch, pointerRepo, description string) string {
	stepRel := filepath.Base(implDir(name+".md")) + "/Step1.md"
	content := "# " + name + "\n\n" + condocV2Header(startTime, branch, pointerRepo) + "\n\n"
	if d := strings.TrimSpace(description); d != "" {
		content += d + "\n\n"
	}
	return content + "### Step 1\n\n[Step 1](" + stepRel + ")\n"
}

// initialStepFile is the step file as written when the condoc begins: a
// backlink and nothing else until the first prompt.
func initialStepFile(name string) string {
	return fmt.Sprintf("# Step 1\n\n[%s](../%s.md)\n", name, name)
}

func sectionHeading(kind string, n int) string {
	return fmt.Sprintf("## %s %d", kind, n)
}

// hasSection reports whether content already holds "## <kind> <n>".
func hasSection(content, kind string, n int) bool {
	for _, m := range interactionHeadingRe.FindAllStringSubmatch(content, -1) {
		if m[1] == kind && m[2] == strconv.Itoa(n) {
			return true
		}
	}
	return false
}

// escapeSectionHeadings keeps text from forging a section heading of its
// own: a line that would read as one is backslash-escaped, which markdown
// renders as the same literal text.
func escapeSectionHeadings(text string) string {
	return interactionHeadingRe.ReplaceAllStringFunc(text, func(h string) string { return `\` + h })
}

// appendSection adds "## <kind> <n>" with body to the end of the step file,
// unless it is already there (a stage being retried after its commit failed).
func appendSection(path, kind string, n int, body string) error {
	content, err := readFileString(path)
	if err != nil {
		return err
	}
	if hasSection(content, kind, n) {
		return nil
	}
	body = strings.TrimSpace(escapeSectionHeadings(body))
	if body == "" {
		body = "(empty)"
	}
	content = strings.TrimRight(content, " \t\n") + "\n\n" + sectionHeading(kind, n) + "\n\n" + body + "\n"
	return writeFileString(path, content)
}

// Interaction is one parsed Prompt/Response/Record round of the step file.
type Interaction struct {
	N        int    `json:"n"`
	Prompt   string `json:"prompt"`
	Response string `json:"response,omitempty"`
	Record   string `json:"record,omitempty"`
}

// parseInteractions reads the step file's sections, in interaction order.
func parseInteractions(content string) []Interaction {
	byN := map[int]*Interaction{}
	idx := interactionHeadingRe.FindAllStringSubmatchIndex(content, -1)
	for i, loc := range idx {
		kind := content[loc[2]:loc[3]]
		n, _ := strconv.Atoi(content[loc[4]:loc[5]])
		end := len(content)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		body := strings.TrimSpace(content[loc[1]:end])
		it := byN[n]
		if it == nil {
			it = &Interaction{N: n}
			byN[n] = it
		}
		switch kind {
		case "Prompt":
			it.Prompt = body
		case "Response":
			it.Response = body
		case "Record":
			it.Record = body
		}
	}
	out := make([]Interaction, 0, len(byN))
	for _, it := range byN {
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out
}

func commitLink(repo, sha string) string {
	short := sha
	if len(short) > 7 {
		short = short[:7]
	}
	return fmt.Sprintf("[`%s`](https://github.com/%s/commit/%s)", short, repo, sha)
}

// recordBody maps each target to the commit the interaction made in it: one
// line per target, from where its branch was before the agent ran to its
// commit, or "(no changes)".
func recordBody(targets, bases, heads []string) string {
	baseOf, headOf := repoShaMap(bases), repoShaMap(heads)
	var lines []string
	for _, repo := range targets {
		base, head := baseOf[repo], headOf[repo]
		switch {
		case head == "" || head == base:
			lines = append(lines, fmt.Sprintf("- %s: (no changes)", repo))
		case base == "":
			lines = append(lines, fmt.Sprintf("- %s: %s", repo, commitLink(repo, head)))
		default:
			lines = append(lines, fmt.Sprintf("- %s: %s → %s", repo, commitLink(repo, base), commitLink(repo, head)))
		}
	}
	return strings.Join(lines, "\n")
}

// ---- the agent's instructions ----

// buildInteractionPrompt points the agent at the step file where it sees it:
// inside the deployed slopspace.
func buildInteractionPrompt(stepPath string, n int) string {
	return fmt.Sprintf("You are carrying out interaction %d of a condoc. The condoc's step file is at '%s'. "+
		"Read it: '## Prompt %d' is the human's request for this interaction, and any earlier "+
		"'## Prompt', '## Response' and '## Record' sections are the conversation so far. Carry out Prompt %d. "+
		"IMPORTANT: Do NOT write to or modify the step file or any other condoc files -- the condoc handler "+
		"captures your terminal response as Response %d. When you have finished, provide a brief summary "+
		"(2-3 sentences) of what you did as your terminal response.", n, stepPath, n, n, n)
}

// slopspaceRepoNote tells the agent where the condoc's repos sit inside the
// deployed slopspace.
func slopspaceRepoNote(deployRepos string, targets []string, condocRepo string) string {
	return fmt.Sprintf("\n\nThis condoc runs in a slopspace. The repos you may change are checked out under '%s': %s. "+
		"The condoc files live in %s (under the same directory) -- read them but do not edit them. "+
		"Do not commit or push; the handler commits and pushes every repo you changed after you finish.",
		deployRepos, strings.Join(targets, ", "), condocRepo)
}
