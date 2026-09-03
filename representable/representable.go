// Package representable provides the shared client/server protocol used by
// federation-command (and other sub-applications) to register with and report
// health to local-representative over a persistent TCP connection.
//
// The protocol is bidirectional newline-delimited JSON:
//   - Client → Server: Msg  (heartbeat, state, log)
//   - Server → Client: ServerMsg (command)
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

// Msg is sent from client to server on the TCP connection.
type Msg struct {
	Type     string          `json:"type"`               // "heartbeat", "state", "log", "data"
	From     string          `json:"from,omitempty"`     // client name
	State    string          `json:"state,omitempty"`    // for type="state": "remote-control" or "local-control"
	Line     string          `json:"line,omitempty"`     // for type="log": command text or output line
	Kind     string          `json:"kind,omitempty"`     // for type="log": "cmd" (command echo) or "output" (stdout/stderr)
	DataType string          `json:"data_type,omitempty"` // for type="data": subtype identifier
	Data     json.RawMessage `json:"data,omitempty"`      // for type="data": arbitrary JSON payload
}

// ServerMsg is sent from server to client on the TCP connection.
type ServerMsg struct {
	Type string `json:"type"`        // "command"
	Cmd  string `json:"cmd,omitempty"` // for type="command"
}

// Client connects to local-representative and sends periodic heartbeats.
type Client struct {
	conn         net.Conn
	name         string
	done         chan struct{}
	disconnected chan struct{} // closed when readLoop exits (connection dropped or closed)
	once         sync.Once
	writeMu      sync.Mutex // serialises writes to conn
	handlerMu    sync.Mutex
	cmdHandler   func(string)
}

// Connect dials addr (TCP) with the given timeout and returns a running Client.
func Connect(addr, name string, connectTimeout time.Duration) (*Client, error) {
	conn, err := net.DialTimeout("tcp", addr, connectTimeout)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:         conn,
		name:         name,
		done:         make(chan struct{}),
		disconnected: make(chan struct{}),
	}
	go c.heartbeatLoop()
	go c.readLoop()
	return c, nil
}

// Close stops heartbeats and closes the TCP connection.
func (c *Client) Close() {
	c.once.Do(func() {
		close(c.done)
		c.conn.Close()
	})
}

// SetCommandHandler registers fn to be called when the server sends a command.
func (c *Client) SetCommandHandler(fn func(string)) {
	c.handlerMu.Lock()
	c.cmdHandler = fn
	c.handlerMu.Unlock()
}

// SendState notifies the server of a control mode change.
func (c *Client) SendState(state string) {
	c.send(Msg{Type: "state", From: c.name, State: state})
}

// SendLog sends a command line to the server for display in local control mode.
func (c *Client) SendLog(line string) {
	c.send(Msg{Type: "log", From: c.name, Line: line, Kind: "cmd"})
}

// SendOutput sends a line of command stdout/stderr output to the server for display.
func (c *Client) SendOutput(line string) {
	c.send(Msg{Type: "log", From: c.name, Line: line, Kind: "output"})
}

// SendData sends a typed JSON payload to the server.
func (c *Client) SendData(dataType string, payload interface{}) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	c.send(Msg{Type: "data", From: c.name, DataType: dataType, Data: json.RawMessage(b)})
}

func (c *Client) send(m Msg) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	json.NewEncoder(c.conn).Encode(m) //nolint:errcheck — best-effort
}

func (c *Client) heartbeatLoop() {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	c.send(Msg{Type: "heartbeat", From: c.name})
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.send(Msg{Type: "heartbeat", From: c.name})
		}
	}
}

// DisconnectCh returns a channel that is closed when the TCP connection drops
// (either because the remote end closed it or because Close was called).
func (c *Client) DisconnectCh() <-chan struct{} {
	return c.disconnected
}

func (c *Client) readLoop() {
	defer close(c.disconnected)
	scanner := bufio.NewScanner(c.conn)
	for scanner.Scan() {
		var sm ServerMsg
		if err := json.Unmarshal(scanner.Bytes(), &sm); err != nil {
			continue
		}
		if sm.Type == "command" && sm.Cmd != "" {
			c.handlerMu.Lock()
			fn := c.cmdHandler
			c.handlerMu.Unlock()
			if fn != nil {
				fn(sm.Cmd)
			}
		}
	}
}

// connState tracks the live health and mode of a single connected client.
type connState struct {
	mu        sync.RWMutex
	lastSeen  time.Time
	connected bool
	state     string   // "remote-control" or "local-control"
	conn      net.Conn // nil when disconnected
	peerHost  string   // remote IP/host of the most recent connection
}

