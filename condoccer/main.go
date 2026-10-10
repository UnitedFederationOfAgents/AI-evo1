package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
	"representable"
	"ufa-loader/restartsignal"
	ufaversion "ufa-version"
)

//go:embed frontend/dist
var embeddedFrontend embed.FS

// Phase represents the current state of a condoc from the condoccer's perspective.
type Phase string

const (
	PhaseProposed       Phase = "proposed"
	PhaseAwaitingStep   Phase = "awaiting_step"
	PhaseAgentRunning   Phase = "agent_running"
	PhaseAwaitingAction Phase = "awaiting_action"
	PhaseCompleted      Phase = "completed"
)

// CondocInfo is the lightweight summary sent in the list view.
type CondocInfo struct {
	Path          string `json:"path"`
	Name          string `json:"name"`
	Phase         Phase  `json:"phase"`
	StepNum       int    `json:"stepNum"`
	StepFile      string `json:"stepFile,omitempty"`
	SubstepFile   string `json:"substepFile,omitempty"`
	SubstepLetter string `json:"substepLetter,omitempty"`

	// JustResumedFromSubstep is true when Phase is PhaseAwaitingAction purely
	// because a substep just completed and control returned to the step (the
	// step file's most recent event is a "## Substep X - ..." heading, not a
	// Reply/Revision/Retry) -- see stepLastEventIsSubstep. It's condoccer-
	// internal bookkeeping for updateCondocLock (see Step1Prompt.md Revision
	// C), not something the UI needs, hence json:"-".
	JustResumedFromSubstep bool `json:"-"`
}

// CondocState is the full detail for the selected condoc.
type CondocState struct {
	Info                  CondocInfo        `json:"info"`
	MainContent           string            `json:"mainContent"`
	StepContent           string            `json:"stepContent,omitempty"`
	SubstepContent        string            `json:"substepContent,omitempty"`
	SubstepIterations     []Iteration       `json:"substepIterations,omitempty"`
	NextLetter            string            `json:"nextLetter"`
	FromOptions           []string          `json:"fromOptions"`
	Meta                  CondocMeta        `json:"meta"`
	Description           string            `json:"description"`
	Steps                 []StepSummary     `json:"steps"`
	Iterations            []Iteration       `json:"iterations"`
	CompletedStepContents map[int]string    `json:"completedStepContents,omitempty"`

	// CompletedSubstepContents holds the full raw content of every substep of
	// the active step other than the one currently active (keyed by substep
	// letter). SubstepContent/SubstepIterations above are only ever populated
	// for a *currently active* substep (info.SubstepFile != ""), so without
	// this a completed substep's entire history (all its replies, revisions,
	// resources) would vanish from the UI the instant "## Substep Completed"
	// landed and control returned to the step, even though nothing was
	// actually deleted on disk -- see
	// condocs/initialShellsSessionManagerAndTheConversationalistImpls/Step1Prompt.md
	// Revision C.
	CompletedSubstepContents map[string]string `json:"completedSubstepContents,omitempty"`
}

// wsMsg is the wire format for WebSocket messages.
type wsMsg struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// ActionRequest is sent by the client when the user clicks an action button.
type ActionRequest struct {
	Action        string `json:"action"`        // handoff, completed, revision, retry, substep, start_step, revert, resubmit, add_resource
	Path          string `json:"path"`          // condoc path (relative to repo root)
	Content       string `json:"content,omitempty"`
	Letter        string `json:"letter,omitempty"`
	From          string `json:"from,omitempty"`
	SubstepTitle  string `json:"substepTitle,omitempty"`  // for substep action
	RevertStep    int    `json:"revertStep,omitempty"`    // for revert action
	RevertIter    string `json:"revertIter,omitempty"`    // for revert action (optional iteration letter)
	RevertSubIter string `json:"revertSubIter,omitempty"` // for revert action (optional substep iter letter)
	ResourceType  string `json:"resourceType,omitempty"`  // for add_resource action: "highlighted" or "voice-note" (Revision B); "upload" goes through /api/upload-resource instead
	ResourceName  string `json:"resourceName,omitempty"`  // for add_resource action: optional display name -> "## Resource N -- <name>"
}

// CondocMeta holds the parsed condoc-yaml fields.
type CondocMeta struct {
	StartTime     int64  `json:"startTime,omitempty"`
	ControlScheme string `json:"controlScheme,omitempty"`
	Branch        string `json:"branch,omitempty"`
	CallerPath    string `json:"callerPath,omitempty"`
}

// StepSummary is a parsed step entry from the condoc main file.
type StepSummary struct {
	Num        int    `json:"num"`
	Title      string `json:"title"`
	Prompt     string `json:"prompt"`
	HasReplace bool   `json:"hasReplace,omitempty"`
}

// Iteration is a parsed section header from a condoc step or substep file.
type Iteration struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"` // "reply", "revision", "retry", "substep", "resource"
	From  string `json:"from,omitempty"`
}

var (
	condocYamlRe         = regexp.MustCompile(`condoc-yaml`)
	completedRe          = regexp.MustCompile(`(?m)^## Condoc Completed\s*$`)
	humanPromptRe        = regexp.MustCompile(`(?m)^## Human-Prompt\s*$`)
	replaceTitleRe       = regexp.MustCompile(`(?m)^### Step \d+ - <REPLACE-TITLE>`)
	stepNumRe            = regexp.MustCompile(`(?m)^### Step (\d+)`)
	replyLetterRe        = regexp.MustCompile(`(?m)^## Reply ([A-Z])\s*$`)
	condocYamlBlockRe    = regexp.MustCompile("(?s)```condoc-yaml\n(.*?)```")
	htmlCommentRe        = regexp.MustCompile(`(?s)<!--.*?-->`)
	stepHeadingRe        = regexp.MustCompile(`(?m)^### Step (\d+) - (.+)$`)
	promptBlockRe        = regexp.MustCompile("(?s)```prompt\n(.*?)```")
	anyH23Re             = regexp.MustCompile(`(?m)^#{2,}`)
	anyH2Re              = regexp.MustCompile(`(?m)^## `)
	iterHeadingRe        = regexp.MustCompile(`(?m)^## (Reply|Revision|Retry)(?: ([A-Z]))?(?: \(from (\w+)\))?`)
	substepHeadingRe     = regexp.MustCompile(`(?m)^## Substep ([A-Z]) - (.+)$`)
	substepCompletedRe   = regexp.MustCompile(`(?m)^## Substep Completed\s*$`)
	substepActiveRe      = regexp.MustCompile(`Substep ([A-Z]) is now active\. Interact with the substep file \(([^)]+)\)`)
	handoffDirectiveRe   = regexp.MustCompile(`(?m)^!HANDOFF!\s*$`)
	completedDirectiveRe = regexp.MustCompile(`(?m)^!COMPLETED!\s*$`)
	commitHashRe         = regexp.MustCompile(`^[a-f0-9]{4,40}$`)
	resourceHeadingRe    = regexp.MustCompile(`(?m)^## Resource (\d+)(?: -- (.+))?\s*$`)
	placeholderLineRe    = regexp.MustCompile(`(?m)^## <REPLACE-Revision\|Retry> [A-Z]\s*$`)
)

