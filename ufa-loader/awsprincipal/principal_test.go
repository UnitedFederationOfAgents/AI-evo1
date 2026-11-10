package awsprincipal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIAM is an in-memory stand-in for the handful of IAM/STS query actions
// Provision uses.
type fakeIAM struct {
	mu       sync.Mutex
	users    map[string]*fakeUser
	keyOwner map[string]string // access key id -> user
	nextKey  int
	failOn   string // action that returns an error
	calls    []string
}

type fakeUser struct {
	path     string
	tags     map[string]string
	created  time.Time
	keys     map[string]time.Time // id -> last used (zero = never)
	policies map[string]bool
}

func newFakeIAM() *fakeIAM {
	return &fakeIAM{users: map[string]*fakeUser{}, keyOwner: map[string]string{}}
}

func (f *fakeIAM) addUser(name string, tags map[string]string, created time.Time, keyLastUsed ...time.Time) {
	u := &fakeUser{path: Path, tags: tags, created: created, keys: map[string]time.Time{}, policies: map[string]bool{policyARN: true}}
	for _, lu := range keyLastUsed {
		f.nextKey++
		id := fmt.Sprintf("AKIASEED%d", f.nextKey)
		u.keys[id] = lu
		f.keyOwner[id] = name
	}
	f.users[name] = u
}

var credentialRE = regexp.MustCompile(`Credential=([^/]+)/`)

func (f *fakeIAM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	action := r.PostForm.Get("Action")
	f.calls = append(f.calls, action)
	fail := func(code string) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "<ErrorResponse><Error><Code>%s</Code><Message>fake</Message></Error></ErrorResponse>", code)
	}
	if action == f.failOn {
		fail("InjectedFailure")
		return
	}
	reply := func(body string) {
		fmt.Fprintf(w, "<%sResponse><%sResult>%s</%sResult></%sResponse>", action, action, body, action, action)
	}
	name := r.PostForm.Get("UserName")
	u := f.users[name]
	needUser := func() bool {
		if u == nil {
			fail("NoSuchEntity")
			return false
		}
		return true
	}

	switch action {
	case "GetCallerIdentity":
		m := credentialRE.FindStringSubmatch(r.Header.Get("Authorization"))
		if m == nil || f.keyOwner[m[1]] == "" {
			fail("InvalidClientTokenId")
			return
		}
		reply("<Arn>arn:aws:iam::123456789012:user" + Path + f.keyOwner[m[1]] + "</Arn>")
	case "CreateUser":
		if u != nil {
			fail("EntityAlreadyExists")
			return
		}
		tags := map[string]string{}
		for i := 1; r.PostForm.Get(fmt.Sprintf("Tags.member.%d.Key", i)) != ""; i++ {
			tags[r.PostForm.Get(fmt.Sprintf("Tags.member.%d.Key", i))] = r.PostForm.Get(fmt.Sprintf("Tags.member.%d.Value", i))
		}
		f.users[name] = &fakeUser{path: r.PostForm.Get("Path"), tags: tags, created: time.Now(), keys: map[string]time.Time{}, policies: map[string]bool{}}
		reply("<User><UserName>" + name + "</UserName></User>")
	case "AttachUserPolicy":
		if needUser() {
			u.policies[r.PostForm.Get("PolicyArn")] = true
			reply("")
		}
	case "CreateAccessKey":
		if needUser() {
			f.nextKey++
			id := fmt.Sprintf("AKIANEW%d", f.nextKey)
			u.keys[id] = time.Time{}
			f.keyOwner[id] = name
			reply("<AccessKey><AccessKeyId>" + id + "</AccessKeyId><SecretAccessKey>secret-" + id + "</SecretAccessKey></AccessKey>")
		}
	case "ListUsers":
		var b strings.Builder
		for n, fu := range f.users {
			if strings.HasPrefix(fu.path, r.PostForm.Get("PathPrefix")) {
				fmt.Fprintf(&b, "<member><UserName>%s</UserName><CreateDate>%s</CreateDate></member>", n, fu.created.UTC().Format(time.RFC3339))
			}
		}
		reply("<IsTruncated>false</IsTruncated><Users>" + b.String() + "</Users>")
	case "ListUserTags":
		if needUser() {
			var b strings.Builder
			for k, v := range u.tags {
				fmt.Fprintf(&b, "<member><Key>%s</Key><Value>%s</Value></member>", k, v)
			}
			reply("<Tags>" + b.String() + "</Tags>")
		}
	case "ListAccessKeys":
		if needUser() {
			var b strings.Builder
			for id := range u.keys {
				fmt.Fprintf(&b, "<member><AccessKeyId>%s</AccessKeyId></member>", id)
			}
			reply("<AccessKeyMetadata>" + b.String() + "</AccessKeyMetadata>")
		}
	case "GetAccessKeyLastUsed":
		id := r.PostForm.Get("AccessKeyId")
		lu := f.users[f.keyOwner[id]].keys[id]
		if lu.IsZero() {
			reply("<AccessKeyLastUsed><Region>N/A</Region></AccessKeyLastUsed>")
		} else {
			reply("<AccessKeyLastUsed><LastUsedDate>" + lu.UTC().Format(time.RFC3339) + "</LastUsedDate></AccessKeyLastUsed>")
		}
	case "DeleteAccessKey":
		if needUser() {
			id := r.PostForm.Get("AccessKeyId")
			delete(u.keys, id)
			delete(f.keyOwner, id)
			reply("")
		}
	case "ListAttachedUserPolicies":
		if needUser() {
			var b strings.Builder
			for arn := range u.policies {
				fmt.Fprintf(&b, "<member><PolicyArn>%s</PolicyArn></member>", arn)
			}
			reply("<AttachedPolicies>" + b.String() + "</AttachedPolicies>")
		}
	case "DetachUserPolicy":
		if needUser() {
			delete(u.policies, r.PostForm.Get("PolicyArn"))
			reply("")
		}
	case "DeleteUser":
		if needUser() {
			if len(u.keys) > 0 || len(u.policies) > 0 {
				fail("DeleteConflict")
				return
			}
			delete(f.users, name)
			reply("")
		}
	default:
		fail("InvalidAction")
	}
}