func (cs *connState) update() {
	cs.mu.Lock()
	cs.lastSeen = time.Now()
	cs.connected = true
	cs.mu.Unlock()
}

func (cs *connState) setState(s string) {
	cs.mu.Lock()
	cs.state = s
	cs.mu.Unlock()
}

func (cs *connState) getState() string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.state
}

func (cs *connState) setConn(conn net.Conn) {
	cs.mu.Lock()
	cs.conn = conn
	if conn != nil {
		if host, _, err := net.SplitHostPort(conn.RemoteAddr().String()); err == nil {
			cs.peerHost = host
		}
	}
	cs.mu.Unlock()
}

func (cs *connState) disconnect() {
	cs.mu.Lock()
	cs.connected = false
	cs.conn = nil
	cs.mu.Unlock()
}

// sendCmd writes a command message to the client connection.
func (cs *connState) sendCmd(cmd string) {
	cs.mu.RLock()
	conn := cs.conn
	cs.mu.RUnlock()
	if conn == nil {
		return
	}
	json.NewEncoder(conn).Encode(ServerMsg{Type: "command", Cmd: cmd}) //nolint:errcheck
}

// IsHealthy returns true if the client has sent a heartbeat within StaleThreshold.
func (cs *connState) IsHealthy() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.connected && time.Since(cs.lastSeen) < StaleThreshold
}

// Server accepts representable TCP connections and tracks client health and state.
type Server struct {
	ln      net.Listener
	mu      sync.RWMutex
	states  map[string]*connState
	onState func(name, state string)                          // called on state change or disconnect
	onLog   func(name, line, kind string)                     // called when client sends a log entry
	onData  func(name, dataType string, data json.RawMessage) // called when client sends a data message
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

// SetStateChangeHandler registers fn to be called when a client changes state or disconnects.
// state will be "disconnected" when the TCP connection is lost.
func (s *Server) SetStateChangeHandler(fn func(name, state string)) {
	s.mu.Lock()
	s.onState = fn
	s.mu.Unlock()
}

// SetLogHandler registers fn to be called when a client sends a log entry.
// kind is "cmd" for command echoes or "output" for stdout/stderr lines.
func (s *Server) SetLogHandler(fn func(name, line, kind string)) {
	s.mu.Lock()
	s.onLog = fn
	s.mu.Unlock()
}

// SetDataHandler registers fn to be called when a client sends a typed data message.
func (s *Server) SetDataHandler(fn func(name, dataType string, data json.RawMessage)) {
	s.mu.Lock()
	s.onData = fn
	s.mu.Unlock()
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

// PeerHost returns the remote IP/host of the named client's most recent
// connection, or "" if the client is unknown. It survives a disconnect so a
// caller can still reach back to a briefly-dropped peer's HTTP endpoint.
func (s *Server) PeerHost(name string) string {
	s.mu.RLock()
	cs, ok := s.states[name]
	s.mu.RUnlock()
	if !ok {
		return ""
	}
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.peerHost
}

// GetState returns the current control state of a named client ("remote-control",
// "local-control", or "" if unknown/disconnected).
func (s *Server) GetState(name string) string {
	s.mu.RLock()
	cs, ok := s.states[name]
	s.mu.RUnlock()
	if !ok {
		return ""
	}
	return cs.getState()
}

// SendCommand delivers cmd to a named client's FC process.
func (s *Server) SendCommand(name, cmd string) {
	s.mu.RLock()
	cs, ok := s.states[name]
	s.mu.RUnlock()
	if !ok {
		return
	}
	cs.sendCmd(cmd)
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
	var clientName string
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var m Msg
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil || m.From == "" {
			continue
		}
		if cs == nil {
			clientName = m.From
			cs = s.getOrCreate(clientName)
			cs.setConn(conn)
		}
		switch m.Type {
		case "heartbeat":
			cs.update()
		case "state":
			cs.setState(m.State)
			s.mu.RLock()
			fn := s.onState
			s.mu.RUnlock()
			if fn != nil {
				fn(clientName, m.State)
			}
		case "log":
			s.mu.RLock()
			fn := s.onLog
			s.mu.RUnlock()
			if fn != nil {
				fn(clientName, m.Line, m.Kind)
			}
		case "data":
			s.mu.RLock()
			fn := s.onData
			s.mu.RUnlock()
			if fn != nil {
				fn(clientName, m.DataType, m.Data)
			}
		}
	}
	if cs != nil {
		cs.disconnect()
		s.mu.RLock()
		fn := s.onState
		s.mu.RUnlock()
		if fn != nil {
			fn(clientName, "disconnected")
		}
	}
}
