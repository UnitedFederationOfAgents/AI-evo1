package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"representable"
)

// This file is local-representative's half of the plug-in sub-app contract
// (see representable/plugin.go and condocs/privateRepoOpsImpls/Step3Prompt.md):
// a registry of plug-ins that announced themselves with a plug-in hello over
// representable, their last-fetched UI manifests, and the one generic
// "/apps/<appId>/*" reverse proxy that replaces a hand-written proxyToX per
// app. The frontends embed every first-party sub-app through it; the
// hand-written proxies stay only as their fallback for a sub-app build that
// doesn't say hello yet.
//
// Version 1 only displays plug-ins: it never launches, restarts or updates
// one. Admission is deliberately narrow:
//   - the hello must come from a loopback connection, and name the same app id
//     the connection itself registered under (so disconnecting is what takes
//     the plug-in offline);
//   - first-party app ids (firstPartyPlugins) are always approved and served
//     same-origin, as before;
//   - any other app id is "pending" -- visible in /api/plugins, but never
//     routed -- until an operator approves it on this host's own dashboard
//     (POST /api/plugins/approval, Step3Prompt.md Revision C). An approved
//     external plug-in is served in the sandboxed tier: every response gets
//     representable.SandboxCSP and the frontends frame it with
//     representable.IframeSandbox. See docs/PluginTrust.md.
//
// Revision E adds two ways in that don't start with the plug-in's own hello,
// both made from the plugins tab's "registration" sub-tab:
//   - manual registration (POST /api/plugins/manual): an operator names an
//     app id and a loopback HTTP port. That approves the app id, and since
//     there is no connection to say whether it's alive, LR polls the
//     manifest instead (manualPollInterval). A hello for the same app id
//     takes the entry over while that connection lasts.
//   - "register example" (POST /api/plugins/example): approves
//     cats-ufa-plugin and launches it from the dev binaries, so it says
//     hello itself.

// firstPartyPlugins are every first-party sub-app with a presented UI
// (Step3Prompt.md Revision B). The frontends embed each at /apps/<appId>/
// once it is online here, and fall back to its legacy /<appId>/ proxy in
// main.go until then (an older build that sends no hello).
var firstPartyPlugins = map[string]bool{
	"condocv2":  true,
	"convo":     true, // the-conversationalist
	"sessions":  true, // session-manager
	"robot":     true, // ianar
	"compose":   true, // composer
	"dg":        true, // diagrammographer -- plug-in only, no legacy proxy
}

const pluginManifestTimeout = 3 * time.Second

// manualPollInterval is how often a manually registered plug-in's manifest
// is fetched to tell whether it is up: it has no connection to say so.
const manualPollInterval = 10 * time.Second

// examplePluginAppID is the plug-in "register example" approves and
// launches: cats-ufa-plugin, the hello-world external plug-in. It is also
// its managedApps name (procman.go).
const examplePluginAppID = "cats-ufa-plugin"

// Where a registry entry came from, in PluginInfo.Source.
const (
	sourceHello  = "hello"  // the plug-in said hello over representable
	sourceManual = "manual" // an operator registered it; liveness is polled
)

// Plug-in statuses in PluginInfo.Status.
const (
	pluginOnline  = "online"  // connected and approved: routed at /apps/<appId>/
	pluginOffline = "offline" // disconnected; its last-seen manifest is kept so a tab can show it greyed out rather than vanish
	pluginPending = "pending" // connected but not approved on this host: never routed
)

// Trust tiers in PluginInfo.Trust -- see docs/PluginTrust.md.
const (
	trustFirstParty = "first-party" // same-origin, as every sub-app was before plug-ins
	trustSandboxed  = "sandboxed"   // opaque origin: SandboxCSP on responses, sandboxed iframe
)

// pluginTrust is the tier appID is served in once approved. It is the one
// place the tier is decided, so making it configurable (docs/PluginTrust.md,
// "Making it toggleable") only has to change this function.
func pluginTrust(appID string) string {
	if firstPartyPlugins[appID] {
		return trustFirstParty
	}
	return trustSandboxed
}

// PluginInfo is one registry entry, as GET /api/plugins reports it.
type PluginInfo struct {
	AppID         string                    `json:"app_id"`
	Status        string                    `json:"status"`
	Trust         string                    `json:"trust"` // the tier it is (or would be, once approved) served in
	Source        string                    `json:"source"` // sourceHello or sourceManual
	// Hello is the plug-in's hello; for a manual registration, one made up
	// from what the operator entered (HelloVersion 0).
	Hello         representable.PluginHello `json:"hello"`
	Manifest      *representable.UIManifest `json:"manifest,omitempty"`
	ETag          string                    `json:"etag,omitempty"`
	ManifestError string                    `json:"manifest_error,omitempty"`
	BasePath      string                    `json:"base_path"` // where this LR serves it, e.g. "/apps/sessions/"
	PeerHost      string                    `json:"peer_host,omitempty"`
	HelloAt       int64                     `json:"hello_at,omitempty"`      // unix seconds of the last accepted hello
	ManifestAt    int64                     `json:"manifest_at,omitempty"`   // unix seconds of the last manifest fetch attempt
	DisconnectAt  int64                     `json:"disconnect_at,omitempty"` // unix seconds it went offline; 0 while connected
}

