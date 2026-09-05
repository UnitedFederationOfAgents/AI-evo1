package main

import (
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"representable"
	ufaconfig "ufa-configurable"
)

//go:embed frontend/dist
var embeddedFrontend embed.FS

// Auto-connect (--auto-connect) tuning: on startup local-representative dials
// agent-coordinator in the background, retrying on an interval until the window
// elapses. Mirrors federation-command's --auto-connect.
const (
	autoConnectInterval = 10 * time.Second
	autoConnectWindow   = 10 * time.Minute

	defaultACHost = "localhost"
	defaultACPort = "8084"
)

// ServiceStatus is the health status of a monitored service.
type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// StatusMsg is the payload of "status" WebSocket messages.
type StatusMsg struct {
	Services []ServiceStatus `json:"services"`
}

// FCStateMsg is the payload of "fc-state" WebSocket messages.
type FCStateMsg struct {
	State string `json:"state"` // "remote-control", "local-control", or "" (disconnected)
}

// FCLogMsg is the payload of "fc-log" WebSocket messages.
type FCLogMsg struct {
	Line string `json:"line"`
	Kind string `json:"kind,omitempty"` // "cmd" or "output"
}

// RidealongStateMsg is the payload of "ridealong-state" WebSocket messages.
type RidealongStateMsg struct {
	Active       bool     `json:"active"`
	Title        string   `json:"title,omitempty"`
	CurrentIndex int      `json:"current_index,omitempty"`
	TotalSteps   int      `json:"total_steps,omitempty"`
	CurrentCmd   string   `json:"current_cmd,omitempty"`
	PrevCmd      string   `json:"prev_cmd,omitempty"`
	PrevExitCode int      `json:"prev_exit_code,omitempty"`
	NextCmd      string   `json:"next_cmd,omitempty"`
	Autoplay     bool     `json:"autoplay,omitempty"`
	Countdown    string   `json:"countdown,omitempty"`
	Waypoints    []string `json:"waypoints,omitempty"`
}

// CondocStateMsg is the payload of "condoc-state" WebSocket messages.
type CondocStateMsg struct {
	Active    bool   `json:"active"`
	Name      string `json:"name,omitempty"`
	Phase     string `json:"phase,omitempty"`
	StepNum   int    `json:"step_num,omitempty"`
	StatusMsg string `json:"status_msg,omitempty"`
}

// ACStateMsg is the payload of "ac-state" WebSocket messages.
type ACStateMsg struct {
	Connected bool   `json:"connected"`
	Host      string `json:"host,omitempty"`
	Port      string `json:"port,omitempty"`
	// Connecting is true while the background --auto-connect retry loop is still
	// attempting to reach agent-coordinator (visible indication in the UI).
	Connecting bool `json:"connecting,omitempty"`
}

// CondocInfo mirrors condoccer's per-condoc summary row (see condoccer/main.go).
type CondocInfo struct {
	Path          string `json:"path"`
	Name          string `json:"name"`
	Phase         string `json:"phase"`
	StepNum       int    `json:"stepNum"`
	StepFile      string `json:"stepFile,omitempty"`
	SubstepFile   string `json:"substepFile,omitempty"`
	SubstepLetter string `json:"substepLetter,omitempty"`
}

// CondoccerStateMsg is the "condoccer-state" payload: a managed condoccer pushes
// it to LR over representable, and LR forwards a copy up to agent-coordinator and
// out to browser clients so the forwarded condoccer view has a summary + the
// port to reverse-proxy from.
type CondoccerStateMsg struct {
	HTTPPort string       `json:"http_port"`
	Root     string       `json:"root"`
	Condocs  []CondocInfo `json:"condocs"`
}

// LRHTTPMsg tells agent-coordinator which HTTP port this LR's dashboard listens
// on, so AC can reverse-proxy the forwarded condoccer UI back through this LR.
type LRHTTPMsg struct {
	Port string `json:"port"`
}

// wsMsg is the wire format for all WebSocket messages.
type wsMsg struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
}

