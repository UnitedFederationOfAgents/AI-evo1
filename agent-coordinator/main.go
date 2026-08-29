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
)

//go:embed frontend/dist
var embeddedFrontend embed.FS

// Host represents a configured local-representative instance.
type Host struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Addr   string `json:"addr"`
	Status string `json:"status"` // "unknown", "connected", "disconnected"
}

// HostsMsg is the payload of "hosts" WebSocket messages.
type HostsMsg struct {
	Hosts []Host `json:"hosts"`
}

// LRStateMsg is the payload of "lr-state" WebSocket messages — state of a
// local-representative on a given host.
type LRStateMsg struct {
	HostID   string          `json:"host_id"`
	Active   bool            `json:"active"`
	Services []ServiceStatus `json:"services,omitempty"`
}

// ServiceStatus is the health status of a monitored service on a remote host.
type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
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

// Server manages WebSocket clients and coordinator state.
type Server struct {
	upgrader websocket.Upgrader
	mu       sync.RWMutex
	clients  map[*wsClient]bool

	hostsMu sync.RWMutex
	hosts   []Host
}

func newServer() *Server {
	return &Server{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients: make(map[*wsClient]bool),
		hosts:   []Host{},
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

func (s *Server) getHosts() []Host {
	s.hostsMu.RLock()
	defer s.hostsMu.RUnlock()
	out := make([]Host, len(s.hosts))
	copy(out, s.hosts)
	return out
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

	// Send initial state on connect.
	go func() {
		s.sendToClient(c, "hosts", HostsMsg{Hosts: s.getHosts()})
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
			if err := json.Unmarshal(m.Payload, &payload); err == nil {
				// TCP connection to local-representative is deferred; reply with inactive state.
				s.sendToClient(c, "lr-state", LRStateMsg{
					HostID: payload.HostID,
					Active: false,
				})
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
	dev := flag.Bool("dev", false, "dev mode: skip serving frontend static files")
	flag.Parse()

	s := newServer()

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