func testClient(t *testing.T, f *fakeIAM, now time.Time) *client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := newClient(Credentials{AccessKeyID: "AKIASTART", SecretAccessKey: "start-secret"})
	c.iamEndpoint = srv.URL + "/"
	c.stsEndpoint = srv.URL + "/sts/"
	c.now = func() time.Time { return now }
	return c
}

func TestProvisionCreatesTaggedPrincipal(t *testing.T) {
	f := newFakeIAM()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	c := testClient(t, f, now)

	res, err := provision(context.Background(), c, Options{HostID: "devbox-ab12", App: "the-conversationalist", IdleDays: 2})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !regexp.MustCompile(`^ufa-tmp-devbox-ab12-20261012-[a-z0-9]{6}$`).MatchString(res.UserName) {
		t.Errorf("UserName = %q", res.UserName)
	}
	u := f.users[res.UserName]
	if u == nil {
		t.Fatalf("user %s not created", res.UserName)
	}
	if u.path != Path {
		t.Errorf("path = %q, want %q", u.path, Path)
	}
	wantTags := map[string]string{"ufa-host": "devbox-ab12", "date": "2026-10-12", "app": "the-conversationalist"}
	for k, v := range wantTags {
		if u.tags[k] != v {
			t.Errorf("tag %s = %q, want %q", k, u.tags[k], v)
		}
	}
	if !u.policies[policyARN] {
		t.Errorf("%s not attached", policyARN)
	}
	if _, ok := u.keys[res.Credentials.AccessKeyID]; !ok || res.Credentials.SecretAccessKey == "" {
		t.Errorf("returned credentials %+v don't match a key on the user", res.Credentials)
	}
	if !res.Confirmed {
		t.Error("new key wasn't confirmed")
	}
}

