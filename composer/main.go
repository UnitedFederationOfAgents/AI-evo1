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
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"

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

// ---- WebSocket server ----

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
}

// Server manages WebSocket clients, the saved compositions (see
// compositions.go) and this composer's representable link to
// local-representative, whose files area holds composer's sources and
// receives its renders (see sources.go).
type Server struct {
	httpPort string // HTTP port this instance serves on (reported to local-representative)
	name     string // identifier reported to local-representative -- "compose" by default
	devMode  bool   // --dev-mode: this instance -- see docs/DevMode.md
	upgrader websocket.Upgrader
	mu       sync.RWMutex
	clients  map[*wsClient]bool

	store *store
	jobs  *jobQueue // render jobs -- see renderjobs.go

	ffmpegBin string // --ffmpeg: what renders jobs without a browser (ffmpeg.go)
	fontFile  string // --font-file: the font ffmpeg draws title cards in; "" finds one

	// lrBaseOverride replaces lrBase's answer -- tests only.
	lrBaseOverride string

	// representable link to local-representative (see repr.go). nil until connected.
	reprMu           sync.Mutex
	reprClient       *representable.Client
	reprStatus       string        // "disconnected" | "connecting" | "connected"
	reprHost         string        // host of the current/last connect attempt (widget default)
	reprPort         string        // port of the current/last connect attempt (widget default)
	reprStop         chan struct{} // non-nil while a connectLoop is running; closing it stops retries
	reprAutoConnect  bool          // persistent auto-connect toggle -- see repr.go's setAutoConnect
	modeMismatch     bool          // true while local-representative discloses a dev/ops mode mismatch -- see docs/DevMode.md
	modeMismatchPeer string        // the mismatched LR's disclosed mode ("dev" or "ops")
}

