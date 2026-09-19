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

// Dev-mode identifiers exchanged in Msg.Mode / ServerMsg.Mode. Every UFA
// sub-application launches as one or the other (see docs/DevMode.md); a peer
// on the other side of a representable connection is a "mode mismatch".
const (
	ModeOps = "ops" // launched without --dev-mode: an operations-branch instance
	ModeDev = "dev" // launched with --dev-mode: a development-branch instance
)

// Mode returns the Msg/ServerMsg mode string for a --dev-mode flag value —
// the argument every Connect/NewServer caller passes.
func Mode(devMode bool) string {
	if devMode {
		return ModeDev
	}
	return ModeOps
}

// Msg is sent from client to server on the TCP connection.
type Msg struct {
	Type     string          `json:"type"`               // "heartbeat", "state", "log", "data"
	From     string          `json:"from,omitempty"`     // client name
	Mode     string          `json:"mode,omitempty"`     // ModeDev or ModeOps — this client's dev-mode status, stamped on every message
	State    string          `json:"state,omitempty"`    // for type="state": "remote-control" or "local-control"
	Line     string          `json:"line,omitempty"`     // for type="log": command text or output line
	Kind     string          `json:"kind,omitempty"`     // for type="log": "cmd" (command echo) or "output" (stdout/stderr)
	DataType string          `json:"data_type,omitempty"` // for type="data": subtype identifier
	Data     json.RawMessage `json:"data,omitempty"`      // for type="data": arbitrary JSON payload
}

// ServerMsg is sent from server to client on the TCP connection.
type ServerMsg struct {
	Type string `json:"type"`          // "command" or "hello"
	Cmd  string `json:"cmd,omitempty"` // for type="command"
	Mode string `json:"mode,omitempty"` // for type="hello": the server's dev-mode status (ModeDev or ModeOps)
}

// Client connects to local-representative and sends periodic heartbeats.
type Client struct {
	conn         net.Conn
	name         string
	mode         string // ModeDev or ModeOps — stamped on every outgoing Msg
	done         chan struct{}
	disconnected chan struct{} // closed when readLoop exits (connection dropped or closed)
	once         sync.Once
	writeMu      sync.Mutex // serialises writes to conn
	handlerMu    sync.Mutex
	cmdHandler   func(string)

	modeMu           sync.RWMutex
	peerMode         string // the server's mode, learned from its "hello" message
	mismatched       bool   // true once peerMode is known and differs from mode
	mismatchHandler  func(mismatched bool, peerMode string)
}

// Connect dials addr (TCP) with the given timeout and returns a running
// Client. mode is this client's dev-mode status (see Mode) — it is stamped on
// every message so the server can detect and disclose a mode mismatch.
func Connect(addr, name, mode string, connectTimeout time.Duration) (*Client, error) {
	conn, err := net.DialTimeout("tcp", addr, connectTimeout)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:         conn,
		name:         name,
		mode:         mode,
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
	m.Mode = c.mode
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	json.NewEncoder(c.conn).Encode(m) //nolint:errcheck — best-effort
}

// SetModeMismatchHandler registers fn to be called whenever this client learns
// (or the answer changes) whether the server it is connected to is running in
// a different dev-mode than this client. Fires once with mismatched=false on a
// clean connect if the modes agree.
func (c *Client) SetModeMismatchHandler(fn func(mismatched bool, peerMode string)) {
	c.modeMu.Lock()
	c.mismatchHandler = fn
	c.modeMu.Unlock()
}

// ModeMismatch returns true once the server's "hello" has been received and
// its mode differs from this client's.
func (c *Client) ModeMismatch() bool {
	c.modeMu.RLock()
	defer c.modeMu.RUnlock()
	return c.mismatched
}

// PeerMode returns the server's disclosed mode, or "" before its "hello"
// arrives.
func (c *Client) PeerMode() string {
	c.modeMu.RLock()
	defer c.modeMu.RUnlock()
	return c.peerMode
}