// DiffHunk represents a single @@ hunk in a unified diff.
type DiffHunk struct {
	Header  string `json:"header"`
	LineIdx int    `json:"lineIdx"`
}

// implDir returns the step-implementation directory adjacent to a condoc main file.
// e.g. Simple.md → simpleImpls/
func implDir(mainPath string) string {
	dir := filepath.Dir(mainPath)
	base := strings.TrimSuffix(filepath.Base(mainPath), ".md")
	if len(base) == 0 {
		return filepath.Join(dir, "Impls")
	}
	runes := []rune(base)
	lower := string(unicode.ToLower(runes[0])) + string(runes[1:])
	return filepath.Join(dir, lower+"Impls")
}

// stepFilePath returns the absolute path to StepNPrompt.md.
func stepFilePath(mainPath string, stepNum int) string {
	return filepath.Join(implDir(mainPath), fmt.Sprintf("Step%dPrompt.md", stepNum))
}

// substepFilePath returns the absolute path to StepNSubstepLPrompt.md,
// mirroring federation-command's condocSubstepFilePath naming convention.
func substepFilePath(mainPath string, stepNum int, letter string) string {
	return filepath.Join(implDir(mainPath), fmt.Sprintf("Step%dSubstep%sPrompt.md", stepNum, letter))
}

// parseMaxStepNum returns the highest step number found in main file content, or 0.
func parseMaxStepNum(content string) int {
	matches := stepNumRe.FindAllStringSubmatch(content, -1)
	n := 0
	for _, m := range matches {
		var num int
		fmt.Sscanf(m[1], "%d", &num)
		if num > n {
			n = num
		}
	}
	return n
}

// detectPhase determines the current phase of a condoc by inspecting its files.
func detectPhase(root, absPath string) (CondocInfo, error) {
	relPath, _ := filepath.Rel(root, absPath)
	name := strings.TrimSuffix(filepath.Base(absPath), ".md")

	content, err := os.ReadFile(absPath)
	if err != nil {
		return CondocInfo{}, err
	}
	text := string(content)

	if completedRe.MatchString(text) {
		return CondocInfo{Path: relPath, Name: name, Phase: PhaseCompleted}, nil
	}

	stepNum := parseMaxStepNum(text)

	// Template present means awaiting human to fill in step details.
	if replaceTitleRe.MatchString(text) {
		return CondocInfo{Path: relPath, Name: name, Phase: PhaseAwaitingStep, StepNum: stepNum}, nil
	}

	if stepNum == 0 {
		return CondocInfo{Path: relPath, Name: name, Phase: PhaseProposed}, nil
	}

	sf := stepFilePath(absPath, stepNum)
	sfRel, _ := filepath.Rel(root, sf)

	sfContent, err := os.ReadFile(sf)
	if err != nil {
		// Step file not created yet — federation-command is in branching/starting phase.
		return CondocInfo{Path: relPath, Name: name, Phase: PhaseProposed, StepNum: stepNum}, nil
	}

	if humanPromptRe.Match(sfContent) {
		// Check if a substep is currently active.
		if m := substepActiveRe.FindSubmatch(sfContent); m != nil {
			substepLetter := string(m[1])
			substepFileName := string(m[2])
			substepPath := filepath.Join(filepath.Dir(sf), substepFileName)
			substepRel, _ := filepath.Rel(root, substepPath)

			substepContent, substepErr := os.ReadFile(substepPath)
			if substepErr != nil {
				// Substep file missing — agent probably hasn't created it yet.
				return CondocInfo{Path: relPath, Name: name, Phase: PhaseAgentRunning, StepNum: stepNum, StepFile: sfRel, SubstepFile: substepRel, SubstepLetter: substepLetter}, nil
			}
			if substepCompletedRe.Match(substepContent) {
				// Substep completed but step not yet resumed — treat as awaiting action on step.
				return CondocInfo{Path: relPath, Name: name, Phase: PhaseAwaitingAction, StepNum: stepNum, StepFile: sfRel, JustResumedFromSubstep: true}, nil
			}
			if handoffDirectiveRe.Match(substepContent) || completedDirectiveRe.Match(substepContent) {
				return CondocInfo{Path: relPath, Name: name, Phase: PhaseAgentRunning, StepNum: stepNum, StepFile: sfRel, SubstepFile: substepRel, SubstepLetter: substepLetter}, nil
			}
			if humanPromptRe.Match(substepContent) {
				return CondocInfo{Path: relPath, Name: name, Phase: PhaseAwaitingAction, StepNum: stepNum, StepFile: sfRel, SubstepFile: substepRel, SubstepLetter: substepLetter}, nil
			}
			return CondocInfo{Path: relPath, Name: name, Phase: PhaseAgentRunning, StepNum: stepNum, StepFile: sfRel, SubstepFile: substepRel, SubstepLetter: substepLetter}, nil
		}

		if handoffDirectiveRe.Match(sfContent) || completedDirectiveRe.Match(sfContent) {
			return CondocInfo{Path: relPath, Name: name, Phase: PhaseAgentRunning, StepNum: stepNum, StepFile: sfRel}, nil
		}
		// No substep currently active. This is either an ordinary
		// revision/retry reply having just landed on the step, or -- if the
		// step file's most recent event is a substep heading rather than a
		// Reply/Revision/Retry -- a substep having just completed and handed
		// control back to the step with no fresh work of the step's own yet.
		// JustResumedFromSubstep distinguishes the two for updateCondocLock's
		// benefit (see its doc comment).
		return CondocInfo{Path: relPath, Name: name, Phase: PhaseAwaitingAction, StepNum: stepNum, StepFile: sfRel, JustResumedFromSubstep: stepLastEventIsSubstep(string(sfContent))}, nil
	}
	return CondocInfo{Path: relPath, Name: name, Phase: PhaseAgentRunning, StepNum: stepNum, StepFile: sfRel}, nil
}

// stepLastEventIsSubstep reports whether a step file's most recent top-level
// event (by position) is a "## Substep X - ..." heading rather than a
// "## Reply/Revision/Retry" one -- i.e. the step reached "awaiting action"
// purely because a substep just completed and handed control back, with no
// fresh agent-produced work of the step's own since. Used by detectPhase to
// set JustResumedFromSubstep.
func stepLastEventIsSubstep(stepContent string) bool {
	lastSubstepPos := -1
	for _, loc := range substepHeadingRe.FindAllStringIndex(stepContent, -1) {
		lastSubstepPos = loc[0]
	}
	if lastSubstepPos < 0 {
		return false
	}
	for _, loc := range iterHeadingRe.FindAllStringIndex(stepContent, -1) {
		if loc[0] > lastSubstepPos {
			return false
		}
	}
	return true
}