// PluginRejection is one refused hello, kept so the debug view's "plugins"
// tab can show why a plug-in never appeared.
type PluginRejection struct {
	Name   string `json:"name"` // the representable connection's name
	AppID  string `json:"app_id,omitempty"`
	Reason string `json:"reason"`
	TS     int64  `json:"ts"`
}

const maxPluginRejections = 20

// PluginsConfig is the admission and fetch configuration this LR applies:
// compiled in, apart from the external approvals an operator made here.
// ExternalApproved are the external app ids approved on this host, kept in
// ApprovalsPath ("" keeps them in memory only); ApprovalsError is the last
// failure reading or writing that file.
type PluginsConfig struct {
	FirstParty          []string `json:"first_party"`
	ExternalApproved    []string `json:"external_approved"`
	ApprovalsPath       string   `json:"approvals_path"`
	ApprovalsError      string   `json:"approvals_error,omitempty"`
	SandboxCSP          string   `json:"sandbox_csp"`    // added to every sandboxed plug-in response
	IframeSandbox       string   `json:"iframe_sandbox"` // the frontends' iframe sandbox attribute for it
	LoopbackOnly        bool     `json:"loopback_only"`
	RequireNameMatch    bool     `json:"require_name_match"`
	RoutePrefix         string   `json:"route_prefix"`
	ManifestPath        string   `json:"manifest_path"`
	ManifestVersion     int      `json:"manifest_version"` // newest UIManifest version this LR reads
	HelloVersion        int      `json:"hello_version"`
	ManifestTimeoutSecs float64  `json:"manifest_timeout_secs"`

	// Manual are the manual registrations made on this host, kept in
	// ApprovalsPath with the approvals; ManualPollSecs is how often each is
	// checked.
	Manual         []ManualPlugin `json:"manual"`
	ManualPollSecs float64        `json:"manual_poll_secs"`
	// ExampleAppID / ExamplePort are what "register example" approves and
	// launches.
	ExampleAppID string `json:"example_app_id"`
	ExamplePort  string `json:"example_port"`
}

// ManualPlugin is one manual registration: an operator's word that appID's
// HTTP server is on this host's loopback at HTTPPort.
type ManualPlugin struct {
	AppID        string `json:"app_id"`
	HTTPPort     string `json:"http_port"`
	DisplayName  string `json:"display_name,omitempty"`
	RegisteredAt int64  `json:"registered_at"`
}

// PluginExampleStatus is the outcome of the last "register example".
type PluginExampleStatus struct {
	At     int64  `json:"at,omitempty"` // unix seconds; 0: never pressed since this LR started
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// PluginsStateMsg is the payload of "plugins-state" messages: configuration
// plus the live registry and recent refusals, broadcast to browser clients
// and mirrored up to agent-coordinator for the debug view's "plugins" tab --
// mirrors StateboardMsg.
type PluginsStateMsg struct {
	Config   PluginsConfig       `json:"config"`
	Plugins  []PluginInfo        `json:"plugins"`
	Rejected []PluginRejection   `json:"rejected"`
	Example  PluginExampleStatus `json:"example"`
}

// pluginRegistry is the Server's plug-in table; its zero value is ready
// (approvals then live in memory only -- see loadPluginApprovals).
type pluginRegistry struct {
	mu       sync.RWMutex
	entries  map[string]*PluginInfo
	rejected []PluginRejection // newest last, capped at maxPluginRejections

	approved      map[string]int64 // external app id -> unix seconds it was approved
	approvalsPath string
	approvalsErr  string

	manual  map[string]ManualPlugin // app id -> manual registration, saved with the approvals
	example PluginExampleStatus
}

func pluginBasePath(appID string) string { return "/apps/" + appID + "/" }

// isPluginPath reports whether path is inside some plug-in's tree -- the
// part of this LR a sandboxed plug-in page may reach (see
// representable.RefuseSandboxedCallers in setupRoutes).
func isPluginPath(path string) bool { return strings.HasPrefix(path, "/apps/") }

// approvedLocked reports whether appID may be routed. Call with
// s.plugins.mu held.
func (s *Server) approvedLocked(appID string) bool {
	if firstPartyPlugins[appID] {
		return true
	}
	_, ok := s.plugins.approved[appID]
	return ok
}

// pluginApprovalsFile is the on-disk form of the external approvals, and
// (Revision E) the manual registrations. Adding Manual didn't need a new
// version: a reader ignores fields it doesn't know. An older LR rewriting
// the file does drop them, though.
type pluginApprovalsFile struct {
	Version  int                    `json:"version"`
	Approved []pluginApprovalRecord `json:"approved"`
	Manual   []ManualPlugin         `json:"manual,omitempty"`
}

type pluginApprovalRecord struct {
	AppID      string `json:"app_id"`
	ApprovedAt int64  `json:"approved_at"`
}

// defaultPluginApprovalsPath is where approvals are kept unless
// --plugin-approvals says otherwise -- beside the control library.
func defaultPluginApprovalsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "local-representative", "plugin-approvals.json")
}

