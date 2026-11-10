package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"representable"
)

// fakePlugin serves a manifest and one page, the way session-manager does.
func fakePlugin(t *testing.T, appID string) (port string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(representable.UIManifestPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		// future_field: readers must ignore fields they don't know.
		io.WriteString(w, `{"manifest_version":1,"app_id":"`+appID+`","display_name":"Fake","icon":"./icon.svg","order":7,"future_field":{"x":1}}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "plugin saw "+r.URL.Path)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, port, _ = net.SplitHostPort(srv.Listener.Addr().String())
	return port
}

// newPluginTestServer wires a real representable server to s the way main()
// does, for plug-in traffic only.
func newPluginTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	addr := freeTCPAddr(t)
	reprSrv, err := representable.NewServer(addr, representable.Mode(false))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	s := newServer("test-lr")
	s.reprServer = reprSrv
	reprSrv.SetDataHandler(func(name, dataType string, data json.RawMessage) {
		switch dataType {
		case representable.DataPluginHello:
			s.notePluginHello(name, data)
		case representable.DataManifestChanged:
			s.notePluginManifestChanged(name, data)
		}
	})
	reprSrv.SetStateChangeHandler(func(name, state string) {
		if state == "disconnected" {
			s.notePluginDisconnected(name)
		}
	})
	return s, addr
}

func waitPlugin(t *testing.T, s *Server, appID string, ok func(PluginInfo) bool) PluginInfo {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range s.listPlugins() {
			if p.AppID == appID && ok(p) {
				return p
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("plug-in %s never reached the expected state; registry: %+v", appID, s.listPlugins())
	return PluginInfo{}
}

func TestPluginHelloRoutesApprovedApp(t *testing.T) {
	s, addr := newPluginTestServer(t)
	port := fakePlugin(t, "sessions")

	c, err := representable.Connect(addr, "sessions", representable.Mode(false), time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	c.SendPluginHello(representable.PluginHello{AppID: "sessions", DisplayName: "Sessions", HTTPPort: port})

	p := waitPlugin(t, s, "sessions", func(p PluginInfo) bool { return p.Manifest != nil })
	if p.Status != pluginOnline || p.Manifest.Order != 7 || p.ETag != `"v1"` || p.BasePath != "/apps/sessions/" {
		t.Fatalf("unexpected registry entry: %+v", p)
	}

	rec := httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/apps/sessions/assets/x.js", nil))
	if got := rec.Body.String(); got != "plugin saw /assets/x.js" {
		t.Fatalf("proxy: got %q", got)
	}

	rec = httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/apps/sessions", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "sessions/" {
		t.Fatalf("bare app path: got %d to %q", rec.Code, rec.Header().Get("Location"))
	}

	// Disconnecting keeps the manifest but stops routing.
	c.Close()
	waitPlugin(t, s, "sessions", func(p PluginInfo) bool { return p.Status == pluginOffline && p.Manifest != nil })
	rec = httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/apps/sessions/", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("offline plug-in: got %d, want 502", rec.Code)
	}
}

func TestPluginHelloUnapprovedIsPending(t *testing.T) {
	s, addr := newPluginTestServer(t)
	port := fakePlugin(t, "stranger")

	c, err := representable.Connect(addr, "stranger", representable.Mode(false), time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()
	c.SendPluginHello(representable.PluginHello{AppID: "stranger", HTTPPort: port})

	p := waitPlugin(t, s, "stranger", func(p PluginInfo) bool { return true })
	if p.Status != pluginPending || p.Manifest != nil {
		t.Fatalf("unapproved plug-in should be pending with no manifest fetched: %+v", p)
	}
	rec := httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/apps/stranger/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("pending plug-in: got %d, want 404", rec.Code)
	}
}

func TestPluginHelloMustMatchConnectionName(t *testing.T) {
	s, addr := newPluginTestServer(t)
	port := fakePlugin(t, "sessions")

	c, err := representable.Connect(addr, "impostor", representable.Mode(false), time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()
	c.SendPluginHello(representable.PluginHello{AppID: "sessions", HTTPPort: port})
	// A later, valid message on the same connection proves the hello was
	// processed (and refused) by then.
	c.SendPluginHello(representable.PluginHello{AppID: "impostor", HTTPPort: port})
	waitPlugin(t, s, "impostor", func(p PluginInfo) bool { return true })
	for _, p := range s.listPlugins() {
		if p.AppID == "sessions" {
			t.Fatalf("hello for another connection's app id was accepted: %+v", p)
		}
	}

	// The debug view's snapshot shows the refusal alongside the live entry.
	st := s.pluginsState()
	if len(st.Rejected) != 1 || st.Rejected[0].Name != "impostor" || st.Rejected[0].AppID != "sessions" {
		t.Fatalf("rejections: %+v", st.Rejected)
	}
	if len(st.Plugins) != 1 || st.Plugins[0].AppID != "impostor" || st.Plugins[0].HelloAt == 0 || !isLoopbackHost(st.Plugins[0].PeerHost) {
		t.Fatalf("plugins: %+v", st.Plugins)
	}
}

func TestPluginsStateConfig(t *testing.T) {
	cfg := newServer("test-lr").pluginsState().Config
	if strings.Join(cfg.FirstParty, ",") != "compose,condocv2,convo,dg,robot,sessions" || len(cfg.ExternalApproved) != 0 ||
		!cfg.LoopbackOnly || !cfg.RequireNameMatch || cfg.SandboxCSP != representable.SandboxCSP ||
		cfg.ManifestPath != representable.UIManifestPath || cfg.ManifestVersion != representable.UIManifestReadVersion ||
		cfg.ExampleAppID != examplePluginAppID || len(cfg.Manual) != 0 {
		t.Fatalf("config: %+v", cfg)
	}
}

// TestPluginManifestV2 reads a version 2 manifest (Revision E) from a
// plug-in whose hello says so.
func TestPluginManifestV2(t *testing.T) {
	s, addr := newPluginTestServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc(representable.UIManifestPath, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"manifest_version":2,"app_id":"sessions","display_name":"Sessions","icon":"./i.svg","icon_mask":true,"description":"d"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	c, err := representable.Connect(addr, "sessions", representable.Mode(false), time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()
	c.SendPluginHello(representable.PluginHello{AppID: "sessions", HTTPPort: port, ManifestVersion: representable.UIManifestVersion2})
	p := waitPlugin(t, s, "sessions", func(p PluginInfo) bool { return p.Manifest != nil || p.ManifestError != "" })
	if p.Manifest == nil || p.Manifest.ManifestVersion != 2 || !p.Manifest.IconMask || p.Manifest.Description != "d" {
		t.Fatalf("v2 manifest: %+v (error %q)", p.Manifest, p.ManifestError)
	}
	if _, _, _, err := getPluginManifest(port, "", representable.UIManifestReadVersion+1); err == nil {
		t.Fatalf("a hello naming a newer manifest version than this host reads should not be fetched")
	}
}

// TestManualPluginRegistration walks a manual registration (Revision E):
// approved and polled online, served sandboxed, offline when its server
// stops answering, persisted, and removed.
func TestManualPluginRegistration(t *testing.T) {
	approvals := filepath.Join(t.TempDir(), "plugin-approvals.json")
	s := newServer("test-lr")
	s.httpPort, s.heartbeatPort = "1", "2"
	s.loadPluginApprovals(approvals)

	mux := http.NewServeMux()
	mux.HandleFunc(representable.UIManifestPath, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"manifest_version":1,"app_id":"manual-one","display_name":"Manual"}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "manual saw "+r.URL.Path) })
	srv := httptest.NewServer(mux)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	for _, bad := range [][2]string{{"Bad Id", port}, {"sessions", port}, {"manual-one", "x"}, {"manual-one", "1"}} {
		if err := s.registerManualPlugin(bad[0], bad[1], ""); err == nil {
			t.Fatalf("registering %q at %q should fail", bad[0], bad[1])
		}
	}
	if err := s.registerManualPlugin("manual-one", port, "Manual One"); err != nil {
		t.Fatalf("register: %v", err)
	}
	p := waitPlugin(t, s, "manual-one", func(p PluginInfo) bool { return p.Status == pluginOnline })
	if p.Source != sourceManual || p.Trust != trustSandboxed || p.Manifest == nil || p.Hello.DisplayName != "Manual One" {
		t.Fatalf("manual entry: %+v", p)
	}
	rec := httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/apps/manual-one/x", nil))
	if rec.Body.String() != "manual saw /x" || rec.Header().Get("Content-Security-Policy") != representable.SandboxCSP {
		t.Fatalf("manual proxy: body %q, CSP %q", rec.Body.String(), rec.Header().Get("Content-Security-Policy"))
	}

	// Persisted with the approvals.
	s2 := newServer("test-lr")
	s2.loadPluginApprovals(approvals)
	if cfg := s2.pluginsState().Config; len(cfg.Manual) != 1 || cfg.Manual[0].HTTPPort != port || strings.Join(cfg.ExternalApproved, ",") != "manual-one" {
		t.Fatalf("reloaded: %+v", cfg)
	}

	srv.Close()
	s.pollManualPlugin("manual-one")
	p = waitPlugin(t, s, "manual-one", func(p PluginInfo) bool { return true })
	if p.Status != pluginOffline || p.ManifestError == "" || p.Manifest == nil {
		t.Fatalf("after its server stopped: %+v", p)
	}

	if err := s.unregisterManualPlugin("manual-one"); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	for _, p := range s.listPlugins() {
		if p.AppID == "manual-one" {
			t.Fatalf("unregistered entry still listed: %+v", p)
		}
	}
}

func TestPluginManualEndpointRefusesRelayed(t *testing.T) {
	s := newServer("test-lr")
	req := httptest.NewRequest("POST", "/api/plugins/manual", strings.NewReader(`{"app_id":"x","http_port":"9","registered":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(proxiedHeader, acRelayStamp)
	rec := httptest.NewRecorder()
	s.handlePluginManual(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/api/plugins/manual relayed through agent-coordinator: got %d, want 403", rec.Code)
	}
}

// TestPluginExampleEndpointAcceptsRelayed: "register example" works from
// agent-coordinator's registration tab too (Step3Prompt.md Revision F). The
// example is already connected here, so nothing is launched.
func TestPluginExampleEndpointAcceptsRelayed(t *testing.T) {
	s, addr := newPluginTestServer(t)
	port := fakePlugin(t, examplePluginAppID)
	c, err := representable.Connect(addr, examplePluginAppID, representable.Mode(false), time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()
	c.SendPluginHello(representable.PluginHello{AppID: examplePluginAppID, HTTPPort: port})
	waitPlugin(t, s, examplePluginAppID, func(p PluginInfo) bool { return p.Status == pluginPending })

	req := httptest.NewRequest("POST", "/api/plugins/example", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(proxiedHeader, acRelayStamp)
	rec := httptest.NewRecorder()
	s.handlePluginExample(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("relayed register example: got %d %q, want 200", rec.Code, rec.Body.String())
	}
	waitPlugin(t, s, examplePluginAppID, func(p PluginInfo) bool { return p.Status == pluginOnline })
	if ex := s.pluginsState().Example; !strings.Contains(ex.Result, "via agent-coordinator") || ex.Error != "" {
		t.Fatalf("example status: %+v", ex)
	}
}

// TestPluginApprovalSandboxesExternalApp walks an external plug-in from
// pending through approval (served sandboxed), a restart that reloads the
// approval, and revocation.
func TestPluginApprovalSandboxesExternalApp(t *testing.T) {
	approvals := filepath.Join(t.TempDir(), "plugin-approvals.json")
	s, addr := newPluginTestServer(t)
	s.loadPluginApprovals(approvals)
	port := fakePlugin(t, "cats")

	c, err := representable.Connect(addr, "cats", representable.Mode(false), time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()
	c.SendPluginHello(representable.PluginHello{AppID: "cats", HTTPPort: port})
	p := waitPlugin(t, s, "cats", func(p PluginInfo) bool { return true })
	if p.Status != pluginPending || p.Trust != trustSandboxed {
		t.Fatalf("before approval: %+v", p)
	}

	if err := s.setPluginApproval("cats", true); err != nil {
		t.Fatalf("approve: %v", err)
	}
	waitPlugin(t, s, "cats", func(p PluginInfo) bool { return p.Status == pluginOnline && p.Manifest != nil })

	req := httptest.NewRequest("GET", "/apps/cats/index.html", nil)
	req.Header.Set("Cookie", "session=secret")
	rec := httptest.NewRecorder()
	s.proxyToApp(rec, req)
	if rec.Body.String() != "plugin saw /index.html" || rec.Header().Get("Content-Security-Policy") != representable.SandboxCSP {
		t.Fatalf("sandboxed proxy: body %q, CSP %q", rec.Body.String(), rec.Header().Get("Content-Security-Policy"))
	}

	// The approval survives a restart; first-party apps never need one.
	s2 := newServer("test-lr")
	s2.loadPluginApprovals(approvals)
	if cfg := s2.pluginsState().Config; strings.Join(cfg.ExternalApproved, ",") != "cats" || cfg.ApprovalsError != "" {
		t.Fatalf("reloaded approvals: %+v", cfg)
	}
	if err := s.setPluginApproval("sessions", false); err == nil {
		t.Fatalf("revoking a first-party app should fail")
	}

	if err := s.setPluginApproval("cats", false); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	p = waitPlugin(t, s, "cats", func(p PluginInfo) bool { return p.Status == pluginPending })
	if p.Manifest != nil {
		t.Fatalf("revoked plug-in kept its manifest: %+v", p)
	}
	rec = httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/apps/cats/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoked plug-in: got %d, want 404", rec.Code)
	}
}

func TestPluginApprovalEndpointRefusesRelayed(t *testing.T) {
	s := newServer("test-lr")
	post := func(body string, header map[string]string) int {
		req := httptest.NewRequest("POST", "/api/plugins/approval", strings.NewReader(body))
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		s.handlePluginApproval(rec, req)
		return rec.Code
	}
	body := `{"app_id":"cats","approved":true}`
	if got := post(body, map[string]string{"Content-Type": "application/json", proxiedHeader: acRelayStamp}); got != http.StatusForbidden {
		t.Fatalf("relayed through agent-coordinator: got %d, want 403", got)
	}
	if got := post(body, map[string]string{"Content-Type": "text/plain"}); got != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON body: got %d, want 415", got)
	}
	if got := post(body, map[string]string{"Content-Type": "application/json"}); got != http.StatusOK {
		t.Fatalf("direct: got %d, want 200", got)
	}
	if cfg := s.pluginsState().Config; strings.Join(cfg.ExternalApproved, ",") != "cats" {
		t.Fatalf("approvals: %+v", cfg)
	}
}

func TestSetupRoutesRefusesSandboxedPage(t *testing.T) {
	h := newServer("test-lr").setupRoutes(true)
	req := httptest.NewRequest("GET", "/api/plugins", nil)
	req.Header.Set("Origin", "null")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("sandboxed page calling the dashboard API: got %d, want 403", rec.Code)
	}
}
