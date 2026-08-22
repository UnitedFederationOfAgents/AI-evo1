// Package representable provides the shared client/server protocol used by
// federation-command (and other sub-applications) to register with and report
// health to local-representative over a persistent TCP connection.
package representable

import (
	"bufio"
	"encoding/json"
	"net"
	"sync"
	"time"
)

const (
	HeartbeatInterval = 2 * time.Second
	StaleThreshold    = 6 * time.Second
)

// heartbeat is the newline-delimited JSON wire format sent from client to server.
type heartbeat struct {
	From string `json:"from"`
}

// Client connects to local-representative and sends periodic heartbeats.
type Client struct {
	conn net.Conn
	name string
	done chan struct{}
	once sync.Once
}

// Connect dials addr (TCP) with the given timeout and returns a running Client.
func Connect(addr, name string, connectTimeout time.Duration) (*Client, error) {
	conn, err := net.DialTimeout("tcp", addr, connectTimeout)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn: conn,
		name: name,
		done: make(chan struct{}),
	}
	go c.heartbeatLoop()
	return c, nil
}

// Close stops heartbeats and closes the TCP connection.
func (c *Client) Close() {
	c.once.Do(func() {
		close(c.done)
		c.conn.Close()
	})
}

func (c *Client) heartbeatLoop() {
	enc := json.NewEncoder(c.conn)
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	if err := enc.Encode(heartbeat{From: c.name}); err != nil {
		return
	}
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if err := enc.Encode(heartbeat{From: c.name}); err != nil {
				return
			}
		}
	}
}

// connState tracks the live health of a single connected client.
type connState struct {
	mu        sync.RWMutex
	lastSeen  time.Time
	connected bool
}

func (cs *connState) update() {
	cs.mu.Lock()
	cs.lastSeen = time.Now()
	cs.connected = true
	cs.mu.Unlock()
}

func (cs *connState) disconnect() {
	cs.mu.Lock()
	cs.connected = false
	cs.mu.Unlock()
}

// IsHealthy returns true if the client has sent a heartbeat within StaleThreshold.
func (cs *connState) IsHealthy() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.connected && time.Since(cs.lastSeen) < StaleThreshold
}

// Server accepts representable TCP connections and tracks client health.
type Server struct {
	ln     net.Listener
	mu     sync.RWMutex
	states map[string]*connState
}

// NewServer starts a TCP listener on addr and begins accepting connections.
func NewServer(addr string) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{
		ln:     ln,
		states: make(map[string]*connState),
	}
	go s.acceptLoop()
	return s, nil
}

// IsHealthy returns true if the named client is connected and heartbeating.
func (s *Server) IsHealthy(name string) bool {
	s.mu.RLock()
	cs, ok := s.states[name]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	return cs.IsHealthy()
}

func (s *Server) getOrCreate(name string) *connState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.states[name]; ok {
		return cs
	}
	cs := &connState{}
	s.states[name] = cs
	return cs
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	var cs *connState
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var hb heartbeat
		if err := json.Unmarshal(scanner.Bytes(), &hb); err != nil || hb.From == "" {
			continue
		}
		if cs == nil {
			cs = s.getOrCreate(hb.From)
		}
		cs.update()
	}
	if cs != nil {
		cs.disconnect()
	}
}
