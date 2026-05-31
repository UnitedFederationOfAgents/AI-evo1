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
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
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
	Path     string `json:"path"`
	Name     string `json:"name"`
	Phase    Phase  `json:"phase"`
	StepNum  int    `json:"stepNum"`
	StepFile string `json:"stepFile,omitempty"`
}

// CondocState is the full detail for the selected condoc.
type CondocState struct {
	Info        CondocInfo    `json:"info"`
	MainContent string        `json:"mainContent"`
	StepContent string        `json:"stepContent,omitempty"`
	NextLetter  string        `json:"nextLetter"`
	FromOptions []string      `json:"fromOptions"`
	Meta        CondocMeta    `json:"meta"`
	Description string        `json:"description"`
	Steps       []StepSummary `json:"steps"`
	Iterations  []Iteration   `json:"iterations"`
}

// wsMsg is the wire format for WebSocket messages.
type wsMsg struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// ActionRequest is sent by the client when the user clicks an action button.
type ActionRequest struct {
	Action  string `json:"action"`  // handoff, completed, revision, retry, start_step
	Path    string `json:"path"`    // condoc path (relative to repo root)
	Content string `json:"content,omitempty"`
	Letter  string `json:"letter,omitempty"`
	From    string `json:"from,omitempty"`
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

// Iteration is a parsed section header from the condoc step file.
type Iteration struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"` // "reply", "revision", "retry"
	From  string `json:"from,omitempty"`
}

var (
	condocYamlRe     = regexp.MustCompile(`condoc-yaml`)
	completedRe      = regexp.MustCompile(`(?m)^## Condoc Completed\s*$`)
	humanPromptRe    = regexp.MustCompile(`(?m)^## Human-Prompt\s*$`)
	replaceTitleRe   = regexp.MustCompile(`(?m)^### Step \d+ - <REPLACE-TITLE>`)
	stepNumRe        = regexp.MustCompile(`(?m)^### Step (\d+)`)
	replyLetterRe    = regexp.MustCompile(`(?m)^## Reply ([A-Z])\s*$`)
	condocYamlBlockRe    = regexp.MustCompile("(?s)```condoc-yaml\n(.*?)```")
	htmlCommentRe        = regexp.MustCompile(`(?s)<!--.*?-->`)
	stepHeadingRe        = regexp.MustCompile(`(?m)^### Step (\d+) - (.+)$`)
	promptBlockRe        = regexp.MustCompile("(?s)```prompt\n(.*?)```")
	anyH23Re             = regexp.MustCompile(`(?m)^#{2,}`)
	anyH2Re              = regexp.MustCompile(`(?m)^## `)
	iterHeadingRe        = regexp.MustCompile(`(?m)^## (Reply|Revision|Retry)(?: ([A-Z]))?(?: \(from (\w+)\))?`)
	handoffDirectiveRe   = regexp.MustCompile(`(?m)^!HANDOFF!\s*$`)
	completedDirectiveRe = regexp.MustCompile(`(?m)^!COMPLETED!\s*$`)
)

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
		if handoffDirectiveRe.Match(sfContent) || completedDirectiveRe.Match(sfContent) {
			return CondocInfo{Path: relPath, Name: name, Phase: PhaseAgentRunning, StepNum: stepNum, StepFile: sfRel}, nil
		}
		return CondocInfo{Path: relPath, Name: name, Phase: PhaseAwaitingAction, StepNum: stepNum, StepFile: sfRel}, nil
	}
	return CondocInfo{Path: relPath, Name: name, Phase: PhaseAgentRunning, StepNum: stepNum, StepFile: sfRel}, nil
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