// nextRevLetter returns the next revision/retry letter based on existing ## Reply X lines.
func nextRevLetter(stepContent string) string {
	matches := replyLetterRe.FindAllStringSubmatch(stepContent, -1)
	if len(matches) == 0 {
		return "A"
	}
	last := matches[len(matches)-1][1]
	return string(rune(last[0]) + 1)
}

// fromOptions returns valid "from" targets for a retry given the next pending letter.
func fromOptions(nextLetter string) []string {
	opts := []string{"start"}
	for l := 'A'; l < rune(nextLetter[0]); l++ {
		opts = append(opts, string(l))
	}
	return opts
}

// parseCondocMeta extracts structured fields from the condoc-yaml block.
func parseCondocMeta(content string) CondocMeta {
	m := condocYamlBlockRe.FindStringSubmatch(content)
	if m == nil {
		return CondocMeta{}
	}
	var meta CondocMeta
	for _, line := range strings.Split(m[1], "\n") {
		kv := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(kv) != 2 {
			continue
		}
		key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		switch key {
		case "startTime":
			fmt.Sscanf(val, "%d", &meta.StartTime)
		case "controlScheme":
			meta.ControlScheme = val
		case "branch":
			meta.Branch = val
		case "callerPath":
			meta.CallerPath = val
		}
	}
	return meta
}

// parseDescription extracts the prose description from the condoc main file.
func parseDescription(content string) string {
	text := htmlCommentRe.ReplaceAllString(content, "")
	// Remove the h1 title line.
	if loc := regexp.MustCompile(`(?m)^#\s+.+\n?`).FindStringIndex(text); loc != nil {
		text = text[loc[1]:]
	}
	// Keep only text before the first h2/h3 heading.
	if loc := anyH23Re.FindStringIndex(text); loc != nil {
		text = text[:loc[0]]
	}
	return strings.TrimSpace(text)
}

// parseSteps extracts the list of step summaries from the condoc main file.
func parseSteps(content string) []StepSummary {
	allIdx := stepHeadingRe.FindAllStringSubmatchIndex(content, -1)
	var steps []StepSummary
	for i, idx := range allIdx {
		numStr := content[idx[2]:idx[3]]
		title := strings.TrimSpace(content[idx[4]:idx[5]])

		sectionStart := idx[1]
		sectionEnd := len(content)
		if i+1 < len(allIdx) {
			sectionEnd = allIdx[i+1][0]
		}
		if loc := anyH2Re.FindStringIndex(content[sectionStart:sectionEnd]); loc != nil {
			sectionEnd = sectionStart + loc[0]
		}
		section := content[sectionStart:sectionEnd]

		prompt := ""
		if pm := promptBlockRe.FindStringSubmatch(section); pm != nil {
			prompt = strings.TrimSpace(pm[1])
		}

		hasReplace := strings.Contains(title, "<REPLACE") || strings.Contains(prompt, "<REPLACE")

		var num int
		fmt.Sscanf(numStr, "%d", &num)
		steps = append(steps, StepSummary{Num: num, Title: title, Prompt: prompt, HasReplace: hasReplace})
	}
	return steps
}

// parseIterations extracts the ordered list of reply/revision/retry/substep/resource sections from a step file.
// Results are ordered by their position in the file so substeps and resources appear in context with other iterations.
func parseIterations(stepContent string) []Iteration {
	type candidate struct {
		pos  int
		iter Iteration
	}
	var candidates []candidate

	// Parse Reply/Revision/Retry headings.
	for _, idx := range iterHeadingRe.FindAllStringSubmatchIndex(stepContent, -1) {
		kind := stepContent[idx[2]:idx[3]]
		letter := ""
		if idx[4] >= 0 {
			letter = stepContent[idx[4]:idx[5]]
		}
		from := ""
		if idx[6] >= 0 {
			from = stepContent[idx[6]:idx[7]]
		}
		var iter Iteration
		switch kind {
		case "Reply":
			if letter == "" {
				iter = Iteration{ID: "reply-initial", Label: "Reply", Type: "reply"}
			} else {
				iter = Iteration{ID: "reply-" + letter, Label: "Reply " + letter, Type: "reply"}
			}
		case "Revision":
			iter = Iteration{ID: "revision-" + letter, Label: "Revision " + letter, Type: "revision"}
		case "Retry":
			label := "Retry " + letter
			if from != "" {
				label += " (from " + from + ")"
			}
			iter = Iteration{ID: "retry-" + letter, Label: label, Type: "retry", From: from}
		}
		candidates = append(candidates, candidate{pos: idx[0], iter: iter})
	}

	// Parse Substep headings and interleave them by position.
	for _, idx := range substepHeadingRe.FindAllStringSubmatchIndex(stepContent, -1) {
		letter := stepContent[idx[2]:idx[3]]
		title := strings.TrimSpace(stepContent[idx[4]:idx[5]])
		candidates = append(candidates, candidate{
			pos: idx[0],
			iter: Iteration{
				ID:    "substep-" + letter,
				Label: "Substep " + letter + " — " + title,
				Type:  "substep",
			},
		})
	}

	// Parse Resource headings and interleave them by position too -- Revision B
	// of Step5SubstepRPrompt.md gives each "## Resource N[ -- <name>]" block its
	// own sidebar entry (previously it had no heading of its own here, so its
	// body just read as an unstyled tail of whichever Reply/Revision preceded
	// it -- see parseStepSections on the frontend for the client-side mirror).
	for _, idx := range resourceHeadingRe.FindAllStringSubmatchIndex(stepContent, -1) {
		num := stepContent[idx[2]:idx[3]]
		name := ""
		if idx[4] >= 0 {
			name = strings.TrimSpace(stepContent[idx[4]:idx[5]])
		}
		label := "Resource " + num
		if name != "" {
			label += " | " + name
		}
		candidates = append(candidates, candidate{
			pos:  idx[0],
			iter: Iteration{ID: "resource-" + num, Label: label, Type: "resource"},
		})
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].pos < candidates[j].pos })

	iters := make([]Iteration, len(candidates))
	for i, c := range candidates {
		iters[i] = c.iter
	}
	return iters
}

