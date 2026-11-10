package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"representable"
)

func TestRoutes(t *testing.T) {
	h := routes("cats-ufa-plugin", nil) // no cats-as-a-service: see TestCataas
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}

	rec := get(representable.UIManifestPath)
	var m representable.UIManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || rec.Header().Get("ETag") == "" {
		t.Fatalf("manifest: %v, etag %q", err, rec.Header().Get("ETag"))
	}
	if m.AppID != "cats-ufa-plugin" || m.Icon != "."+representable.PluginIconPath || m.Entry != "./" || m.Version != pluginVersion {
		t.Fatalf("manifest: %+v", m)
	}

	if rec := get(representable.PluginIconPath); rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("icon content type %q", rec.Header().Get("Content-Type"))
	}

	// The page only uses relative URLs, so it works under any base path.
	page := get("/").Body.String()
	if !strings.Contains(page, "fetch('./api/cat'") || strings.Contains(page, `src="/`) || strings.Contains(page, `href="/`) {
		t.Fatalf("page should fetch its API relatively and load nothing absolute")
	}
	// It comes with its cat filled in, rather than fetching one (Revision F).
	if strings.Contains(page, catPlaceholder) || !strings.Contains(page, `"fact":"`) {
		t.Fatalf("page should be served with a cat in place of %s", catPlaceholder)
	}
	if rec := get("/"); rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("page Cache-Control %q: \"another cat\" reloads it", rec.Header().Get("Cache-Control"))
	}

	rec = get("/api/cat")
	var c Cat
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil || c.Art == "" || c.Fact == "" {
		t.Fatalf("cat: %v %+v", err, c)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("a sandboxed page can only read /api/cat with Access-Control-Allow-Origin")
	}

	if rec := get("/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path: %d", rec.Code)
	}
}

// servedCat is the Cat the page at "/" was served with.
func servedCat(t *testing.T, h http.Handler) Cat {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	page := rec.Body.String()
	const prefix = "const CAT = "
	i := strings.Index(page, prefix)
	if i < 0 {
		t.Fatalf("page has no %q", prefix)
	}
	var c Cat
	if err := json.NewDecoder(strings.NewReader(page[i+len(prefix):])).Decode(&c); err != nil {
		t.Fatalf("served cat: %v", err)
	}
	return c
}

// TestCataas serves the page against a stand-in cats-as-a-service (Revision G).
func TestCataas(t *testing.T) {
	var counts atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cat" && r.URL.Query().Get("json") == "true":
			w.Write([]byte(`{"id":"abc123XYZ","tags":["cute","orange"],"mimetype":"image/jpeg"}`))
		case r.URL.Path == "/api/count":
			counts.Add(1)
			w.Write([]byte(`{"count":1984}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()

	h := routes("cats-ufa-plugin", newCataas(fake.URL+"/"))
	c := servedCat(t, h)
	if c.Cataas == nil || c.Cataas.ID != "abc123XYZ" || c.Cataas.Base != fake.URL || c.Cataas.Count != 1984 ||
		len(c.Cataas.Tags) != 2 || c.Cataas.Error != "" {
		t.Fatalf("cataas: %+v", c.Cataas)
	}
	if c.Art == "" {
		t.Fatalf("the ASCII cat should still be there")
	}
	servedCat(t, h)
	if n := counts.Load(); n != 1 {
		t.Fatalf("/api/count called %d times, want 1 (cached)", n)
	}

	// An older answer names the id _id; an id that isn't one is refused.
	for body, wantID := range map[string]string{
		`{"_id":"legacy_1","tags":[]}`: "legacy_1",
		`{"id":"../../x"}`:             "",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		got := servedCat(t, routes("cats-ufa-plugin", newCataas(srv.URL))).Cataas
		srv.Close()
		if got.ID != wantID || (wantID == "") != (got.Error != "") {
			t.Fatalf("%s: %+v", body, got)
		}
	}

	// Unreachable: the page still comes, with the reason and its ASCII cat.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	c = servedCat(t, routes("cats-ufa-plugin", newCataas(down.URL)))
	down.Close()
	if c.Cataas == nil || c.Cataas.ID != "" || !strings.Contains(c.Cataas.Error, "503") || c.Art == "" {
		t.Fatalf("down: %+v", c)
	}

	if newCataas("") != nil || servedCat(t, routes("cats-ufa-plugin", nil)).Cataas != nil {
		t.Fatalf(`--cataas-url "" should never call it`)
	}
}
