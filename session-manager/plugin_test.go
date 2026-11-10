package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"representable"
)

func TestUIManifestServedWithETag(t *testing.T) {
	s := newServer()
	s.name = "sessions"
	h := s.setupRoutes(true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", representable.UIManifestPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("no ETag")
	}
	var m representable.UIManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if m.AppID != "sessions" || m.ManifestVersion != representable.UIManifestVersion2 || m.Icon != "./plugin-icon.svg" || !m.IconMask {
		t.Fatalf("unexpected manifest: %+v", m)
	}

	req := httptest.NewRequest("GET", representable.UIManifestPath, nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: status %d, want 304", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", representable.PluginIconPath, nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("icon: status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