// Server manages WebSocket clients and broadcasts status updates.
type Server struct {
	upgrader   websocket.Upgrader
	mu         sync.RWMutex
	clients    map[*wsClient]bool
	reprServer *representable.Server
	lrName     string

	fcMu    sync.RWMutex
	fcState string // "remote-control", "local-control", or "" (disconnected)

	ridealongMu    sync.RWMutex
	ridealongState *RidealongStateMsg

	condocMu    sync.RWMutex
	condocState *CondocStateMsg

	acMu                sync.RWMutex
	acClient            *representable.Client
	acHost              string
	acPort              string
	acAutoConnecting    bool          // true while the startup --auto-connect retry loop is trying
	acAutoConnectCancel chan struct{} // closed to stop the auto-connect retry loop early

	// System tab: LR's own process plus any child applications it launches.
	heartbeatPort string                  // representable port, passed to launched children
	httpPort      string                  // LR's own dashboard HTTP port (reported to agent-coordinator)
	condoccerPort string                  // HTTP port a managed condoccer serves on / is proxied from
	condoccerRoot string                  // repo root a managed condoccer scans (empty: condoccer's default)
	selfStart     time.Time               // when this LR process started
	binOverrides  map[string]string       // app name -> explicit binary path (from config)
	terminalCmd   string                  // command prefix that hosts an interactive child in a terminal
	procMu        sync.Mutex
	managed       map[string]*managedProc // instance id -> running/finished child
	instanceSeq   map[string]int          // app name -> highest instance ordinal handed out

	// Latest condoc summary pushed up by a managed condoccer over representable.
	condoccerMu    sync.RWMutex
	condoccerState *CondoccerStateMsg
}

func newServer(lrName string) *Server {
	return &Server{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:      make(map[*wsClient]bool),
		lrName:       lrName,
		selfStart:    time.Now(),
		binOverrides: make(map[string]string),
		managed:      make(map[string]*managedProc),
		instanceSeq:  make(map[string]int),
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

func (s *Server) broadcast(typ string, payload interface{}) {
	msg := s.marshalMsg(typ, payload)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		case <-c.done:
		default:
		}
	}
}

// currentStatus returns service statuses; federation-command reflects live heartbeat health.
func (s *Server) currentStatus() StatusMsg {
	fcStatus := "unhealthy"
	if s.reprServer != nil && s.reprServer.IsHealthy("federation-command") {
		fcStatus = "healthy"
	}
	condoccerStatus := "unhealthy"
	if s.reprServer != nil && s.reprServer.IsHealthy("condoccer") {
		condoccerStatus = "healthy"
	}
	return StatusMsg{
		Services: []ServiceStatus{
			{Name: "federation-command", Status: fcStatus},
			{Name: "condoccer", Status: condoccerStatus},
			{Name: "worker", Status: "healthy"},
		},
	}
}

func (s *Server) getFCState() string {
	s.fcMu.RLock()
	defer s.fcMu.RUnlock()
	return s.fcState
}

func (s *Server) setFCState(state string) {
	s.fcMu.Lock()
	s.fcState = state
	s.fcMu.Unlock()
	s.broadcast("fc-state", FCStateMsg{State: state})
	s.acMu.RLock()
	ac := s.acClient
	s.acMu.RUnlock()
	if ac != nil {
		ac.SendData("fc-state", FCStateMsg{State: state})
	}
}

func (s *Server) getRidealongState() RidealongStateMsg {
	s.ridealongMu.RLock()
	defer s.ridealongMu.RUnlock()
	if s.ridealongState == nil {
		return RidealongStateMsg{Active: false}
	}
	return *s.ridealongState
}

func (s *Server) getCondocState() CondocStateMsg {
	s.condocMu.RLock()
	defer s.condocMu.RUnlock()
	if s.condocState == nil {
		return CondocStateMsg{Active: false}
	}
	return *s.condocState
}

func (s *Server) getCondoccerState() *CondoccerStateMsg {
	s.condoccerMu.RLock()
	defer s.condoccerMu.RUnlock()
	return s.condoccerState
}

