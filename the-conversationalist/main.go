package main

import (
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
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

	// transcribe holds this browser tab's in-progress AWS Transcribe
	// streaming session, if any -- see transcribe.go. nil while idle.
	transcribeMu sync.Mutex
	transcribe   *transcribeSession
}

// Server manages WebSocket clients and this the-conversationalist's
// representable link to local-representative. This is a minimal application
// shell (see condocs/InitialShellsSessionManagerAndTheConversationalist.md)
// -- domain functionality for conversing lands in a later step.
type Server struct {
	httpPort string // HTTP port this instance serves on (reported to local-representative)
	name     string // identifier reported to local-representative -- "convo" by default
	devMode  bool   // --dev-mode: this instance -- see docs/DevMode.md
	upgrader websocket.Upgrader
	mu       sync.RWMutex
	clients  map[*wsClient]bool

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

	// awsRegion is the AWS region AWS Transcribe streaming sessions are
	// started in (see transcribe.go). Empty defers to the AWS SDK's own
	// default region resolution (env/shared config/instance role) -- see
	// Revision D's note that we lean on host-level AWS credentials/config
	// rather than anything app-managed.
	awsRegion string
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

	// Send initial representable connection status and self-info.
	go s.sendReprStatus(c)
	go s.sendToClient(c, "self-info", SelfInfoMsg{DevMode: s.devMode, Version: ufaversion.Version})
	go s.sendModeMismatch(c)

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
		// A dropped tab shouldn't leave AWS Transcribe billing for a
		// session nobody's listening to anymore.
		s.stopTranscription(c)
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

	// Transcription: see transcribe.go. Mirrors agent-scribe's
	// startTranscription/audioData/stopTranscription/saveTranscription
	// socket.io events (ignored-scratch/AI-sandboxing/agent-scribe), but
	// over this app's own typed WebSocket protocol and writing into
	// local-representative's files area instead of a HEURISTIC.md intake.
	case "start-transcription":
		s.startTranscription(c)

	case "audio-chunk":
		var p struct {
			Data string `json:"data"` // base64-encoded 16-bit PCM audio chunk
		}
		json.Unmarshal(m.Payload, &p)
		s.sendAudioChunk(c, p.Data)

	case "stop-transcription":
		s.stopTranscription(c)

	case "save-transcript":
		s.saveTranscript(c)
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
		if err := restartsignal.Announce(os.Stdout, "the-conversationalist", "sighup"); err != nil {
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
	port := flag.String("port", "8086", "HTTP port to listen on")
	dev := flag.Bool("dev", false, "dev mode: skip serving frontend static files")
	devMode := flag.Bool("dev-mode", false, "dev mode (SDLC sense, see docs/DevMode.md): this instance is running from an in-progress branch. Unrelated to --dev.")
	name := flag.String("name", "convo", "identifier reported to local-representative")
	autoConnect := flag.Bool("auto-connect", false, "dial local-representative in the background on startup, retrying every 10s for up to 10m")
	lrHost := flag.String("lr-host", "localhost", "local-representative host/IP for --auto-connect")
	lrPort := flag.String("lr-port", "8082", "local-representative representable port for --auto-connect")
	awsRegion := flag.String("aws-region", "", "AWS region for Transcribe streaming (default: the AWS SDK's own region resolution -- env/shared config/instance role)")
	flag.Parse()

	s := newServer()
	s.httpPort = *port
	s.name = *name
	s.devMode = *devMode
	s.awsRegion = *awsRegion
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
	log.Printf("the-conversationalist listening on http://localhost%s", addr)
	if *dev {
		log.Printf("dev mode: connect frontend to ws://localhost%s/ws", addr)
	}

	if err := http.ListenAndServe(addr, s.setupRoutes(*dev)); err != nil {
		log.Fatal(err)
	}
}