// getCondocState returns full detail for a condoc at absPath.
func getCondocState(root, absPath string) (CondocState, error) {
	info, err := detectPhase(root, absPath)
	if err != nil {
		return CondocState{}, err
	}

	mainContent, _ := os.ReadFile(absPath)

	var stepContent []byte
	if info.StepFile != "" {
		stepContent, _ = os.ReadFile(filepath.Join(root, info.StepFile))
	}

	var substepContent []byte
	if info.SubstepFile != "" {
		substepContent, _ = os.ReadFile(filepath.Join(root, info.SubstepFile))
	}

	mainStr := string(mainContent)
	stepStr := string(stepContent)
	substepStr := string(substepContent)

	// nextLetter and fromOptions reflect the active file (substep if present, else step).
	activeForLetter := stepStr
	if substepStr != "" {
		activeForLetter = substepStr
	}
	letter := nextRevLetter(activeForLetter)
	opts := fromOptions(letter)

	// Load past step files so the UI can show iterations for completed steps.
	completedStepContents := make(map[int]string)
	maxStep := parseMaxStepNum(mainStr)
	for n := 1; n <= maxStep; n++ {
		if n == info.StepNum {
			continue // current active step is already in StepContent
		}
		sfPath := stepFilePath(absPath, n)
		if b, readErr := os.ReadFile(sfPath); readErr == nil {
			completedStepContents[n] = string(b)
		}
	}

	// Load every other substep of the active step (all but the currently
	// active one, already covered by SubstepContent above) so the UI can
	// still show a completed substep's full history -- see
	// CompletedSubstepContents' doc comment.
	completedSubstepContents := make(map[string]string)
	for _, m := range substepHeadingRe.FindAllStringSubmatch(stepStr, -1) {
		sLetter := m[1]
		if sLetter == info.SubstepLetter {
			continue // currently-active substep is already in SubstepContent
		}
		if b, readErr := os.ReadFile(substepFilePath(absPath, info.StepNum, sLetter)); readErr == nil {
			completedSubstepContents[sLetter] = string(b)
		}
	}

	return CondocState{
		Info:                     info,
		MainContent:              mainStr,
		StepContent:              stepStr,
		SubstepContent:           substepStr,
		SubstepIterations:        parseIterations(substepStr),
		NextLetter:               letter,
		FromOptions:              opts,
		Meta:                     parseCondocMeta(mainStr),
		Description:              parseDescription(mainStr),
		Steps:                    parseSteps(mainStr),
		Iterations:               parseIterations(stepStr),
		CompletedStepContents:    completedStepContents,
		CompletedSubstepContents: completedSubstepContents,
	}, nil
}

// findCondocs walks root returning all condoc main files (identified by condoc-yaml header).
func findCondocs(root string) ([]CondocInfo, error) {
	var infos []CondocInfo

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "Impls") || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || !condocYamlRe.Match(b) {
			return nil
		}
		info, err := detectPhase(root, path)
		if err == nil {
			infos = append(infos, info)
		}
		return nil
	})

	sort.Slice(infos, func(i, j int) bool { return infos[i].Path < infos[j].Path })
	return infos, err
}

// ---- WebSocket server ----

type wsClient struct {
	conn       *websocket.Conn
	subscribed string // relative path of condoc being watched
	send       chan []byte
	done       chan struct{}
}

// Server manages WebSocket clients and polls condoc files for changes.
type Server struct {
	// root is the scan scope condocs are discovered under (and that every
	// CondocInfo.Path is relative to). It starts as --root and can be changed
	// at runtime via the UI's "Set Working Dir" control (see setRoot), so read
	// it through getRoot. It is deliberately *not* assumed to be a git repo
	// root -- git work (the .condoc lock, diffs, resubmit) runs against each
	// condoc's own repo instead (see condocRepoRoot).
	rootMu   sync.RWMutex
	root     string
	httpPort string // HTTP port this condoccer serves on (reported to local-representative)
	name     string // identifier reported to local-representative
	devMode  bool   // --dev-mode: this condoccer instance -- see docs/DevMode.md
	upgrader websocket.Upgrader
	mu       sync.RWMutex
	clients  map[*wsClient]bool

	// representable link to local-representative (see repr.go). nil until connected.
	reprMu       sync.Mutex
	reprClient   *representable.Client
	reprStatus   string        // "disconnected" | "connecting" | "connected"
	reprHost     string        // host of the current/last connect attempt (widget default)
	reprPort     string        // port of the current/last connect attempt (widget default)
	reprStop     chan struct{} // non-nil while a connectLoop is running; closing it stops retries
	// reprAutoConnect is the persistent auto-connect toggle (see Revision I
	// of Step3Prompt.md) -- a first-class state independent of any single
	// connection attempt. It stays true across a successful connection, so a
	// later unintentional disconnect resumes the retry cycle on its own (see
	// connectLoop); only an explicit disconnect (see disconnectRepr) turns it
	// off.
	reprAutoConnect  bool
	modeMismatch     bool   // true while local-representative discloses a dev/ops mode mismatch -- see docs/DevMode.md
	modeMismatchPeer string // the mismatched LR's disclosed mode ("dev" or "ops")

	// tcMu/tcAvailable hold the aggregate "is a the-conversationalist instance
	// available on any host" verdict, relayed down from local-representative
	// (which in turn relays it from agent-coordinator's own aggregate) -- see
	// tcavailability.go and
	// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
	// Step2Prompt.md.
	tcMu        sync.RWMutex
	tcAvailable bool
}

func newServer(root string) *Server {
	return &Server{
		root: root,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:    make(map[*wsClient]bool),
		reprStatus: "disconnected",
	}
}

// RootMsg is the "root" WebSocket payload: condoccer's current working dir
// (scan root), sent on connect and broadcast whenever it changes.
type RootMsg struct {
	Root string `json:"root"`
}

func (s *Server) getRoot() string {
	s.rootMu.RLock()
	defer s.rootMu.RUnlock()
	return s.root
}

// setRoot changes the scan root ("Set Working Dir"). The new root must be an
// existing directory; a relative path is resolved against the current root.
// Every client's subscription is dropped, since condoc paths are relative to
// the root and the old ones no longer mean anything; the frontend returns to
// the condoc list when it sees the new root. watchLoop notices the change on
// its next tick and resets its per-condoc tracking.
func (s *Server) setRoot(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("working dir is empty")
	}
	if strings.HasPrefix(path, "~/") || path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.getRoot(), path)
	}
	path = filepath.Clean(path)
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("working dir: %v", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("working dir: %s is not a directory", path)
	}

	s.rootMu.Lock()
	changed := s.root != path
	s.root = path
	s.rootMu.Unlock()
	if !changed {
		return nil
	}
	log.Printf("working dir changed to %s", path)

	msg := s.marshalMsg("root", RootMsg{Root: path})
	s.mu.Lock()
	for c := range s.clients {
		c.subscribed = ""
		select {
		case c.send <- msg:
		default:
		}
	}
	s.mu.Unlock()
	s.broadcastList()
	s.pushCondoccerState()
	return nil
}

