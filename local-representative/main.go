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

	// Send initial status.
	go s.sendToClient(c, "status", s.currentStatus())

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
		if _, _, err := conn.ReadMessage(); err != nil {
			return
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
