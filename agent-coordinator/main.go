package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
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
	ufahostid "ufa-hostid"
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

// hostDialTimeout bounds how long proxyToHost's reverse proxy and
// handleFileUploadRelay's outbound request will wait to establish the TCP
// connection to a peer host's local-representative. Without it, both ride
// http.DefaultTransport's 30s dial timeout -- far longer than every caller
// that sits on top of this proxy budgets for the whole round trip
// (local-representative's sessionsDiscoverHTTPTimeout is 2s,
// sessionsPullHTTPTimeout 5s; federation-command's and session-manager's own
// end-to-end discovery timeout 8s). Against an unreachable host (firewalled,
// an unpublished container port, offline) every one of those callers was
// already giving up on its own side -- "sessions discover: indexing host
// ...: context deadline exceeded" -- while AC's dial kept running uselessly
// in the background, and a human navigating straight to that host's
// dashboard through AC sat on a spinner for up to 30s before seeing "not
// reachable". A short dial timeout here makes AC's own "not reachable"
// verdict arrive well inside every existing caller's budget instead of
// racing (and losing to) it.
const hostDialTimeout = 1500 * time.Millisecond

// hostIDContextKey carries a proxied request's target host id through to
// newHostProxyTransport's DialContext, which otherwise only sees the literal
// "host:port" dial target proxyToHost/handleFileUploadRelay resolved -- it
// needs the host id too, to look up that host's pooled tunnel connection.
type hostIDContextKey struct{}

