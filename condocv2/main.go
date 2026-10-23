package main

// condocv2 -- condoccer's successor. Slopspace condocs only, explicitly
// incompatible with condoccer's (v1) condocs: its own pointers, condoc files
// and branches, each filtered out of the other (see spec.go). It keeps v1's
// outer sequence -- propose a condoc in the pointer repo, accept it to begin
// it -- and then runs the condoc as one linear step of locked interactions
// (see slopspace.go), shown both as a text flow and as swimlanes of the
// commits each interaction makes (see timeline.go).

import (
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"representable"
	"ufa-loader/restartsignal"
	ufaversion "ufa-version"
)

//go:embed frontend/dist
var embeddedFrontend embed.FS

// wsMsg is the wire format for WebSocket messages.
type wsMsg struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type wsClient struct {
	conn       *websocket.Conn
	subscribed string // pointer stem of the condoc being watched
	send       chan []byte
	done       chan struct{}
}

// Server manages WebSocket clients and the condocs this condocv2 holds.
type Server struct {
	httpPort string // HTTP port this condocv2 serves on (reported to local-representative)
	name     string // identifier reported to local-representative
	devMode  bool   // --dev-mode -- see docs/DevMode.md
	upgrader websocket.Upgrader
	mu       sync.RWMutex
	clients  map[*wsClient]bool

	// representable link to local-representative (see repr.go). nil until connected.
	reprMu     sync.Mutex
	reprClient *representable.Client
	reprStatus string        // "disconnected" | "connecting" | "connected"
	reprHost   string        // host of the current/last connect attempt (widget default)
	reprPort   string        // port of the current/last connect attempt (widget default)
	reprStop   chan struct{} // non-nil while a connectLoop is running; closing it stops retries
	// reprAutoConnect is the persistent auto-connect toggle: it stays true
	// across a successful connection, so a later unintentional disconnect
	// resumes the retry cycle on its own (see connectLoop); only an explicit
	// disconnect (see disconnectRepr) turns it off.
	reprAutoConnect  bool
	modeMismatch     bool   // true while local-representative discloses a dev/ops mode mismatch -- see docs/DevMode.md
	modeMismatchPeer string // the mismatched LR's disclosed mode ("dev" or "ops")

	// tcMu/tcAvailable hold the aggregate "is a the-conversationalist
	// instance available on any host" verdict relayed down from
	// local-representative -- see tcavailability.go.
	tcMu        sync.RWMutex
	tcAvailable bool

	// pointer is the pointer repo -- see pointer.go.
	pointer pointerState

	// slop is the handler's state -- see slopspace.go.
	slop slopState
}

func newServer() *Server {
	return &Server{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:    make(map[*wsClient]bool),
		reprStatus: "disconnected",
	}
}

// findGitRoot walks up from dir to the nearest directory containing a .git
// entry (a directory for an ordinary clone, a file for a worktree). Returns
// "" when there is none.
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

func (s *Server) marshalMsg(typ string, payload interface{}) []byte {
	p, _ := json.Marshal(payload)
	b, _ := json.Marshal(wsMsg{Type: typ, Payload: p})
	return b
}

func (s *Server) sendToClient(c *wsClient, typ string, payload interface{}) {
	select {
	case c.send <- s.marshalMsg(typ, payload):
	default:
	}
}

// replyErr reports err, if any, to the client whose request caused it.
func (s *Server) replyErr(c *wsClient, err error) {
	if err != nil {
		s.sendToClient(c, "error", map[string]string{"message": err.Error()})
	}
}

func (s *Server) sendCondocState(c *wsClient, stem string) {
	state, err := s.condocState(stem)
	if err != nil {
		s.replyErr(c, err)
		return
	}
	s.sendToClient(c, "condoc", state)
}

func (s *Server) broadcastCondocState(stem string, state CondocState) {
	msg := s.marshalMsg("condoc", state)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		if c.subscribed == stem {
			select {
			case c.send <- msg:
			default:
			}
		}
	}
}

