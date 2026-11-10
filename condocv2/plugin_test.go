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
	s.name = "condocv2"
	h := s.setupRoutes(true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", representable.UIManifestPath, nil))
	var m representable.UIManifest
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == "" || json.Unmarshal(rec.Body.Bytes(), &m) != nil {
		t.Fatalf("manifest: status %d, etag %q, body %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	if m.AppID != "condocv2" || m.Icon != "."+representable.PluginIconPath {
		t.Fatalf("unexpected manifest: %+v", m)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", representable.PluginIconPath, nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("icon: status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