// findGitRoot walks up from dir to the nearest directory containing a .git
// entry (a directory for an ordinary clone, a file for a worktree), mirroring
// federation-command's condocFindGitRoot. Returns "" when there is none.
func findGitRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// condocRepoRoot returns the git repo root owning the condoc at relPath
// (relative to the current scan root) -- the place its .condoc lock lives and
// its git operations run. Falls back to the scan root when the condoc isn't
// inside any repo, which preserves the old single-root behaviour.
func (s *Server) condocRepoRoot(relPath string) string {
	root := s.getRoot()
	if relPath == "" {
		if r := findGitRoot(root); r != "" {
			return r
		}
		return root
	}
	if r := findGitRoot(filepath.Dir(filepath.Join(root, relPath))); r != "" {
		return r
	}
	return root
}

func (s *Server) marshalMsg(typ string, payload interface{}) []byte {
	p, _ := json.Marshal(payload)
	m := wsMsg{Type: typ, Payload: p}
	b, _ := json.Marshal(m)
	return b
}

func (s *Server) sendToClient(c *wsClient, typ string, payload interface{}) {
	select {
	case c.send <- s.marshalMsg(typ, payload):
	default:
	}
}

func (s *Server) sendList(c *wsClient) {
	infos, _ := findCondocs(s.getRoot())
	s.sendToClient(c, "list", map[string]interface{}{"condocs": infos})
}

func (s *Server) sendRoot(c *wsClient) {
	s.sendToClient(c, "root", RootMsg{Root: s.getRoot()})
}

func (s *Server) sendCondocState(c *wsClient, relPath string) {
	root := s.getRoot()
	absPath := filepath.Join(root, relPath)
	state, err := getCondocState(root, absPath)
	if err != nil {
		s.sendToClient(c, "error", map[string]string{"message": err.Error()})
		return
	}
	s.sendToClient(c, "condoc", state)
}

func (s *Server) broadcastList() {
	infos, _ := findCondocs(s.getRoot())
	msg := s.marshalMsg("list", map[string]interface{}{"condocs": infos})
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

func (s *Server) broadcastCondocUpdate(relPath string) {
	root := s.getRoot()
	absPath := filepath.Join(root, relPath)
	state, err := getCondocState(root, absPath)
	if err != nil {
		return
	}
	msg := s.marshalMsg("condoc", state)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		if c.subscribed == relPath {
			select {
			case c.send <- msg:
			default:
			}
		}
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("ws upgrade:", err)
		return
	}

	c := &wsClient{
		conn: conn,
		send: make(chan []byte, 64),
		done: make(chan struct{}),
	}

	s.mu.Lock()
	s.clients[c] = true
	s.mu.Unlock()

	// Send initial condoc list and representable connection status.
	go s.sendList(c)
	go s.sendRoot(c)
	go s.sendReprStatus(c)
	go s.sendToClient(c, "self-info", SelfInfoMsg{DevMode: s.devMode, Version: ufaversion.Version})
	go s.sendModeMismatch(c)
	go s.sendTCAvailability(c)

	// Write pump.
	go func() {
		defer conn.Close()
		for {
			select {
			case msg, ok := <-c.send:
				if !ok {
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			case <-c.done:
				return
			}
		}
	}()

	// Read pump (blocks until client disconnects).
	defer func() {
		close(c.done)
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var m wsMsg
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		go s.handleClientMsg(c, m)
	}
}

func (s *Server) handleClientMsg(c *wsClient, m wsMsg) {
	switch m.Type {
	case "list":
		s.sendList(c)

	case "subscribe":
		var p struct {
			Path string `json:"path"`
		}
		json.Unmarshal(m.Payload, &p)
		s.mu.Lock()
		c.subscribed = p.Path
		s.mu.Unlock()
		s.sendCondocState(c, p.Path)

	case "action":
		var action ActionRequest
		json.Unmarshal(m.Payload, &action)
		if err := s.performAction(action); err != nil {
			s.sendToClient(c, "error", map[string]string{"message": err.Error()})
		}

	case "connect":
		// Manual connect: the widget lets a condoccer started without
		// --auto-connect (or one whose auto-connect gave up) link up to
		// local-representative on demand.
		var p struct {
			Host string `json:"host"`
			Port string `json:"port"`
		}
		json.Unmarshal(m.Payload, &p)
		host := strings.TrimSpace(p.Host)
		if host == "" {
			host = "localhost"
		}
		port := strings.TrimSpace(p.Port)
		if port == "" {
			port = "8082"
		}
		s.startConnectLoop(host, port)

	case "disconnect":
		s.disconnectRepr()

	case "set-auto-connect":
		var p struct {
			Enabled bool   `json:"enabled"`
			Host    string `json:"host"`
			Port    string `json:"port"`
		}
		json.Unmarshal(m.Payload, &p)
		s.setAutoConnect(p.Enabled, strings.TrimSpace(p.Host), strings.TrimSpace(p.Port))

	case "set-root":
		var p struct {
			Root string `json:"root"`
		}
		json.Unmarshal(m.Payload, &p)
		if err := s.setRoot(p.Root); err != nil {
			s.sendToClient(c, "error", map[string]string{"message": err.Error()})
		}

	// get-diff/get-file-diff carry the condoc's path so git runs against
	// that condoc's own repo (see condocRepoRoot), not the scan root.
	case "get-diff":
		var p struct {
			Path       string `json:"path"`
			FromCommit string `json:"fromCommit"`
			ToCommit   string `json:"toCommit"`
		}
		json.Unmarshal(m.Payload, &p)
		go s.handleGetDiff(c, p.Path, p.FromCommit, p.ToCommit)

	case "get-file-diff":
		var p struct {
			Path       string `json:"path"`
			FromCommit string `json:"fromCommit"`
			ToCommit   string `json:"toCommit"`
			File       string `json:"file"`
		}
		json.Unmarshal(m.Payload, &p)
		go s.handleGetFileDiff(c, p.Path, p.FromCommit, p.ToCommit, p.File)
	}
}

func (s *Server) performAction(action ActionRequest) error {
	root := s.getRoot()
	absPath := filepath.Join(root, action.Path)
	info, err := detectPhase(root, absPath)
	if err != nil {
		return err
	}

	// activeFile is the file that receives directives (substep if active, else step or main).
	activeFile := absPath
	if info.Phase == PhaseAwaitingAction {
		if info.SubstepFile != "" {
			activeFile = filepath.Join(root, info.SubstepFile)
		} else if info.StepFile != "" {
			activeFile = filepath.Join(root, info.StepFile)
		}
	}

	switch action.Action {
	case "handoff":
		return appendToFile(activeFile, "\n!HANDOFF!\n")

	case "completed":
		return appendToFile(activeFile, "\n!COMPLETED!\n")

	case "start_step":
		// Write edited main file content (with filled template), then trigger HANDOFF.
		if err := os.WriteFile(absPath, []byte(action.Content), 0644); err != nil {
			return err
		}
		return appendToFile(absPath, "\n!HANDOFF!\n")

	case "revision":
		if info.SubstepFile != "" {
			sfPath := filepath.Join(root, info.SubstepFile)
			header := fmt.Sprintf("## Revision %s", action.Letter)
			return replaceIterationPlaceholder(sfPath, action.Letter, header, action.Content)
		}
		if info.StepFile == "" {
			return fmt.Errorf("no active step or substep file")
		}
		sfPath := filepath.Join(root, info.StepFile)
		header := fmt.Sprintf("## Revision %s", action.Letter)
		return replaceIterationPlaceholder(sfPath, action.Letter, header, action.Content)

	case "retry":
		if info.SubstepFile != "" {
			sfPath := filepath.Join(root, info.SubstepFile)
			header := fmt.Sprintf("## Retry %s", action.Letter)
			if action.From != "" {
				header = fmt.Sprintf("## Retry %s (from %s)", action.Letter, action.From)
			}
			return replaceIterationPlaceholder(sfPath, action.Letter, header, action.Content)
		}
		if info.StepFile == "" {
			return fmt.Errorf("no active step or substep file")
		}
		sfPath := filepath.Join(root, info.StepFile)
		header := fmt.Sprintf("## Retry %s", action.Letter)
		if action.From != "" {
			header = fmt.Sprintf("## Retry %s (from %s)", action.Letter, action.From)
		}
		return replaceIterationPlaceholder(sfPath, action.Letter, header, action.Content)

	case "substep":
		if info.StepFile == "" {
			return fmt.Errorf("no active step file")
		}
		sfPath := filepath.Join(root, info.StepFile)
		header := fmt.Sprintf("## Substep %s - %s", action.Letter, action.SubstepTitle)
		promptBlock := fmt.Sprintf("```prompt\n%s\n```", action.Content)
		return replaceIterationPlaceholder(sfPath, action.Letter, header, promptBlock)

	case "revert":
		// Build the revert directive and append it to the active file.
		var directive string
		switch {
		case action.RevertSubIter != "":
			directive = fmt.Sprintf("!REVERT-%d-%s-%s!", action.RevertStep, action.RevertIter, action.RevertSubIter)
		case action.RevertIter != "":
			directive = fmt.Sprintf("!REVERT-%d-%s!", action.RevertStep, action.RevertIter)
		default:
			directive = fmt.Sprintf("!REVERT-%d!", action.RevertStep)
		}
		// Write revert directive to the active condoc file so the handler sees it.
		return appendToFile(activeFile, "\n"+directive+"\n")

	case "add_resource":
		return s.addResource(absPath, info, action)

	case "resubmit":
		// Stage and commit any outstanding working-tree changes so that the
		// agent coordinator's git pull --rebase can succeed after it picks up
		// the !HANDOFF! directive written below. Runs in the condoc's own
		// repo, which needn't be the scan root.
		repoRoot := s.condocRepoRoot(info.Path)
		exec.Command("git", "-C", repoRoot, "add", "-A").Run()
		if exec.Command("git", "-C", repoRoot, "diff", "--cached", "--quiet").Run() != nil {
			exec.Command("git", "-C", repoRoot, "commit", "-m", "condoc: recovery commit before resubmit").Run()
		}
		resubmitFile := absPath
		if info.StepFile != "" {
			resubmitFile = filepath.Join(root, info.StepFile)
		}
		if info.SubstepFile != "" {
			resubmitFile = filepath.Join(root, info.SubstepFile)
		}
		return appendToFile(resubmitFile, "\n!HANDOFF!\n")

	default:
		return fmt.Errorf("unknown action: %s", action.Action)
	}
}

// isCondocFile reports whether a repo-relative file path is a condoc file
// (lives under a directory segment named "condocs").
func isCondocFile(path string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == "condocs" {
			return true
		}
	}
	return false
}

