package main

import (
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"representable"
)

//go:embed frontend/dist
var embeddedFrontend embed.FS

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

	fcMu    sync.RWMutex
	fcState string // "remote-control", "local-control", or "" (disconnected)

	ridealongMu    sync.RWMutex
	ridealongState *RidealongStateMsg

	condocMu    sync.RWMutex
	condocState *CondocStateMsg
}

func newServer() *Server {
	return &Server{
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
	return StatusMsg{
		Services: []ServiceStatus{
			{Name: "federation-command", Status: fcStatus},
			{Name: "condoccer", Status: "healthy"},
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
	}
}

// broadcastLoop periodically pushes status updates to all connected clients.
func (s *Server) broadcastLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.broadcast("status", s.currentStatus())
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
	port := flag.String("port", "8081", "HTTP port to listen on")
	reprPort := flag.String("repr-port", "8082", "TCP port for representable heartbeat server")
	dev := flag.Bool("dev", false, "dev mode: skip serving frontend static files")
	flag.Parse()

	s := newServer()

	reprSrv, err := representable.NewServer(":" + *reprPort)
	if err != nil {
		log.Fatal("representable server:", err)
	}
	s.reprServer = reprSrv

	// Track FC control mode changes and forward log entries to browser clients.
	reprSrv.SetStateChangeHandler(func(name, state string) {
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
			}
		}
	})
	reprSrv.SetLogHandler(func(name, line, kind string) {
		if name == "federation-command" {
			s.broadcast("fc-log", FCLogMsg{Line: line, Kind: kind})
		}
	})
	reprSrv.SetDataHandler(func(name, dataType string, data json.RawMessage) {
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
			}
		case "condoc-state":
			var payload CondocStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				s.condocMu.Lock()
				s.condocState = &payload
				s.condocMu.Unlock()
				s.broadcast("condoc-state", payload)
			}
		}
	})

	log.Printf("representable server listening on tcp://localhost:%s", *reprPort)

	go s.broadcastLoop()

	addr := ":" + *port
	log.Printf("local-representative listening on http://localhost%s", addr)
	if *dev {
		log.Printf("dev mode: connect frontend to ws://localhost%s/ws", addr)
	}

	if err := http.ListenAndServe(addr, s.setupRoutes(*dev)); err != nil {
		log.Fatal(err)
	}
}
