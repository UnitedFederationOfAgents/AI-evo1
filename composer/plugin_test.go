package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"representable"
)

func TestUIManifestRoutes(t *testing.T) {
	s := newServer(t.TempDir())
	s.name = "compose"
	h := s.setupRoutes(true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", representable.UIManifestPath, nil))
	var m representable.UIManifest
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == "" || json.Unmarshal(rec.Body.Bytes(), &m) != nil {
		t.Fatalf("manifest: status %d, etag %q, body %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	if m.AppID != "compose" || m.Icon != "."+representable.PluginIconPath ||
		m.ManifestVersion != representable.UIManifestVersion2 || m.Version != pluginVersion || !m.IconMask {
		t.Fatalf("unexpected manifest: %+v", m)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", representable.PluginIconPath, nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("icon: status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