// logLineIsCommitHeader reports whether a line from "git log --oneline" output
// is a commit header (short hash + space + message) rather than a filename.
func logLineIsCommitHeader(line string) bool {
	// oneline header: 4–40 hex chars followed by a space
	i := 0
	for i < len(line) && i < 40 && (line[i] >= '0' && line[i] <= '9' || line[i] >= 'a' && line[i] <= 'f') {
		i++
	}
	return i >= 4 && i < len(line) && line[i] == ' '
}

// diffRepoRoot validates the condoc path a diff request carries and resolves
// the repo to run git in. An empty path (an older frontend) falls back to the
// scan root's repo.
func (s *Server) diffRepoRoot(condocRel string) (string, error) {
	if filepath.IsAbs(condocRel) || strings.Contains(condocRel, "..") {
		return "", fmt.Errorf("invalid condoc path")
	}
	return s.condocRepoRoot(condocRel), nil
}

func (s *Server) handleGetDiff(c *wsClient, condocRel, fromCommit, toCommit string) {
	repoRoot, err := s.diffRepoRoot(condocRel)
	if err != nil {
		s.sendToClient(c, "error", map[string]string{"message": err.Error()})
		return
	}
	if !commitHashRe.MatchString(fromCommit) {
		s.sendToClient(c, "error", map[string]string{"message": "invalid commit hash"})
		return
	}
	if toCommit != "" && !commitHashRe.MatchString(toCommit) {
		s.sendToClient(c, "error", map[string]string{"message": "invalid commit hash"})
		return
	}
	rangeSpec := fromCommit + "..HEAD"
	if toCommit != "" {
		rangeSpec = fromCommit + ".." + toCommit
	}
	out, err := exec.Command("git", "-C", repoRoot, "log", "--name-only", "--oneline", rangeSpec).Output()
	if err != nil {
		s.sendToClient(c, "error", map[string]string{"message": "git log failed: " + err.Error()})
		return
	}
	fileSet := make(map[string]bool)
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l == "" || logLineIsCommitHeader(l) {
			continue
		}
		if !isCondocFile(l) {
			fileSet[l] = true
		}
	}
	var files []string
	for f := range fileSet {
		files = append(files, f)
	}
	sort.Strings(files)
	s.sendToClient(c, "diff-list", map[string]interface{}{
		"fromCommit": fromCommit,
		"toCommit":   toCommit,
		"files":      files,
	})
}

