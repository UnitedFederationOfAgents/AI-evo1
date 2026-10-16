package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"ufa-loader/awsprincipal"
)

// clearProviderEnv removes every credential source detect looks at, so each
// test starts from "nothing present".
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range append([]string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_DEFAULT_PROFILE"}, githubTokenEnvVars...) {
		t.Setenv(k, "")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_CONFIG_FILE", missing)
}

func fakeGitHub(t *testing.T, goodToken, login string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer "+goodToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"login":"` + login + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestConnectivityNothingPresent(t *testing.T) {
	clearProviderEnv(t)
	w := newConnectivityWatch(nil)
	if failed := w.check(); failed {
		t.Error("check reported a failure with no providers present")
	}
	if got := w.connections(); len(got) != 0 {
		t.Errorf("connections = %+v, want none", got)
	}
}

func TestConnectivityVerifiesBothProviders(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("GH_TOKEN", "good")

	notified := 0
	w := newConnectivityWatch(func() { notified++ })
	w.githubAPI = fakeGitHub(t, "good", "octocat")
	w.awsIdentity = func(_ context.Context, c awsprincipal.Credentials) (string, error) {
		if c.AccessKeyID != "AKIATEST" {
			return "", errors.New("wrong key")
		}
		return "arn:aws:iam::123456789012:user/tester", nil
	}

	if failed := w.check(); failed {
		t.Fatalf("check failed: %+v", w.connections())
	}
	want := []ConnectionInfo{
		{Provider: "aws", Status: connOK, Principal: "arn:aws:iam::123456789012:user/tester", Source: "environment"},
		{Provider: "github", Status: connOK, Principal: "octocat", Source: "GH_TOKEN"},
	}
	got := w.connections()
	if len(got) != len(want) {
		t.Fatalf("connections = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("connections[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if notified == 0 {
		t.Error("notify never called")
	}

	// A steady state doesn't re-notify.
	before := notified
	w.check()
	if notified != before {
		t.Errorf("unchanged re-check notified %d more time(s)", notified-before)
	}
}

func TestConnectivityRejectedGitHubToken(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("TF_VAR_github_pat", "stale")

	w := newConnectivityWatch(nil)
	w.githubAPI = fakeGitHub(t, "good", "octocat")
	if failed := w.check(); !failed {
		t.Fatal("check didn't report the rejected token")
	}
	got := w.connections()
	if len(got) != 1 || got[0].Provider != "github" || got[0].Status != connFailed || got[0].Principal != "" || got[0].Detail == "" {
		t.Errorf("connections = %+v, want one failed github entry with a detail", got)
	}
}

func TestSystemStateConnectionsNeverNull(t *testing.T) {
	s := newServer("test")
	if st := s.systemState(); st.Connections == nil {
		t.Error("Connections is nil before the watch exists; want an empty slice")
	}
}
