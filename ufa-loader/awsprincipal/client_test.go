package awsprincipal

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Known-answer tests from AWS's SigV4 documentation / test suite, all with
// the documentation's example key at 2015-08-30T12:36:00Z.
var (
	exampleCreds = Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	exampleTime  = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
)

func TestSignGetVanilla(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	sign(req, nil, exampleCreds, "us-east-1", "service", exampleTime)

	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n  %s\nwant\n  %s", got, want)
	}
	if got := req.Header.Get("X-Amz-Date"); got != "20150830T123600Z" {
		t.Errorf("X-Amz-Date = %q", got)
	}
}

func TestSignIAMListUsersExample(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sign(req, nil, exampleCreds, "us-east-1", "iam", exampleTime)

	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-date, Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n  %s\nwant\n  %s", got, want)
	}
}

func TestSignSessionTokenIsSigned(t *testing.T) {
	creds := exampleCreds
	creds.SessionToken = "tok"
	req, _ := http.NewRequest(http.MethodPost, "https://sts.amazonaws.com/", nil)
	sign(req, []byte("Action=GetCallerIdentity"), creds, "us-east-1", "sts", exampleTime)

	if got := req.Header.Get("X-Amz-Security-Token"); got != "tok" {
		t.Errorf("X-Amz-Security-Token = %q", got)
	}
	if got := req.Header.Get("Authorization"); !strings.Contains(got,"SignedHeaders=host;x-amz-date;x-amz-security-token,") {
		t.Errorf("session token not among signed headers: %s", got)
	}
}

func TestCanonicalQueryEscaping(t *testing.T) {
	q := map[string][]string{"b": {"a b"}, "a": {"x~y*"}}
	if got, want := canonicalQuery(q), "a=x~y%2A&b=a%20b"; got != want {
		t.Errorf("canonicalQuery = %q, want %q", got, want)
	}
}
