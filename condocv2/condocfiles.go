package main

// The condocv2 condoc files. A v2 condoc's main file carries a condocv2-yaml
// header (federation-command only recognizes condoc-yaml, so it never picks
// one up), and
// its interaction is a single linear step file of numbered sections --
//
//	## Prompt N     the human's text (the prompt commit)
//	## Response N   the agent's reply (the response commit)
//	## Proposals N  the DTT tool calls it proposed, if any (the same commit;
//	                the documents are copied beside the step file, dtt/N/)
//	## Record N     each target repo's commit for it (the record commit)
//	## DTT N.k      sub-iteration k: one proposal accepted or rejected (the
//	                proposal commit, see review.go)
//	## Result N.k   what dtt returned for it (the image commit; the image is
//	                beside the document)
//
// -- rather than Reply/Revision/Retry/Substep iterations. There is no
// Human-Prompt or directives: the UI and the pointer's lock drive everything.
//
// That file is the main scope's. A step (steps.go) is a scope of its own with
// a file of its own beside it, Step-<path>.md (Step-1.md, Step-1-1.md, ...),
// holding the same sections for the interactions made in it; the scope it
// was entered from links it under
//
//	## Step <path>            the step entered (the step commit)
//	## Step <path> Completed  and left again (the step-complete commit; the
//	                          step's own file ends with it too)
//
// A resource added between interactions (resources.go) has a section in the
// scope it was added in, its files beside the step files under resources/R/:
//
//	## Resource R             its title and files (the resource commit)

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
	// interactionHeadingRe matches a section heading of the step file: an
	// interaction's "## <kind> N", or a sub-iteration's "## <kind> N.k".
	// Review N is an older condoc's whole-set decision on its proposals.
	interactionHeadingRe = regexp.MustCompile(`(?m)^## (Prompt|Response|Proposals|Record|Review|DTT|Result) (\d+)(?:\.(\d+))?[ \t]*$`)
	// stepHeadingRe matches a step's sections in a scope file: "## Step
	// <path>" and "## Step <path> Completed".
	stepHeadingRe = regexp.MustCompile(`(?m)^## Step (\d+(?:-\d+)*)( Completed)?[ \t]*$`)
	ansiEscRe     = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b[^[]`)
	// backtickedRe finds the first `code` span: the proposal a "## DTT N.k"
	// section is about.
	backtickedRe = regexp.MustCompile("`([^`]+)`")
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

// stepFilePath is the main scope's step file. (It kept its name from when it
// was the condoc's only one.)
func stepFilePath(mainPath string) string {
	return filepath.Join(implDir(mainPath), "Step1.md")
}

// scopeFilePath is a scope's file: the main scope's ("") or a step's.
func scopeFilePath(mainPath, step string) string {
	if step == "" {
		return stepFilePath(mainPath)
	}
	return filepath.Join(implDir(mainPath), scopeFileName(step))
}

func scopeFileName(step string) string {
	if step == "" {
		return "Step1.md"
	}
	return "Step-" + step + ".md"
}

// scopeTitle names a scope in the condoc files and the agent's prompt.
func scopeTitle(step string) string {
	if step == "" {
		return "the main scope"
	}
	return "step-" + step
}

// initialScopeFile is a step's file as written when the step is entered: a
// backlink to the scope it was entered from, and its title.
func initialScopeFile(step, parent, title string) string {
	content := fmt.Sprintf("# Step %s\n\n[%s](%s)\n", step, strings.TrimSuffix(scopeFileName(parent), ".md"), scopeFileName(parent))
	if t := strings.TrimSpace(title); t != "" {
		content += "\n" + escapeSectionHeadings(t) + "\n"
	}
	return content
}

// appendStepSection adds "## Step <path>[ Completed]" with body to a scope
// file, unless it is already there.
func appendStepSection(path, step string, completed bool, body string) error {
	content, err := readFileString(path)
	if err != nil {
		return err
	}
	for _, m := range stepHeadingRe.FindAllStringSubmatch(content, -1) {
		if m[1] == step && (m[2] != "") == completed {
			return nil
		}
	}
	heading := "## Step " + step
	if completed {
		heading += " Completed"
	}
	content = strings.TrimRight(content, " \t\n") + "\n\n" + heading + "\n\n" + strings.TrimSpace(body) + "\n"
	return writeFileString(path, content)
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
	return content + "### Main scope\n\n[Main scope](" + stepRel + ")\n"
}

// initialStepFile is the main scope's file as written when the condoc
// begins: a backlink and nothing else until the first prompt.
func initialStepFile(name string) string {
	return fmt.Sprintf("# Main scope\n\n[%s](../%s.md)\n", name, name)
}

// sectionLabel is an interaction's number, N, or a sub-iteration's, N.k.
func sectionLabel(n, k int) string {
	if k > 0 {
		return fmt.Sprintf("%d.%d", n, k)
	}
	return strconv.Itoa(n)
}

// hasSection reports whether content already holds "## <kind> <label>".
func hasSection(content, kind, label string) bool {
	for _, m := range interactionHeadingRe.FindAllStringSubmatch(content, -1) {
		got := m[2]
		if m[3] != "" {
			got += "." + m[3]
		}
		if m[1] == kind && got == label {
			return true
		}
	}
	return false
}

// escapeSectionHeadings keeps text from forging a section heading of its
// own: a line that would read as one is backslash-escaped, which markdown
// renders as the same literal text.
func escapeSectionHeadings(text string) string {
	escape := func(h string) string { return `\` + h }
	for _, re := range []*regexp.Regexp{interactionHeadingRe, stepHeadingRe, resourceHeadingRe} {
		text = re.ReplaceAllStringFunc(text, escape)
	}
	return text
}

// sectionBoundaries is where each section of a scope file starts, in order:
// an interaction's, a step's (steps.go) or a resource's (resources.go).
func sectionBoundaries(content string) []int {
	var bounds []int
	for _, re := range []*regexp.Regexp{interactionHeadingRe, stepHeadingRe, resourceHeadingRe} {
		for _, loc := range re.FindAllStringIndex(content, -1) {
			bounds = append(bounds, loc[0])
		}
	}
	sort.Ints(bounds)
	return bounds
}

// appendSection adds "## <kind> <n>" with body to the end of the step file,
// unless it is already there (a stage being retried after its commit failed).
func appendSection(path, kind string, n int, body string) error {
	return appendSectionLabel(path, kind, sectionLabel(n, 0), body)
}

// appendSubSection is appendSection for sub-iteration n.k's "## <kind> n.k".
func appendSubSection(path, kind string, n, k int, body string) error {
	return appendSectionLabel(path, kind, sectionLabel(n, k), body)
}

func appendSectionLabel(path, kind, label, body string) error {
	content, err := readFileString(path)
	if err != nil {
		return err
	}
	if hasSection(content, kind, label) {
		return nil
	}
	body = strings.TrimSpace(escapeSectionHeadings(body))
	if body == "" {
		body = "(empty)"
	}
	content = strings.TrimRight(content, " \t\n") + "\n\n## " + kind + " " + label + "\n\n" + body + "\n"
	return writeFileString(path, content)
}

// Interaction is one parsed Prompt/Response/Record round of the step file.
type Interaction struct {
	N         int        `json:"n"`
	Prompt    string     `json:"prompt"`
	Response  string     `json:"response,omitempty"`
	Proposals string     `json:"proposals,omitempty"` // the DTT tool calls its turn proposed
	Record    string     `json:"record,omitempty"`
	Review    string     `json:"review,omitempty"` // an older condoc's whole-set decision on them
	Tools     []ToolText `json:"tools,omitempty"`  // its sub-iterations, one per proposal (see review.go)
	Step      string     `json:"step,omitempty"`   // the scope it was made in ("" the main scope, see steps.go)
}

// ToolText is sub-iteration N.k's text: the decision and what dtt returned.
type ToolText struct {
	K      int    `json:"k"`
	DTT    string `json:"dtt,omitempty"`
	Result string `json:"result,omitempty"`
}

// parseInteractions reads the step file's sections, in interaction order.
func parseInteractions(content string) []Interaction {
	byN := map[int]*Interaction{}
	idx := interactionHeadingRe.FindAllStringSubmatchIndex(content, -1)
	// A step's or a resource's section ends the one before it too.
	bounds := sectionBoundaries(content)
	for _, loc := range idx {
		kind := content[loc[2]:loc[3]]
		n, _ := strconv.Atoi(content[loc[4]:loc[5]])
		end := len(content)
		if j := sort.SearchInts(bounds, loc[1]); j < len(bounds) {
			end = bounds[j]
		}
		body := strings.TrimSpace(content[loc[1]:end])
		it := byN[n]
		if it == nil {
			it = &Interaction{N: n}
			byN[n] = it
		}
		if loc[6] >= 0 {
			k, _ := strconv.Atoi(content[loc[6]:loc[7]])
			var tool *ToolText
			for j := range it.Tools {
				if it.Tools[j].K == k {
					tool = &it.Tools[j]
				}
			}
			if tool == nil {
				it.Tools = append(it.Tools, ToolText{K: k})
				tool = &it.Tools[len(it.Tools)-1]
			}
			switch kind {
			case "DTT":
				tool.DTT = body
			case "Result":
				tool.Result = body
			}
			continue
		}
		switch kind {
		case "Prompt":
			it.Prompt = body
		case "Response":
			it.Response = body
		case "Proposals":
			it.Proposals = body
		case "Record":
			it.Record = body
		case "Review":
			it.Review = body
		}
	}
	out := make([]Interaction, 0, len(byN))
	for _, it := range byN {
		sort.Slice(it.Tools, func(i, j int) bool { return it.Tools[i].K < it.Tools[j].K })
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
	return fmt.Sprintf("[`%s`](%s)", short, repoCommitURL(repo, sha))
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
		"'## Prompt', '## Response', '## Proposals', '## Record', '## Review', '## DTT' and '## Result' sections are the conversation "+
		"so far (a DTT section is the human's answer to one of your DTT proposals, its Result what dtt returned). "+
		"A '## Resource' section is files the human added for you to use, linked from it (beside the step file, under resources/). Carry out Prompt %d. "+
		"IMPORTANT: Do NOT write to or modify the step file or any other condoc files -- the condoc handler "+
		"captures your terminal response as Response %d. When you have finished, provide a brief summary "+
		"(2-3 sentences) of what you did as your terminal response.", n, stepPath, n, n, n)
}

// scopeNote tells the agent, when the interaction is in a step, that its step
// file holds only that step's interactions, and where the files of the scopes
// around it are, should it need what came before.
func scopeNote(mainPath, step string) string {
	if step == "" {
		return ""
	}
	var outer []string
	for _, s := range stepAncestors(step) {
		outer = append(outer, fmt.Sprintf("%s '%s'", scopeTitle(s), scopeFilePath(mainPath, s)))
	}
	return fmt.Sprintf("\n\nThis interaction is in %s of the condoc: its step file holds only the interactions made in this step. "+
		"The step was entered from the scopes around it, whose files hold what came before it -- %s -- read them if you need that context.",
		scopeTitle(step), strings.Join(outer, ", "))
}

// slopspaceRepoNote tells the agent where the condoc's repos sit inside the
// deployed slopspace.
func slopspaceRepoNote(deployRepos string, targets []string, condocRepo string) string {
	return fmt.Sprintf("\n\nThis condoc runs in a slopspace. The repos you may change are checked out under '%s': %s. "+
		"The condoc files live in %s (under the same directory) -- read them but do not edit them. "+
		"Do not commit or push; the handler commits and pushes every repo you changed after you finish.",
		deployRepos, strings.Join(targets, ", "), condocRepo)
}