// loadPluginApprovals reads the external approvals from path ("" keeps them
// in memory only). A missing file is an empty list; an unreadable one is
// recorded for the debug view and also treated as empty.
func (s *Server) loadPluginApprovals(path string) {
	s.plugins.mu.Lock()
	defer s.plugins.mu.Unlock()
	s.plugins.approvalsPath = path
	s.plugins.approved = map[string]int64{}
	s.plugins.manual = map[string]ManualPlugin{}
	s.plugins.approvalsErr = ""
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var f pluginApprovalsFile
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if err != nil {
		s.plugins.approvalsErr = "reading " + path + ": " + err.Error()
		log.Printf("plugins: %s", s.plugins.approvalsErr)
		return
	}
	for _, a := range f.Approved {
		if representable.ValidAppID(a.AppID) && !firstPartyPlugins[a.AppID] {
			s.plugins.approved[a.AppID] = a.ApprovedAt
		}
	}
	for _, m := range f.Manual {
		if representable.ValidAppID(m.AppID) && !firstPartyPlugins[m.AppID] && validPluginPort(m.HTTPPort) {
			s.plugins.manual[m.AppID] = m
		}
	}
}

// validPluginPort reports whether port is a TCP port number.
func validPluginPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n < 65536 && strconv.Itoa(n) == port
}

// saveApprovalsLocked writes the external approvals back to their file. Call
// with s.plugins.mu held.
func (s *Server) saveApprovalsLocked() error {
	path := s.plugins.approvalsPath
	if path == "" {
		return nil
	}
	f := pluginApprovalsFile{Version: 1, Approved: []pluginApprovalRecord{}}
	for id, at := range s.plugins.approved {
		f.Approved = append(f.Approved, pluginApprovalRecord{AppID: id, ApprovedAt: at})
	}
	sort.Slice(f.Approved, func(i, j int) bool { return f.Approved[i].AppID < f.Approved[j].AppID })
	f.Manual = s.manualListLocked()
	b, _ := json.MarshalIndent(f, "", "  ")
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, append(b, '\n'), 0o644); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		s.plugins.approvalsErr = "writing " + path + ": " + err.Error()
		return err
	}
	s.plugins.approvalsErr = ""
	return nil
}

// setPluginApproval approves (or revokes) an external plug-in on this host.
// A connected pending plug-in goes online, and its manifest is fetched, at
// once; revoking an online one takes it back to pending (and an offline one
// out of the registry) and drops its manifest.
func (s *Server) setPluginApproval(appID string, approve bool) error {
	if !representable.ValidAppID(appID) {
		return fmt.Errorf("invalid app id %q", appID)
	}
	if firstPartyPlugins[appID] {
		return fmt.Errorf("%s is first-party: it is always approved", appID)
	}
	fetch := false
	s.plugins.mu.Lock()
	if s.plugins.approved == nil {
		s.plugins.approved = map[string]int64{}
	}
	if approve {
		if _, ok := s.plugins.approved[appID]; !ok {
			s.plugins.approved[appID] = time.Now().Unix()
		}
	} else {
		delete(s.plugins.approved, appID)
	}
	err := s.saveApprovalsLocked()
	if p := s.plugins.entries[appID]; p != nil {
		switch {
		case approve && p.Status == pluginPending:
			p.Status = pluginOnline
			fetch = true
		case !approve && p.Status == pluginOffline:
			delete(s.plugins.entries, appID)
		case !approve && p.Status == pluginOnline:
			p.Status = pluginPending
			p.Manifest, p.ETag, p.ManifestError, p.ManifestAt = nil, "", "", 0
		}
	}
	s.plugins.mu.Unlock()

	verb := map[bool]string{true: "approved", false: "revoked"}[approve]
	log.Printf("plugins: %s %s on this host", appID, verb)
	s.broadcastPlugins()
	if fetch {
		go s.fetchPluginManifest(appID)
	}
	if err != nil {
		return fmt.Errorf("%s, but not saved: %w", verb, err)
	}
	return nil
}

