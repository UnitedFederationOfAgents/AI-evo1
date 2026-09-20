package main

import (
	"embed"
	"encoding/json"
	"flag"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sort"
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

// proxiedHeader is the header proxyToHost's transparent passthrough stamps on
// every request it forwards, so a host's local-representative can refuse
// input it only accepts from a direct client (see
// docs/DistributedExchange.md).
const proxiedHeader = "X-UFA-Proxied-By"

// relayedUploadHeader marks a file upload that handleFileUploadRelay (not the
// transparent passthrough above) forwarded to a host on a browser's behalf --
// Path 1 of docs/DistributedExchange.md. It can only ever originate there:
// proxyToHost's transparent Director strips any client-supplied copy before
// forwarding, so a request reaching LR through that passthrough can never
// carry it, and LR's upload handler treats the two headers together as proof
// the request came through AC's dedicated relay route rather than being
// spoofed.
const relayedUploadHeader = "X-UFA-Relayed-Upload-By"

// acRelayStamp is the value both proxiedHeader and relayedUploadHeader carry
// on a request handleFileUploadRelay makes.
const acRelayStamp = "agent-coordinator"

// Host represents a local-representative instance known to agent-coordinator.
type Host struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"` // "connected" or "disconnected"
}

// HostsMsg is the payload of "hosts" WebSocket messages.
type HostsMsg struct {
	Hosts []Host `json:"hosts"`
}

// LRStateMsg is the payload of "lr-state" WebSocket messages.
type LRStateMsg struct {
	HostID   string          `json:"host_id"`
	Active   bool            `json:"active"`
	Services []ServiceStatus `json:"services,omitempty"`
}

// ServiceStatus is the health status of a monitored service.
type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// StatusMsg matches the services payload sent from LR over representable.
type StatusMsg struct {
	Services []ServiceStatus `json:"services"`
}

// FCStateMsg matches the fc-state payload sent from LR.
type FCStateMsg struct {
	State string `json:"state"`
}

// FCLogMsg is the payload of "lr-fc-log" WebSocket messages.
type FCLogMsg struct {
	Line string `json:"line"`
	Kind string `json:"kind,omitempty"`
}

// RidealongStateMsg matches the ridealong-state payload.
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

// CondocStateMsg matches the condoc-state payload.
type CondocStateMsg struct {
	Active    bool   `json:"active"`
	Name      string `json:"name,omitempty"`
	Phase     string `json:"phase,omitempty"`
	StepNum   int    `json:"step_num,omitempty"`
	StatusMsg string `json:"status_msg,omitempty"`
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

// CondoccerStateMsg matches the condoccer-state payload forwarded up from LR
// (originating at a managed condoccer). HTTPPort is condoccer's own port on the
// LR box; the coordinator reverse-proxies its UI at /host/<id>/condoccer/.
type CondoccerStateMsg struct {
	HTTPPort string       `json:"http_port"`
	Root     string       `json:"root"`
	Condocs  []CondocInfo `json:"condocs"`
}

// LRHTTPMsg matches the lr-http payload: the HTTP port an LR's dashboard listens
// on, used to build the /host/<id>/ reverse-proxy target.
type LRHTTPMsg struct {
	Port string `json:"port"`
}

// FileInfo mirrors one row of local-representative's files tab: a file sitting
// in that host's host-cache directory.
type FileInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	UploadedAt int64  `json:"uploaded_at"`
	ExpiresAt  int64  `json:"expires_at"`
}

// FilesStateMsg matches the files-state payload sent from LR over representable.
type FilesStateMsg struct {
	Files []FileInfo `json:"files"`
}

// LRCondoccerMsg is the host-scoped "lr-condoccer-state" message sent to browser
// clients: a per-host condoc summary plus whether the forwarded UI is available.
type LRCondoccerMsg struct {
	HostID    string       `json:"host_id"`
	Available bool         `json:"available"`
	Root      string       `json:"root,omitempty"`
	Condocs   []CondocInfo `json:"condocs,omitempty"`
}