func (s *Server) getACClient() *representable.Client {
	s.acMu.RLock()
	defer s.acMu.RUnlock()
	return s.acClient
}

func (s *Server) getACState() ACStateMsg {
	s.acMu.RLock()
	defer s.acMu.RUnlock()
	return ACStateMsg{
		Connected:  s.acClient != nil,
		Host:       s.acHost,
		Port:       s.acPort,
		Connecting: s.acAutoConnecting,
	}
}

// acStateMsg builds an ac-state payload for an explicit host/port, stamping the
// current auto-connect retry status so the UI can show a "connecting…" hint even
// before connectAC has recorded the target on the Server.
func (s *Server) acStateMsg(connected bool, host, port string) ACStateMsg {
	s.acMu.RLock()
	connecting := s.acAutoConnecting
	s.acMu.RUnlock()
	return ACStateMsg{Connected: connected, Host: host, Port: port, Connecting: connecting}
}

func (s *Server) setACAutoConnecting(v bool) {
	s.acMu.Lock()
	s.acAutoConnecting = v
	s.acMu.Unlock()
}

// pushStateToAC sends a full state snapshot to the agent-coordinator.
func (s *Server) pushStateToAC() {
	ac := s.getACClient()
	if ac == nil {
		return
	}
	ac.SendData("services", s.currentStatus())
	ac.SendData("fc-state", FCStateMsg{State: s.getFCState()})
	ac.SendData("ridealong-state", s.getRidealongState())
	ac.SendData("condoc-state", s.getCondocState())
	ac.SendData("system-state", s.systemState())
	ac.SendData("lr-http", LRHTTPMsg{Port: s.httpPort})
	if cc := s.getCondoccerState(); cc != nil {
		ac.SendData("condoccer-state", *cc)
	}
}

// connectAC dials agent-coordinator and maintains the connection lifecycle.
// Must be called in its own goroutine.
func (s *Server) connectAC(host, port string) {
	s.acMu.Lock()
	if s.acClient != nil {
		s.acClient.Close()
		s.acClient = nil
	}
	s.acHost = host
	s.acPort = port
	s.acMu.Unlock()

	addr := host + ":" + port
	log.Printf("connecting to agent-coordinator at %s as %q", addr, s.lrName)

	client, err := representable.Connect(addr, s.lrName, 5*time.Second)
	if err != nil {
		log.Printf("failed to connect to agent-coordinator: %v", err)
		s.acMu.RLock()
		stillPending := s.acHost == host && s.acPort == port && s.acClient == nil
		s.acMu.RUnlock()
		if stillPending {
			s.broadcast("ac-state", s.acStateMsg(false, host, port))
		}
		return
	}

	s.acMu.Lock()
	// If a newer connectAC call changed the target, abandon this connection.
	if s.acHost != host || s.acPort != port {
		s.acMu.Unlock()
		client.Close()
		return
	}
	s.acClient = client
	s.acMu.Unlock()

	// Commands from AC: "__system:" commands drive this LR's own system tab
	// (launch/terminate managed apps); everything else is forwarded to FC.
	client.SetCommandHandler(func(cmd string) {
		if strings.HasPrefix(cmd, "__system:") {
			s.handleSystemCommand(cmd)
			return
		}
		if s.reprServer != nil {
			s.reprServer.SendCommand("federation-command", cmd)
		}
	})

	s.pushStateToAC()
	s.broadcast("ac-state", s.acStateMsg(true, host, port))
	log.Printf("connected to agent-coordinator at %s", addr)

	// Block until the connection drops (either remotely or via Close).
	<-client.DisconnectCh()

	s.acMu.Lock()
	if s.acClient == client {
		s.acClient = nil
	}
	s.acMu.Unlock()

	log.Printf("disconnected from agent-coordinator at %s", addr)
	s.broadcast("ac-state", s.acStateMsg(false, host, port))
}