// newHostProxyTransport builds the http.Transport shared by proxyToHost and
// handleFileUploadRelay. Its DialContext prefers a pooled tunnel connection --
// one local-representative already opened to this agent-coordinator via
// representable.DialTunnel, the one direction guaranteed to work when LR
// sits behind NAT -- over dialing the host fresh (see hostDialTimeout and
// condocs/initialDistributedSessionsImpls/Step1SubstepBPrompt.md, Revision
// I). It falls back to a direct dial whenever that host has no tunnel
// parked (no representable server at all, or the host just hasn't opened one
// yet), so this keeps working unchanged for a same-machine/non-NAT host and
// in tests, which never register a tunnel.
func newHostProxyTransport(s *Server) *http.Transport {
	dialer := &net.Dialer{Timeout: hostDialTimeout}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if hostID, _ := ctx.Value(hostIDContextKey{}).(string); hostID != "" && s.reprServer != nil {
				if conn, ok := s.reprServer.ClaimTunnel(hostID); ok {
					return conn, nil
				}
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
}

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

// FCStateMsg matches the fc-state payload sent from LR. FC is the FC
// instance it's about.
type FCStateMsg struct {
	FC    string `json:"fc,omitempty"`
	State string `json:"state"`
}

// FCLogMsg matches the fc-log payload sent from LR.
type FCLogMsg struct {
	FC   string `json:"fc,omitempty"`
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

// SessionsStateMsg matches the sessions-state payload forwarded up from LR
// (originating at a managed session-manager). HTTPPort is session-manager's
// own port on the LR box; the coordinator reverse-proxies its UI at
// /host/<id>/sessions/. Grows domain-specific fields in a later step (see
// condocs/InitialShellsSessionManagerAndTheConversationalist.md).
type SessionsStateMsg struct {
	HTTPPort string `json:"http_port"`
}

// ConvoStateMsg matches the convo-state payload forwarded up from LR
// (originating at a managed the-conversationalist). HTTPPort is
// the-conversationalist's own port on the LR box; the coordinator
// reverse-proxies its UI at /host/<id>/convo/. Grows domain-specific fields
// in a later step (see condocs/InitialShellsSessionManagerAndTheConversationalist.md).
type ConvoStateMsg struct {
	HTTPPort string `json:"http_port"`
}

// RobotStateMsg matches the robot-state payload forwarded up from LR
// (originating at a managed ianar). HTTPPort is ianar's own port on the LR
// box; the coordinator reverse-proxies its UI at /host/<id>/robot/. Grows
// domain-specific fields in a later step (see condocs/InitialRobot.md).
type RobotStateMsg struct {
	HTTPPort string `json:"http_port"`
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

	// Highlighted mirrors local-representative's FileInfo.Highlighted -- see
	// local-representative/files.go. Without this field, decoding LR's
	// files-state payload into this struct would silently drop the flag (the
	// same class of bug as ProcInfo.AutoUpdate, Revision K), so the
	// file-details dialog's "highlight" toggle could never render as active
	// when viewed through agent-coordinator.
	Highlighted bool `json:"highlighted"`

	// MarkedUp mirrors local-representative's FileInfo.MarkedUp (see
	// local-representative/files.go) -- same reasoning as Highlighted above,
	// so the markup dialog's bright-orange-border cue renders correctly when
	// viewed through agent-coordinator's per-host files tab.
	MarkedUp bool `json:"marked_up"`
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

// LRSessionsMsg is the host-scoped "lr-sessions-state" message sent to
// browser clients: whether a managed session-manager's forwarded UI is
// available on that host -- mirrors LRCondoccerMsg.
type LRSessionsMsg struct {
	HostID    string `json:"host_id"`
	Available bool   `json:"available"`
}

// LRConvoMsg is the host-scoped "lr-convo-state" message sent to browser
// clients: whether a managed the-conversationalist's forwarded UI is
// available on that host -- mirrors LRCondoccerMsg.
type LRConvoMsg struct {
	HostID    string `json:"host_id"`
	Available bool   `json:"available"`
}

// LRRobotMsg is the host-scoped "lr-robot-state" message sent to browser
// clients: whether a managed ianar's forwarded UI is available on that host
// -- mirrors LRCondoccerMsg.
type LRRobotMsg struct {
	HostID    string `json:"host_id"`
	Available bool   `json:"available"`
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

	// LoaderManaged is only meaningful on Self: whether that local-representative
	// was launched by ufa-loader, i.e. whether its "restart" control can be
	// expected to actually come back up -- see docs/DevMode.md "Loader".
	LoaderManaged bool `json:"loader_managed,omitempty"`

	// Version is this process's build version -- see docs/DevMode.md
	// "Versioning". Empty until it has reported at least once.
	Version string `json:"version,omitempty"`

	// UpdateAvailable is true once this process's on-disk binary answers
	// "--version" differently than the version currently running. On Self
	// that's the LR itself -- see docs/DevMode.md "Loader". On a managed
	// instance it's the same comparison against that application's binary --
	// see local-representative/procman.go's pollManagedVersions and
	// condocs/initialDistributedDevelopmentImpls/Step4Prompt.md Revision D.
	UpdateAvailable bool `json:"update_available,omitempty"`

	// PendingVersion is the on-disk version UpdateAvailable refers to -- see
	// local-representative/procman.go's ProcInfo. Empty whenever
	// UpdateAvailable is false.
	PendingVersion string `json:"pending_version,omitempty"`

	// AutoUpdate is only meaningful on Self: whether that local-representative
	// restarts itself automatically the instant UpdateAvailable goes true --
	// see local-representative/selfversion.go and
	// condocs/initialDistributedDevelopmentImpls/Step5Prompt.md Revision E.
	// Without this field, decoding LR's system-state payload into this struct
	// silently dropped the flag, so the frontend's "auto-update" checkbox
	// could never render as checked (Revision K).
	AutoUpdate bool `json:"auto_update,omitempty"`

	// Session is federation-command-specific: the session it most recently
	// reported over representable's "fc-session" data message, relayed here
	// unchanged by LR -- see local-representative/procman.go's own ProcInfo.
	// Without this field, decoding LR's system-state payload into this struct
	// silently dropped it the same way AutoUpdate did above (Revision K), so
	// the federation-command tab's session tag could never render here even
	// though LR's own frontend showed it fine (see
	// condocs/initialDistributedSessionsImpls/Step2Prompt.md Revision D).
	Session string `json:"session,omitempty"`
}

// SystemStateMsg matches the system-state payload sent from LR over representable.
type SystemStateMsg struct {
	Self    ProcInfo   `json:"self"`
	Managed []ProcInfo `json:"managed"`
}

// RepoStateMsg mirrors local-representative's dev-repo watcher payload (see
// local-representative/repowatch.go): its current view of the git repo it's
// watching for rebuild-worthy changes. Watched is false when that LR wasn't
// launched with --dev-repo.
type RepoStateMsg struct {
	Watched            bool   `json:"watched"`
	Root               string `json:"root,omitempty"`
	Dirty              bool   `json:"dirty"`
	RebuildReady       bool   `json:"rebuild_ready"`
	Building           bool   `json:"building"`
	AutoRebuild        bool   `json:"auto_rebuild"`
	AutoRebuildPending bool   `json:"auto_rebuild_pending,omitempty"`
	AutoRebuildSeconds int    `json:"auto_rebuild_seconds,omitempty"`
	Head               string `json:"head,omitempty"`
	LastError          string `json:"last_error,omitempty"`
	CondocLocked       bool   `json:"condoc_locked,omitempty"`
}

// Host-scoped WS message types sent to browser clients.

type LRFCStateMsg struct {
	HostID string `json:"host_id"`
	FC     string `json:"fc,omitempty"` // the FC instance on that host -- see local-representative/fcinstances.go
	State  string `json:"state"`
}

type LRFCLogMsg struct {
	HostID string `json:"host_id"`
	FC     string `json:"fc,omitempty"`
	Line   string `json:"line"`
	Kind   string `json:"kind,omitempty"`
}

// LRFCInstancesMsg relays a host's "fc-instances" snapshot (every connected
// federation-command instance -- see local-representative/fcinstances.go)
// as-is.
type LRFCInstancesMsg struct {
	HostID    string          `json:"host_id"`
	Instances json.RawMessage `json:"instances"`
}

// LRControlMsg relays a host's "control-state" (its control tab's sequences
// and current run -- see local-representative/control.go) as-is. Control is
// null when the host has disconnected.
type LRControlMsg struct {
	HostID  string          `json:"host_id"`
	Control json.RawMessage `json:"control"`
}

// LRControlLibraryMsg relays a host's "control-library" (its control tab's
// definer actions and composer sequences -- see
// local-representative/controllib.go) as-is; null once it disconnects.
type LRControlLibraryMsg struct {
	HostID  string          `json:"host_id"`
	Library json.RawMessage `json:"library"`
}

// maxControlLibCommand caps a library request relayed to a host: the
// representable link reads a line at a time, up to 64 KiB.
const maxControlLibCommand = 48 << 10

// LRControlReplyMsg relays a host's "control-reply", its answer to a
// library request ("lr-control-lib") or a run that couldn't start. Every
// browser gets it; the one that asked matches it by the reply's req.
type LRControlReplyMsg struct {
	HostID string          `json:"host_id"`
	Reply  json.RawMessage `json:"reply"`
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

// LRRepoStateMsg is the host-scoped "lr-repo-state" message sent to browser
// clients: local-representative's dev-repo watcher for one host (see
// local-representative/repowatch.go). Watched is false both when that LR
// isn't watching a repo and when it isn't connected.
type LRRepoStateMsg struct {
	HostID             string `json:"host_id"`
	Watched            bool   `json:"watched"`
	Root               string `json:"root,omitempty"`
	Dirty              bool   `json:"dirty"`
	RebuildReady       bool   `json:"rebuild_ready"`
	Building           bool   `json:"building"`
	AutoRebuild        bool   `json:"auto_rebuild"`
	AutoRebuildPending bool   `json:"auto_rebuild_pending,omitempty"`
	AutoRebuildSeconds int    `json:"auto_rebuild_seconds,omitempty"`
	Head               string `json:"head,omitempty"`
	LastError          string `json:"last_error,omitempty"`
	CondocLocked       bool   `json:"condoc_locked,omitempty"`
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

// DebugLogEntry mirrors local-representative's same-named type (see
// local-representative/procman.go): one captured stdout/stderr line from an
// LR-managed sub-app, for the system tab's debug view
// (condocs/initialDistributedSessionsImpls/Step1SubstepBPrompt.md Revision E).
type DebugLogEntry struct {
	InstanceID string `json:"instance_id"`
	App        string `json:"app"`
	Stream     string `json:"stream"`
	Line       string `json:"line"`
	TS         int64  `json:"ts"`
}

// DebugLogStateMsg matches the debug-log-state payload sent from LR over
// representable.
type DebugLogStateMsg struct {
	Entries []DebugLogEntry `json:"entries"`
}

// LRDebugLogMsg is the host-scoped "lr-debug-log-state" message sent to
// browser clients: the debug view's current log buffer for one host. Active
// is false when that LR is not connected -- mirrors LRFilesMsg.
type LRDebugLogMsg struct {
	HostID  string          `json:"host_id"`
	Active  bool            `json:"active"`
	Entries []DebugLogEntry `json:"entries,omitempty"`
}

// ChainCallEntry mirrors local-representative's same-named type (see
// local-representative/procman.go): one outbound HTTP call made on the
// SM<->LR<->AC chain, for the system tab's debug view's "network" tab
// (condocs/initialDistributedSessionsImpls/Step1SubstepBPrompt.md Revision
// F).
type ChainCallEntry struct {
	Hop        string `json:"hop"`
	Method     string `json:"method"`
	URL        string `json:"url"`
	Status     int    `json:"status"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	TS         int64  `json:"ts"`
}

// ChainCallStateMsg matches the chain-call-state payload sent from LR over
// representable.
type ChainCallStateMsg struct {
	Entries []ChainCallEntry `json:"entries"`
}

// LRChainCallMsg is the host-scoped "lr-chain-call-state" message sent to
// browser clients: the debug view's current chain-call buffer for one host.
// Active is false when that LR is not connected -- mirrors LRDebugLogMsg.
type LRChainCallMsg struct {
	HostID  string           `json:"host_id"`
	Active  bool             `json:"active"`
	Entries []ChainCallEntry `json:"entries,omitempty"`
}

// StateboardEntry mirrors local-representative's same-named type (see
// local-representative/stateboard.go): one key/value row, nested two levels
// deep under the sub-app that owns it, on the system tab's debug view's
// "stateboard" tab (condocs/initialDistributedSessionsImpls/Step2Prompt.md
// Revision E, nesting added in Revision F).
type StateboardEntry struct {
	App   string `json:"app"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// StateboardMsg matches the stateboard-state payload sent from LR over
// representable.
type StateboardMsg struct {
	Entries []StateboardEntry `json:"entries"`
}

// LRStateboardMsg is the host-scoped "lr-stateboard-state" message sent to
// browser clients: the debug view's current stateboard for one host. Active
// is false when that LR is not connected -- mirrors LRDebugLogMsg/
// LRChainCallMsg.
type LRStateboardMsg struct {
	HostID  string            `json:"host_id"`
	Active  bool              `json:"active"`
	Entries []StateboardEntry `json:"entries,omitempty"`
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
	repo       *RepoStateMsg
	condoccer  *CondoccerStateMsg
	sessions   *SessionsStateMsg
	convo      *ConvoStateMsg
	robot      *RobotStateMsg
	files      *FilesStateMsg
	debugLog   *DebugLogStateMsg
	chainCall  *ChainCallStateMsg
	stateboard *StateboardMsg
	lrHTTPPort string

	// fcInstances, control and controlLib are relayed to browsers as-is --
	// see LRFCInstancesMsg, LRControlMsg and LRControlLibraryMsg.
	fcInstances json.RawMessage
	control     json.RawMessage
	controlLib  json.RawMessage
}

// Server manages WebSocket clients and coordinator state.
type Server struct {
	upgrader   websocket.Upgrader
	mu         sync.RWMutex
	clients    map[*wsClient]bool
	reprServer *representable.Server
	devMode    bool      // --dev-mode: this agent-coordinator instance -- see docs/DevMode.md
	selfHostID string    // ufahostid.GetHostID() for this machine -- see SelfInfoMsg
	startedAt  time.Time // when this agent-coordinator process started -- see SelfInfoMsg.StartedAt

	// loaderManaged is true when this process was launched by ufa-loader (see
	// restartsignal.IsLoaderManaged), i.e. when an operator-driven "restart
	// agent-coordinator" (Step4Prompt.md Revision E) can be expected to
	// actually come back up rather than just stop.
	loaderManaged bool

	// selfVersion watches this process's own on-disk binary for a newer
	// build landing while it runs (see selfversion.go) so the "restart
	// agent-coordinator" control can offer "restart and update". Only
	// started when loaderManaged, since that's the only case restart
	// actually helps; nil (and selfVersion.available() reports false)
	// otherwise.
	selfVersion *selfVersionWatch

	hostsMu    sync.RWMutex
	hostStates map[string]*hostState

	modeMu         sync.RWMutex
	modeMismatches map[string]ModeMismatchMsg // LR host id -> current mismatch disclosure, mismatched entries only

	// tcMu/tcAvailable track the aggregate "is a the-conversationalist
	// instance available on any host" verdict -- see tcavailability.go and
	// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
	// Step2Prompt.md.
	tcMu        sync.RWMutex
	tcAvailable bool

	// hostProxyTransport is shared by proxyToHost and handleFileUploadRelay --
	// see newHostProxyTransport.
	hostProxyTransport *http.Transport
}

func newServer() *Server {
	s := &Server{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:        make(map[*wsClient]bool),
		hostStates:     make(map[string]*hostState),
		modeMismatches: make(map[string]ModeMismatchMsg),
		startedAt:      time.Now(),
	}
	s.hostProxyTransport = newHostProxyTransport(s)
	return s
}

// SelfInfoMsg discloses this agent-coordinator instance's own dev-mode
// status, host identity, and restart-ability to its frontend (see
// docs/DevMode.md) — sent when a browser client connects and re-broadcast
// whenever LoaderManaged/UpdateAvailable change, since AC has no
// representable server above it forwarding a "self" ProcInfo the way LR
// forwards one for itself. HostID is the same ufahostid.GetHostID() value a
// co-located local-representative defaults its "-name" to, letting the
// frontend recognize which connected host (if any) is the one
// agent-coordinator itself runs on -- see the global topology panel's
// self-card collapsing in App.tsx. LoaderManaged/UpdateAvailable mirror
// ProcInfo's same-named fields for a local-representative's own self row,
// duplicated here (see selfversion.go) rather than reused because it's a
// legitimate deployment to run agent-coordinator alone on a box with only
// off-node local-representatives -- driving the "restart agent-coordinator"
// control added to the global topology view's Details & Control pane, see
// condocs/initialDistributedDevelopmentImpls/Step4Prompt.md Revision E.
// Version is this process's own build version (ufaversion.Version), used by
// the frontend to detect a rebuild+restart out from under an already-open
// tab and reload itself -- see
// condocs/initialDistributedDevelopmentImpls/BrowserRefreshStrategy.md.
// AutoUpdate mirrors ProcInfo's same-named field for a local-representative's
// own self row: whether an available update should make AC restart itself
// the moment selfVersion next notices it, rather than waiting for an
// operator to press "restart and update AC" -- see selfversion.go and
// condocs/initialShellsSessionManagerAndTheConversationalistImpls/
// Step1SubstepCPrompt.md Revision D.
type SelfInfoMsg struct {
	DevMode         bool   `json:"dev_mode"`
	HostID          string `json:"host_id"`
	LoaderManaged   bool   `json:"loader_managed"`
	UpdateAvailable bool   `json:"update_available"`
	AutoUpdate      bool   `json:"auto_update"`
	Version         string `json:"version"`
	StartedAt       int64  `json:"started_at"` // unix seconds this process started -- see ProcInfo.StartedAt for LR's equivalent
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

// selfInfo returns this agent-coordinator instance's current SelfInfoMsg
// snapshot, including its own restart-ability (see selfversion.go).
func (s *Server) selfInfo() SelfInfoMsg {
	return SelfInfoMsg{
		DevMode:         s.devMode,
		HostID:          s.selfHostID,
		LoaderManaged:   s.loaderManaged,
		UpdateAvailable: s.selfVersion.available(),
		AutoUpdate:      s.selfVersion.autoUpdateEnabled(),
		Version:         ufaversion.Version,
		StartedAt:       s.startedAt.Unix(),
	}
}

// broadcastSelfInfo pushes a fresh self-info snapshot to every connected
// browser client -- called whenever selfVersion's verdict changes (see
// newSelfVersionWatch in main below), since self-info is otherwise only sent
// once per connection.
func (s *Server) broadcastSelfInfo() {
	s.broadcast("self-info", s.selfInfo())
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

// handleHostsAPI answers GET /api/hosts: the same host list/status the "hosts"
// WebSocket message carries, as plain JSON -- for a local-representative's
// own outbound calls (discovering which other hosts are LR-active before a
// session-file pull -- see local-representative/sessions.go and
// docs/DistributedSessionsBrainstorm.md) rather than only for browsers over
// the WebSocket.
func (s *Server) handleHostsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(HostsMsg{Hosts: s.getHosts()})
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
	repo := hs.repo
	condoccer := hs.condoccer
	sessions := hs.sessions
	convo := hs.convo
	robot := hs.robot
	files := hs.files
	debugLog := hs.debugLog
	chainCall := hs.chainCall
	stateboard := hs.stateboard
	fcInstances := hs.fcInstances
	control := hs.control
	controlLib := hs.controlLib
	hs.mu.RUnlock()

	s.sendToClient(c, "lr-state", LRStateMsg{HostID: name, Active: connected, Services: services})
	s.sendToClient(c, "lr-fc-state", LRFCStateMsg{HostID: name, State: fcState})
	s.sendToClient(c, "lr-fc-instances", fcInstancesMsg(name, fcInstances))
	s.sendToClient(c, "lr-control-state", LRControlMsg{HostID: name, Control: control})
	s.sendToClient(c, "lr-control-library", LRControlLibraryMsg{HostID: name, Library: controlLib})
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
	s.sendToClient(c, "lr-repo-state", repoStateMsg(name, repo))
	s.sendToClient(c, "lr-condoccer-state", condoccerMsg(name, condoccer))
	s.sendToClient(c, "lr-sessions-state", sessionsMsg(name, sessions))
	s.sendToClient(c, "lr-convo-state", convoMsg(name, convo))
	s.sendToClient(c, "lr-robot-state", robotMsg(name, robot))
	if files != nil {
		s.sendToClient(c, "lr-files-state", LRFilesMsg{HostID: name, Active: connected, Files: files.Files})
	} else {
		s.sendToClient(c, "lr-files-state", LRFilesMsg{HostID: name, Active: false})
	}
	// debugLog/chainCall/stateboard were missing here entirely (Revision F:
	// "nothing is currently displaying in the stateboard view") -- a browser
	// client connecting (or reconnecting) after LR's one-time initial
	// pushStateToAC never got this host's current debug-view data until the
	// next broadcastStateboard()-triggering event, which for the
	// comparatively static stateboard tab (unlike the constantly-streaming
	// logs/network tabs) could be a very long wait or never.
	if debugLog != nil {
		s.sendToClient(c, "lr-debug-log-state", LRDebugLogMsg{HostID: name, Active: connected, Entries: debugLog.Entries})
	} else {
		s.sendToClient(c, "lr-debug-log-state", LRDebugLogMsg{HostID: name, Active: false})
	}
	if chainCall != nil {
		s.sendToClient(c, "lr-chain-call-state", LRChainCallMsg{HostID: name, Active: connected, Entries: chainCall.Entries})
	} else {
		s.sendToClient(c, "lr-chain-call-state", LRChainCallMsg{HostID: name, Active: false})
	}
	if stateboard != nil {
		s.sendToClient(c, "lr-stateboard-state", LRStateboardMsg{HostID: name, Active: connected, Entries: stateboard.Entries})
	} else {
		s.sendToClient(c, "lr-stateboard-state", LRStateboardMsg{HostID: name, Active: false})
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

// sessionsMsg builds a host-scoped lr-sessions-state payload; a nil state
// means no managed session-manager is currently reporting on that host --
// mirrors condoccerMsg.
func sessionsMsg(hostID string, sm *SessionsStateMsg) LRSessionsMsg {
	if sm == nil || sm.HTTPPort == "" {
		return LRSessionsMsg{HostID: hostID, Available: false}
	}
	return LRSessionsMsg{HostID: hostID, Available: true}
}

// convoMsg builds a host-scoped lr-convo-state payload; a nil state means no
// managed the-conversationalist is currently reporting on that host --
// mirrors condoccerMsg.
func convoMsg(hostID string, cv *ConvoStateMsg) LRConvoMsg {
	if cv == nil || cv.HTTPPort == "" {
		return LRConvoMsg{HostID: hostID, Available: false}
	}
	return LRConvoMsg{HostID: hostID, Available: true}
}

// robotMsg builds a host-scoped lr-robot-state payload; a nil state means no
// managed ianar is currently reporting on that host -- mirrors condoccerMsg.
func robotMsg(hostID string, rb *RobotStateMsg) LRRobotMsg {
	if rb == nil || rb.HTTPPort == "" {
		return LRRobotMsg{HostID: hostID, Available: false}
	}
	return LRRobotMsg{HostID: hostID, Available: true}
}

// fcInstancesMsg builds a host-scoped lr-fc-instances payload; nil means no
// snapshot yet (or the host disconnected), sent as an empty list.
func fcInstancesMsg(hostID string, raw json.RawMessage) LRFCInstancesMsg {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("[]")
	}
	return LRFCInstancesMsg{HostID: hostID, Instances: raw}
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

// repoStateMsg builds a host-scoped lr-repo-state payload; a nil state means
// no repo-state has been reported yet (treated the same as "not watched").
func repoStateMsg(hostID string, r *RepoStateMsg) LRRepoStateMsg {
	if r == nil {
		return LRRepoStateMsg{HostID: hostID}
	}
	return LRRepoStateMsg{
		HostID:             hostID,
		Watched:            r.Watched,
		Root:               r.Root,
		Dirty:              r.Dirty,
		RebuildReady:       r.RebuildReady,
		Building:           r.Building,
		AutoRebuild:        r.AutoRebuild,
		AutoRebuildPending: r.AutoRebuildPending,
		AutoRebuildSeconds: r.AutoRebuildSeconds,
		Head:               r.Head,
		LastError:          r.LastError,
		CondocLocked:       r.CondocLocked,
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

// fcTargeted addresses cmd to one FC instance on a host: LR runs
// "__fc:<instance> <cmd>" on that instance only, and a bare cmd on its
// default instance.
func fcTargeted(fc, cmd string) string {
	if fc == "" {
		return cmd
	}
	return "__fc:" + fc + " " + cmd
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
		s.sendToClient(c, "self-info", s.selfInfo())
		s.sendToClient(c, "tc-availability", TCAvailabilityMsg{Available: s.anyConvoAvailable()})
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
			// fc picks one FC instance on the host (see
			// local-representative/fcinstances.go); empty leaves it to LR.
			var payload struct {
				HostID string `json:"host_id"`
				FC     string `json:"fc"`
				Cmd    string `json:"cmd"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && payload.Cmd != "" && s.reprServer != nil {
				s.reprServer.SendCommand(payload.HostID, fcTargeted(payload.FC, payload.Cmd))
			}
		case "lr-ridealong-command":
			var payload struct {
				HostID string `json:"host_id"`
				FC     string `json:"fc"`
				Action string `json:"action"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && payload.Action != "" && s.reprServer != nil {
				s.reprServer.SendCommand(payload.HostID, fcTargeted(payload.FC, "__ridealong:"+payload.Action))
			}
		case "lr-control-run":
			// The host's control tab -- see local-representative/control.go.
			var payload struct {
				HostID   string            `json:"host_id"`
				Sequence string            `json:"sequence"`
				Controls map[string]string `json:"controls"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && payload.Sequence != "" && !strings.ContainsAny(payload.Sequence, " \n") && s.reprServer != nil {
				cmd := "__control:run " + payload.Sequence
				if len(payload.Controls) > 0 {
					values, _ := json.Marshal(payload.Controls)
					cmd += " " + string(values)
				}
				s.reprServer.SendCommand(payload.HostID, cmd)
			}
		case "lr-control-lib":
			// The host's control tab definer/composer: the request is passed
			// on as-is ("__control:lib <json>") and the host's answer comes
			// back as "control-reply".
			var payload struct {
				HostID  string          `json:"host_id"`
				Request json.RawMessage `json:"request"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil && payload.HostID != "" && len(payload.Request) > 0 && s.reprServer != nil {
				var compact bytes.Buffer
				if err := json.Compact(&compact, payload.Request); err != nil {
					break
				}
				if compact.Len() > maxControlLibCommand {
					// Representable reads a line at a time, up to 64 KiB; a
					// longer one would drop the host's connection.
					var req struct {
						Req string `json:"req"`
						Op  string `json:"op"`
					}
					_ = json.Unmarshal(payload.Request, &req)
					reply, _ := json.Marshal(map[string]any{"req": req.Req, "op": req.Op, "success": false,
						"error": fmt.Sprintf("too large to send to %s through agent-coordinator (%d KiB, the limit is %d KiB) -- import it on that host's own control tab", payload.HostID, compact.Len()>>10, maxControlLibCommand>>10)})
					s.sendToClient(c, "lr-control-reply", LRControlReplyMsg{HostID: payload.HostID, Reply: reply})
					break
				}
				s.reprServer.SendCommand(payload.HostID, "__control:lib "+compact.String())
			}
		case "lr-control-cancel":
			var payload struct {
				HostID string `json:"host_id"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil && payload.HostID != "" && s.reprServer != nil {
				s.reprServer.SendCommand(payload.HostID, "__control:cancel")
			}
		case "lr-control-continue":
			// The continue button on a host's step waiting for the user.
			var payload struct {
				HostID string `json:"host_id"`
				Run    string `json:"run"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil && payload.HostID != "" &&
				!strings.ContainsAny(payload.Run, " \n") && s.reprServer != nil {
				s.reprServer.SendCommand(payload.HostID, strings.TrimSpace("__control:continue "+payload.Run))
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
		case "lr-restart-app":
			var payload struct {
				HostID string `json:"host_id"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && s.reprServer != nil {
				s.reprServer.SendCommand(payload.HostID, "__system:restart")
			}
		case "lr-restart-managed-app":
			var payload struct {
				HostID string `json:"host_id"`
				ID     string `json:"id"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && payload.ID != "" && s.reprServer != nil {
				s.reprServer.SendCommand(payload.HostID, "__system:restart-managed "+payload.ID)
			}
		case "lr-rebuild-app":
			var payload struct {
				HostID string `json:"host_id"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && s.reprServer != nil {
				s.reprServer.SendCommand(payload.HostID, "__system:rebuild")
			}
		case "lr-set-auto-rebuild":
			var payload struct {
				HostID  string `json:"host_id"`
				Enabled bool   `json:"enabled"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && s.reprServer != nil {
				state := "off"
				if payload.Enabled {
					state = "on"
				}
				s.reprServer.SendCommand(payload.HostID, "__system:auto-rebuild "+state)
			}
		case "lr-set-auto-update":
			var payload struct {
				HostID  string `json:"host_id"`
				Enabled bool   `json:"enabled"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil &&
				payload.HostID != "" && s.reprServer != nil {
				state := "off"
				if payload.Enabled {
					state = "on"
				}
				s.reprServer.SendCommand(payload.HostID, "__system:auto-update "+state)
			}
		case "ac-restart-app":
			// Restarts agent-coordinator itself, not any host's LR -- the
			// global topology view's "restart agent-coordinator" control
			// (Step4Prompt.md Revision E). No payload: there's only ever one
			// agent-coordinator to restart.
			s.requestRestart("operator")
		case "ac-set-auto-update":
			// Toggles whether AC restarts itself the instant an update
			// becomes available -- the global topology view's
			// "agent-coordinator" section's own auto-update checkbox. No
			// host to target, mirroring ac-restart-app; see selfversion.go
			// and condocs/
			// initialShellsSessionManagerAndTheConversationalistImpls/
			// Step1SubstepCPrompt.md Revision D.
			var payload struct {
				Enabled bool `json:"enabled"`
			}
			if err := json.Unmarshal(m.Payload, &payload); err == nil {
				s.setAutoUpdate(payload.Enabled)
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
	r = r.WithContext(context.WithValue(r.Context(), hostIDContextKey{}, hostID))
	target := &url.URL{Scheme: "http", Host: addr}
	prefix := "/host/" + hostID
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = s.hostProxyTransport
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

	ctx := context.WithValue(r.Context(), hostIDContextKey{}, hostID)
	outReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/api/files", r.Body)
	if err != nil {
		http.Error(w, "building relay request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	outReq.ContentLength = r.ContentLength
	outReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	outReq.Header.Set(proxiedHeader, acRelayStamp)
	outReq.Header.Set(relayedUploadHeader, acRelayStamp)

	relayClient := &http.Client{Transport: s.hostProxyTransport}
	resp, err := relayClient.Do(outReq)
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
	mux.HandleFunc("/api/hosts", s.handleHostsAPI)
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

// announceRestartAndExit writes the restartsignal announcement (see
// ufa-loader/README.md and docs/DevMode.md's "Loader" section) as this
// process's final act before exiting 0, attaching this AC's own live state
// (see reststate.go) for the instance replacing it to pick back up --
// otherwise an auto-update-triggered restart would silently turn
// auto-update back off. Callers are expected to have already decided a
// restart is appropriate; this never returns.
func (s *Server) announceRestartAndExit(reason string) {
	log.Printf("announcing a restart (%s) and exiting", reason)
	st := s.currentACState()
	if err := restartsignal.AnnounceState(os.Stdout, "agent-coordinator", reason, st); err != nil {
		log.Printf("restartsignal.AnnounceState: %v", err)
	}
	os.Exit(0)
}

// watchRestartSignal blocks waiting for SIGHUP and, on receipt, announces a
// restart as this process's final act before exiting 0. The signal is sent
// directly to this process's own pid (e.g. `kill -HUP <pid>`), not through
// ufa-loader itself: ufa-loader only watches stdout, it doesn't originate the
// restart trigger. Unlike requestRestart, this always restarts -- a bare
// `kill -HUP` without a wrapping ufa-loader is documented to still announce
// and exit, just with nothing there to relaunch it. Run in its own
// goroutine; never returns.
func (s *Server) watchRestartSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	for range sigCh {
		s.announceRestartAndExit("sighup")
	}
}

// requestRestart handles an operator-driven restart request -- the global
// topology view's "restart agent-coordinator" control (WebSocket
// "ac-restart-app", see condocs/initialDistributedDevelopmentImpls/
// Step4Prompt.md Revision E) -- which terminates this process such that a
// wrapping ufa-loader relaunches it with the same config. Unlike SIGHUP,
// this refuses (logging why) when the process isn't loaderManaged: the
// frontend control is greyed out in that case since pressing it wouldn't
// come back up, and this is the server-side enforcement of that same guard
// for any caller that bypasses the UI.
func (s *Server) requestRestart(reason string) {
	if !s.loaderManaged {
		log.Printf("restart requested (%s) but agent-coordinator is not loader-managed (no %s) — ignoring", reason, restartsignal.InitEnvVar)
		return
	}
	s.announceRestartAndExit(reason)
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
	s.selfHostID = ufahostid.GetHostID()

	prevACState, havePrevACState := loadPreviousACState()

	s.loaderManaged = restartsignal.IsLoaderManaged()
	if s.loaderManaged {
		// Only worth polling for an on-disk update when a restart could
		// actually pick it up -- see selfversion.go. restart wires AC's own
		// "auto-update" toggle to the same requestRestart the "restart and
		// update AC" button drives, per condocs/
		// initialShellsSessionManagerAndTheConversationalistImpls/
		// Step1SubstepCPrompt.md Revision D.
		s.selfVersion = newSelfVersionWatch(ufaversion.Version, s.broadcastSelfInfo, func() { s.requestRestart("auto-update") })
		if s.selfVersion != nil {
			if havePrevACState && prevACState.AutoUpdate {
				s.selfVersion.setAutoUpdate(true)
			}
			go s.selfVersion.watchLoop()
		}
	}

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
			hs.repo = nil
			hs.condoccer = nil
			hs.sessions = nil
			hs.convo = nil
			hs.robot = nil
			hs.files = nil
			hs.debugLog = nil
			hs.chainCall = nil
			hs.stateboard = nil
			hs.lrHTTPPort = ""
			hs.fcInstances = nil
			hs.control = nil
			hs.controlLib = nil
			hs.mu.Unlock()
			s.broadcast("hosts", HostsMsg{Hosts: s.getHosts()})
			s.broadcast("lr-state", LRStateMsg{HostID: name, Active: false})
			s.broadcast("lr-fc-state", LRFCStateMsg{HostID: name, State: ""})
			s.broadcast("lr-fc-instances", fcInstancesMsg(name, nil))
			s.broadcast("lr-control-state", LRControlMsg{HostID: name})
			s.broadcast("lr-control-library", LRControlLibraryMsg{HostID: name})
			s.broadcast("lr-ridealong-state", LRRidealongMsg{HostID: name, Active: false})
			s.broadcast("lr-condoc-state", LRCondocMsg{HostID: name, Active: false})
			s.broadcast("lr-system-state", LRSystemStateMsg{HostID: name, Active: false})
			s.broadcast("lr-repo-state", LRRepoStateMsg{HostID: name})
			s.broadcast("lr-condoccer-state", LRCondoccerMsg{HostID: name, Available: false})
			s.broadcast("lr-sessions-state", LRSessionsMsg{HostID: name, Available: false})
			s.broadcast("lr-convo-state", LRConvoMsg{HostID: name, Available: false})
			s.broadcast("lr-robot-state", LRRobotMsg{HostID: name, Available: false})
			s.broadcast("lr-files-state", LRFilesMsg{HostID: name, Active: false})
			s.broadcast("lr-debug-log-state", LRDebugLogMsg{HostID: name, Active: false})
			s.broadcast("lr-chain-call-state", LRChainCallMsg{HostID: name, Active: false})
			s.broadcast("lr-stateboard-state", LRStateboardMsg{HostID: name, Active: false})
			s.setModeMismatch(name, false, "")
			s.broadcastTCAvailability()
			s.broadcastControlNodes()
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
			// A freshly-connected LR has never received a tc-availability
			// command before -- push the current aggregate to it directly,
			// even if the aggregate hasn't changed (broadcastTCAvailability
			// below only re-pushes to hosts on a change).
			s.sendTCAvailabilityTo(name)
			// Every host's control tab offers the connected nodes, this one
			// now among them.
			s.broadcastControlNodes()
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
				s.broadcast("lr-fc-state", LRFCStateMsg{HostID: name, FC: payload.FC, State: payload.State})
			}
		case "fc-log":
			var payload FCLogMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				s.broadcast("lr-fc-log", LRFCLogMsg{HostID: name, FC: payload.FC, Line: payload.Line, Kind: payload.Kind})
			}
		case "fc-instances":
			var payload struct {
				Instances json.RawMessage `json:"instances"`
			}
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				hs.fcInstances = payload.Instances
				hs.mu.Unlock()
				s.broadcast("lr-fc-instances", fcInstancesMsg(name, payload.Instances))
			}
		case "control-state":
			hs.mu.Lock()
			hs.control = append(json.RawMessage(nil), data...)
			hs.mu.Unlock()
			s.broadcast("lr-control-state", LRControlMsg{HostID: name, Control: data})
		case "control-library":
			hs.mu.Lock()
			hs.controlLib = append(json.RawMessage(nil), data...)
			hs.mu.Unlock()
			s.broadcast("lr-control-library", LRControlLibraryMsg{HostID: name, Library: data})
		case "control-reply":
			s.broadcast("lr-control-reply", LRControlReplyMsg{HostID: name, Reply: data})
		case "node-capture", "node-fetch":
			// A control sequence on this host wants another node's
			// screenshot or files -- see controlnodes.go.
			s.relayNodeRequest(dataType, name, data)
		case "node-capture-result", "node-fetch-result":
			s.relayNodeResult(strings.TrimSuffix(dataType, "-result"), name, data)
		case "control-nodes-request":
			s.sendControlNodes(name, s.connectedHostNames())
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
		case "repo-state":
			var payload RepoStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				hs.repo = &payload
				hs.mu.Unlock()
				s.broadcast("lr-repo-state", repoStateMsg(name, &payload))
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
		case "sessions-state":
			var payload SessionsStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				var sm *SessionsStateMsg
				if payload.HTTPPort != "" {
					sm = &payload
				}
				hs.mu.Lock()
				hs.sessions = sm
				hs.mu.Unlock()
				s.broadcast("lr-sessions-state", sessionsMsg(name, sm))
			}
		case "convo-state":
			var payload ConvoStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				var cv *ConvoStateMsg
				if payload.HTTPPort != "" {
					cv = &payload
				}
				hs.mu.Lock()
				hs.convo = cv
				hs.mu.Unlock()
				s.broadcast("lr-convo-state", convoMsg(name, cv))
				s.broadcastTCAvailability()
			}
		case "robot-state":
			var payload RobotStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				var rb *RobotStateMsg
				if payload.HTTPPort != "" {
					rb = &payload
				}
				hs.mu.Lock()
				hs.robot = rb
				hs.mu.Unlock()
				s.broadcast("lr-robot-state", robotMsg(name, rb))
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
		case "debug-log-state":
			var payload DebugLogStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				hs.debugLog = &payload
				hs.mu.Unlock()
				s.broadcast("lr-debug-log-state", LRDebugLogMsg{HostID: name, Active: true, Entries: payload.Entries})
			}
		case "chain-call-state":
			var payload ChainCallStateMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				hs.chainCall = &payload
				hs.mu.Unlock()
				s.broadcast("lr-chain-call-state", LRChainCallMsg{HostID: name, Active: true, Entries: payload.Entries})
			}
		case "stateboard-state":
			var payload StateboardMsg
			if err := json.Unmarshal(data, &payload); err == nil {
				hs.mu.Lock()
				hs.stateboard = &payload
				hs.mu.Unlock()
				s.broadcast("lr-stateboard-state", LRStateboardMsg{HostID: name, Active: true, Entries: payload.Entries})
			}
		}
	})

	log.Printf("representable server (LR connections) listening on tcp://localhost:%s", *reprPort)

	go s.watchRestartSignal()
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
