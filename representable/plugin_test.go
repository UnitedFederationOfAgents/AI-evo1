package representable

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidAppID(t *testing.T) {
	for id, want := range map[string]bool{
		"sessions":  true,
		"tc-2":      true,
		"":          false,
		"2fast":     false,
		"Sessions":  false,
		"a/b":       false,
		"..":        false,
		"has space": false,
		"abcdefghijklmnopqrstuvwxyz012345":  true,
		"abcdefghijklmnopqrstuvwxyz0123456": false,
	} {
		if got := ValidAppID(id); got != want {
			t.Errorf("ValidAppID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestPluginHelloValidate(t *testing.T) {
	if err := (PluginHello{AppID: "sessions", HTTPPort: "8085"}).Validate(); err != nil {
		t.Errorf("valid hello: %v", err)
	}
	if err := (PluginHello{AppID: "sessions"}).Validate(); err == nil {
		t.Errorf("hello without a port should not validate")
	}
	if err := (PluginHello{AppID: "../x", HTTPPort: "8085"}).Validate(); err == nil {
		t.Errorf("hello with a path in its app id should not validate")
	}
}

func TestServeUIManifestETag(t *testing.T) {
	m := UIManifest{ManifestVersion: UIManifestVersion, AppID: "sessions", DisplayName: "Sessions"}
	h := ServeUIManifest(func() UIManifest { return m })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", UIManifestPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("no ETag")
	}
	var got UIManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got != m {
		t.Fatalf("decoded %+v (%v), want %+v", got, err, m)
	}

	req := httptest.NewRequest("GET", UIManifestPath, nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: status %d, want 304", rec.Code)
	}

	// A changed manifest gets a new ETag.
	m.Order = 5
	req = httptest.NewRequest("GET", UIManifestPath, nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == etag {
		t.Fatalf("changed manifest: status %d, etag %q (old %q)", rec.Code, rec.Header().Get("ETag"), etag)
	}
}

func TestRefuseSandboxedCallers(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := RefuseSandboxedCallers(ok, func(p string) bool { return len(p) >= 6 && p[:6] == "/apps/" })
	for _, tc := range []struct {
		name    string
		path    string
		headers map[string]string
		want    int
	}{
		{"own UI", "/api/plugins", map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors"}, http.StatusNoContent},
		{"no browser headers", "/ws", nil, http.StatusNoContent},
		{"opaque websocket", "/ws", map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"opaque image", "/api/files/x", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image"}, http.StatusForbidden},
		{"frame navigating itself", "/api/x", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"}, http.StatusForbidden},
		{"operator following a link", "/", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}, http.StatusNoContent},
		{"plug-in's own tree", "/apps/cats/api/cat", map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site"}, http.StatusNoContent},
	} {
		req := httptest.NewRequest("GET", tc.path, nil)
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}