// notePluginHello handles a DataPluginHello from the representable client
// name.
func (s *Server) notePluginHello(name string, data json.RawMessage) {
	var hello representable.PluginHello
	if err := json.Unmarshal(data, &hello); err != nil {
		s.rejectPluginHello(name, "", "unreadable hello: "+err.Error())
		return
	}
	if err := hello.Validate(); err != nil {
		s.rejectPluginHello(name, hello.AppID, err.Error())
		return
	}
	if hello.AppID != name {
		s.rejectPluginHello(name, hello.AppID, "app id must match the connection's name")
		return
	}
	host := s.reprServer.PeerHost(name)
	if !isLoopbackHost(host) {
		s.rejectPluginHello(name, hello.AppID, "plug-ins must connect over loopback, not from "+host)
		return
	}

	s.plugins.mu.Lock()
	status := pluginOnline
	if !s.approvedLocked(hello.AppID) {
		status = pluginPending
	}
	if s.plugins.entries == nil {
		s.plugins.entries = map[string]*PluginInfo{}
	}
	p := s.plugins.entries[hello.AppID]
	if p == nil {
		p = &PluginInfo{AppID: hello.AppID, BasePath: pluginBasePath(hello.AppID)}
		s.plugins.entries[hello.AppID] = p
	}
	if p.Source == sourceManual && p.Hello.HTTPPort != hello.HTTPPort {
		// A manual entry the plug-in now speaks for itself, on another
		// port: what was fetched from the old port says nothing about this one.
		p.Manifest, p.ETag, p.ManifestError = nil, "", ""
	}
	p.Source = sourceHello
	p.Hello = hello
	p.Status = status
	p.Trust = pluginTrust(hello.AppID)
	p.PeerHost = host
	p.HelloAt = time.Now().Unix()
	p.DisconnectAt = 0
	s.plugins.mu.Unlock()

	log.Printf("plugins: %s is %s at %s (port %s)", hello.AppID, status, pluginBasePath(hello.AppID), hello.HTTPPort)
	s.broadcastPlugins()
	if status == pluginOnline {
		go s.fetchPluginManifest(hello.AppID)
	}
}

// notePluginManifestChanged handles a DataManifestChanged: re-fetch unless
// the ETag is the one already held.
func (s *Server) notePluginManifestChanged(name string, data json.RawMessage) {
	var msg representable.ManifestChanged
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	s.plugins.mu.RLock()
	p := s.plugins.entries[name]
	stale := p != nil && p.Status == pluginOnline && (msg.ETag == "" || msg.ETag != p.ETag)
	s.plugins.mu.RUnlock()
	if stale {
		go s.fetchPluginManifest(name)
	}
}

// notePluginDisconnected marks a registered plug-in offline, keeping its
// last-seen manifest. A pending one is dropped: nothing ever showed it. A
// manual entry isn't the connection's to change; and once a connected
// plug-in with a manual registration goes, its next poll takes it back.
func (s *Server) notePluginDisconnected(name string) {
	s.plugins.mu.Lock()
	p := s.plugins.entries[name]
	_, manual := s.plugins.manual[name]
	switch {
	case p == nil || p.Source == sourceManual:
		s.plugins.mu.Unlock()
		return
	case p.Status == pluginPending && !manual:
		delete(s.plugins.entries, name)
	default:
		p.Status = pluginOffline
		p.DisconnectAt = time.Now().Unix()
	}
	s.plugins.mu.Unlock()
	s.broadcastPlugins()
	if manual {
		go s.pollManualPlugin(name)
	}
}

// manualListLocked returns the manual registrations sorted by app id. Call
// with s.plugins.mu held.
func (s *Server) manualListLocked() []ManualPlugin {
	out := make([]ManualPlugin, 0, len(s.plugins.manual))
	for _, m := range s.plugins.manual {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppID < out[j].AppID })
	return out
}

// connectedLocked reports whether p is speaking for itself over a live
// representable connection. Call with s.plugins.mu held.
func connectedLocked(p *PluginInfo) bool {
	return p != nil && p.Source == sourceHello && p.Status != pluginOffline
}