func (s *Server) handleGetFileDiff(c *wsClient, condocRel, fromCommit, toCommit, file string) {
	repoRoot, err := s.diffRepoRoot(condocRel)
	if err != nil {
		s.sendToClient(c, "error", map[string]string{"message": err.Error()})
		return
	}
	if !commitHashRe.MatchString(fromCommit) {
		s.sendToClient(c, "error", map[string]string{"message": "invalid commit hash"})
		return
	}
	if toCommit != "" && !commitHashRe.MatchString(toCommit) {
		s.sendToClient(c, "error", map[string]string{"message": "invalid commit hash"})
		return
	}
	if strings.Contains(file, "..") {
		s.sendToClient(c, "error", map[string]string{"message": "invalid file path"})
		return
	}
	rangeSpec := fromCommit + "..HEAD"
	if toCommit != "" {
		rangeSpec = fromCommit + ".." + toCommit
	}
	out, err := exec.Command("git", "-C", repoRoot, "diff", rangeSpec, "--", file).Output()
	if err != nil {
		s.sendToClient(c, "error", map[string]string{"message": "git diff failed: " + err.Error()})
		return
	}
	content := string(out)
	lines := strings.Split(content, "\n")
	var hunks []DiffHunk
	for i, line := range lines {
		if strings.HasPrefix(line, "@@") {
			hunks = append(hunks, DiffHunk{Header: line, LineIdx: i})
		}
	}
	s.sendToClient(c, "file-diff", map[string]interface{}{
		"fromCommit": fromCommit,
		"toCommit":   toCommit,
		"file":       file,
		"content":    content,
		"hunks":      hunks,
	})
}

func appendToFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// replaceIterationPlaceholder replaces the "## <REPLACE-Revision|Retry> X\n\n<REPLACE-PROMPT>"
// block in-place with the real header+content, then appends !HANDOFF! at the end.
func replaceIterationPlaceholder(sfPath, letter, header, content string) error {
	data, err := os.ReadFile(sfPath)
	if err != nil {
		return err
	}
	placeholder := fmt.Sprintf("## <REPLACE-Revision|Retry> %s\n\n<REPLACE-PROMPT>", letter)
	replacement := fmt.Sprintf("%s\n\n%s", header, content)
	newContent := strings.Replace(string(data), placeholder, replacement, 1)
	if err := os.WriteFile(sfPath, []byte(newContent), 0644); err != nil {
		return err
	}
	return appendToFile(sfPath, "\n!HANDOFF!\n")
}

// ---- condoc lock file (see condocs/initialDistributedDevelopmentImpls/Step5Prompt.md) ----
//
// condoccer maintains a '.condoc' file at the repo root while any condoc is
// mid-transition. local-representative treats its mere presence as a signal
// to always report "rebuild available" as false (see repowatch.go's
// rebuildReadyLocked), so a dev-repo rebuild never lands out from under an
// in-flight condoc handoff. The point is to prevent excessive rebuilds, not
// to track exact repo state -- so the file is a dumb presence/absence lock,
// not something condoccer ever reads back.
//
// The lock lives at the root of the git repo that owns the condoc in
// question (see condocRepoRoot), not at condoccer's scan root -- that is the
// one place local-representative and federation-command look for it, and it
// lets condocs in different repos each hold their own lock: one active
// condoc per repo, rather than one shared toggle across every repo under the
// scan root. Two condocs active in the *same* repo still share that repo's
// lock.

// condocLockPath is the lock file's well-known location within repoRoot.
func condocLockPath(repoRoot string) string {
	return filepath.Join(repoRoot, ".condoc")
}

// writeCondocLock creates or overwrites repoRoot's lock file to record the
// most recent condoc state transition.
func (s *Server) writeCondocLock(repoRoot, action string) {
	now := time.Now()
	content := fmt.Sprintf("Condoccer %s at %s (%d)\n", action, now.Format(time.RFC1123), now.Unix())
	if err := os.WriteFile(condocLockPath(repoRoot), []byte(content), 0644); err != nil {
		log.Printf("condoc lock: write failed: %v", err)
		return
	}
	s.commitCondocLock(repoRoot, "condoc: lock - "+action)
}

// removeCondocLock deletes repoRoot's lock file, if present.
func (s *Server) removeCondocLock(repoRoot string) {
	if err := os.Remove(condocLockPath(repoRoot)); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("condoc lock: remove failed: %v", err)
		}
		return
	}
	s.commitCondocLock(repoRoot, "condoc: lock released")
}

// commitCondocLock stages and commits only the '.condoc' lock file itself,
// right after writeCondocLock/removeCondocLock change it on disk. '.condoc'
// is tracked in git (unlike '.building' -- see the root .gitignore) because
// other nodes following this branch rely on its presence/absence to gate
// their own rebuilds, so it can't just be gitignored away.
//
// condoccer writes/removes that file directly from its own watch loop,
// outside of whatever commits the rest of a condoc's prompt/reply content,
// so without this the lock file's change (especially its removal once an
// agent finishes a revision) sits as an uncommitted, untracked-by-anyone
// working-tree change until someone notices the dirty repo and cleans it up
// by hand (see the "fix condoc rails" commits). Committing it immediately,
// scoped to just this one file, keeps the tree clean without touching
// whatever else may be mid-edit at the same time.
func (s *Server) commitCondocLock(repoRoot, message string) {
	if err := exec.Command("git", "-C", repoRoot, "add", "--", ".condoc").Run(); err != nil {
		log.Printf("condoc lock: git add failed: %v", err)
		return
	}
	if exec.Command("git", "-C", repoRoot, "diff", "--cached", "--quiet", "--", ".condoc").Run() == nil {
		return // nothing staged -- not a git repo, or no-op write of identical content
	}
	if err := exec.Command("git", "-C", repoRoot, "commit", "-m", message, "--", ".condoc").Run(); err != nil {
		log.Printf("condoc lock: git commit failed: %v", err)
	}
}

// updateCondocLock reacts to one condoc's phase (possibly) having changed
// since the last poll, creating/updating/removing the lock file in that
// condoc's own repo (see condocRepoRoot) per Step5Prompt.md:
//   - a condoc seen for the first time -- condoccer "begins working" on it --
//     creates the lock
//   - reaching "awaiting action" (an agent just completed its work) or
//     "completed" removes the lock -- both are safe points for LR to rebuild
//   - every other transition (re-)creates the lock, so it stays present for
//     the duration of anything else in flight (e.g. an agent about to run,
//     or having just been handed off to)
//
// One exception, per
// condocs/initialShellsSessionManagerAndTheConversationalistImpls/Step1Prompt.md
// Revision C: reaching "awaiting action" because a substep just completed
// (info.JustResumedFromSubstep) is bookkeeping only, not "an agent completing
// work" -- no fresh code landed in that commit, and the step it returns to
// is almost always about to receive more work. That case keeps the lock in
// place (treated like any other in-flight transition) instead of unlocking,
// so LR doesn't consider a rebuild "ready" until the step reaches a real
// safe point of its own.
func (s *Server) updateCondocLock(info CondocInfo, prev Phase, existed bool) {
	safeToRebuild := (info.Phase == PhaseAwaitingAction && !info.JustResumedFromSubstep) || info.Phase == PhaseCompleted
	switch {
	case !existed:
		if safeToRebuild {
			// Already sitting at a safe-to-rebuild point the first time we
			// see it -- e.g. condoccer just (re)started mid-condoc. Nothing
			// to lock until it actually transitions.
			return
		}
		s.writeCondocLock(s.condocRepoRoot(info.Path), fmt.Sprintf("began work on %s", info.Name))
	case prev == info.Phase:
		// no transition
	case safeToRebuild:
		s.removeCondocLock(s.condocRepoRoot(info.Path))
	default:
		s.writeCondocLock(s.condocRepoRoot(info.Path), fmt.Sprintf("advanced %s to %s", info.Name, info.Phase))
	}
}

