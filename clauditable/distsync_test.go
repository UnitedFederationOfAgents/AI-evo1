package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestTriggerOnceTransferPostsExpectedPullRequest verifies clauditable's half
// of the once-transfer wiring (see main.go's dispatch-time call): a primary
// invocation POSTs a "mode=once" pull for the "-s-processed" glob to its
// local local-representative, addressed via LR_HTTP_HOST/LR_HTTP_PORT.
func TestTriggerOnceTransferPostsExpectedPullRequest(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	lr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
	}))
	defer lr.Close()
	lrURL, err := url.Parse(lr.URL)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvLRHTTPHost, lrURL.Hostname())
	t.Setenv(EnvLRHTTPPort, lrURL.Port())

	triggerOnceTransfer("my-session")

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/sessions/my-session/pull" {
		t.Errorf("path = %q, want /api/sessions/my-session/pull", gotPath)
	}
	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("glob") != "*-s-processed.txt" {
		t.Errorf("glob = %q, want *-s-processed.txt", q.Get("glob"))
	}
	if q.Get("mode") != "once" {
		t.Errorf("mode = %q, want once", q.Get("mode"))
	}
}

// TestTriggerOnceTransferNoLocalRepresentativeIsSilent verifies the
// overwhelmingly common case (no local-representative running at all, e.g.
// tests or simple local use) neither errors nor hangs -- it must never
// meaningfully delay the command clauditable is wrapping.
func TestTriggerOnceTransferNoLocalRepresentativeIsSilent(t *testing.T) {
	t.Setenv(EnvLRHTTPHost, "127.0.0.1")
	t.Setenv(EnvLRHTTPPort, "1") // nothing listens on port 1
	triggerOnceTransfer("my-session")
}