// parseIterations extracts the ordered list of reply/revision/retry sections from a step file.
func parseIterations(stepContent string) []Iteration {
	allIdx := iterHeadingRe.FindAllStringSubmatchIndex(stepContent, -1)
	var iters []Iteration
	for _, idx := range allIdx {
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
		iters = append(iters, iter)
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

	mainStr := string(mainContent)
	stepStr := string(stepContent)
	letter := nextRevLetter(stepStr)
	opts := fromOptions(letter)

	return CondocState{
		Info:        info,
		MainContent: mainStr,
		StepContent: stepStr,
		NextLetter:  letter,
		FromOptions: opts,
		Meta:        parseCondocMeta(mainStr),
		Description: parseDescription(mainStr),
		Steps:       parseSteps(mainStr),
		Iterations:  parseIterations(stepStr),
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
	root     string
	upgrader websocket.Upgrader
	mu       sync.RWMutex
	clients  map[*wsClient]bool
}

func newServer(root string) *Server {
	return &Server{
		root: root,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients: make(map[*wsClient]bool),
	}
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
	infos, _ := findCondocs(s.root)
	s.sendToClient(c, "list", map[string]interface{}{"condocs": infos})
}

func (s *Server) sendCondocState(c *wsClient, relPath string) {
	absPath := filepath.Join(s.root, relPath)
	state, err := getCondocState(s.root, absPath)
	if err != nil {
		s.sendToClient(c, "error", map[string]string{"message": err.Error()})
		return
	}
	s.sendToClient(c, "condoc", state)
}

func (s *Server) broadcastList() {
	infos, _ := findCondocs(s.root)
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
	absPath := filepath.Join(s.root, relPath)
	state, err := getCondocState(s.root, absPath)
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

	// Send initial condoc list.
	go s.sendList(c)

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
	}
}

func (s *Server) performAction(action ActionRequest) error {
	absPath := filepath.Join(s.root, action.Path)
	info, err := detectPhase(s.root, absPath)
	if err != nil {
		return err
	}

	// targetFile is where we write directives; defaults to main condoc file.
	targetFile := absPath
	if info.StepFile != "" && (info.Phase == PhaseAwaitingAction) {
		targetFile = filepath.Join(s.root, info.StepFile)
	}

	switch action.Action {
	case "handoff":
		return appendToFile(targetFile, "\n!HANDOFF!\n")

	case "completed":
		return appendToFile(targetFile, "\n!COMPLETED!\n")

	case "start_step":
		// Write edited main file content (with filled template), then trigger HANDOFF.
		if err := os.WriteFile(absPath, []byte(action.Content), 0644); err != nil {
			return err
		}
		return appendToFile(absPath, "\n!HANDOFF!\n")

	case "revision":
		if info.StepFile == "" {
			return fmt.Errorf("no active step file")
		}
		sfPath := filepath.Join(s.root, info.StepFile)
		header := fmt.Sprintf("## Revision %s", action.Letter)
		return replaceIterationPlaceholder(sfPath, action.Letter, header, action.Content)

	case "retry":
		if info.StepFile == "" {
			return fmt.Errorf("no active step file")
		}
		sfPath := filepath.Join(s.root, info.StepFile)
		header := fmt.Sprintf("## Retry %s", action.Letter)
		if action.From != "" {
			header = fmt.Sprintf("## Retry %s (from %s)", action.Letter, action.From)
		}
		return replaceIterationPlaceholder(sfPath, action.Letter, header, action.Content)

	default:
		return fmt.Errorf("unknown action: %s", action.Action)
	}
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

// watchLoop polls condoc files every second and pushes updates to subscribed clients.
func (s *Server) watchLoop() {
	var lastList []CondocInfo
	lastContent := make(map[string]string) // relPath → last known content fingerprint

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for range ticker.C {
		current, _ := findCondocs(s.root)
		if !condocListEqual(lastList, current) {
			lastList = current
			s.broadcastList()
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
			absPath := filepath.Join(s.root, relPath)
			info, err := detectPhase(s.root, absPath)
			if err != nil {
				continue
			}

			// Fingerprint the relevant file (step file if active, else main file).
			watchFile := absPath
			if info.StepFile != "" {
				watchFile = filepath.Join(s.root, info.StepFile)
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

func main() {
	port := flag.String("port", "8080", "HTTP port to listen on")
	root := flag.String("root", ".", "repository root to scan for condocs")
	dev := flag.Bool("dev", false, "dev mode: skip serving frontend static files")
	flag.Parse()

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		log.Fatal("invalid root:", err)
	}

	s := newServer(absRoot)
	go s.watchLoop()

	addr := ":" + *port
	log.Printf("condoccer listening on http://localhost%s (root: %s)", addr, absRoot)
	if *dev {
		log.Printf("dev mode: connect frontend to ws://localhost%s/ws", addr)
	}

	if err := http.ListenAndServe(addr, s.setupRoutes(*dev)); err != nil {
		log.Fatal(err)
	}
}