// disconnectAC closes the AC connection; the connectAC goroutine handles cleanup.
func (s *Server) disconnectAC() {
	s.acMu.Lock()
	client := s.acClient
	s.acMu.Unlock()
	if client != nil {
		client.Close()
	}
}

// startAutoConnectAC launches the background agent-coordinator auto-connect loop.
// It is a no-op if a loop is already running.
func (s *Server) startAutoConnectAC(host, port string) {
	s.acMu.Lock()
	if s.acAutoConnectCancel != nil {
		s.acMu.Unlock()
		return
	}
	cancel := make(chan struct{})
	s.acAutoConnectCancel = cancel
	s.acMu.Unlock()
	go s.autoConnectAC(host, port, cancel)
}

// stopAutoConnectAC cancels the background auto-connect retry loop if it is
// running. Called when the operator drives an explicit connect/disconnect from
// the UI, which supersedes auto-connect.
func (s *Server) stopAutoConnectAC() {
	s.acMu.Lock()
	if s.acAutoConnectCancel != nil {
		close(s.acAutoConnectCancel)
		s.acAutoConnectCancel = nil
	}
	s.acMu.Unlock()
}

// autoConnectAC dials agent-coordinator in the background on startup, retrying
// every autoConnectInterval until it connects or autoConnectWindow elapses. This
// mirrors federation-command's --auto-connect: it prints on startup that the mode
// is selected, keeps a visible "connecting" indicator live in the UI while it
// retries, and prints once when it gives up. Runs in its own goroutine; closing
// cancel stops it.
func (s *Server) autoConnectAC(host, port string, cancel chan struct{}) {
	defer func() {
		s.acMu.Lock()
		if s.acAutoConnectCancel == cancel {
			s.acAutoConnectCancel = nil
		}
		s.acMu.Unlock()
	}()

	deadline := time.Now().Add(autoConnectWindow)
	log.Printf("auto-connect enabled: dialing agent-coordinator at %s:%s every %s for up to %s (runs in background)",
		host, port, autoConnectInterval, autoConnectWindow)

	// finish clears the retry indicator and pushes a final ac-state to the UI.
	finish := func() {
		s.setACAutoConnecting(false)
		s.broadcast("ac-state", s.acStateMsg(s.getACClient() != nil, host, port))
	}

	for {
		select {
		case <-cancel:
			finish()
			return
		default:
		}
		if s.getACClient() != nil {
			finish() // connected another way in the meantime
			return
		}

		s.setACAutoConnecting(true)
		s.broadcast("ac-state", s.acStateMsg(false, host, port))
		log.Printf("auto-connect: attempting connection to agent-coordinator at %s:%s", host, port)
		go s.connectAC(host, port)

		select {
		case <-cancel:
			finish()
			return
		case <-time.After(autoConnectInterval):
		}

		if s.getACClient() != nil {
			finish() // the attempt landed a connection
			return
		}
		if time.Now().After(deadline) {
			log.Printf("auto-connect: gave up after %s — agent-coordinator at %s:%s did not respond",
				autoConnectWindow, host, port)
			finish()
			return
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

	// Send initial status and FC state.
	go func() {
		s.sendToClient(c, "status", s.currentStatus())
		s.sendToClient(c, "fc-state", FCStateMsg{State: s.getFCState()})
		s.sendToClient(c, "ridealong-state", s.getRidealongState())
		s.sendToClient(c, "condoc-state", s.getCondocState())
		s.sendToClient(c, "ac-state", s.getACState())
		s.sendToClient(c, "system-state", s.systemState())
		if cc := s.getCondoccerState(); cc != nil {
			s.sendToClient(c, "condoccer-state", *cc)
		}
	}()

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

	// Read pump: handle commands from browser clients.
	defer func() {
		close(c.done)
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var m wsMsg
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if s.reprServer != nil {
			switch m.Type {
			case "command":
				var payload struct {
					Cmd string `json:"cmd"`
				}
				if err := json.Unmarshal(m.Payload, &payload); err == nil && payload.Cmd != "" {
					s.reprServer.SendCommand("federation-command", payload.Cmd)
				}
			case "ridealong-command":
				var payload struct {
					Action string `json:"action"`
				}
				if err := json.Unmarshal(m.Payload, &payload); err == nil && payload.Action != "" {
					s.reprServer.SendCommand("federation-command", "__ridealong:"+payload.Action)
				}
			}
		}
		switch m.Type {
		case "connect-ac":
			var payload struct {
				Host string `json:"host"`
				Port string `json:"port"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil {
				s.stopAutoConnectAC() // an explicit connect supersedes auto-connect
				host := payload.Host
				if host == "" {
					host = "localhost"
				}
				port := payload.Port
				if port == "" {
					port = "8084"
				}
				go s.connectAC(host, port)
			}
		case "disconnect-ac":
			s.stopAutoConnectAC()
			s.disconnectAC()
		case "launch-app":
			var payload struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil && payload.Name != "" {
				if _, err := s.launchManaged(payload.Name); err != nil {
					log.Printf("launch-app %q: %v", payload.Name, err)
				}
			}
		case "terminate-app":
			var payload struct {
				ID   string `json:"id"`
				Name string `json:"name"` // backward-compat: dismiss by app name when a single instance is present
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil {
				target := payload.ID
				if target == "" {
					target = payload.Name
				}
				if target != "" {
					if err := s.terminateManaged(target); err != nil {
						log.Printf("terminate-app %q: %v", target, err)
					}
				}
			}
		}
	}
}

// broadcastLoop periodically pushes status updates to all connected clients.
func (s *Server) broadcastLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		status := s.currentStatus()
		s.broadcast("status", status)
		if ac := s.getACClient(); ac != nil {
			ac.SendData("services", status)
		}
	}
}

// proxyToCondoccer reverse-proxies /condoccer/* to a managed condoccer's HTTP
// server on loopback, stripping the /condoccer prefix. This is how the condoccer
// UI is "forwarded through LR": a browser (or agent-coordinator's /host/<id>/
// proxy) reaches condoccer without condoccer needing its own ingress. WebSocket
// upgrades on /condoccer/ws are carried through by httputil.ReverseProxy.
func (s *Server) proxyToCondoccer(w http.ResponseWriter, r *http.Request) {
	port := s.condoccerPort
	if cc := s.getCondoccerState(); cc != nil && cc.HTTPPort != "" {
		port = cc.HTTPPort // trust the port condoccer actually reported
	}
	if port == "" {
		http.Error(w, "condoccer port unknown on this host", http.StatusBadGateway)
		return
	}
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + port}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/condoccer")
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "condoccer not reachable on this host: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) setupRoutes(devMode bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/condoccer/", s.proxyToCondoccer)

	if devMode {
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

// appConfig is local-representative's fully-resolved startup configuration.
type appConfig struct {
	httpPort      string
	heartbeatPort string
	name          string
	dev           bool
	autoConnect   bool
	acHost        string
	acPort        string
	autoLaunch    []string // child applications to launch on startup ("app" or "app:N" tokens)
	fcBin         string   // explicit path to the federation-command binary
	terminal      string   // command prefix used to host an interactive child in a terminal
	condoccerPort string   // HTTP port a managed condoccer serves on / is reverse-proxied from
	condoccerRoot string   // repo root a managed condoccer scans (empty: condoccer's default)
}

// splitList parses a comma/whitespace-separated list, dropping empty entries.
func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// resolveConfig layers the ufa-configurable config files beneath the parsed
// flags: a flag named in setOnCLI keeps its command-line value, otherwise the
// config files (per-app over global) supply it, otherwise the flag default in
// defaults is used. Config keys match the flag names.
func resolveConfig(conf *ufaconfig.Config, setOnCLI map[string]bool, defaults appConfig) (appConfig, error) {
	pick := func(key, cur string) string {
		if setOnCLI[key] {
			return cur
		}
		return conf.String(key, cur)
	}
	pickBool := func(key string, cur bool) (bool, error) {
		if setOnCLI[key] {
			return cur, nil
		}
		return conf.Bool(key, cur)
	}
	out := appConfig{
		httpPort:      pick("port", defaults.httpPort),
		heartbeatPort: pick("repr-port", defaults.heartbeatPort),
		name:          pick("name", defaults.name),
		acHost:        pick("ac-host", defaults.acHost),
		acPort:        pick("ac-port", defaults.acPort),
		autoLaunch:    splitList(pick("auto-launch", strings.Join(defaults.autoLaunch, ","))),
		fcBin:         pick("fc-bin", defaults.fcBin),
		terminal:      pick("terminal", defaults.terminal),
		condoccerPort: pick("condoccer-port", defaults.condoccerPort),
		condoccerRoot: pick("condoccer-root", defaults.condoccerRoot),
	}
	var err error
	if out.dev, err = pickBool("dev", defaults.dev); err != nil {
		return out, err
	}
	if out.autoConnect, err = pickBool("auto-connect", defaults.autoConnect); err != nil {
		return out, err
	}
	return out, nil
}

func main() {
	defaultName, _ := os.Hostname()
	if defaultName == "" {
		defaultName = "local"
	}

	configDir := flag.String("config", "", "directory holding ufa-configurable YAML files (default ~/.ufa/config)")
	port := flag.String("port", "8081", "HTTP port to listen on")
	reprPort := flag.String("repr-port", "8082", "TCP port for representable heartbeat server")
	name := flag.String("name", defaultName, "name used to identify this LR to agent-coordinator")
	dev := flag.Bool("dev", false, "dev mode: skip serving frontend static files")
	autoConnect := flag.Bool("auto-connect", false, "dial agent-coordinator in the background on startup, retrying every 10s for up to 10m")
	acHost := flag.String("ac-host", defaultACHost, "agent-coordinator host/IP to auto-connect to")
	acPort := flag.String("ac-port", defaultACPort, "agent-coordinator port to auto-connect to")
	autoLaunch := flag.String("auto-launch", "", "comma/space-separated child applications to launch on startup; each token is \"app\" or \"app:N\" (e.g. federation-command:2)")
	fcBin := flag.String("fc-bin", "", "path to the federation-command binary (default: search next to LR, the dev bin dir, then PATH)")
	terminal := flag.String("terminal", "", "command prefix used to host federation-command in a terminal (e.g. \"xterm -e\" or \"tmux new-session -d -s fc\"); default: autodetect")
	condoccerPort := flag.String("condoccer-port", "8080", "HTTP port a managed condoccer serves on; its UI is reverse-proxied at /condoccer/")
	condoccerRoot := flag.String("condoccer-root", "", "repo root a managed condoccer scans (default: condoccer's own -root default)")
	flag.Parse()

	// Layer ~/.ufa/config/{global,local-representative}.yaml beneath the flags:
	// a flag set explicitly on the command line always wins.
	conf, err := ufaconfig.Load("local-representative", *configDir)
	if err != nil {
		log.Fatal(err)
	}
	setOnCLI := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setOnCLI[f.Name] = true })
	cfg, err := resolveConfig(conf, setOnCLI, appConfig{
		httpPort:      *port,
		heartbeatPort: *reprPort,
		name:          *name,
		dev:           *dev,
		autoConnect:   *autoConnect,
		acHost:        *acHost,
		acPort:        *acPort,
		autoLaunch:    splitList(*autoLaunch),
		fcBin:         *fcBin,
		terminal:      *terminal,
		condoccerPort: *condoccerPort,
		condoccerRoot: *condoccerRoot,
	})
	if err != nil {
		log.Fatal(err)
	}

	s := newServer(cfg.name)
	s.heartbeatPort = cfg.heartbeatPort
	s.httpPort = cfg.httpPort
	s.condoccerPort = cfg.condoccerPort
	s.condoccerRoot = cfg.condoccerRoot
	s.terminalCmd = cfg.terminal
	if cfg.fcBin != "" {
		s.binOverrides["federation-command"] = cfg.fcBin
	}

	reprSrv, err := representable.NewServer(":" + cfg.heartbeatPort)
	if err != nil {
		log.Fatal("representable server:", err)
	}
	s.reprServer = reprSrv

	// Track FC control mode changes and forward log entries to browser clients.
	reprSrv.SetStateChangeHandler(func(name, state string) {
		if name == "condoccer" {
			if state == "disconnected" {
				s.condoccerMu.Lock()
				s.condoccerState = nil
				s.condoccerMu.Unlock()
				empty := CondoccerStateMsg{}
				s.broadcast("condoccer-state", empty)
				if ac := s.getACClient(); ac != nil {
					ac.SendData("condoccer-state", empty)
				}
			}
			return
		}
		if name == "federation-command" {
			s.setFCState(state)
			if state == "disconnected" {
				s.ridealongMu.Lock()
				s.ridealongState = nil
				s.ridealongMu.Unlock()
				s.broadcast("ridealong-state", RidealongStateMsg{Active: false})
				s.condocMu.Lock()
				s.condocState = nil
				s.condocMu.Unlock()
				s.broadcast("condoc-state", CondocStateMsg{Active: false})
				if ac := s.getACClient(); ac != nil {
					ac.SendData("ridealong-state", RidealongStateMsg{Active: false})
					ac.SendData("condoc-state", CondocStateMsg{Active: false})
				}
			}
		}
	})

	reprSrv.SetLogHandler(func(name, line, kind string) {
		if name == "federation-command" {
			s.broadcast("fc-log", FCLogMsg{Line: line, Kind: kind})
			if ac := s.getACClient(); ac != nil {
				if kind == "output" {
					ac.SendOutput(line)
				} else {
					ac.SendLog(line)
				}
			}
		}
	})

	reprSrv.SetDataHandler(func(name, dataType string, data json.RawMessage) {
		if name == "condoccer" {
			if dataType == "condoccer-state" {
				var payload CondoccerStateMsg
				if err := json.Unmarshal(data, &payload); err == nil {
					s.condoccerMu.Lock()
					s.condoccerState = &payload
					s.condoccerMu.Unlock()
					s.broadcast("condoccer-state", payload)
					if ac := s.getACClient(); ac != nil {
						ac.SendData("condoccer-state", payload)
					}
				}
			}
			return
		}
		if name != "federation-command" {
			return
		}
		switch dataType {
		case "ridealong-state":
			var payload RidealongStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				s.ridealongMu.Lock()
				s.ridealongState = &payload
				s.ridealongMu.Unlock()
				s.broadcast("ridealong-state", payload)
				if ac := s.getACClient(); ac != nil {
					ac.SendData("ridealong-state", payload)
				}
			}
		case "condoc-state":
			var payload CondocStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				s.condocMu.Lock()
				s.condocState = &payload
				s.condocMu.Unlock()
				s.broadcast("condoc-state", payload)
				if ac := s.getACClient(); ac != nil {
					ac.SendData("condoc-state", payload)
				}
			}
		}
	})

	log.Printf("representable server listening on tcp://localhost:%s", cfg.heartbeatPort)

	go s.broadcastLoop()

	if cfg.autoConnect {
		log.Printf("auto-connect configuration selected for agent-coordinator at %s:%s", cfg.acHost, cfg.acPort)
		s.startAutoConnectAC(cfg.acHost, cfg.acPort)
	}

	if len(cfg.autoLaunch) > 0 {
		log.Printf("auto-launch configuration selected: %s", strings.Join(cfg.autoLaunch, ", "))
		s.startAutoLaunch(cfg.autoLaunch)
	}

	addr := ":" + cfg.httpPort
	log.Printf("local-representative %q listening on http://localhost%s", cfg.name, addr)
	if cfg.dev {
		log.Printf("dev mode: connect frontend to ws://localhost%s/ws", addr)
	}

	if err := http.ListenAndServe(addr, s.setupRoutes(cfg.dev)); err != nil {
		log.Fatal(err)
	}
}
