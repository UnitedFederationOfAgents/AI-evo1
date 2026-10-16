package prkeeper

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func boolp(b bool) *bool { return &b }

func TestReadyAndStatus(t *testing.T) {
	cases := []struct {
		pr     PullRequest
		ready  bool
		status string
	}{
		{PullRequest{State: "open", Mergeable: boolp(true), MergeableState: "clean"}, true, "mergeable"},
		{PullRequest{State: "open", Mergeable: boolp(true), MergeableState: "unstable"}, true, "mergeable"},
		{PullRequest{State: "open", Mergeable: boolp(true), MergeableState: "blocked"}, false, "blocked"},
		{PullRequest{State: "open", Mergeable: boolp(false), MergeableState: "dirty"}, false, "dirty"},
		{PullRequest{State: "open", MergeableState: "unknown"}, false, "pending"},
		{PullRequest{State: "closed", Merged: true}, false, "merged"},
		{PullRequest{State: "closed"}, false, "closed"},
	}
	for i, c := range cases {
		if got := c.pr.Ready(); got != c.ready {
			t.Errorf("%d: Ready() = %v, want %v", i, got, c.ready)
		}
		if got := c.pr.Status(); got != c.status {
			t.Errorf("%d: Status() = %q, want %q", i, got, c.status)
		}
	}
}

func TestClientRoundTrip(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls":
			var req CreateRequest
			json.Unmarshal(body, &req)
			if req.Head != "condoc/X-1/main" || req.Base != "main" || req.Title != "T" {
				t.Errorf("create request = %+v", req)
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"number":7,"html_url":"https://github.com/o/r/pull/7","state":"open","head":{"ref":"condoc/X-1/main","sha":"abc"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls":
			if r.URL.Query().Get("head") != "o:condoc/X-1/main" {
				t.Errorf("head query = %q", r.URL.Query().Get("head"))
			}
			w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls/7":
			w.Write([]byte(`{"number":7,"state":"open","mergeable":true,"mergeable_state":"clean","head":{"sha":"abc"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/pulls/7":
			if !strings.Contains(string(body), "siblings") {
				t.Errorf("patch body = %s", body)
			}
			w.Write([]byte(`{}`))
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/pulls/7/merge":
			if !strings.Contains(string(body), `"merge_method":"merge"`) || !strings.Contains(string(body), `"sha":"abc"`) {
				t.Errorf("merge body = %s", body)
			}
			w.Write([]byte(`{"merged":true}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/git/refs/heads/condoc/X-1/main":
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"Reference does not exist"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/missing":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not Found"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	c := NewClient("tok")
	c.BaseURL = srv.URL

	if pr, err := c.FindOpen("o/r", "condoc/X-1/main"); err != nil || pr != nil {
		t.Fatalf("FindOpen = %+v, %v", pr, err)
	}
	pr, err := c.Create("o/r", CreateRequest{Title: "T", Head: "condoc/X-1/main", Base: "main", Body: "B"})
	if err != nil || pr.Number != 7 || pr.URL == "" {
		t.Fatalf("Create = %+v, %v", pr, err)
	}
	got, err := c.Get("o/r", 7)
	if err != nil || !got.Ready() {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if err := c.UpdateBody("o/r", 7, "siblings"); err != nil {
		t.Fatal(err)
	}
	if err := c.Merge("o/r", 7, MergeMethodMerge, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteBranch("o/r", "condoc/X-1/main"); err != nil {
		t.Fatalf("DeleteBranch of a missing branch: %v", err)
	}
	_, err = c.DefaultBranch("o/missing")
	if !IsStatus(err, http.StatusNotFound) || !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("DefaultBranch error = %v", err)
	}
	if len(seen) != 7 {
		t.Errorf("requests = %v", seen)
	}
}