// registerManualPlugin records an operator's manual registration of appID
// at a loopback HTTP port (Step3Prompt.md Revision E) and approves the app
// id: registering it here is the same decision as approving it here. It is
// then checked straight away, and every manualPollInterval after.
func (s *Server) registerManualPlugin(appID, port, displayName string) error {
	switch {
	case !representable.ValidAppID(appID):
		return fmt.Errorf("invalid app id %q (lowercase letters, digits and dashes, starting with a letter)", appID)
	case firstPartyPlugins[appID]:
		return fmt.Errorf("%s is first-party: it registers itself", appID)
	case !validPluginPort(port):
		return fmt.Errorf("invalid port %q", port)
	case port == s.httpPort || port == s.heartbeatPort:
		return fmt.Errorf("port %s is this local-representative's own", port)
	}
	displayName = strings.TrimSpace(displayName)
	if len(displayName) > 64 {
		displayName = displayName[:64]
	}

	s.plugins.mu.Lock()
	if p := s.plugins.entries[appID]; connectedLocked(p) {
		s.plugins.mu.Unlock()
		return fmt.Errorf("%s is connected and registered itself (port %s); it needs no manual registration", appID, p.Hello.HTTPPort)
	}
	if s.plugins.manual == nil {
		s.plugins.manual = map[string]ManualPlugin{}
	}
	if s.plugins.approved == nil {
		s.plugins.approved = map[string]int64{}
	}
	now := time.Now().Unix()
	s.plugins.manual[appID] = ManualPlugin{AppID: appID, HTTPPort: port, DisplayName: displayName, RegisteredAt: now}
	if _, ok := s.plugins.approved[appID]; !ok {
		s.plugins.approved[appID] = now
	}
	err := s.saveApprovalsLocked()
	if p := s.plugins.entries[appID]; p != nil && p.Hello.HTTPPort != port {
		p.Manifest, p.ETag, p.ManifestError = nil, "", ""
	}
	s.plugins.mu.Unlock()

	log.Printf("plugins: %s manually registered at port %s on this host", appID, port)
	s.pollManualPlugin(appID)
	if err != nil {
		return fmt.Errorf("registered, but not saved: %w", err)
	}
	return nil
}

// unregisterManualPlugin removes appID's manual registration, and its entry
// unless the plug-in is connected and speaking for itself. Its approval
// stays: revoke that separately.
func (s *Server) unregisterManualPlugin(appID string) error {
	s.plugins.mu.Lock()
	if _, ok := s.plugins.manual[appID]; !ok {
		s.plugins.mu.Unlock()
		return fmt.Errorf("%s has no manual registration on this host", appID)
	}
	delete(s.plugins.manual, appID)
	err := s.saveApprovalsLocked()
	if p := s.plugins.entries[appID]; p != nil && p.Source == sourceManual {
		delete(s.plugins.entries, appID)
	}
	s.plugins.mu.Unlock()

	log.Printf("plugins: %s's manual registration removed on this host", appID)
	s.broadcastPlugins()
	if err != nil {
		return fmt.Errorf("removed, but not saved: %w", err)
	}
	return nil
}

// pollManualPlugin checks one manual registration by fetching its manifest:
// an answer means online, none means offline. It leaves alone an entry the
// plug-in is speaking for over a live connection, and an app id whose
// approval was revoked (pending: never fetched). It only broadcasts when
// something an operator would see changed.
func (s *Server) pollManualPlugin(appID string) {
	s.plugins.mu.Lock()
	m, ok := s.plugins.manual[appID]
	p := s.plugins.entries[appID]
	if !ok || connectedLocked(p) {
		s.plugins.mu.Unlock()
		return
	}
	if s.plugins.entries == nil {
		s.plugins.entries = map[string]*PluginInfo{}
	}
	changed := false
	if p == nil || p.Source != sourceManual {
		if p == nil {
			p = &PluginInfo{AppID: appID, BasePath: pluginBasePath(appID), Status: pluginOffline}
			s.plugins.entries[appID] = p
		}
		p.Source = sourceManual
		p.PeerHost = ""
		changed = true
	}
	p.Hello = representable.PluginHello{AppID: appID, DisplayName: m.DisplayName, HTTPPort: m.HTTPPort}
	p.Trust = pluginTrust(appID)
	if p.HelloAt == 0 {
		p.HelloAt = m.RegisteredAt
	}
	approved := s.approvedLocked(appID)
	if !approved {
		if p.Status != pluginPending {
			p.Status = pluginPending
			p.Manifest, p.ETag, p.ManifestError, p.ManifestAt = nil, "", "", 0
			changed = true
		}
		s.plugins.mu.Unlock()
		if changed {
			s.broadcastPlugins()
		}
		return
	}
	etag := p.ETag
	s.plugins.mu.Unlock()

	manifest, newETag, notModified, err := getPluginManifest(m.HTTPPort, etag, 0)

	s.plugins.mu.Lock()
	p = s.plugins.entries[appID]
	if p == nil || p.Source != sourceManual || p.Hello.HTTPPort != m.HTTPPort || !s.approvedLocked(appID) {
		s.plugins.mu.Unlock()
		return // taken over, re-registered, unregistered or revoked meanwhile
	}
	p.ManifestAt = time.Now().Unix()
	if err != nil {
		if p.Status != pluginOffline {
			p.Status = pluginOffline
			p.DisconnectAt = time.Now().Unix()
			changed = true
		}
		if p.ManifestError != err.Error() {
			p.ManifestError = err.Error()
			changed = true
		}
	} else {
		if p.Status != pluginOnline {
			p.Status = pluginOnline
			p.DisconnectAt = 0
			changed = true
		}
		if p.ManifestError != "" {
			p.ManifestError = ""
			changed = true
		}
		if !notModified {
			if manifest.AppID != "" && manifest.AppID != appID {
				log.Printf("plugins: %s's manifest names app id %q; ignoring that field", appID, manifest.AppID)
			}
			manifest.AppID = appID
			p.Manifest = manifest
			p.ETag = newETag
			changed = true
		}
	}
	s.plugins.mu.Unlock()
	if changed {
		s.broadcastPlugins()
	}
}

