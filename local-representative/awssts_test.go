package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Known-answer test from AWS's SigV4 test suite (get-vanilla), with the
// documentation's example key at 2015-08-30T12:36:00Z.
func TestSigV4SignGetVanilla(t *testing.T) {
	creds := awsCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	sigV4Sign(req, nil, creds, "us-east-1", "service", time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC))

	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n  %s\nwant\n  %s", got, want)
	}
}

func TestSTSCallerIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "Action=GetCallerIdentity") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=AKIAGOOD/") {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`<ErrorResponse><Error><Code>InvalidClientTokenId</Code><Message>bad key</Message></Error></ErrorResponse>`))
			return
		}
		w.Write([]byte(`<GetCallerIdentityResponse><GetCallerIdentityResult>` +
			`<Arn>arn:aws:iam::123456789012:user/tester</Arn></GetCallerIdentityResult></GetCallerIdentityResponse>`))
	}))
	defer srv.Close()

	arn, err := stsCallerIdentity(context.Background(), srv.Client(), srv.URL+"/", awsCredentials{AccessKeyID: "AKIAGOOD", SecretAccessKey: "x"}, time.Now())
	if err != nil {
		t.Fatalf("stsCallerIdentity: %v", err)
	}
	if arn != "arn:aws:iam::123456789012:user/tester" {
		t.Errorf("arn = %q", arn)
	}

	_, err = stsCallerIdentity(context.Background(), srv.Client(), srv.URL+"/", awsCredentials{AccessKeyID: "AKIABAD", SecretAccessKey: "x"}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "InvalidClientTokenId") {
		t.Errorf("rejected key: err = %v, want an InvalidClientTokenId error", err)
	}
}

func TestResolveAWSCredentialsFromProfile(t *testing.T) {
	clearProviderEnv(t)
	path := filepath.Join(t.TempDir(), "credentials")
	os.WriteFile(path, []byte("[default]\naws_access_key_id = AKIADEF\naws_secret_access_key = s1\n\n[work]\naws_access_key_id = AKIAWORK\naws_secret_access_key = s2\n"), 0o600)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", path)
	t.Setenv("AWS_PROFILE", "work")

	c, source, ok := resolveAWSCredentials()
	if !ok || c.AccessKeyID != "AKIAWORK" || c.SecretAccessKey != "s2" || source != "profile work" {
		t.Errorf("resolveAWSCredentials = %+v, %q, %v; want the work profile", c, source, ok)
	}
}