func TestProvisionRollsBackOnFailure(t *testing.T) {
	f := newFakeIAM()
	f.failOn = "CreateAccessKey"
	c := testClient(t, f, time.Now())

	if _, err := provision(context.Background(), c, Options{HostID: "h", App: "a", IdleDays: 2}); err == nil {
		t.Fatal("provision succeeded despite CreateAccessKey failing")
	}
	if len(f.users) != 0 {
		t.Errorf("half-built principal left behind: %v", f.users)
	}
}

func TestCleanupRemovesOnlyStalePreviousDayPrincipals(t *testing.T) {
	f := newFakeIAM()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	ours := func(date string) map[string]string {
		return map[string]string{"ufa-host": "h", "date": date, "app": "a"}
	}
	f.addUser("stale-used", ours("2026-10-08"), now.Add(-4*day), now.Add(-3*day))
	f.addUser("stale-never-used", ours("2026-10-07"), now.Add(-5*day), time.Time{})
	f.addUser("yesterday-in-use", ours("2026-10-11"), now.Add(-1*day), now.Add(-time.Hour))
	f.addUser("old-but-used", ours("2026-10-01"), now.Add(-11*day), now.Add(-day))
	f.addUser("today", ours("2026-10-12"), now.Add(-time.Hour), time.Time{})
	f.addUser("not-ours", map[string]string{"date": "2026-10-01"}, now.Add(-11*day), time.Time{})
	c := testClient(t, f, now)

	removed, failed := cleanup(context.Background(), c, now, 2)
	if removed != 2 || failed != 0 {
		t.Errorf("cleanup = (%d removed, %d failed), want (2, 0)", removed, failed)
	}
	for _, gone := range []string{"stale-used", "stale-never-used"} {
		if f.users[gone] != nil {
			t.Errorf("%s should have been removed", gone)
		}
	}
	for _, kept := range []string{"yesterday-in-use", "old-but-used", "today", "not-ours"} {
		if f.users[kept] == nil {
			t.Errorf("%s should have been kept", kept)
		}
	}
}

func TestUserNameFitsIAMLimits(t *testing.T) {
	name := userName(strings.Repeat("very-long_host.name!", 10), time.Now())
	if len(name) > 64 {
		t.Errorf("len(%q) = %d > 64", name, len(name))
	}
	if !regexp.MustCompile(`^[\w+=,.@-]+$`).MatchString(name) {
		t.Errorf("%q has characters IAM doesn't allow", name)
	}
}

func clearAWSEnv(t *testing.T) {
	t.Helper()
	for _, k := range append(credentialEnvVars, "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE", "AWS_REGION", "AWS_DEFAULT_REGION") {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", t.TempDir())
}

func TestResolveCredentials(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		clearAWSEnv(t)
		if c, ok := ResolveCredentials(); ok {
			t.Errorf("found credentials %+v with none configured", c)
		}
	})
	t.Run("env", func(t *testing.T) {
		clearAWSEnv(t)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAENV")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "s")
		t.Setenv("AWS_SESSION_TOKEN", "tok")
		c, ok := ResolveCredentials()
		if !ok || c != (Credentials{"AKIAENV", "s", "tok"}) {
			t.Errorf("got %+v, %v", c, ok)
		}
	})
	t.Run("credentials file profile", func(t *testing.T) {
		clearAWSEnv(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "credentials")
		os.WriteFile(path, []byte("[default]\naws_access_key_id = AKIADEF\naws_secret_access_key = d\n\n[work]\naws_access_key_id=AKIAWORK\naws_secret_access_key=w\n"), 0600)
		t.Setenv("AWS_SHARED_CREDENTIALS_FILE", path)
		t.Setenv("AWS_PROFILE", "work")
		c, ok := ResolveCredentials()
		if !ok || c.AccessKeyID != "AKIAWORK" || c.SecretAccessKey != "w" {
			t.Errorf("got %+v, %v", c, ok)
		}
	})
	t.Run("config file profile", func(t *testing.T) {
		clearAWSEnv(t)
		home := os.Getenv("HOME")
		os.MkdirAll(filepath.Join(home, ".aws"), 0700)
		os.WriteFile(filepath.Join(home, ".aws", "config"), []byte("[profile  dev]\nregion = us-west-2\naws_access_key_id = AKIADEV\naws_secret_access_key = v\n"), 0600)
		t.Setenv("AWS_PROFILE", "dev")
		c, ok := ResolveCredentials()
		if !ok || c.AccessKeyID != "AKIADEV" {
			t.Errorf("got %+v, %v", c, ok)
		}
	})
}