func newServer(dataDir string) *Server {
	s := &Server{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:    make(map[*wsClient]bool),
		reprStatus: "disconnected",
		store:      newStore(filepath.Join(dataDir, "compositions")),
		jobs:       newJobQueue(),
		ffmpegBin:  "ffmpeg",
	}
	s.jobs.changed = s.broadcastRenderJobs
	return s
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

// broadcast sends one message to every connected WebSocket client.
func (s *Server) broadcast(typ string, payload interface{}) {
	msg := s.marshalMsg(typ, payload)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// CompositionsMsg is the "compositions" WebSocket payload: the saved
// compositions plus the examples a new one can start from.
type CompositionsMsg struct {
	Compositions []CompositionSummary `json:"compositions"`
	Examples     []ExampleSummary     `json:"examples"`
	Error        string               `json:"error,omitempty"`
}

// ExampleSummary is one entry of CompositionsMsg.Examples.
type ExampleSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Server) compositionsMsg() CompositionsMsg {
	msg := CompositionsMsg{Examples: []ExampleSummary{}}
	for _, e := range examples {
		msg.Examples = append(msg.Examples, ExampleSummary{ID: e.ID, Name: e.Composition.Name, Description: e.Description})
	}
	list, err := s.store.list()
	if err != nil {
		msg.Error = err.Error()
		list = []CompositionSummary{}
	}
	msg.Compositions = list
	return msg
}

// broadcastCompositions sends the composition list to every client and the
// new count to local-representative.
func (s *Server) broadcastCompositions() {
	s.broadcast("compositions", s.compositionsMsg())
	s.pushComposeState()
}

// SavedMsg is the "saved" WebSocket payload answering save-composition,
// new-from-example, delete-composition and delete-all-compositions to the
// client that asked.
type SavedMsg struct {
	Action string       `json:"action"` // "save" | "example" | "delete" | "delete-all"
	ID     string       `json:"id,omitempty"`
	Count  int          `json:"count,omitempty"` // on "delete-all": how many were deleted
	Comp   *Composition `json:"composition,omitempty"`
	Error  string       `json:"error,omitempty"`
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

	// Send initial representable connection status, self-info and the
	// composition list; sources follow once LR has answered.
	go s.sendReprStatus(c)
	go s.sendToClient(c, "self-info", SelfInfoMsg{DevMode: s.devMode, Version: ufaversion.Version})
	go s.sendModeMismatch(c)
	go func() { s.sendToClient(c, "compositions", s.compositionsMsg()) }()
	go func() { s.sendToClient(c, "sources", s.sourcesMsg()) }()
	go s.sendToClient(c, "render-jobs", RenderJobsMsg{Waiting: s.jobs.waiting()})
	go s.sendToClient(c, "render-capability", s.renderCapability())

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
		// A render job this tab claimed but didn't finish goes back to
		// waiting, for another tab.
		s.jobs.release(c)
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
	// Each message runs in its own goroutine, where a panic would take the
	// whole process down. Report it to the client instead.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("handling %q: panic: %v\n%s", m.Type, r, debug.Stack())
			s.sendToClient(c, "error", fmt.Sprintf("%s: internal error: %v", m.Type, r))
		}
	}()
	switch m.Type {
	case "connect":
		// Manual connect: the widget lets an instance started without
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

	// Compositions -- see compositions.go.
	case "list-compositions":
		s.sendToClient(c, "compositions", s.compositionsMsg())

	case "get-composition":
		var p struct {
			ID string `json:"id"`
		}
		json.Unmarshal(m.Payload, &p)
		comp, err := s.store.get(strings.TrimSpace(p.ID))
		if err != nil {
			s.sendToClient(c, "error", err.Error())
			return
		}
		s.sendToClient(c, "composition", comp)

	case "save-composition":
		var comp Composition
		if err := json.Unmarshal(m.Payload, &comp); err != nil {
			s.sendToClient(c, "saved", SavedMsg{Action: "save", Error: "bad composition: " + err.Error()})
			return
		}
		saved, err := s.store.save(comp)
		if err != nil {
			s.sendToClient(c, "saved", SavedMsg{Action: "save", ID: comp.ID, Error: err.Error()})
			return
		}
		s.sendToClient(c, "saved", SavedMsg{Action: "save", ID: saved.ID, Comp: saved})
		s.broadcast("composition", saved)
		s.broadcastCompositions()

	case "new-from-example":
		var p struct {
			ID string `json:"id"`
		}
		json.Unmarshal(m.Payload, &p)
		saved, err := s.store.fromExample(strings.TrimSpace(p.ID))
		if err != nil {
			s.sendToClient(c, "saved", SavedMsg{Action: "example", Error: err.Error()})
			return
		}
		s.sendToClient(c, "saved", SavedMsg{Action: "example", ID: saved.ID, Comp: saved})
		s.broadcastCompositions()

	case "delete-composition":
		var p struct {
			ID string `json:"id"`
		}
		json.Unmarshal(m.Payload, &p)
		id := strings.TrimSpace(p.ID)
		if err := s.store.remove(id); err != nil {
			s.sendToClient(c, "saved", SavedMsg{Action: "delete", ID: id, Error: err.Error()})
			return
		}
		s.sendToClient(c, "saved", SavedMsg{Action: "delete", ID: id})
		s.broadcastCompositions()

	case "delete-all-compositions":
		n, err := s.store.removeAll()
		if err != nil {
			s.sendToClient(c, "saved", SavedMsg{Action: "delete-all", Count: n, Error: err.Error()})
		} else {
			s.sendToClient(c, "saved", SavedMsg{Action: "delete-all", Count: n})
		}
		s.broadcastCompositions()

	// Sources -- see sources.go.
	case "list-sources":
		s.sendToClient(c, "sources", s.sourcesMsg())

	// Whether this node has ffmpeg -- see ffmpeg.go.
	case "check-render-capability":
		s.sendToClient(c, "render-capability", s.renderCapability())

	// Render jobs -- see renderjobs.go. The tab's finished render arrives
	// over POST /api/render?job=<id>; a render that failed in the tab is
	// reported here instead.
	case "claim-render-job":
		var p struct {
			ID string `json:"id"`
		}
		json.Unmarshal(m.Payload, &p)
		s.claimRenderJob(c, strings.TrimSpace(p.ID))

	case "render-job-failed":
		var p struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		}
		json.Unmarshal(m.Payload, &p)
		if job, _, ok := s.jobs.snapshot(p.ID); ok && s.jobs.heldBy(p.ID, c) {
			s.jobs.fail(p.ID, job.Composition, "the compose tab couldn't render it: "+p.Error)
		}
	}
}