// watchManualPlugins polls every manual registration each
// manualPollInterval, forever. Run in its own goroutine (see main.go).
func (s *Server) watchManualPlugins() {
	ticker := time.NewTicker(manualPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.plugins.mu.RLock()
		ids := make([]string, 0, len(s.plugins.manual))
		for id := range s.plugins.manual {
			ids = append(ids, id)
		}
		s.plugins.mu.RUnlock()
		for _, id := range ids {
			s.pollManualPlugin(id)
		}
	}
}

// registerExamplePlugin is "register example" (Step3Prompt.md Revision E):
// approve cats-ufa-plugin on this host, and launch it from the dev binaries
// (deploy-dev-binaries builds it) unless it is already connected. Once up it
// says hello like any plug-in. The outcome is kept for the registration tab;
// viaAC notes that it was pressed on agent-coordinator's copy of that tab.
func (s *Server) registerExamplePlugin(viaAC bool) error {
	result, err := s.registerExamplePluginSteps()
	if viaAC {
		result = strings.TrimSpace(result + " (via agent-coordinator)")
	}
	s.plugins.mu.Lock()
	s.plugins.example = PluginExampleStatus{At: time.Now().Unix(), Result: result}
	if err != nil {
		s.plugins.example.Error = err.Error()
	}
	s.plugins.mu.Unlock()
	s.broadcastPlugins()
	return err
}

func (s *Server) registerExamplePluginSteps() (string, error) {
	if err := s.setPluginApproval(examplePluginAppID, true); err != nil {
		return "", err
	}
	s.plugins.mu.RLock()
	connected := connectedLocked(s.plugins.entries[examplePluginAppID])
	s.plugins.mu.RUnlock()
	if connected {
		return "approved; it was already connected", nil
	}
	s.procMu.Lock()
	running := s.runningCountLocked(examplePluginAppID) > 0
	s.procMu.Unlock()
	if running {
		return "approved; already launched, waiting for its hello", nil
	}
	id, err := s.launchManaged(examplePluginAppID)
	if err != nil {
		return "approved, but not launched", err
	}
	return "approved and launched as " + id + "; waiting for its hello", nil
}

// rejectPluginHello logs a refused hello and remembers it for the debug
// view.
func (s *Server) rejectPluginHello(name, appID, reason string) {
	log.Printf("plugins: refusing %s's hello: %s", name, reason)
	s.plugins.mu.Lock()
	s.plugins.rejected = append(s.plugins.rejected, PluginRejection{Name: name, AppID: appID, Reason: reason, TS: time.Now().Unix()})
	if n := len(s.plugins.rejected); n > maxPluginRejections {
		s.plugins.rejected = append([]PluginRejection(nil), s.plugins.rejected[n-maxPluginRejections:]...)
	}
	s.plugins.mu.Unlock()
	s.broadcastPlugins()
}

// fetchPluginManifest GETs appID's manifest from its own HTTP server, with
// If-None-Match, and records it (or why it couldn't).
func (s *Server) fetchPluginManifest(appID string) {
	s.plugins.mu.RLock()
	p := s.plugins.entries[appID]
	var port, etag string
	var helloManifestVersion int
	if p != nil {
		port, etag, helloManifestVersion = p.Hello.HTTPPort, p.ETag, p.Hello.ManifestVersion
	}
	s.plugins.mu.RUnlock()
	if p == nil {
		return
	}

	manifest, newETag, notModified, err := getPluginManifest(port, etag, helloManifestVersion)

	defer s.broadcastPlugins() // deferred first, so it runs after the unlock below
	s.plugins.mu.Lock()
	defer s.plugins.mu.Unlock()
	p = s.plugins.entries[appID]
	if p == nil || p.Hello.HTTPPort != port {
		return // gone, or re-registered elsewhere while this fetch ran
	}
	p.ManifestAt = time.Now().Unix()
	switch {
	case err != nil:
		p.ManifestError = err.Error()
		log.Printf("plugins: fetching %s's manifest: %v", appID, err)
	case notModified:
		p.ManifestError = ""
	default:
		if manifest.AppID != "" && manifest.AppID != appID {
			log.Printf("plugins: %s's manifest names app id %q; ignoring that field", appID, manifest.AppID)
		}
		manifest.AppID = appID
		p.Manifest = manifest
		p.ETag = newETag
		p.ManifestError = ""
	}
}