func TestApplyToEnvReplacesStartingCredentials(t *testing.T) {
	clearAWSEnv(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIASTART")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "start")
	t.Setenv("AWS_SESSION_TOKEN", "start-token")
	t.Setenv("AWS_PROFILE", "start-profile")
	t.Setenv("AWS_REGION", "eu-west-1")

	ApplyToEnv(Credentials{AccessKeyID: "AKIANEW", SecretAccessKey: "new"})

	for k, want := range map[string]string{
		"AWS_ACCESS_KEY_ID":     "AKIANEW",
		"AWS_SECRET_ACCESS_KEY": "new",
		"AWS_REGION":            "eu-west-1",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"AWS_SESSION_TOKEN", "AWS_PROFILE"} {
		if _, set := os.LookupEnv(k); set {
			t.Errorf("%s still set", k)
		}
	}
}

// TestApplyToEnvCarriesProfileRegion: ApplyToEnv clears AWS_PROFILE, so a
// region that only lived in that profile has to come across as AWS_REGION,
// or the sub-application's SDK ends up with no region at all.
func TestApplyToEnvCarriesProfileRegion(t *testing.T) {
	clearAWSEnv(t)
	home := os.Getenv("HOME")
	os.MkdirAll(filepath.Join(home, ".aws"), 0700)
	os.WriteFile(filepath.Join(home, ".aws", "config"), []byte("[default]\nregion = us-east-1\n\n[profile dev]\nregion = us-west-2\n"), 0600)
	t.Setenv("AWS_PROFILE", "dev")

	ApplyToEnv(Credentials{AccessKeyID: "AKIANEW", SecretAccessKey: "new"})

	if got := os.Getenv("AWS_REGION"); got != "us-west-2" {
		t.Errorf("AWS_REGION = %q, want the dev profile's us-west-2", got)
	}
	if _, set := os.LookupEnv("AWS_PROFILE"); set {
		t.Error("AWS_PROFILE still set")
	}
}

// TestApplyToEnvKeepsEnvironmentRegion: a region already in the environment
// wins over the profile's, and isn't duplicated into AWS_REGION.
func TestApplyToEnvKeepsEnvironmentRegion(t *testing.T) {
	clearAWSEnv(t)
	home := os.Getenv("HOME")
	os.MkdirAll(filepath.Join(home, ".aws"), 0700)
	os.WriteFile(filepath.Join(home, ".aws", "config"), []byte("[profile dev]\nregion = us-west-2\n"), 0600)
	t.Setenv("AWS_PROFILE", "dev")
	t.Setenv("AWS_DEFAULT_REGION", "eu-central-1")

	ApplyToEnv(Credentials{AccessKeyID: "AKIANEW", SecretAccessKey: "new"})

	if got := os.Getenv("AWS_REGION"); got != "" {
		t.Errorf("AWS_REGION = %q, want it left empty when AWS_DEFAULT_REGION is set", got)
	}
	if got := os.Getenv("AWS_DEFAULT_REGION"); got != "eu-central-1" {
		t.Errorf("AWS_DEFAULT_REGION = %q, want eu-central-1", got)
	}
}