func (s *Server) broadcastCondocUpdate(stem string) {
	if state, err := s.condocState(stem); err == nil {
		s.broadcastCondocState(stem, state)
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

	go s.sendPointer(c)
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
	case "subscribe":
		var p struct {
			Stem string `json:"stem"`
		}
		json.Unmarshal(m.Payload, &p)
		s.mu.Lock()
		c.subscribed = p.Stem
		s.mu.Unlock()
		if p.Stem != "" {
			s.sendCondocState(c, p.Stem)
		}

	case "connect":
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

	case "set-pointer":
		var p PointerConfig
		json.Unmarshal(m.Payload, &p)
		s.replyErr(c, s.setPointer(p))

	case "clone-pointer":
		s.replyErr(c, s.clonePointer())

	case "propose":
		var p SlopspaceProposal
		json.Unmarshal(m.Payload, &p)
		s.replyErr(c, s.slopPropose(p))

	case "check-branch":
		var p struct {
			Repos  []string `json:"repos"`
			Branch string   `json:"branch"`
		}
		json.Unmarshal(m.Payload, &p)
		var repos []string
		for _, r := range p.Repos {
			if r = normalizeRepo(r); r != "" {
				repos = append(repos, r)
			}
		}
		s.sendToClient(c, "branch-check", map[string]interface{}{
			"branch": strings.TrimSpace(p.Branch),
			"checks": slopCheckBranch(repos, strings.TrimSpace(p.Branch)),
		})

	// Everything else is keyed on the condoc's pointer stem; a failure is
	// reported back to the client that asked.
	case "withdraw", "accept", "resubmit", "prompt", "submit", "complete", "redraft", "raise-prs", "merge":
		var p struct {
			Stem       string `json:"stem"`
			Text       string `json:"text"`
			AutoSubmit bool   `json:"autoSubmit"`
			Template   string `json:"template"`
		}
		json.Unmarshal(m.Payload, &p)
		var err error
		switch m.Type {
		case "withdraw":
			err = s.slopWithdraw(p.Stem)
		case "accept":
			err = s.slopAccept(p.Stem)
		case "resubmit":
			err = s.slopResubmit(p.Stem)
		case "prompt":
			err = s.slopPrompt(p.Stem, p.Text, p.AutoSubmit)
		case "submit":
			err = s.slopSubmit(p.Stem)
		case "complete":
			err = s.slopComplete(p.Stem, p.Text)
		case "redraft":
			err = s.slopRedraft(p.Stem, p.Text, p.Template)
		case "raise-prs":
			err = s.slopRaisePRs(p.Stem, p.Template)
		case "merge":
			err = s.slopMerge(p.Stem)
		}
		s.replyErr(c, err)
		if m.Type != "withdraw" {
			s.broadcastCondocUpdate(p.Stem)
		}
	}
}

// watchLoop polls the subscribed condocs and pushes a fresh state to their
// subscribers whenever it changes -- the pointer, the files, or the commits
// the timeline reads.
func (s *Server) watchLoop() {
	last := make(map[string]string) // stem -> last state sent
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.RLock()
		subscribed := make(map[string]bool)
		for c := range s.clients {
			if c.subscribed != "" {
				subscribed[c.subscribed] = true
			}
		}
		s.mu.RUnlock()

		for stem := range last {
			if !subscribed[stem] {
				delete(last, stem)
			}
		}
		for stem := range subscribed {
			state, err := s.condocState(stem)
			if err != nil {
				continue
			}
			b, _ := json.Marshal(state)
			if last[stem] != string(b) {
				last[stem] = string(b)
				s.broadcastCondocState(stem, state)
			}
		}
	}
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
// as this process's final act before exiting 0. Run in its own goroutine;
// never returns.
func watchRestartSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	for range sigCh {
		log.Printf("received SIGHUP: announcing a restart and exiting")
		if err := restartsignal.Announce(os.Stdout, "condocv2", "sighup"); err != nil {
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
	port := flag.String("port", "8088", "HTTP port to listen on")
	root := flag.String("root", ".", "where condocv2 was started from: a relative --pointer-path resolves against the git repo holding it")
	dev := flag.Bool("dev", false, "dev mode: skip serving frontend static files")
	devMode := flag.Bool("dev-mode", false, "dev mode (SDLC sense, see docs/DevMode.md): this condocv2 is running from an in-progress branch. Unrelated to --dev.")
	name := flag.String("name", "condocv2", "identifier reported to local-representative")
	autoConnect := flag.Bool("auto-connect", false, "dial local-representative in the background on startup, retrying every 10s for up to 10m")
	lrHost := flag.String("lr-host", "localhost", "local-representative host/IP for --auto-connect")
	lrPort := flag.String("lr-port", "8082", "local-representative representable port for --auto-connect")
	pointerRepo := flag.String("pointer-repo", defaultPointerRepo, "pointer repo (owner/name, cloned over HTTPS with TF_VAR_github_pat); changeable from the UI")
	pointerBranch := flag.String("pointer-branch", defaultPointerBranch, "branch of the pointer repo condocv2 pulls and pushes")
	pointerPath := flag.String("pointer-path", defaultPointerPath, "where the pointer repo is checked out; relative paths resolve against the git repo holding --root")
	slopAgent := flag.String("slop-agent", "claude", "agent new condoc proposals default to")
	slopModel := flag.String("slop-model", "", "model new condoc proposals default to (blank: the agent's default)")
	flag.Parse()

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		log.Fatal("invalid root:", err)
	}

	s := newServer()
	s.httpPort = *port
	s.name = *name
	s.devMode = *devMode

	pointerCfg, err := validatePointerConfig(PointerConfig{Repo: *pointerRepo, Branch: *pointerBranch, Path: *pointerPath})
	if err != nil {
		log.Fatal("invalid pointer repo: ", err)
	}
	pointerBase := findGitRoot(absRoot)
	if pointerBase == "" {
		pointerBase = absRoot
	}
	s.initPointer(pointerCfg, pointerBase)
	s.initSlopspace()
	s.slop.agent, s.slop.model = strings.TrimSpace(*slopAgent), strings.TrimSpace(*slopModel)

	go s.watchLoop()
	go s.pointerLoop()
	go s.slopLoop()
	go s.slopTurnLoop()
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
	log.Printf("condocv2 listening on http://localhost%s (pointer base: %s)", addr, pointerBase)
	if *dev {
		log.Printf("dev mode: connect frontend to ws://localhost%s/ws", addr)
	}

	if err := http.ListenAndServe(addr, s.setupRoutes(*dev)); err != nil {
		log.Fatal(err)
	}
}