// noteServerMode records the server's disclosed mode and reports (via the
// registered handler, if any) when the mismatch verdict changes.
func (c *Client) noteServerMode(peerMode string) {
	c.modeMu.Lock()
	mismatched := peerMode != "" && peerMode != c.mode
	changed := c.peerMode != peerMode || c.mismatched != mismatched
	c.peerMode = peerMode
	c.mismatched = mismatched
	fn := c.mismatchHandler
	c.modeMu.Unlock()
	if changed && fn != nil {
		fn(mismatched, peerMode)
	}
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
		switch sm.Type {
		case "hello":
			c.noteServerMode(sm.Mode)
		case "command":
			if sm.Cmd == "" || c.ModeMismatch() {
				// A mismatched peer gets nothing beyond the heartbeat/hello
				// exchange above — see docs/DevMode.md.
				continue
			}
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
	mu         sync.RWMutex
	lastSeen   time.Time
	connected  bool
	state      string   // "remote-control" or "local-control"
	conn       net.Conn // nil when disconnected
	peerHost   string   // remote IP/host of the most recent connection
	peerMode   string   // ModeDev or ModeOps, learned from the client's messages
	mismatched bool      // true once peerMode is known and differs from the server's own mode
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

// setMode records the client's disclosed mode and reports whether the
// mismatch verdict (against selfMode) changed.
func (cs *connState) setMode(peerMode, selfMode string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	mismatched := peerMode != selfMode
	changed := cs.peerMode != peerMode || cs.mismatched != mismatched
	cs.peerMode = peerMode
	cs.mismatched = mismatched
	return changed
}

func (cs *connState) getMode() (peerMode string, mismatched bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.peerMode, cs.mismatched
}

// Server accepts representable TCP connections and tracks client health and state.
type Server struct {
	ln             net.Listener
	mode           string // this server's own dev-mode status (ModeDev or ModeOps)
	mu             sync.RWMutex
	states         map[string]*connState
	onState        func(name, state string)                          // called on state change or disconnect
	onLog          func(name, line, kind string)                     // called when client sends a log entry
	onData         func(name, dataType string, data json.RawMessage) // called when client sends a data message
	onModeMismatch func(name string, mismatched bool, peerMode string) // called when a client's mode mismatch verdict changes
}

// NewServer starts a TCP listener on addr and begins accepting connections.
// mode is this server's own dev-mode status (see Mode) — it is disclosed to
// every connecting client via a "hello" message so both sides can detect and
// react to a mode mismatch (see docs/DevMode.md).
func NewServer(addr, mode string) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{
		ln:     ln,
		mode:   mode,
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

// SetModeMismatchHandler registers fn to be called whenever a named client's
// mode-mismatch verdict (its dev-mode status vs. this server's own) changes,
// including the initial verdict once the client's first message arrives.
func (s *Server) SetModeMismatchHandler(fn func(name string, mismatched bool, peerMode string)) {
	s.mu.Lock()
	s.onModeMismatch = fn
	s.mu.Unlock()
}

// PeerMode returns the named client's disclosed dev-mode status, or "" if it
// is unknown (never connected, or no message received yet).
func (s *Server) PeerMode(name string) string {
	s.mu.RLock()
	cs, ok := s.states[name]
	s.mu.RUnlock()
	if !ok {
		return ""
	}
	mode, _ := cs.getMode()
	return mode
}

// ModeMismatch returns true if the named client's disclosed dev-mode status
// differs from this server's own.
func (s *Server) ModeMismatch(name string) bool {
	s.mu.RLock()
	cs, ok := s.states[name]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	_, mismatched := cs.getMode()
	return mismatched
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
			// Disclose our own mode right away so the client can flag a
			// mismatch before it has sent anything beyond this first message.
			json.NewEncoder(conn).Encode(ServerMsg{Type: "hello", Mode: s.mode}) //nolint:errcheck — best-effort
		}
		if m.Mode != "" {
			if changed := cs.setMode(m.Mode, s.mode); changed {
				_, mismatched := cs.getMode()
				s.mu.RLock()
				fn := s.onModeMismatch
				s.mu.RUnlock()
				if fn != nil {
					fn(clientName, mismatched, m.Mode)
				}
			}
		}
		_, mismatched := cs.getMode()
		switch m.Type {
		case "heartbeat":
			// Health/connectivity always flows, mismatch or not.
			cs.update()
		case "state":
			if mismatched {
				// Refuse anything beyond health exchange and the mismatch
				// disclosure above — see docs/DevMode.md.
				continue
			}
			cs.setState(m.State)
			s.mu.RLock()
			fn := s.onState
			s.mu.RUnlock()
			if fn != nil {
				fn(clientName, m.State)
			}
		case "log":
			if mismatched {
				continue
			}
			s.mu.RLock()
			fn := s.onLog
			s.mu.RUnlock()
			if fn != nil {
				fn(clientName, m.Line, m.Kind)
			}
		case "data":
			if mismatched {
				continue
			}
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