// ProcInfo mirrors one row of local-representative's system tab: LR itself or a
// child application instance it manages.
type ProcInfo struct {
	Name       string `json:"name"`
	InstanceID string `json:"instance_id"`
	Instance   int    `json:"instance"`
	PID        int    `json:"pid"`
	Status     string `json:"status"`
	Managed    bool   `json:"managed"`
	StartedAt  int64  `json:"started_at"`
	ExitCode   int    `json:"exit_code"`
	Detail     string `json:"detail,omitempty"`
	DevMode    bool   `json:"dev_mode,omitempty"` // launched with --dev-mode -- see docs/DevMode.md
}

// SystemStateMsg matches the system-state payload sent from LR over representable.
type SystemStateMsg struct {
	Self    ProcInfo   `json:"self"`
	Managed []ProcInfo `json:"managed"`
}

// Host-scoped WS message types sent to browser clients.

type LRFCStateMsg struct {
	HostID string `json:"host_id"`
	State  string `json:"state"`
}

type LRFCLogMsg struct {
	HostID string `json:"host_id"`
	Line   string `json:"line"`
	Kind   string `json:"kind,omitempty"`
}

type LRRidealongMsg struct {
	HostID       string   `json:"host_id"`
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

type LRCondocMsg struct {
	HostID    string `json:"host_id"`
	Active    bool   `json:"active"`
	Name      string `json:"name,omitempty"`
	Phase     string `json:"phase,omitempty"`
	StepNum   int    `json:"step_num,omitempty"`
	StatusMsg string `json:"status_msg,omitempty"`
}

// LRSystemStateMsg is the host-scoped "lr-system-state" message sent to browser
// clients: local-representative's system tab for one host. Active is false when
// that LR is not connected to the coordinator.
type LRSystemStateMsg struct {
	HostID  string     `json:"host_id"`
	Active  bool       `json:"active"`
	Self    ProcInfo   `json:"self"`
	Managed []ProcInfo `json:"managed"`
}

// LRFilesMsg is the host-scoped "lr-files-state" message sent to browser
// clients: local-representative's files tab for one host. Upload is relayed
// through handleFileUploadRelay rather than AC keeping its own copy of the
// file (see docs/DistributedExchange.md, Path 1). Active is false when that
// LR is not connected.
type LRFilesMsg struct {
	HostID string     `json:"host_id"`
	Active bool       `json:"active"`
	Files  []FileInfo `json:"files,omitempty"`
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

// hostState tracks the live state of a connected local-representative.
type hostState struct {
	mu         sync.RWMutex
	connected  bool
	services   []ServiceStatus
	fcState    string
	ridealong  *RidealongStateMsg
	condoc     *CondocStateMsg
	system     *SystemStateMsg
	condoccer  *CondoccerStateMsg
	files      *FilesStateMsg
	lrHTTPPort string
}

// Server manages WebSocket clients and coordinator state.
type Server struct {
	upgrader   websocket.Upgrader
	mu         sync.RWMutex
	clients    map[*wsClient]bool
	reprServer *representable.Server
	devMode    bool // --dev-mode: this agent-coordinator instance -- see docs/DevMode.md

	hostsMu    sync.RWMutex
	hostStates map[string]*hostState

	modeMu         sync.RWMutex
	modeMismatches map[string]ModeMismatchMsg // LR host id -> current mismatch disclosure, mismatched entries only
}

func newServer() *Server {
	return &Server{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:        make(map[*wsClient]bool),
		hostStates:     make(map[string]*hostState),
		modeMismatches: make(map[string]ModeMismatchMsg),
	}
}

// SelfInfoMsg discloses this agent-coordinator instance's own dev-mode status
// to its frontend (see docs/DevMode.md) — sent once when a browser client
// connects, since AC has no representable server above it forwarding a "self"
// ProcInfo the way LR forwards one for itself.
type SelfInfoMsg struct {
	DevMode bool `json:"dev_mode"`
}

// ModeMismatchMsg discloses that a connected local-representative's dev-mode
// status differs from this agent-coordinator's own (see docs/DevMode.md).
// Mismatched=false clears a previously-disclosed mismatch.
type ModeMismatchMsg struct {
	HostID     string `json:"host_id"`
	Mismatched bool   `json:"mismatched"`
	PeerMode   string `json:"peer_mode,omitempty"`
}

// setModeMismatch records hostID's current mismatch verdict and broadcasts it
// to every connected browser client.
func (s *Server) setModeMismatch(hostID string, mismatched bool, peerMode string) {
	s.modeMu.Lock()
	if mismatched {
		s.modeMismatches[hostID] = ModeMismatchMsg{HostID: hostID, Mismatched: true, PeerMode: peerMode}
	} else {
		delete(s.modeMismatches, hostID)
	}
	s.modeMu.Unlock()
	s.broadcast("mode-mismatch", ModeMismatchMsg{HostID: hostID, Mismatched: mismatched, PeerMode: peerMode})
}

// currentModeMismatches returns a snapshot of every host currently disclosed
// as mismatched, for a newly-connected browser client.
func (s *Server) currentModeMismatches() []ModeMismatchMsg {
	s.modeMu.RLock()
	defer s.modeMu.RUnlock()
	out := make([]ModeMismatchMsg, 0, len(s.modeMismatches))
	for _, m := range s.modeMismatches {
		out = append(out, m)
	}
	return out
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

func (s *Server) getOrCreateHost(name string) (*hostState, bool) {
	s.hostsMu.Lock()
	defer s.hostsMu.Unlock()
	if hs, ok := s.hostStates[name]; ok {
		return hs, false
	}
	hs := &hostState{}
	s.hostStates[name] = hs
	return hs, true
}

func (s *Server) getHosts() []Host {
	s.hostsMu.RLock()
	defer s.hostsMu.RUnlock()
	hosts := make([]Host, 0, len(s.hostStates))
	for name, hs := range s.hostStates {
		hs.mu.RLock()
		status := "disconnected"
		if hs.connected {
			status = "connected"
		}
		hs.mu.RUnlock()
		hosts = append(hosts, Host{ID: name, Label: name, Status: status})
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
	return hosts
}

func (s *Server) sendHostSnapshot(c *wsClient, name string) {
	s.hostsMu.RLock()
	hs, ok := s.hostStates[name]
	s.hostsMu.RUnlock()
	if !ok {
		return
	}
	hs.mu.RLock()
	connected := hs.connected
	services := hs.services
	fcState := hs.fcState
	ridealong := hs.ridealong
	condoc := hs.condoc
	system := hs.system
	condoccer := hs.condoccer
	files := hs.files
	hs.mu.RUnlock()

	s.sendToClient(c, "lr-state", LRStateMsg{HostID: name, Active: connected, Services: services})
	s.sendToClient(c, "lr-fc-state", LRFCStateMsg{HostID: name, State: fcState})
	if ridealong != nil {
		s.sendToClient(c, "lr-ridealong-state", ridealongMsg(name, ridealong))
	} else {
		s.sendToClient(c, "lr-ridealong-state", LRRidealongMsg{HostID: name, Active: false})
	}
	if condoc != nil {
		s.sendToClient(c, "lr-condoc-state", condocMsg(name, condoc))
	} else {
		s.sendToClient(c, "lr-condoc-state", LRCondocMsg{HostID: name, Active: false})
	}
	if system != nil {
		s.sendToClient(c, "lr-system-state", LRSystemStateMsg{
			HostID: name, Active: connected, Self: system.Self, Managed: system.Managed,
		})
	} else {
		s.sendToClient(c, "lr-system-state", LRSystemStateMsg{HostID: name, Active: false})
	}
	s.sendToClient(c, "lr-condoccer-state", condoccerMsg(name, condoccer))
	if files != nil {
		s.sendToClient(c, "lr-files-state", LRFilesMsg{HostID: name, Active: connected, Files: files.Files})
	} else {
		s.sendToClient(c, "lr-files-state", LRFilesMsg{HostID: name, Active: false})
	}
}

// condoccerMsg builds a host-scoped lr-condoccer-state payload; a nil state means
// no managed condoccer is currently reporting on that host.
func condoccerMsg(hostID string, cc *CondoccerStateMsg) LRCondoccerMsg {
	if cc == nil || cc.HTTPPort == "" {
		return LRCondoccerMsg{HostID: hostID, Available: false}
	}
	return LRCondoccerMsg{HostID: hostID, Available: true, Root: cc.Root, Condocs: cc.Condocs}
}

func ridealongMsg(hostID string, r *RidealongStateMsg) LRRidealongMsg {
	return LRRidealongMsg{
		HostID:       hostID,
		Active:       r.Active,
		Title:        r.Title,
		CurrentIndex: r.CurrentIndex,
		TotalSteps:   r.TotalSteps,
		CurrentCmd:   r.CurrentCmd,
		PrevCmd:      r.PrevCmd,
		PrevExitCode: r.PrevExitCode,
		NextCmd:      r.NextCmd,
		Autoplay:     r.Autoplay,
		Countdown:    r.Countdown,
		Waypoints:    r.Waypoints,
	}
}

func condocMsg(hostID string, c *CondocStateMsg) LRCondocMsg {
	return LRCondocMsg{
		HostID:    hostID,
		Active:    c.Active,
		Name:      c.Name,
		Phase:     c.Phase,
		StepNum:   c.StepNum,
		StatusMsg: c.StatusMsg,
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

	// Send initial state.
	go func() {
		s.sendToClient(c, "self-info", SelfInfoMsg{DevMode: s.devMode})
		s.sendToClient(c, "hosts", HostsMsg{Hosts: s.getHosts()})
		s.hostsMu.RLock()
		names := make([]string, 0, len(s.hostStates))
		for name := range s.hostStates {
			names = append(names, name)
		}
		s.hostsMu.RUnlock()
		for _, name := range names {
			s.sendHostSnapshot(c, name)
		}
		for _, mm := range s.currentModeMismatches() {
			s.sendToClient(c, "mode-mismatch", mm)
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

	// Read pump.
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
		switch m.Type {
		case "select-host":
			var payload struct {
				HostID string `json:"host_id"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil && payload.HostID != "" {
				s.sendHostSnapshot(c, payload.HostID)
			}
		case "lr-command":
			var payload struct {
				HostID string `json:"host_id"`
				Cmd    string `json:"cmd"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && payload.Cmd != "" && s.reprServer != nil {
				s.reprServer.SendCommand(payload.HostID, payload.Cmd)
			}
		case "lr-ridealong-command":
			var payload struct {
				HostID string `json:"host_id"`
				Action string `json:"action"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && payload.Action != "" && s.reprServer != nil {
				s.reprServer.SendCommand(payload.HostID, "__ridealong:"+payload.Action)
			}
		case "lr-launch-app":
			var payload struct {
				HostID string `json:"host_id"`
				Name   string `json:"name"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && payload.Name != "" && s.reprServer != nil {
				s.reprServer.SendCommand(payload.HostID, "__system:launch "+payload.Name)
			}
		case "lr-terminate-app":
			var payload struct {
				HostID string `json:"host_id"`
				ID     string `json:"id"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && payload.ID != "" && s.reprServer != nil {
				s.reprServer.SendCommand(payload.HostID, "__system:terminate "+payload.ID)
			}
		}
	}
}

// broadcastLoop periodically pushes host status updates to all connected clients.
func (s *Server) broadcastLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.broadcast("hosts", HostsMsg{Hosts: s.getHosts()})
	}
}

// resolveHostTarget returns "host:port" for a connected host's
// local-representative HTTP dashboard, or ok=false with a ready-to-use HTTP
// status and message when it isn't reachable. Shared by proxyToHost and
// handleFileUploadRelay, which both need to turn a host id into somewhere to
// send an HTTP request.
func (s *Server) resolveHostTarget(hostID string) (addr string, status int, errMsg string, ok bool) {
	s.hostsMu.RLock()
	hs, known := s.hostStates[hostID]
	s.hostsMu.RUnlock()
	if !known {
		return "", http.StatusNotFound, "unknown host: " + hostID, false
	}
	hs.mu.RLock()
	port := hs.lrHTTPPort
	connected := hs.connected
	hs.mu.RUnlock()
	if !connected || port == "" {
		return "", http.StatusBadGateway, "local-representative on " + hostID + " is not reachable", false
	}
	host := ""
	if s.reprServer != nil {
		host = s.reprServer.PeerHost(hostID)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return host + ":" + port, 0, "", true
}

// proxyToHost reverse-proxies /host/<hostID>/* to that host's local-representative
// dashboard, which in turn forwards /condoccer/* down to condoccer. This is the
// "forward the UI through AC" half of the chain: a browser on the coordinator —
// including one reaching it through the web-exposure path — drives condoccer on
// any connected box over a single origin, with no direct link to that box.
//
// A POST to /host/<hostID>/api/files is the one path this transparent
// passthrough doesn't carry itself: that's a file upload, a write to that
// host's filesystem, so it's handled by the dedicated handleFileUploadRelay
// route instead (see docs/DistributedExchange.md, Path 1).
func (s *Server) proxyToHost(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/host/")
	hostID, subPath, _ := strings.Cut(rest, "/")
	if hostID == "" {
		http.Error(w, "missing host id", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost && subPath == "api/files" {
		s.handleFileUploadRelay(w, r, hostID)
		return
	}

	addr, status, errMsg, ok := s.resolveHostTarget(hostID)
	if !ok {
		http.Error(w, errMsg, status)
		return
	}
	target := &url.URL{Scheme: "http", Host: addr}
	prefix := "/host/" + hostID
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		req.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(req.URL.Path, prefix), "/")
		req.Host = target.Host
		// Mark the request as having arrived through this transparent proxy so
		// LR can refuse input it only accepts from a direct client or from
		// handleFileUploadRelay's dedicated route (e.g. file uploads — see
		// docs/DistributedExchange.md). relayedUploadHeader is stripped here so
		// a client can never spoof its way past that distinction: it can only
		// be set by handleFileUploadRelay building its own outbound request.
		req.Header.Del(relayedUploadHeader)
		req.Header.Set(proxiedHeader, acRelayStamp)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "host "+hostID+" not reachable: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// handleFileUploadRelay implements Path 1 of docs/DistributedExchange.md: an
// operator at AC's dashboard drops a file onto the selected host's files tab,
// same as if they'd connected to that LR directly. AC acts purely as a
// relay — it streams the multipart body straight through to that host's
// `POST /api/files`, stamping the request with relayedUploadHeader (alongside
// proxiedHeader, since it did arrive via AC) so LR's upload handler accepts
// it as the one deliberate exception to refusing proxied uploads. AC never
// buffers the file to its own filesystem, or looks at LR's, in the process.
func (s *Server) handleFileUploadRelay(w http.ResponseWriter, r *http.Request, hostID string) {
	addr, status, errMsg, ok := s.resolveHostTarget(hostID)
	if !ok {
		http.Error(w, errMsg, status)
		return
	}

	outReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://"+addr+"/api/files", r.Body)
	if err != nil {
		http.Error(w, "building relay request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	outReq.ContentLength = r.ContentLength
	outReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	outReq.Header.Set(proxiedHeader, acRelayStamp)
	outReq.Header.Set(relayedUploadHeader, acRelayStamp)

	resp, err := http.DefaultClient.Do(outReq)
	if err != nil {
		http.Error(w, "host "+hostID+" not reachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if ctype := resp.Header.Get("Content-Type"); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck — best-effort once headers/status are already written
}

func (s *Server) setupRoutes(devMode bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/host/", s.proxyToHost)

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
		if err := restartsignal.Announce(os.Stdout, "agent-coordinator", "sighup"); err != nil {
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
	port := flag.String("port", "8083", "HTTP port to listen on")
	reprPort := flag.String("repr-port", "8084", "TCP port for local-representative connections")
	dev := flag.Bool("dev", false, "dev mode: skip serving frontend static files")
	devMode := flag.Bool("dev-mode", false, "dev mode (SDLC sense, see docs/DevMode.md): this agent-coordinator is running from an in-progress branch. Unrelated to --dev.")
	flag.Parse()

	s := newServer()
	s.devMode = *devMode

	reprSrv, err := representable.NewServer(":"+*reprPort, representable.Mode(s.devMode))
	if err != nil {
		log.Fatal("representable server:", err)
	}
	s.reprServer = reprSrv

	reprSrv.SetStateChangeHandler(func(name, state string) {
		if state == "disconnected" {
			hs, _ := s.getOrCreateHost(name)
			hs.mu.Lock()
			hs.connected = false
			hs.fcState = ""
			hs.ridealong = nil
			hs.condoc = nil
			hs.system = nil
			hs.condoccer = nil
			hs.files = nil
			hs.lrHTTPPort = ""
			hs.mu.Unlock()
			s.broadcast("hosts", HostsMsg{Hosts: s.getHosts()})
			s.broadcast("lr-state", LRStateMsg{HostID: name, Active: false})
			s.broadcast("lr-fc-state", LRFCStateMsg{HostID: name, State: ""})
			s.broadcast("lr-ridealong-state", LRRidealongMsg{HostID: name, Active: false})
			s.broadcast("lr-condoc-state", LRCondocMsg{HostID: name, Active: false})
			s.broadcast("lr-system-state", LRSystemStateMsg{HostID: name, Active: false})
			s.broadcast("lr-condoccer-state", LRCondoccerMsg{HostID: name, Available: false})
			s.broadcast("lr-files-state", LRFilesMsg{HostID: name, Active: false})
			s.setModeMismatch(name, false, "")
		}
	})

	// Disclose a dev/ops mode mismatch with a connecting local-representative —
	// see docs/DevMode.md. Both still exchange heartbeats; representable itself
	// refuses their state/log/data traffic while mismatched.
	reprSrv.SetModeMismatchHandler(func(name string, mismatched bool, peerMode string) {
		s.setModeMismatch(name, mismatched, peerMode)
	})

	reprSrv.SetLogHandler(func(name, line, kind string) {
		s.broadcast("lr-fc-log", LRFCLogMsg{HostID: name, Line: line, Kind: kind})
	})

	reprSrv.SetDataHandler(func(name, dataType string, data json.RawMessage) {
		hs, isNew := s.getOrCreateHost(name)
		hs.mu.Lock()
		wasConnected := hs.connected
		hs.connected = true
		hs.mu.Unlock()

		if !wasConnected || isNew {
			s.broadcast("hosts", HostsMsg{Hosts: s.getHosts()})
		}

		switch dataType {
		case "services":
			var payload StatusMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				hs.services = payload.Services
				hs.mu.Unlock()
				s.broadcast("lr-state", LRStateMsg{HostID: name, Active: true, Services: payload.Services})
			}
		case "fc-state":
			var payload FCStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				hs.fcState = payload.State
				hs.mu.Unlock()
				s.broadcast("lr-fc-state", LRFCStateMsg{HostID: name, State: payload.State})
			}
		case "ridealong-state":
			var payload RidealongStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				if payload.Active {
					hs.ridealong = &payload
				} else {
					hs.ridealong = nil
				}
				hs.mu.Unlock()
				s.broadcast("lr-ridealong-state", ridealongMsg(name, &payload))
			}
		case "condoc-state":
			var payload CondocStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				if payload.Active {
					hs.condoc = &payload
				} else {
					hs.condoc = nil
				}
				hs.mu.Unlock()
				s.broadcast("lr-condoc-state", condocMsg(name, &payload))
			}
		case "system-state":
			var payload SystemStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				hs.system = &payload
				hs.mu.Unlock()
				s.broadcast("lr-system-state", LRSystemStateMsg{
					HostID: name, Active: true, Self: payload.Self, Managed: payload.Managed,
				})
			}
		case "condoccer-state":
			var payload CondoccerStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				var cc *CondoccerStateMsg
				if payload.HTTPPort != "" {
					cc = &payload
				}
				hs.mu.Lock()
				hs.condoccer = cc
				hs.mu.Unlock()
				s.broadcast("lr-condoccer-state", condoccerMsg(name, cc))
			}
		case "lr-http":
			var payload LRHTTPMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				hs.lrHTTPPort = payload.Port
				hs.mu.Unlock()
			}
		case "files-state":
			var payload FilesStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				hs.files = &payload
				hs.mu.Unlock()
				s.broadcast("lr-files-state", LRFilesMsg{HostID: name, Active: true, Files: payload.Files})
			}
		}
	})

	log.Printf("representable server (LR connections) listening on tcp://localhost:%s", *reprPort)

	go watchRestartSignal()
	go s.broadcastLoop()

	addr := ":" + *port
	log.Printf("agent-coordinator listening on http://localhost%s", addr)
	if *dev {
		log.Printf("dev mode: connect frontend to ws://localhost%s/ws", addr)
	}

	if err := http.ListenAndServe(addr, s.setupRoutes(*dev)); err != nil {
		log.Fatal(err)
	}
}