// getPluginManifest fetches a manifest from 127.0.0.1:port. notModified is
// true on a 304 against etag.
func getPluginManifest(port, etag string, helloManifestVersion int) (m *representable.UIManifest, newETag string, notModified bool, err error) {
	if helloManifestVersion > representable.UIManifestReadVersion {
		return nil, "", false, fmt.Errorf("manifest version %d is newer than this host reads (%d)", helloManifestVersion, representable.UIManifestReadVersion)
	}
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+port+representable.UIManifestPath, nil)
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := (&http.Client{Timeout: pluginManifestTimeout}).Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, etag, true, nil
	case http.StatusOK:
	default:
		return nil, "", false, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var manifest representable.UIManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, "", false, fmt.Errorf("decoding: %w", err)
	}
	if manifest.ManifestVersion > representable.UIManifestReadVersion {
		return nil, "", false, fmt.Errorf("manifest version %d is newer than this host reads (%d)", manifest.ManifestVersion, representable.UIManifestReadVersion)
	}
	return &manifest, resp.Header.Get("ETag"), false, nil
}

// listPlugins returns copies of every registry entry, ordered by manifest
// order hint, then app id.
func (s *Server) listPlugins() []PluginInfo {
	s.plugins.mu.RLock()
	out := make([]PluginInfo, 0, len(s.plugins.entries))
	for _, p := range s.plugins.entries {
		c := *p
		if p.Manifest != nil {
			m := *p.Manifest
			c.Manifest = &m
		}
		out = append(out, c)
	}
	s.plugins.mu.RUnlock()
	order := func(p PluginInfo) int {
		if p.Manifest == nil {
			return 0
		}
		return p.Manifest.Order
	}
	sort.Slice(out, func(i, j int) bool {
		if oi, oj := order(out[i]), order(out[j]); oi != oj {
			return oi < oj
		}
		return out[i].AppID < out[j].AppID
	})
	return out
}

// pluginsConfigLocked reports the admission and fetch settings. Call with
// s.plugins.mu held (for reading).
func (s *Server) pluginsConfigLocked() PluginsConfig {
	firstParty := make([]string, 0, len(firstPartyPlugins))
	for id, ok := range firstPartyPlugins {
		if ok {
			firstParty = append(firstParty, id)
		}
	}
	sort.Strings(firstParty)
	external := make([]string, 0, len(s.plugins.approved))
	for id := range s.plugins.approved {
		external = append(external, id)
	}
	sort.Strings(external)
	return PluginsConfig{
		FirstParty:          firstParty,
		ExternalApproved:    external,
		ApprovalsPath:       s.plugins.approvalsPath,
		ApprovalsError:      s.plugins.approvalsErr,
		SandboxCSP:          representable.SandboxCSP,
		IframeSandbox:       representable.IframeSandbox,
		LoopbackOnly:        true,
		RequireNameMatch:    true,
		RoutePrefix:         "/apps/",
		ManifestPath:        representable.UIManifestPath,
		ManifestVersion:     representable.UIManifestReadVersion,
		HelloVersion:        representable.PluginHelloVersion,
		ManifestTimeoutSecs: pluginManifestTimeout.Seconds(),
		Manual:              s.manualListLocked(),
		ManualPollSecs:      manualPollInterval.Seconds(),
		ExampleAppID:        examplePluginAppID,
		ExamplePort:         s.examplePluginPort,
	}
}

// pluginsState assembles the debug view's "plugins" snapshot.
func (s *Server) pluginsState() PluginsStateMsg {
	s.plugins.mu.RLock()
	rejected := append([]PluginRejection{}, s.plugins.rejected...)
	cfg := s.pluginsConfigLocked()
	example := s.plugins.example
	s.plugins.mu.RUnlock()
	return PluginsStateMsg{Config: cfg, Plugins: s.listPlugins(), Rejected: rejected, Example: example}
}

// broadcastPlugins pushes a fresh snapshot to browser clients and
// agent-coordinator -- mirrors broadcastStateboard. Call it without holding
// s.plugins.mu.
func (s *Server) broadcastPlugins() {
	st := s.pluginsState()
	s.broadcast("plugins-state", st)
	if ac := s.getACClient(); ac != nil {
		ac.SendData("plugins-state", st)
	}
}

// handlePluginsAPI answers GET /api/plugins: the registry, for a frontend to
// build tabs from.
func (s *Server) handlePluginsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Plugins []PluginInfo `json:"plugins"`
	}{s.listPlugins()})
}

// handlePluginApproval answers POST /api/plugins/approval
// {"app_id": "...", "approved": true|false}: approve or revoke an external
// plug-in on this host (Step3Prompt.md Revision C). Only an operator on this
// host's own dashboard may: a request relayed by agent-coordinator's /host/
// proxy is refused, so a decision made for one host never spreads fleet-wide.
// Requiring a JSON body means a cross-origin page can't send it without a
// CORS preflight, which nothing here answers; RefuseSandboxedCallers already
// stops a sandboxed plug-in page.
func (s *Server) handlePluginApproval(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AppID    string `json:"app_id"`
		Approved bool   `json:"approved"`
	}
	if !decodePluginOperatorRequest(w, r, &req, false) {
		return
	}
	s.answerPluginOperatorRequest(w, s.setPluginApproval(req.AppID, req.Approved))
}