// watchLoop polls condoc files every second and pushes updates to subscribed clients.
func (s *Server) watchLoop() {
	var lastList []CondocInfo
	lastContent := make(map[string]string) // relPath → last known content fingerprint
	lastPhase := make(map[string]Phase)    // relPath → last known phase, for lock-file transitions

	lastRoot := s.getRoot()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for range ticker.C {
		root := s.getRoot()
		if root != lastRoot {
			// Working dir changed (see setRoot): every tracked path was
			// relative to the old root, so start over as on a fresh start --
			// condocs already sitting at a safe point won't lock, anything
			// mid-flight will (re)assert its own repo's lock.
			lastRoot = root
			lastList = nil
			lastContent = make(map[string]string)
			lastPhase = make(map[string]Phase)
		}

		current, _ := findCondocs(root)
		if s.getRoot() != root {
			continue // changed again mid-scan; pick it up next tick
		}

		for _, info := range current {
			prev, existed := lastPhase[info.Path]
			s.updateCondocLock(info, prev, existed)
			lastPhase[info.Path] = info.Phase
		}

		if !condocListEqual(lastList, current) {
			lastList = current
			s.broadcastList()
			// Mirror the change up to local-representative (and, through it, to
			// agent-coordinator) so the forwarded view stays in sync.
			s.pushCondoccerState()
		}

		// Collect which condocs have subscribers.
		s.mu.RLock()
		subscribed := make(map[string]bool)
		for c := range s.clients {
			if c.subscribed != "" {
				subscribed[c.subscribed] = true
			}
		}
		s.mu.RUnlock()

		for relPath := range subscribed {
			absPath := filepath.Join(root, relPath)
			info, err := detectPhase(root, absPath)
			if err != nil {
				continue
			}

			// Fingerprint the most-active file: substep if present, else step, else main.
			watchFile := absPath
			if info.SubstepFile != "" {
				watchFile = filepath.Join(root, info.SubstepFile)
			} else if info.StepFile != "" {
				watchFile = filepath.Join(root, info.StepFile)
			}

			b, err := os.ReadFile(watchFile)
			if err != nil {
				continue
			}
			content := string(b)
			if lastContent[relPath] != content {
				lastContent[relPath] = content
				s.broadcastCondocUpdate(relPath)
			}
		}
	}
}

func condocListEqual(a, b []CondocInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].Phase != b[i].Phase || a[i].StepNum != b[i].StepNum {
			return false
		}
	}
	return true
}

// ---- HTTP ----

func (s *Server) setupRoutes(devMode bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/resource/", s.handleResourceFile)
	mux.HandleFunc("/api/upload-resource", s.handleUploadResource)

	if devMode {
		// In dev mode, don't serve static files — Vite dev server handles the frontend.
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "dev mode: serve frontend via 'make dev-frontend'", http.StatusServiceUnavailable)
		})
		return mux
	}

	distFS, err := fs.Sub(embeddedFrontend, "frontend/dist")
	if err != nil {
		log.Fatal("embed sub FS:", err)
	}
	fileServer := http.FileServer(http.FS(distFS))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		// Check if the file exists in the embedded FS.
		if _, err := fs.Stat(distFS, path); err == nil && path != "index.html" {
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback to index.html.
		idx, err := fs.ReadFile(distFS, "index.html")
		if err != nil {
			http.Error(w, "frontend not built — run 'make build'", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(idx)
	})

	return mux
}

// watchRestartSignal blocks waiting for SIGHUP and, on receipt, announces a
// restart (see ufa-loader/README.md and docs/DevMode.md's "Loader" section)
// as this process's final act before exiting 0. The signal is sent directly
// to this process's own pid (e.g. `kill -HUP <pid>`), not through
// ufa-loader itself. Run in its own goroutine; never returns.
func watchRestartSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	for range sigCh {
		log.Printf("received SIGHUP: announcing a restart and exiting")
		if err := restartsignal.Announce(os.Stdout, "condoccer", "sighup"); err != nil {
			log.Printf("restartsignal.Announce: %v", err)
		}
		os.Exit(0)
	}
}

func main() {
	if ufaversion.HandleVersionFlag() {
		return
	}

	flag.Bool("version", false, "print version and exit (checked ahead of every other flag; see the HandleVersionFlag call above)")
	port := flag.String("port", "8080", "HTTP port to listen on")
	root := flag.String("root", ".", "initial working dir to scan for condocs (changeable from the UI); each condoc's lock and git work use its own repo root")
	dev := flag.Bool("dev", false, "dev mode: skip serving frontend static files")
	devMode := flag.Bool("dev-mode", false, "dev mode (SDLC sense, see docs/DevMode.md): this condoccer is running from an in-progress branch. Unrelated to --dev.")
	name := flag.String("name", "condoccer", "identifier reported to local-representative")
	autoConnect := flag.Bool("auto-connect", false, "dial local-representative in the background on startup, retrying every 10s for up to 10m")
	lrHost := flag.String("lr-host", "localhost", "local-representative host/IP for --auto-connect")
	lrPort := flag.String("lr-port", "8082", "local-representative representable port for --auto-connect")
	flag.Parse()

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		log.Fatal("invalid root:", err)
	}

	s := newServer(absRoot)
	s.httpPort = *port
	s.name = *name
	s.devMode = *devMode
	go s.watchLoop()
	go watchRestartSignal()

	if *autoConnect {
		log.Printf("auto-connect enabled: dialing local-representative at %s:%s every %s for up to %s (runs in background)",
			*lrHost, *lrPort, autoConnectInterval, autoConnectWindow)
		s.setAutoConnect(true, *lrHost, *lrPort)
	} else {
		// No --auto-connect: still record the configured target as the manual
		// widget's default so a "Connect" click dials the same place
		// --auto-connect would have.
		s.reprMu.Lock()
		s.reprHost, s.reprPort = *lrHost, *lrPort
		s.reprMu.Unlock()
	}

	addr := ":" + *port
	log.Printf("condoccer listening on http://localhost%s (root: %s)", addr, absRoot)
	if *dev {
		log.Printf("dev mode: connect frontend to ws://localhost%s/ws", addr)
	}

	if err := http.ListenAndServe(addr, s.setupRoutes(*dev)); err != nil {
		log.Fatal(err)
	}
}
