package main

import (
	"embed"
	"encoding/json"
	"flag"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"representable"
	ufaconfig "ufa-configurable"
	ufahostid "ufa-hostid"
	"ufa-loader/restartsignal"
	ufaversion "ufa-version"
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
	// defaultACHTTPPort matches agent-coordinator's own "-port" flag default
	// (see agent-coordinator/main.go) -- distinct from defaultACPort above,
	// which is its representable port.
	defaultACHTTPPort = "8083"
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

// FCStateMsg is the payload of "fc-state" WebSocket messages. FC is the
// instance it's about (see fcinstances.go).
type FCStateMsg struct {
	FC    string `json:"fc,omitempty"`
	State string `json:"state"` // "remote-control", "local-control", or "" (disconnected)
}

// FCLogMsg is the payload of "fc-log" WebSocket messages.
type FCLogMsg struct {
	FC   string `json:"fc,omitempty"`
	Line string `json:"line"`
	Kind string `json:"kind,omitempty"` // "cmd" or "output"
}

// FCSessionMsg is the payload of federation-command's "fc-session" data
// message: the session it is currently on (see federation-command/main.go's
// sendSessionState). Not broadcast as its own WebSocket message type --
// setFCSessionState folds it into ProcInfo.Session on the next system-state
// (see procman.go), mirroring how a reported build version folds into
// ProcInfo.Version.
type FCSessionMsg struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`

	// InstanceID and Head are the stateboard's half of this message (see
	// stateboard.go's setFCHead): InstanceID is the LR-assigned instance id
	// this FC instance was launched with (FC_INSTANCE_ID -- see
	// managedApps["federation-command"].buildEnv), empty for an
	// independently-launched FC; Head is that instance's own self-generated
	// head ID (federation-command/main.go's fcHeadID). Both piggyback on
	// this already-frequent message rather than a dedicated one.
	InstanceID string `json:"instance_id,omitempty"`
	Head       string `json:"head,omitempty"`
}

// RidealongStateMsg is the payload of "ridealong-state" WebSocket messages.
type RidealongStateMsg struct {
	FC           string   `json:"fc,omitempty"` // the FC instance it's about -- set by LR
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
	FC        string `json:"fc,omitempty"` // the FC instance it's about -- set by LR
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
	// AutoConnect is the persistent auto-connect toggle (see Revision I of
	// Step3Prompt.md): true whenever the auto-connect cycle is armed, whether
	// or not it is currently connected/connecting -- it stays true across a
	// successful connection and only an explicit disconnect turns it off.
	AutoConnect bool `json:"auto_connect,omitempty"`
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

// SessionsStateMsg is the "sessions-state" payload: a managed session-manager
// pushes it to LR over representable, and LR forwards a copy up to
// agent-coordinator and out to browser clients so the forwarded 'sessions'
// view has the port to reverse-proxy from. Grows domain-specific fields in a
// later step (see condocs/InitialShellsSessionManagerAndTheConversationalist.md).
type SessionsStateMsg struct {
	HTTPPort string `json:"http_port"`
}

// ConvoStateMsg is the "convo-state" payload: a managed the-conversationalist
// pushes it to LR over representable, and LR forwards a copy up to
// agent-coordinator and out to browser clients so the forwarded 'convo' view
// has the port to reverse-proxy from. Grows domain-specific fields in a later
// step (see condocs/InitialShellsSessionManagerAndTheConversationalist.md).
type ConvoStateMsg struct {
	HTTPPort string `json:"http_port"`
}

// RobotStateMsg is the "robot-state" payload: a managed ianar pushes it to
// LR over representable, and LR forwards a copy up to agent-coordinator and
// out to browser clients so the forwarded 'robot' view has the port to
// reverse-proxy from. Grows domain-specific fields in a later step (see
// condocs/InitialRobot.md).
type RobotStateMsg struct {
	HTTPPort string `json:"http_port"`
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
	devMode    bool // --dev-mode: cascaded to every managed instance this LR launches — see docs/DevMode.md
	dev        bool // --dev: skip serving the embedded frontend (see setupRoutes) — also what the tunnel conn (see serveTunnel) serves

	// repoWatch is the dev-repo watcher (--dev-repo — see docs/DevMode.md and
	// repowatch.go); nil unless this LR was launched with --dev-repo.
	repoWatch *repoWatch

	modeMu         sync.RWMutex
	modeMismatches map[string]ModeMismatchMsg // peer name -> current mismatch disclosure, mismatched entries only

	// fcInst holds every connected federation-command instance, keyed by the
	// name it connected under -- see fcinstances.go.
	fcMu   sync.RWMutex
	fcInst map[string]*fcInstance

	// control runs the control tab's sequences -- see control.go.
	control *controlEngine

	acMu                sync.RWMutex
	acClient            *representable.Client
	acHost              string
	acPort              string
	acAutoConnecting    bool          // true while the --auto-connect retry loop is currently trying
	acAutoConnectCancel chan struct{} // closed to stop the auto-connect retry loop early
	// acAutoConnectEnabled is the persistent auto-connect toggle (see Revision
	// I of Step3Prompt.md) -- a first-class state independent of any single
	// connection attempt. It stays true across a successful connection, so a
	// later unintentional disconnect resumes the retry cycle on its own (see
	// connectAC); only an explicit disconnect (see disconnectAC) turns it off.
	acAutoConnectEnabled bool
	// acIntentionalDisconnect marks the next DisconnectCh close as
	// operator-driven (see disconnectAC) so connectAC's teardown can tell it
	// apart from the remote end dropping unexpectedly, which representable
	// itself does not distinguish (both close the same channel).
	acIntentionalDisconnect bool

	// loaderManaged is true when this process was launched by ufa-loader (see
	// restartsignal.IsLoaderManaged), i.e. when an operator-driven "restart"
	// can be expected to actually come back up rather than just stop.
	loaderManaged bool

	// selfVersion watches this process's own on-disk binary for a newer
	// build landing while it runs (see selfversion.go) so the system tab's
	// restart control can offer "update and restart". Only started when
	// loaderManaged, since that's the only case restart actually helps; nil
	// (and selfVersion.available() reports false) otherwise.
	selfVersion *selfVersionWatch

	// System tab: LR's own process plus any child applications it launches.
	heartbeatPort string                  // representable port, passed to launched children
	httpPort      string                  // LR's own dashboard HTTP port (reported to agent-coordinator)
	condoccerPort string                  // HTTP port a managed condoccer serves on / is proxied from
	condoccerRoot string                  // repo root a managed condoccer scans (empty: condoccer's default)
	sessionsPort  string                  // HTTP port a managed session-manager serves on / is proxied from
	convoPort     string                  // HTTP port a managed the-conversationalist serves on / is proxied from
	robotPort     string                  // HTTP port a managed ianar serves on / is proxied from
	selfStart     time.Time               // when this LR process started
	binOverrides  map[string]string       // app name -> explicit binary path (from config)
	terminalCmd   string                  // command prefix that hosts an interactive child in a terminal
	procMu        sync.Mutex
	managed       map[string]*managedProc // instance id -> running/finished child
	instanceSeq   map[string]int          // app name -> highest instance ordinal handed out

	// versionMu guards managedVersions: app name -> the build version most
	// recently reported over representable's "version" data message (see
	// procman.go). Keyed by app name, not instance id, mirroring how
	// representable itself only tracks one connection identity per app name
	// today (see reprServer.IsHealthy("federation-command")) -- multiple
	// instances of the same N-per-host app share one reported version.
	versionMu       sync.RWMutex
	managedVersions map[string]string

	// managedUpdateAvailable mirrors selfVersion.available() but per managed
	// sub-application: app name -> whether that app's on-disk binary now
	// answers "--version" differently than the version most recently
	// reported in managedVersions (see pollManagedVersions). Guarded by
	// versionMu alongside managedVersions since the two are always read and
	// compared together. Drives the topology view's per-sub-app halo (see
	// condocs/initialDistributedDevelopmentImpls/Step4Prompt.md Revision D).
	managedUpdateAvailable map[string]bool

	// managedPendingVersion mirrors managedUpdateAvailable but holds the
	// actual on-disk version string observed by pollManagedVersions (after
	// stripping any app-specific "--version" prefix, e.g.
	// federation-command's), rather than just a boolean -- so the system
	// tab's drill-down can show what version a rebuild/relaunch would pick
	// up, not merely that one is available. Guarded by versionMu alongside
	// the two maps above. Empty until pollManagedVersions has run at least
	// once for that app.
	managedPendingVersion map[string]string

	// fcSessionMu guards fcSessionID/fcSessionName: the session a
	// federation-command instance most recently reported over representable's
	// "fc-session" data message (see setFCSessionState in procman.go), folded
	// into ProcInfo.Session on the system tab. Keyed implicitly to the app
	// name "federation-command" like managedVersions above, for the same
	// reason. Deliberately *not* cleared when FC disconnects (unlike
	// ridealongState/condocState below) -- it has to survive the gap between
	// restartManaged's terminate and relaunch so the relaunch can pass it back
	// (see managedApps["federation-command"].buildArgs/buildEnv and
	// condocs/initialDistributedSessionsImpls/Step2Prompt.md Revision B).
	fcSessionMu   sync.RWMutex
	fcSessionID   string
	fcSessionName string

	// smSessionMu guards smSessionID: the session a managed session-manager
	// most recently reported over representable's generic "stateboard" data
	// message (App "session-manager", Key "current-session" -- see
	// session-manager/repr.go's pushCurrentSessionStateboard), mirroring
	// fcSessionID above. Deliberately *not* cleared when session-manager
	// disconnects (unlike the stateboard row itself, which setStateChangeHandler
	// does blank for display -- see stateboard's "session-manager" case) so it
	// survives the gap between restartManaged's terminate and relaunch, letting
	// managedApps["sessions"].buildArgs hand it straight back (see
	// condocs/initialDistributedSessionsImpls/Step2Prompt.md Revision H).
	smSessionMu sync.RWMutex
	smSessionID string

	// Latest condoc summary pushed up by a managed condoccer over representable.
	condoccerMu    sync.RWMutex
	condoccerState *CondoccerStateMsg

	// Latest state pushed up by a managed session-manager / the-conversationalist
	// / ianar over representable -- see condoccerState above.
	sessionsMu    sync.RWMutex
	sessionsState *SessionsStateMsg
	convoMu       sync.RWMutex
	convoState    *ConvoStateMsg
	robotMu       sync.RWMutex
	robotState    *RobotStateMsg

	// Files tab: where uploaded files land, and where "persist" moves them to
	// (see files.go). listFiles() scans these directly, so no further
	// mutex-guarded state is needed here.
	fileCacheDir string
	hostStoreDir string

	// Debug view (condocs/initialDistributedSessionsImpls/Step1SubstepBPrompt.md
	// Revision E): a rolling buffer of stdout/stderr lines from the LR-managed
	// sub-apps (see procman.go's lineLogWriter), capped at maxDebugLogEntries
	// so a chatty child can't grow this unbounded. Oldest-first; recordDebugLog
	// appends and trims under debugLogMu.
	debugLogMu sync.Mutex
	debugLog   []DebugLogEntry

	// Debug view's "network" tab (Step1SubstepBPrompt.md Revision F): a
	// rolling buffer of outbound HTTP calls made anywhere on the
	// SM<->LR<->AC chain -- this LR's own "lr->ac" hop (see sessions.go's
	// httpGetWithTimeout/fetchSessionFile) plus session-manager's "sm->lr"
	// hop, reported over representable as a "chain-call" data message
	// (see reprServer.SetDataHandler) -- capped at maxChainCallEntries.
	// Mirrors debugLogMu/debugLog above; recordChainCall appends and trims
	// under chainCallMu.
	chainCallMu sync.Mutex
	chainCall   []ChainCallEntry

	// Debug view's "stateboard" tab (condocs/initialDistributedSessionsImpls/
	// Step2Prompt.md Revision E): a generic key/value board any connected
	// sub-app can post custom entries to (see setStateboardKV, stateboard.go),
	// plus the default "present"/"hosts" rows derived live from
	// representable's own connection health for every app named in
	// stateboardApps. stateboardCustom is keyed by sub-app then key, matching
	// StateboardEntry's two-level nesting (Revision F). fcHeads holds
	// federation-command's self-reported head IDs, one per LR-launched
	// instance (see setFCHead) -- the only way to list them individually
	// despite representable tracking one connection identity per app name
	// (see versionMu's comment above).
	stateboardMu     sync.Mutex
	stateboardCustom map[string]map[string]string
	fcHeads          map[string]string // instance id -> federation-command's self-reported head ID

	// Session sync (see sessions.go and
	// docs/DistributedSessionsBrainstorm.md): recordsPath is where clauditable
	// session directories live (AGENT_RECORDS_PATH, resolved the same way
	// clauditable itself does); acHTTPPort is agent-coordinator's own HTTP
	// port (distinct from acPort above, which is its representable port),
	// used to reach both agent-coordinator's "/api/hosts" and its
	// "/host/<id>/*" transparent proxy when pulling another host's session
	// files. Both are set once at startup, unlike acHost/acPort, which can
	// change at runtime via the "connect to AC" widget.
	recordsPath string
	acHTTPPort  string

	// tcMu/tcAvailable hold the aggregate "is a the-conversationalist instance
	// available on any host" verdict, relayed down from agent-coordinator's
	// own aggregate -- see tcavailability.go and
	// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
	// Step2Prompt.md.
	tcMu        sync.RWMutex
	tcAvailable bool
}

func newServer(lrName string) *Server {
	s := &Server{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:                make(map[*wsClient]bool),
		lrName:                 lrName,
		selfStart:              time.Now(),
		binOverrides:           make(map[string]string),
		managed:                make(map[string]*managedProc),
		instanceSeq:            make(map[string]int),
		modeMismatches:         make(map[string]ModeMismatchMsg),
		managedVersions:        make(map[string]string),
		managedUpdateAvailable: make(map[string]bool),
		managedPendingVersion:  make(map[string]string),
		stateboardCustom:       make(map[string]map[string]string),
		fcHeads:                make(map[string]string),
		fcInst:                 make(map[string]*fcInstance),
	}
	s.control = newControlEngine(s)
	return s
}

// ModeMismatchMsg is the "mode-mismatch" WebSocket payload disclosing that a
// connected peer's dev-mode status differs from this LR's own (see
// docs/DevMode.md). peer is "agent-coordinator" for the uplink or a
// representable client name ("federation-command", "condoccer", ...) for a
// downlink. Mismatched=false clears a previously-disclosed mismatch.
type ModeMismatchMsg struct {
	Peer       string `json:"peer"`
	Mismatched bool   `json:"mismatched"`
	PeerMode   string `json:"peer_mode,omitempty"`
}

// setModeMismatch records peer's current mismatch verdict and broadcasts it
// to every connected browser client. Called from both the reprServer handler
// (federation-command/condoccer dialling in) and the acClient handler (this
// LR dialling out to agent-coordinator).
func (s *Server) setModeMismatch(peer string, mismatched bool, peerMode string) {
	s.modeMu.Lock()
	if mismatched {
		s.modeMismatches[peer] = ModeMismatchMsg{Peer: peer, Mismatched: true, PeerMode: peerMode}
	} else {
		delete(s.modeMismatches, peer)
	}
	s.modeMu.Unlock()
	s.broadcast("mode-mismatch", ModeMismatchMsg{Peer: peer, Mismatched: mismatched, PeerMode: peerMode})
}

// currentModeMismatches returns a snapshot of every peer currently disclosed
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

// currentStatus returns service statuses; federation-command reflects live heartbeat health.
func (s *Server) currentStatus() StatusMsg {
	fcStatus := "unhealthy"
	if s.anyFCHealthy() {
		fcStatus = "healthy"
	}
	condoccerStatus := "unhealthy"
	if s.reprServer != nil && s.reprServer.IsHealthy("condoccer") {
		condoccerStatus = "healthy"
	}
	sessionsStatus := "unhealthy"
	if s.reprServer != nil && s.reprServer.IsHealthy("sessions") {
		sessionsStatus = "healthy"
	}
	convoStatus := "unhealthy"
	if s.reprServer != nil && s.reprServer.IsHealthy("convo") {
		convoStatus = "healthy"
	}
	robotStatus := "unhealthy"
	if s.reprServer != nil && s.reprServer.IsHealthy("robot") {
		robotStatus = "healthy"
	}
	return StatusMsg{
		Services: []ServiceStatus{
			{Name: "federation-command", Status: fcStatus},
			{Name: "condoccer", Status: condoccerStatus},
			{Name: "convo", Status: convoStatus},
			{Name: "sessions", Status: sessionsStatus},
			{Name: "robot", Status: robotStatus},
			{Name: "worker", Status: "healthy"},
		},
	}
}

func (s *Server) getCondoccerState() *CondoccerStateMsg {
	s.condoccerMu.RLock()
	defer s.condoccerMu.RUnlock()
	return s.condoccerState
}

func (s *Server) getSessionsState() *SessionsStateMsg {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	return s.sessionsState
}

func (s *Server) getConvoState() *ConvoStateMsg {
	s.convoMu.RLock()
	defer s.convoMu.RUnlock()
	return s.convoState
}

func (s *Server) getRobotState() *RobotStateMsg {
	s.robotMu.RLock()
	defer s.robotMu.RUnlock()
	return s.robotState
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
		Connected:   s.acClient != nil,
		Host:        s.acHost,
		Port:        s.acPort,
		Connecting:  s.acAutoConnecting,
		AutoConnect: s.acAutoConnectEnabled,
	}
}

// acStateMsg builds an ac-state payload for an explicit host/port, stamping the
// current auto-connect retry status so the UI can show a "connecting…" hint even
// before connectAC has recorded the target on the Server.
func (s *Server) acStateMsg(connected bool, host, port string) ACStateMsg {
	s.acMu.RLock()
	connecting := s.acAutoConnecting
	enabled := s.acAutoConnectEnabled
	s.acMu.RUnlock()
	return ACStateMsg{Connected: connected, Host: host, Port: port, Connecting: connecting, AutoConnect: enabled}
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
	s.pushFCStateToAC()
	ac.SendData("control-state", s.control.state())
	ac.SendData("control-library", s.control.libraryForAC())
	// Which nodes a control sequence can reach -- see controlnodes.go.
	ac.SendData("control-nodes-request", struct{}{})
	ac.SendData("system-state", s.systemState())
	ac.SendData("repo-state", s.repoState())
	ac.SendData("lr-http", LRHTTPMsg{Port: s.httpPort})
	ac.SendData("files-state", FilesStateMsg{Files: s.listFiles()})
	ac.SendData("debug-log-state", s.debugLogState())
	ac.SendData("chain-call-state", s.chainCallState())
	ac.SendData("stateboard-state", s.stateboard())
	if cc := s.getCondoccerState(); cc != nil {
		ac.SendData("condoccer-state", *cc)
	}
	if sm := s.getSessionsState(); sm != nil {
		ac.SendData("sessions-state", *sm)
	}
	if cv := s.getConvoState(); cv != nil {
		ac.SendData("convo-state", *cv)
	}
	if rb := s.getRobotState(); rb != nil {
		ac.SendData("robot-state", *rb)
	}
}

// notifyCloseConn wraps a net.Conn so closing it -- as net/http does once the
// peer (agent-coordinator's proxy) disconnects -- also signals a
// singleConnListener, so its second Accept call returns instead of blocking
// forever. That lets http.Serve return from serveTunnel so it can redial a
// replacement tunnel.
type notifyCloseConn struct {
	net.Conn
	onClose func()
}

func (c *notifyCloseConn) Close() error {
	err := c.Conn.Close()
	c.onClose()
	return err
}

// singleConnListener is a net.Listener whose Accept hands back one
// pre-established connection exactly once, then blocks until Close -- just
// enough of net.Listener for http.Serve to run a full http.Server off a
// single tunnel connection (see serveTunnel) instead of a real listening
// socket.
type singleConnListener struct {
	conn   net.Conn
	used   bool
	closed chan struct{}
	once   sync.Once
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	l := &singleConnListener{closed: make(chan struct{})}
	signalClosed := func() { l.once.Do(func() { close(l.closed) }) }
	l.conn = &notifyCloseConn{Conn: conn, onClose: signalClosed}
	return l
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	if !l.used {
		l.used = true
		return l.conn, nil
	}
	<-l.closed
	return nil, io.EOF
}

func (l *singleConnListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// serveTunnel opens the standing tunnel connection a NAT'd local-representative
// needs (see representable.DialTunnel) and serves this LR's own HTTP mux over
// it, so agent-coordinator's proxy can reach this host without dialing back
// in -- the only direction guaranteed to work from behind NAT is the one LR
// already opened (see condocs/initialDistributedSessionsImpls/
// Step1SubstepBPrompt.md, Revision I). For this iteration there is exactly
// one such connection (N==1) parked at any moment, replacing
// agent-coordinator's old dial-out for this host entirely.
//
// A naive "redial only after the current one closes" loop starves every
// other proxied request for as long as the current tunnel stays claimed --
// which, for a long-lived use like the dashboard's own WebSocket (see
// handleWS), can be the entire lifetime of that browser tab. Revision J
// fixed this (see Resource 3, "Debug Sessions 2": the per-host dashboard
// showing "no sessions yet" and a false "not connected" footer, even though
// the remote-session fan-out -- a separate, one-shot HTTP call that doesn't
// compete for this tunnel -- had just successfully listed that same host's
// sessions): as soon as the standing tunnel is actually claimed and used,
// this immediately starts opening its replacement concurrently, rather than
// waiting for agent-coordinator to finish with (and close) the one just
// claimed.
//
// Revision J judged "claimed and used" by the first Read *attempt*, via a
// now-removed firstUseConn wrapper, regardless of whether that Read ever
// actually succeeded -- so a tunnel that gets parked and then torn down
// (agent-coordinator restarted, a stale registration got evicted, a NAT/
// firewall idle-reaped it) before any real request ever lands on it still
// counted as "used": http.Serve's very first attempt to parse a request off
// the dead conn fails instantly, firing that trigger on its way to an
// immediate "closed" below. With no delay anywhere on that path, a tunnel
// that keeps dying this way (see the architecture doc's "known gap" note on
// NAT-idle-reaping) reopened and redialed at native CPU speed -- "tunnel:
// opened"/"tunnel: closed (EOF)" pairs many times a second.
//
// Revision C tried to fix this by adding firstByteConn below, which only
// fires once a Read actually returns bytes, and gating a 2s backoff on
// whether it ever did -- but it left the *trigger that opens the next
// tunnel* wired to the old first-Read-attempt signal instead of switching it
// to firstByteConn too. That trigger called openNext() directly and
// unconditionally, and openNextOnce means whichever caller reaches it first
// wins -- so a dead conn's immediate, failed first Read still spawned the
// replacement instantly, and the backoff-gated openNext() call below arrived
// after the fact as a no-op. The busy loop was unchanged in practice.
//
// Revision D removes the separate first-attempt trigger entirely and drives
// both decisions -- "this tunnel is genuinely in use, open a concurrent
// replacement" and "this tunnel was used for real, so no backoff is needed"
// -- off the single firstByteConn.onFirstByte signal below: a tunnel that
// returns real data opens its replacement immediately (preserving the
// no-starvation behavior Revision J added), while a tunnel that dies
// without ever doing so only gets a replacement after the same 2s backoff
// as a failed dial.
func (s *Server) serveTunnel(addr string, devMode bool, stillCurrent func() bool) {
	// This goroutine serves exactly one tunnel connection per iteration, then
	// returns -- a replacement is always handed off to a freshly spawned
	// goroutine (see openNext below) rather than looped back onto here, so a
	// long AC outage retrying every 2s can't grow this call stack unbounded.
	for stillCurrent() {
		conn, err := representable.DialTunnel(addr, s.lrName, 5*time.Second)
		if err != nil {
			log.Printf("tunnel: failed to open to agent-coordinator at %s: %v", addr, err)
			time.Sleep(2 * time.Second)
			continue
		}
		log.Printf("tunnel: opened to agent-coordinator at %s", addr)

		var openNextOnce sync.Once
		var usedForReal atomic.Bool
		openNext := func() {
			openNextOnce.Do(func() {
				go s.serveTunnel(addr, devMode, stillCurrent)
			})
		}
		tracked := &firstByteConn{Conn: conn, onFirstByte: func() {
			usedForReal.Store(true)
			openNext()
		}}

		err = http.Serve(newSingleConnListener(tracked), s.setupRoutes(devMode))
		log.Printf("tunnel: closed (%v)", err)
		// If this tunnel closed (idle-reaped, or the AC process restarted)
		// before agent-coordinator ever claimed and used it, openNext above
		// never fired -- make sure a replacement still gets opened. Either
		// way, a replacement is now owned by another goroutine, so this one
		// is done.
		if !usedForReal.Load() {
			// Never carried a single real byte -- dialing a replacement
			// immediately would just repeat whatever killed this one, at
			// native CPU speed. Back off like a failed dial.
			time.Sleep(2 * time.Second)
		}
		openNext()
		return
	}
}

// firstByteConn wraps a net.Conn and invokes onFirstByte (at most once) the
// first time a Read actually returns data, as opposed to a Read merely being
// *attempted* -- a dead/reaped tunnel conn's first (and only) Read fails
// instantly with zero bytes, so it never fires. serveTunnel (see Revision D
// above) drives both "open the next tunnel concurrently" and "this tunnel
// was used for real, skip the backoff" off this single signal, so a tunnel
// that never carries real traffic can no longer trigger an instant,
// backoff-free redial of its replacement.
type firstByteConn struct {
	net.Conn
	once        sync.Once
	onFirstByte func()
}

func (c *firstByteConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.once.Do(c.onFirstByte)
	}
	return n, err
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

	client, err := representable.Connect(addr, s.lrName, representable.Mode(s.devMode), 5*time.Second)
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
	// (launch/terminate managed apps), "__control:" its control tab (see
	// control.go), and "__fc:<instance> <cmd>" goes to one FC instance (see
	// fcinstances.go); everything else is forwarded to the default FC
	// instance.
	client.SetCommandHandler(func(cmd string) {
		if strings.HasPrefix(cmd, "__system:") {
			s.handleSystemCommand(cmd)
			return
		}
		if strings.HasPrefix(cmd, "__tc-availability:") {
			s.handleTCAvailabilityCommand(cmd)
			return
		}
		if strings.HasPrefix(cmd, "__control:") {
			s.control.handleCommand(cmd)
			return
		}
		if rest, ok := strings.CutPrefix(cmd, "__fc:"); ok {
			key, fcCmd, _ := strings.Cut(rest, " ")
			if isFCName(key) && fcCmd != "" {
				s.sendFCCommand(key, fcCmd)
			}
			return
		}
		s.sendFCCommand("", cmd)
	})
	client.SetModeMismatchHandler(func(mismatched bool, peerMode string) {
		s.setModeMismatch("agent-coordinator", mismatched, peerMode)
	})

	// Open this connection's standing tunnel (see serveTunnel) -- the second,
	// plain socket agent-coordinator's proxy now reaches this host through,
	// instead of dialing back in.
	go s.serveTunnel(addr, s.dev, func() bool {
		s.acMu.RLock()
		defer s.acMu.RUnlock()
		return s.acClient == client
	})

	s.pushStateToAC()
	s.broadcast("ac-state", s.acStateMsg(true, host, port))
	log.Printf("connected to agent-coordinator at %s", addr)

	// Block until the connection drops (either remotely or via Close).
	<-client.DisconnectCh()

	s.acMu.Lock()
	current := s.acClient == client
	if current {
		s.acClient = nil
	}
	intentional := s.acIntentionalDisconnect
	if current {
		s.acIntentionalDisconnect = false
	}
	s.acMu.Unlock()

	if !current {
		// This connection was already superseded by a newer connect attempt
		// (e.g. an explicit connect to a different target, which closes
		// whatever was live first) -- that attempt owns the current
		// ac-state and any resume decision, so there's nothing left to
		// report for this one.
		return
	}

	log.Printf("disconnected from agent-coordinator at %s", addr)
	s.control.setNodes(nil) // no nodes to choose among until it reconnects
	s.broadcast("ac-state", s.acStateMsg(false, host, port))
	s.setModeMismatch("agent-coordinator", false, "")

	if !intentional && s.acAutoConnectEnabled {
		// Auto-connect is still enabled and this wasn't an operator-driven
		// disconnect -- the cycle begins automatically again at the same
		// target (see Revision I of Step3Prompt.md: "the auto-connect cycle
		// begins automatically upon unintentional disconnection").
		log.Printf("auto-connect: connection to agent-coordinator dropped unexpectedly -- resuming the retry cycle")
		s.startAutoConnectAC(host, port)
	}
}

// disconnectAC closes the AC connection; the connectAC goroutine handles
// cleanup. Being operator-driven, this is always an *intentional* disconnect,
// which (per Revision I of Step3Prompt.md) terminates auto-connect entirely:
// it marks the drop so connectAC's teardown won't resume the retry cycle, and
// clears the persistent enabled flag so the toggle reports off afterward.
func (s *Server) disconnectAC() {
	s.acMu.Lock()
	client := s.acClient
	s.acAutoConnectEnabled = false
	if client != nil {
		s.acIntentionalDisconnect = true
	}
	s.acMu.Unlock()
	if client != nil {
		client.Close()
	}
}

// startAutoConnectAC arms the persistent auto-connect toggle and launches the
// background agent-coordinator retry loop unless one is already running.
func (s *Server) startAutoConnectAC(host, port string) {
	s.acMu.Lock()
	s.acAutoConnectEnabled = true
	if s.acAutoConnectCancel != nil {
		s.acMu.Unlock()
		return
	}
	cancel := make(chan struct{})
	s.acAutoConnectCancel = cancel
	s.acMu.Unlock()
	go s.autoConnectAC(host, port, cancel)
}

// setAutoConnectAC drives the auto-connect toggle from an explicit UI/API
// action (see the "set-auto-connect-ac" WebSocket message): enabling it arms
// the persistent state and, unless already connected, (re)starts the
// background retry loop at host/port (falling back to the last-used target,
// then defaultACHost/defaultACPort, when unset). Disabling it only stops a
// retry in progress -- it does not drop an existing connection; only an
// explicit disconnect (see disconnectAC) does that, and disconnect already
// clears this flag on its own.
func (s *Server) setAutoConnectAC(enabled bool, host, port string) {
	s.acMu.Lock()
	s.acAutoConnectEnabled = enabled
	if host == "" {
		host = s.acHost
	}
	if host == "" {
		host = defaultACHost
	}
	if port == "" {
		port = s.acPort
	}
	if port == "" {
		port = defaultACPort
	}
	connected := s.acClient != nil
	s.acMu.Unlock()

	if !enabled {
		s.stopAutoConnectAC()
	} else if !connected {
		s.startAutoConnectAC(host, port)
	}
	s.broadcast("ac-state", s.acStateMsg(connected, host, port))
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
		s.sendToClient(c, "fc-instances", s.fcInstances())
		s.sendToClient(c, "control-state", s.control.state())
		s.sendToClient(c, "control-library", s.control.library())
		s.sendToClient(c, "ac-state", s.getACState())
		s.sendToClient(c, "system-state", s.systemState())
		s.sendToClient(c, "repo-state", s.repoState())
		s.sendToClient(c, "files-state", FilesStateMsg{Files: s.listFiles()})
		s.sendToClient(c, "debug-log-state", s.debugLogState())
		s.sendToClient(c, "chain-call-state", s.chainCallState())
		s.sendToClient(c, "stateboard-state", s.stateboard())
		if cc := s.getCondoccerState(); cc != nil {
			s.sendToClient(c, "condoccer-state", *cc)
		}
		if sm := s.getSessionsState(); sm != nil {
			s.sendToClient(c, "sessions-state", *sm)
		}
		if cv := s.getConvoState(); cv != nil {
			s.sendToClient(c, "convo-state", *cv)
		}
		if rb := s.getRobotState(); rb != nil {
			s.sendToClient(c, "robot-state", *rb)
		}
		s.sendToClient(c, "tc-availability", TCAvailabilityMsg{Available: s.getTCAvailability()})
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
		switch m.Type {
		case "command":
			// fc names the instance to run it on (see fcinstances.go); empty
			// sends it to the default instance.
			var payload struct {
				Cmd string `json:"cmd"`
				FC  string `json:"fc"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil && payload.Cmd != "" {
				s.sendFCCommand(payload.FC, payload.Cmd)
			}
		case "ridealong-command":
			var payload struct {
				Action string `json:"action"`
				FC     string `json:"fc"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil && payload.Action != "" {
				s.sendFCCommand(payload.FC, "__ridealong:"+payload.Action)
			}
		case "control-run":
			var payload struct {
				Sequence string            `json:"sequence"`
				Controls map[string]string `json:"controls"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil {
				if err := s.control.start(payload.Sequence, payload.Controls); err != nil {
					log.Printf("control-run %q: %v", payload.Sequence, err)
					s.sendToClient(c, "control-reply", ControlLibReply{Op: "run", Error: err.Error()})
				}
			}
		case "control-cancel":
			s.control.cancel()
		case "control-continue":
			// The continue button on a step waiting for the user (ask-user).
			var payload struct {
				Run string `json:"run"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil {
				if err := s.control.continueRun(payload.Run); err != nil {
					log.Printf("control-continue: %v", err)
				}
			}
		case "control-lib":
			// The definer's and composer's edits, imports and exports -- see
			// controllib.go.
			var req ControlLibRequest
			if err := json.Unmarshal(m.Payload, &req); err == nil {
				s.sendToClient(c, "control-reply", s.control.handleLibRequest(req))
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
		case "set-auto-connect-ac":
			var payload struct {
				Enabled bool   `json:"enabled"`
				Host    string `json:"host,omitempty"`
				Port    string `json:"port,omitempty"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil {
				s.setAutoConnectAC(payload.Enabled, payload.Host, payload.Port)
			}
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
		case "restart-app":
			s.requestRestart("operator")
		case "rebuild-app":
			s.requestRebuild("operator")
		case "set-auto-rebuild":
			var payload struct {
				Enabled bool `json:"enabled"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil {
				s.setAutoRebuild(payload.Enabled)
			}
		case "set-auto-update":
			var payload struct {
				Enabled bool `json:"enabled"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil {
				s.setAutoUpdate(payload.Enabled)
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

// proxyToSessions reverse-proxies /sessions/* to a managed session-manager's
// HTTP server on loopback, stripping the /sessions prefix -- mirrors
// proxyToCondoccer.
func (s *Server) proxyToSessions(w http.ResponseWriter, r *http.Request) {
	port := s.sessionsPort
	if sm := s.getSessionsState(); sm != nil && sm.HTTPPort != "" {
		port = sm.HTTPPort // trust the port session-manager actually reported
	}
	if port == "" {
		http.Error(w, "session-manager port unknown on this host", http.StatusBadGateway)
		return
	}
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + port}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/sessions")
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "session-manager not reachable on this host: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// proxyToConvo reverse-proxies /convo/* to a managed the-conversationalist's
// HTTP server on loopback, stripping the /convo prefix -- mirrors
// proxyToCondoccer.
func (s *Server) proxyToConvo(w http.ResponseWriter, r *http.Request) {
	port := s.convoPort
	if cv := s.getConvoState(); cv != nil && cv.HTTPPort != "" {
		port = cv.HTTPPort // trust the port the-conversationalist actually reported
	}
	if port == "" {
		http.Error(w, "the-conversationalist port unknown on this host", http.StatusBadGateway)
		return
	}
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + port}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/convo")
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "the-conversationalist not reachable on this host: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// proxyToRobot reverse-proxies /robot/* to a managed ianar's HTTP server on
// loopback, stripping the /robot prefix -- mirrors proxyToCondoccer.
func (s *Server) proxyToRobot(w http.ResponseWriter, r *http.Request) {
	port := s.robotPort
	if rb := s.getRobotState(); rb != nil && rb.HTTPPort != "" {
		port = rb.HTTPPort // trust the port ianar actually reported
	}
	if port == "" {
		http.Error(w, "ianar port unknown on this host", http.StatusBadGateway)
		return
	}
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + port}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/robot")
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "ianar not reachable on this host: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) setupRoutes(devMode bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/condoccer/", s.proxyToCondoccer)
	mux.HandleFunc("/sessions/", s.proxyToSessions)
	mux.HandleFunc("/convo/", s.proxyToConvo)
	mux.HandleFunc("/robot/", s.proxyToRobot)
	mux.HandleFunc("/api/files", s.handleFilesAPI)
	mux.HandleFunc("/api/files/", s.handleFileItem)
	mux.HandleFunc("/api/sessions", s.handleSessionsIndex)
	mux.HandleFunc("/api/sessions/", s.handleSessionsAPI)

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
	devMode       bool // --dev-mode: this instance (and everything it launches) runs from an in-progress branch — see docs/DevMode.md. Distinct from dev, which just skips serving the embedded frontend.
	devRepo       bool // --dev-repo: implies devMode and watches the launch working directory's git repo for rebuild-worthy changes — see docs/DevMode.md and repowatch.go.
	autoConnect   bool
	acHost        string
	acPort        string
	acHTTPPort    string   // agent-coordinator's HTTP port -- see Server.acHTTPPort
	autoLaunch    []string // child applications to launch on startup ("app" or "app:N" tokens)
	fcBin         string   // explicit path to the federation-command binary
	terminal      string   // command prefix used to host an interactive child in a terminal
	condoccerPort string   // HTTP port a managed condoccer serves on / is reverse-proxied from
	condoccerRoot string   // repo root a managed condoccer scans (empty: condoccer's default)
	sessionsPort  string   // HTTP port a managed session-manager serves on / is reverse-proxied from
	convoPort     string   // HTTP port a managed the-conversationalist serves on / is reverse-proxied from
	robotPort     string   // HTTP port a managed ianar serves on / is reverse-proxied from
	fileCacheDir  string   // directory uploaded files land in for the files tab
	hostStoreDir  string   // directory a "persist" press moves a file into
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
		acHTTPPort:    pick("ac-http-port", defaults.acHTTPPort),
		autoLaunch:    splitList(pick("auto-launch", strings.Join(defaults.autoLaunch, ","))),
		fcBin:         pick("fc-bin", defaults.fcBin),
		terminal:      pick("terminal", defaults.terminal),
		condoccerPort: pick("condoccer-port", defaults.condoccerPort),
		condoccerRoot: pick("condoccer-root", defaults.condoccerRoot),
		sessionsPort:  pick("sessions-port", defaults.sessionsPort),
		convoPort:     pick("convo-port", defaults.convoPort),
		robotPort:     pick("robot-port", defaults.robotPort),
		fileCacheDir:  pick("file-cache-dir", defaults.fileCacheDir),
		hostStoreDir:  pick("host-store-dir", defaults.hostStoreDir),
	}
	var err error
	if out.dev, err = pickBool("dev", defaults.dev); err != nil {
		return out, err
	}
	if out.devMode, err = pickBool("dev-mode", defaults.devMode); err != nil {
		return out, err
	}
	if out.devRepo, err = pickBool("dev-repo", defaults.devRepo); err != nil {
		return out, err
	}
	if out.autoConnect, err = pickBool("auto-connect", defaults.autoConnect); err != nil {
		return out, err
	}
	return out, nil
}

// announceRestartAndExit writes the restartsignal announcement (the
// "structured section after an identifying banner" a ufa-loader wrapping
// this process watches stdout for — see ufa-loader/README.md and
// docs/DevMode.md), attaching this process's live state (see
// lrState/currentState and Revision D of
// condocs/initialDistributedDevelopmentImpls/Step3Prompt.md) for the
// instance replacing it to pick back up, and exits 0. As its own final act
// it also terminates every LR-launched managed sub-app still running (see
// terminateManagedForRestart) -- the state snapshot is taken first so the
// announcement's ManagedApps reflects what was actually running rather than
// racing the termination it triggers (see Step4Prompt.md Revision I).
// Callers are expected to have already decided a restart is appropriate;
// this never returns.
func (s *Server) announceRestartAndExit(reason string) {
	log.Printf("announcing a restart (%s) and exiting", reason)
	st := s.currentState()
	s.terminateManagedForRestart()
	if err := restartsignal.AnnounceState(os.Stdout, "local-representative", reason, st); err != nil {
		log.Printf("restartsignal.AnnounceState: %v", err)
	}
	os.Exit(0)
}

// watchRestartSignal blocks waiting for SIGHUP and, on receipt, announces a
// restart as this process's final act before exiting 0. The signal is sent
// directly to this process's own pid (e.g. `kill -HUP <pid>`), not through
// ufa-loader itself: ufa-loader only watches stdout, it doesn't originate
// the restart trigger. Unlike requestRestart, this always restarts — a bare
// `kill -HUP` without a wrapping ufa-loader is documented to still announce
// and exit, just with nothing there to relaunch it (see README.md). Run in
// its own goroutine; never returns.
func (s *Server) watchRestartSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	for range sigCh {
		s.announceRestartAndExit("sighup")
	}
}

// requestRestart handles an operator-driven restart request — the system
// tab's "restart" control (WebSocket "restart-app") or agent-coordinator's
// "__system:restart" — which terminates this process such that a wrapping
// ufa-loader relaunches it with the same config, plus this instance's live
// state carried forward (see announceRestartAndExit/lrState). Unlike
// SIGHUP, this refuses (logging why) when the process isn't loaderManaged:
// the system tab greys the control out in that case since pressing it
// wouldn't come back up, and this is the server-side enforcement of that
// same guard for any caller (e.g. agent-coordinator) that bypasses the UI.
func (s *Server) requestRestart(reason string) {
	if !s.loaderManaged {
		log.Printf("restart requested (%s) but this process is not loader-managed (no %s) — ignoring", reason, restartsignal.InitEnvVar)
		return
	}
	s.announceRestartAndExit(reason)
}

func main() {
	if ufaversion.HandleVersionFlag() {
		return
	}

	defaultName := ufahostid.GetHostID()

	flag.Bool("version", false, "print version and exit (checked ahead of every other flag; see the HandleVersionFlag call above)")
	configDir := flag.String("config", "", "directory holding ufa-configurable YAML files (default ~/.ufa/config)")
	port := flag.String("port", "8081", "HTTP port to listen on")
	reprPort := flag.String("repr-port", "8082", "TCP port for representable heartbeat server")
	name := flag.String("name", defaultName, "name used to identify this LR to agent-coordinator")
	dev := flag.Bool("dev", false, "dev mode: skip serving frontend static files")
	devMode := flag.Bool("dev-mode", false, "dev mode (SDLC sense, see docs/DevMode.md): launched from an in-progress branch. Cascades to every federation-command/condoccer instance this LR launches. Unrelated to --dev.")
	devRepo := flag.Bool("dev-repo", false, "dev mode plus watch the current working directory's git repo for changes to rebuild from (see docs/DevMode.md); implies --dev-mode; requires running inside a git repository")
	autoConnect := flag.Bool("auto-connect", false, "dial agent-coordinator in the background on startup, retrying every 10s for up to 10m")
	acHost := flag.String("ac-host", defaultACHost, "agent-coordinator host/IP to auto-connect to")
	acPort := flag.String("ac-port", defaultACPort, "agent-coordinator port to auto-connect to")
	acHTTPPort := flag.String("ac-http-port", defaultACHTTPPort, "agent-coordinator HTTP port (for session-file pulls via its /host/<id>/* proxy and /api/hosts -- see sessions.go)")
	autoLaunch := flag.String("auto-launch", "", "comma/space-separated child applications to launch on startup; each token is \"app\" or \"app:N\" (e.g. federation-command:2)")
	fcBin := flag.String("fc-bin", "", "path to the federation-command binary (default: search next to LR, the dev bin dir, then PATH)")
	terminal := flag.String("terminal", "", "command prefix used to host federation-command in a terminal (e.g. \"xterm -e\" or \"tmux new-session -d -s fc\"); default: autodetect")
	condoccerPort := flag.String("condoccer-port", "8080", "HTTP port a managed condoccer serves on; its UI is reverse-proxied at /condoccer/")
	condoccerRoot := flag.String("condoccer-root", "", "repo root a managed condoccer scans (default: condoccer's own -root default)")
	sessionsPort := flag.String("sessions-port", "8085", "HTTP port a managed session-manager serves on; its UI is reverse-proxied at /sessions/")
	convoPort := flag.String("convo-port", "8086", "HTTP port a managed the-conversationalist serves on; its UI is reverse-proxied at /convo/")
	robotPort := flag.String("robot-port", "8087", "HTTP port a managed ianar serves on; its UI is reverse-proxied at /robot/")
	fileCacheDir := flag.String("file-cache-dir", defaultFileCacheDir, "directory uploaded files land in for the files tab; files older than 1 hour are swept")
	hostStoreDir := flag.String("host-store-dir", defaultHostStoreDir, "directory the file-details dialog's \"persist\" button moves a file into; never swept")
	controlLibPath := flag.String("control-library", defaultControlLibraryPath(), "YAML file the control tab's actions and sequences are kept in (see controllib.go); empty keeps them in memory only")
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
		devMode:       *devMode,
		devRepo:       *devRepo,
		autoConnect:   *autoConnect,
		acHost:        *acHost,
		acPort:        *acPort,
		acHTTPPort:    *acHTTPPort,
		autoLaunch:    splitList(*autoLaunch),
		fcBin:         *fcBin,
		terminal:      *terminal,
		condoccerPort: *condoccerPort,
		condoccerRoot: *condoccerRoot,
		sessionsPort:  *sessionsPort,
		convoPort:     *convoPort,
		robotPort:     *robotPort,
		fileCacheDir:  *fileCacheDir,
		hostStoreDir:  *hostStoreDir,
	})
	if err != nil {
		log.Fatal(err)
	}
	if cfg.devRepo {
		// --dev-repo automatically sets dev-mode too — see docs/DevMode.md.
		cfg.devMode = true
	}

	// A restart-carrying relaunch (see reststate.go and Revision D of
	// condocs/initialDistributedDevelopmentImpls/Step3Prompt.md) overrides
	// the auto-connect and auto-launch settings just resolved above -- the
	// live state as of the moment the prior instance asked to be restarted
	// wins over whatever flags/config this launch happens to carry, which is
	// also how the LR-launched managed sub-apps that were running right
	// before the restart (terminated as that instance's final act -- see
	// terminateManagedForRestart) come back: prevState.ManagedApps feeds
	// cfg.autoLaunch below, so the ordinary auto-launch path (further down)
	// relaunches them -- Step4Prompt.md Revision I. prevState/havePrevState
	// is also consulted below once repoWatch/selfVersion exist, for the
	// auto-rebuild and auto-update halves.
	prevState, havePrevState := loadPreviousState()
	if havePrevState {
		log.Printf("restart state: restoring auto-rebuild=%v auto-update=%v auto-connect=%v (ac=%s:%s) managed-apps=%v from before the restart",
			prevState.AutoRebuild, prevState.AutoUpdate, prevState.AutoConnect, prevState.ACHost, prevState.ACPort, prevState.ManagedApps)
		prevState.applyToConfig(&cfg)
	}

	s := newServer(cfg.name)
	s.control.lib = openControlLibrary(*controlLibPath)
	s.loaderManaged = restartsignal.IsLoaderManaged()
	if s.loaderManaged {
		// Only worth polling for an on-disk update when a restart could
		// actually pick it up -- see selfversion.go. restart wires the
		// "auto-update" toggle to the same requestRestart an operator's
		// "update and restart" button drives, per
		// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision E.
		s.selfVersion = newSelfVersionWatch(ufaversion.Version, s.broadcastSystemState, func() { s.requestRestart("auto-update") })
		if s.selfVersion != nil {
			if havePrevState && prevState.AutoUpdate {
				s.selfVersion.setAutoUpdate(true)
			}
			go s.selfVersion.watchLoop()
		}
	}
	s.devMode = cfg.devMode
	s.dev = cfg.dev
	if s.devMode {
		// Per-sub-application out-of-date detection (see pollManagedVersions)
		// is, like the rest of this dev/ops-mode-gated feature set, only
		// meaningful for a dev workflow -- see
		// condocs/initialDistributedDevelopmentImpls/Step4Prompt.md Revision C/D.
		go s.watchManagedVersions()
	}
	s.heartbeatPort = cfg.heartbeatPort
	s.httpPort = cfg.httpPort
	s.condoccerPort = cfg.condoccerPort
	s.condoccerRoot = cfg.condoccerRoot
	s.sessionsPort = cfg.sessionsPort
	s.convoPort = cfg.convoPort
	s.robotPort = cfg.robotPort
	s.terminalCmd = cfg.terminal
	s.fileCacheDir = cfg.fileCacheDir
	s.hostStoreDir = cfg.hostStoreDir
	s.acHTTPPort = cfg.acHTTPPort
	s.recordsPath = resolveRecordsPath()
	if cfg.fcBin != "" {
		s.binOverrides["federation-command"] = cfg.fcBin
	}
	if err := ensureFileCacheDir(s.fileCacheDir); err != nil {
		log.Fatal("file cache dir: ", err)
	}
	if err := ensureFileCacheDir(s.hostStoreDir); err != nil {
		log.Fatal("host store dir: ", err)
	}
	go s.cleanupFilesLoop()

	if cfg.devRepo {
		root, err := repoRootFromCWD()
		if err != nil {
			log.Fatalf("--dev-repo requires running inside a git repository: %v", err)
		}
		s.repoWatch = newRepoWatch(root, s.broadcastRepoState)
		log.Printf("dev-repo: watching %s for changes to rebuild from (see docs/DevMode.md)", root)
		if havePrevState && prevState.AutoRebuild {
			s.repoWatch.setAutoRebuild(true)
		}
		go s.repoWatch.watchLoop()
	}

	reprSrv, err := representable.NewServer(":"+cfg.heartbeatPort, representable.Mode(s.devMode))
	if err != nil {
		log.Fatal("representable server:", err)
	}
	// Disclose our own HTTP port to every connecting client (federation-command,
	// condoccer, ...) so a sub-app that only knows our representable dial
	// address can still reach our HTTP API directly -- see
	// condocs/initialDistributedDevelopmentImpls/Step5SubstepRPrompt.md,
	// Revision A.
	reprSrv.SetHTTPPort(s.httpPort)
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
				s.setModeMismatch("condoccer", false, "")
			}
			return
		}
		if name == "sessions" {
			if state == "disconnected" {
				s.sessionsMu.Lock()
				s.sessionsState = nil
				s.sessionsMu.Unlock()
				empty := SessionsStateMsg{}
				s.broadcast("sessions-state", empty)
				if ac := s.getACClient(); ac != nil {
					ac.SendData("sessions-state", empty)
				}
				s.setModeMismatch("sessions", false, "")
				// Clear its stateboard row rather than leave a stale session
				// id up once session-manager itself is gone.
				s.setStateboardKV("session-manager", "current-session", "")
			}
			return
		}
		if name == "convo" {
			if state == "disconnected" {
				s.convoMu.Lock()
				s.convoState = nil
				s.convoMu.Unlock()
				empty := ConvoStateMsg{}
				s.broadcast("convo-state", empty)
				if ac := s.getACClient(); ac != nil {
					ac.SendData("convo-state", empty)
				}
				s.setModeMismatch("convo", false, "")
			}
			return
		}
		if name == "robot" {
			if state == "disconnected" {
				s.robotMu.Lock()
				s.robotState = nil
				s.robotMu.Unlock()
				empty := RobotStateMsg{}
				s.broadcast("robot-state", empty)
				if ac := s.getACClient(); ac != nil {
					ac.SendData("robot-state", empty)
				}
				s.setModeMismatch("robot", false, "")
			}
			return
		}
		if isFCName(name) {
			// Each FC instance connects under its own name, so this is one
			// instance's state change or disconnect -- see fcinstances.go.
			s.setFCInstanceState(name, state)
		}
	})

	// Disclose a dev/ops mode mismatch with a connecting FC or condoccer — see
	// docs/DevMode.md. Both still exchange heartbeats; representable itself
	// refuses their state/log/data traffic while mismatched.
	reprSrv.SetModeMismatchHandler(func(name string, mismatched bool, peerMode string) {
		s.setModeMismatch(name, mismatched, peerMode)
	})

	reprSrv.SetLogHandler(func(name, line, kind string) {
		if isFCName(name) {
			s.handleFCLog(name, line, kind)
		}
	})

	reprSrv.SetDataHandler(func(name, dataType string, data json.RawMessage) {
		if dataType == "stateboard" {
			// Generic across every app name, like "version" above -- any
			// connected sub-app can post any key, which is the point of the
			// stateboard (see stateboard.go's setStateboardKV and
			// condocs/initialDistributedSessionsImpls/Step2Prompt.md
			// Revision E).
			var payload StateboardEntry
			if err := json.Unmarshal(data, &payload); err == nil {
				s.setStateboardKV(payload.App, payload.Key, payload.Value)
				if payload.App == "session-manager" && payload.Key == "current-session" && payload.Value != "" {
					// Remembered separately from the stateboard row itself
					// (which gets blanked for display on disconnect) so a
					// relaunch can hand it back -- see smSessionID's comment
					// and managedApps["sessions"].buildArgs.
					s.smSessionMu.Lock()
					s.smSessionID = payload.Value
					s.smSessionMu.Unlock()
				}
			}
			return
		}
		if dataType == "version" {
			// Every managed app reports its build version once it connects
			// (see docs/DevMode.md "Versioning") -- generic across app names
			// so any future adopter gets it for free, unlike the
			// per-app-name dispatch below.
			var payload VersionMsg
			if err := json.Unmarshal(data, &payload); err == nil && payload.Version != "" {
				app := name
				if isFCName(name) {
					app = fcAppName // versions are tracked per app, not per instance
				}
				s.setManagedVersion(app, payload.Version)
			}
			if name == "condoccer" {
				// condoccer has never received a tc-availability command
				// before this (re)connection -- push the current aggregate
				// now, even if it hasn't changed since before condoccer
				// dropped (setTCAvailability only re-pushes on a change).
				s.sendTCAvailabilityToCondoccer()
			}
			return
		}
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
		if name == "sessions" {
			if dataType == "sessions-state" {
				var payload SessionsStateMsg
				if err := json.Unmarshal(data, &payload); err == nil {
					s.sessionsMu.Lock()
					s.sessionsState = &payload
					s.sessionsMu.Unlock()
					s.broadcast("sessions-state", payload)
					if ac := s.getACClient(); ac != nil {
						ac.SendData("sessions-state", payload)
					}
				}
			} else if dataType == "chain-call" {
				// session-manager's own report of its "sm->lr" hop (see
				// repr.go's reportChainCall) -- folded into the same buffer
				// as this LR's "lr->ac" hop, see recordChainCall.
				var payload ChainCallEntry
				if err := json.Unmarshal(data, &payload); err == nil {
					s.recordChainCall(payload)
				}
			}
			return
		}
		if name == "convo" {
			if dataType == "convo-state" {
				var payload ConvoStateMsg
				if err := json.Unmarshal(data, &payload); err == nil {
					s.convoMu.Lock()
					s.convoState = &payload
					s.convoMu.Unlock()
					s.broadcast("convo-state", payload)
					if ac := s.getACClient(); ac != nil {
						ac.SendData("convo-state", payload)
					}
				}
			}
			return
		}
		if name == "robot" {
			switch dataType {
			case "robot-state":
				var payload RobotStateMsg
				if err := json.Unmarshal(data, &payload); err == nil {
					s.robotMu.Lock()
					s.robotState = &payload
					s.robotMu.Unlock()
					s.broadcast("robot-state", payload)
					if ac := s.getACClient(); ac != nil {
						ac.SendData("robot-state", payload)
					}
				}
			case "robot-run-progress", "robot-run-result":
				// A run the control tab asked for -- see control.go.
				var payload RobotRunMsg
				if err := json.Unmarshal(data, &payload); err == nil {
					s.control.noteRobotRun(dataType == "robot-run-result", payload)
				}
			case "robot-record-result":
				// A control sequence's screen recording -- see control.go.
				var payload RobotRecordMsg
				if err := json.Unmarshal(data, &payload); err == nil {
					s.control.noteRobotRecord(payload)
				}
			case "robot-capture-result":
				// A control sequence's screenshot of this node -- see
				// controlnodes.go.
				var payload RobotCaptureMsg
				if err := json.Unmarshal(data, &payload); err == nil {
					s.control.noteRobotCapture(payload)
				}
			case "robot-ops":
				// The ops the control tab's definer offers as robot.<op> --
				// see controlops.go.
				var payload struct {
					Ops []ControlOpSpec `json:"ops"`
				}
				if err := json.Unmarshal(data, &payload); err == nil {
					s.control.setRobotOps(payload.Ops)
				}
			}
			return
		}
		if !isFCName(name) {
			return
		}
		switch dataType {
		case "ridealong-state":
			var payload RidealongStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				s.setFCInstanceRidealong(name, payload)
			}
		case "condoc-state":
			var payload CondocStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				s.setFCInstanceCondoc(name, payload)
			}
		case "fc-session":
			var payload FCSessionMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				s.setFCSessionState(payload.ID, payload.Name)
				s.setFCHead(payload.InstanceID, payload.Head)
				s.setFCInstanceSession(name, payload)
			}
		}
	})

	log.Printf("representable server listening on tcp://localhost:%s", cfg.heartbeatPort)

	go s.watchRestartSignal()
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