// ---- HTTP ----

func (s *Server) setupRoutes(devMode bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc(representable.UIManifestPath, representable.ServeUIManifest(s.uiManifest)) // see plugin.go
	mux.HandleFunc(representable.PluginIconPath, representable.ServeSVG(pluginIconSVG))
	mux.HandleFunc("/api/source/", s.handleSource)
	mux.HandleFunc("/api/render", s.handleRender)
	// The HTTP API -- see api.go.
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/compositions", s.handleCompositionsAPI)
	mux.HandleFunc("/api/compositions/", s.handleCompositionsAPI)
	mux.HandleFunc("/api/render-jobs", s.handleRenderJobs)
	mux.HandleFunc("/api/render-jobs/", s.handleRenderJobs)

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
// as this process's final act before exiting 0. Run in its own goroutine;
// never returns.
func watchRestartSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	for range sigCh {
		log.Printf("received SIGHUP: announcing a restart and exiting")
		if err := restartsignal.Announce(os.Stdout, "composer", "sighup"); err != nil {
			log.Printf("restartsignal.Announce: %v", err)
		}
		os.Exit(0)
	}
}

// defaultDataDir is ~/.ufa/composer, beside the other ~/.ufa state.
func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "ufa-composer")
	}
	return filepath.Join(home, ".ufa", "composer")
}

func main() {
	if ufaversion.HandleVersionFlag() {
		return
	}

	flag.Bool("version", false, "print version and exit (checked ahead of every other flag; see the HandleVersionFlag call above)")
	port := flag.String("port", "8089", "HTTP port to listen on")
	dev := flag.Bool("dev", false, "dev mode: skip serving frontend static files")
	devMode := flag.Bool("dev-mode", false, "dev mode (SDLC sense, see docs/DevMode.md): this instance is running from an in-progress branch. Unrelated to --dev.")
	name := flag.String("name", "compose", "identifier reported to local-representative")
	autoConnect := flag.Bool("auto-connect", false, "dial local-representative in the background on startup, retrying every 10s for up to 10m")
	lrHost := flag.String("lr-host", "localhost", "local-representative host/IP for --auto-connect")
	lrPort := flag.String("lr-port", "8082", "local-representative representable port for --auto-connect")
	dataDir := flag.String("data-dir", defaultDataDir(), "where compositions are kept (as compositions/<id>.json)")
	ffmpegBin := flag.String("ffmpeg", "ffmpeg", "the ffmpeg render jobs use when no browser renders them (ffprobe is looked for beside it)")
	fontFile := flag.String("font-file", "", "a .ttf/.otf font ffmpeg draws title cards in; empty finds a common one, else fontconfig's sans")
	flag.Parse()

	s := newServer(*dataDir)
	s.ffmpegBin, s.fontFile = *ffmpegBin, *fontFile
	s.httpPort = *port
	s.name = *name
	s.devMode = *devMode
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
	log.Printf("composer listening on http://localhost%s (compositions in %s)", addr, s.store.dir)
	if *dev {
		log.Printf("dev mode: connect frontend to ws://localhost%s/ws", addr)
	}

	if err := http.ListenAndServe(addr, s.setupRoutes(*dev)); err != nil {
		log.Fatal(err)
	}
}
