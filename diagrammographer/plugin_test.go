package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"representable"
)

func TestUIManifestRoutes(t *testing.T) {
	s := newServer()
	s.name = "dg"
	h := s.setupRoutes(true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", representable.UIManifestPath, nil))
	var m representable.UIManifest
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == "" || json.Unmarshal(rec.Body.Bytes(), &m) != nil {
		t.Fatalf("manifest: status %d, etag %q, body %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	if m.AppID != "dg" || m.Icon != "."+representable.PluginIconPath ||
		m.ManifestVersion != representable.UIManifestVersion2 || m.Version != pluginVersion || !m.IconMask {
		t.Fatalf("unexpected manifest: %+v", m)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", representable.PluginIconPath, nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("icon: status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestStatusAPI(t *testing.T) {
	s := newServer()
	s.name = "dg"
	h := s.setupRoutes(true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/status", nil))
	var st StatusMsg
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
		t.Fatalf("status: %d, body %s", rec.Code, rec.Body)
	}
	if st.AppID != "dg" || st.ReprStatus != "disconnected" {
		t.Fatalf("unexpected status: %+v", st)
	}
}
