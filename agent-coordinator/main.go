package main

import (
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"representable"
)

//go:embed frontend/dist
var embeddedFrontend embed.FS

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
	mu        sync.RWMutex
	connected bool
	services  []ServiceStatus
	fcState   string
	ridealong *RidealongStateMsg
	condoc    *CondocStateMsg
	system    *SystemStateMsg
}

// Server manages WebSocket clients and coordinator state.
type Server struct {
	upgrader   websocket.Upgrader
	mu         sync.RWMutex
	clients    map[*wsClient]bool
	reprServer *representable.Server

	hostsMu    sync.RWMutex
	hostStates map[string]*hostState
}

func newServer() *Server {
	return &Server{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:    make(map[*wsClient]bool),
		hostStates: make(map[string]*hostState),
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

func (s *Server) setupRoutes(devMode bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)

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

func main() {
	port := flag.String("port", "8083", "HTTP port to listen on")
	reprPort := flag.String("repr-port", "8084", "TCP port for local-representative connections")
	dev := flag.Bool("dev", false, "dev mode: skip serving frontend static files")
	flag.Parse()

	s := newServer()

	reprSrv, err := representable.NewServer(":" + *reprPort)
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
			hs.mu.Unlock()
			s.broadcast("hosts", HostsMsg{Hosts: s.getHosts()})
			s.broadcast("lr-state", LRStateMsg{HostID: name, Active: false})
			s.broadcast("lr-fc-state", LRFCStateMsg{HostID: name, State: ""})
			s.broadcast("lr-ridealong-state", LRRidealongMsg{HostID: name, Active: false})
			s.broadcast("lr-condoc-state", LRCondocMsg{HostID: name, Active: false})
			s.broadcast("lr-system-state", LRSystemStateMsg{HostID: name, Active: false})
		}
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
		}
	})

	log.Printf("representable server (LR connections) listening on tcp://localhost:%s", *reprPort)

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