// handlePluginManual answers POST /api/plugins/manual
// {"app_id", "http_port", "display_name", "registered": true|false}: add (or
// remove) a manual registration on this host (Step3Prompt.md Revision E).
// Registering approves, so it is refused in the same cases as approval.
func (s *Server) handlePluginManual(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AppID       string `json:"app_id"`
		HTTPPort    string `json:"http_port"`
		DisplayName string `json:"display_name"`
		Registered  bool   `json:"registered"`
	}
	if !decodePluginOperatorRequest(w, r, &req, false) {
		return
	}
	var err error
	if req.Registered {
		err = s.registerManualPlugin(strings.TrimSpace(req.AppID), strings.TrimSpace(req.HTTPPort), req.DisplayName)
	} else {
		err = s.unregisterManualPlugin(req.AppID)
	}
	s.answerPluginOperatorRequest(w, err)
}

// handlePluginExample answers POST /api/plugins/example {}: "register
// example" -- see registerExamplePlugin. Unlike approval and manual
// registration it is also accepted relayed through agent-coordinator's
// /host/<id>/ proxy (Step3Prompt.md Revision F): it can only ever approve
// the one compiled-in example, never an app id the caller names, and the
// relay still targets a single host, so the decision stays per host.
func (s *Server) handlePluginExample(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodePluginOperatorRequest(w, r, &req, true) {
		return
	}
	s.answerPluginOperatorRequest(w, s.registerExamplePlugin(r.Header.Get(proxiedHeader) != ""))
}

// decodePluginOperatorRequest applies the rules every plug-in decision on
// this host shares (approval, manual registration, register example) and
// decodes the JSON body into req. Unless allowRelayed, only an operator on
// this host's own dashboard may make one: a request relayed by
// agent-coordinator's /host/ proxy is refused, so a decision made for one
// host never spreads fleet-wide. Requiring a JSON body means a cross-origin
// page can't send it without a CORS preflight, which nothing here answers;
// RefuseSandboxedCallers (here and in agent-coordinator) already stops a
// sandboxed plug-in page. It has answered the request itself when it
// returns false.
func decodePluginOperatorRequest(w http.ResponseWriter, r *http.Request, req any, allowRelayed bool) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if !allowRelayed && r.Header.Get(proxiedHeader) != "" {
		http.Error(w, "plug-ins are approved and registered on this host's own local-representative dashboard, not through agent-coordinator", http.StatusForbidden)
		return false
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(ct) != "application/json" {
		http.Error(w, "expected a JSON body", http.StatusUnsupportedMediaType)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// answerPluginOperatorRequest answers a plug-in decision: err as a 400, or
// the new snapshot.
func (s *Server) answerPluginOperatorRequest(w http.ResponseWriter, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.pluginsState())
}

// proxyToApp reverse-proxies /apps/<appId>/* to an online, approved
// plug-in's HTTP server on loopback, stripping the /apps/<appId> prefix --
// the generic form of proxyToSessions and friends. A sandboxed plug-in's
// requests lose their cookies and credentials, and its responses gain
// representable.SandboxCSP and lose any Set-Cookie.
func (s *Server) proxyToApp(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/apps/")
	appID, _, hasSlash := strings.Cut(rest, "/")
	if !representable.ValidAppID(appID) {
		http.NotFound(w, r)
		return
	}
	if !hasSlash {
		// The plug-in's relative asset URLs only resolve under the trailing
		// slash. Relative, so it still works under agent-coordinator's
		// /host/<id>/ prefix.
		target := appID + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}

	s.plugins.mu.RLock()
	p := s.plugins.entries[appID]
	var status, port, trust string
	if p != nil {
		status, port, trust = p.Status, p.Hello.HTTPPort, p.Trust
	}
	s.plugins.mu.RUnlock()
	switch status {
	case pluginOnline:
	case pluginOffline:
		http.Error(w, "plug-in "+appID+" is offline on this host", http.StatusBadGateway)
		return
	default: // unknown or pending: not routed
		http.NotFound(w, r)
		return
	}

	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + port}
	prefix := "/apps/" + appID
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		req.URL.RawPath = ""
		req.Host = target.Host
		if trust != trustFirstParty {
			req.Header.Del("Cookie")
			req.Header.Del("Authorization")
			req.Header.Del(proxiedHeader)
		}
	}
	if trust != trustFirstParty {
		proxy.ModifyResponse = func(resp *http.Response) error {
			// Added, not set: a policy the plug-in sends itself still applies
			// too, and sandbox flags only ever combine to the stricter set.
			resp.Header.Add("Content-Security-Policy", representable.SandboxCSP)
			resp.Header.Del("Set-Cookie")
			return nil
		}
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "plug-in "+appID+" not reachable on this host: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}
