package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LR's own AWS credential inspection for the system tab's connectivity
// check (connectivity.go): find whatever static keys this process can see
// and ask STS who they belong to. Kept here rather than in
// ufa-loader/awsprincipal so ufa-loader stays limited to issuing the
// temporary principal -- LR reads the result back out of its own
// environment like any other consumer would. Standard library only (a
// small SigV4 signer) rather than aws-sdk-go-v2, for one STS call.

const (
	// STS's global endpoint signs as us-east-1; only the standard "aws"
	// partition is supported.
	defaultSTSEndpoint = "https://sts.amazonaws.com/"
	stsSigningRegion   = "us-east-1"
	stsAPIVersion      = "2011-06-15"
)

// awsCredentials is one set of AWS access keys; SessionToken is empty for
// long-lived IAM user keys (including the ones ufa-loader issues).
type awsCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

func (c awsCredentials) ok() bool { return c.AccessKeyID != "" && c.SecretAccessKey != "" }

// resolveAWSCredentials finds the static keys an AWS SDK in this process
// would use, and names where they came from: AWS_ACCESS_KEY_ID/
// AWS_SECRET_ACCESS_KEY(/AWS_SESSION_TOKEN) in the environment (where a
// wrapping ufa-loader leaves its temporary principal); else the selected
// profile (AWS_PROFILE, else AWS_DEFAULT_PROFILE, else "default") in the
// shared credentials file, then the shared config file. Profiles that only
// point elsewhere (SSO, role_arn, credential_process) count as none.
func resolveAWSCredentials() (awsCredentials, string, bool) {
	env := awsCredentials{
		AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
	}
	if env.ok() {
		return env, "environment", true
	}

	profile := "default"
	for _, k := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE"} {
		if p := os.Getenv(k); p != "" {
			profile = p
			break
		}
	}
	source := "profile " + profile
	home, _ := os.UserHomeDir()
	if c := iniCredentials(awsSharedFile("AWS_SHARED_CREDENTIALS_FILE", home, "credentials"), profile); c.ok() {
		return c, source, true
	}
	cfg := awsSharedFile("AWS_CONFIG_FILE", home, "config")
	if c := iniCredentials(cfg, "profile "+profile); c.ok() {
		return c, source, true
	}
	if profile == "default" {
		if c := iniCredentials(cfg, "default"); c.ok() {
			return c, source, true
		}
	}
	return awsCredentials{}, "", false
}

// awsSharedFile returns the path named by envVar, else ~/.aws/<name> (or ""
// with no home directory).
func awsSharedFile(envVar, home, name string) string {
	if p := os.Getenv(envVar); p != "" {
		return p
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".aws", name)
}

// iniCredentials reads the aws_access_key_id / aws_secret_access_key /
// aws_session_token keys from section [section] of the INI file at path.
func iniCredentials(path, section string) awsCredentials {
	var c awsCredentials
	if path == "" {
		return c
	}
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()

	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && strings.HasSuffix(line, "]") {
			in = strings.Join(strings.Fields(line[1:len(line)-1]), " ") == section
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !in || !found {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "aws_access_key_id":
			c.AccessKeyID = v
		case "aws_secret_access_key":
			c.SecretAccessKey = v
		case "aws_session_token":
			c.SessionToken = v
		}
	}
	return c
}

// stsCallerIdentity calls STS GetCallerIdentity at endpoint with creds and
// returns the caller's ARN -- both "do these keys work" and the principal
// name the providers section shows. Further STS/IAM lookups can chain off
// the same signed-call path should the system tab ever need more.
func stsCallerIdentity(ctx context.Context, hc *http.Client, endpoint string, creds awsCredentials, now time.Time) (string, error) {
	body := []byte(url.Values{"Action": {"GetCallerIdentity"}, "Version": {stsAPIVersion}}.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("GetCallerIdentity: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sigV4Sign(req, body, creds, stsSigningRegion, "sts", now)

	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("GetCallerIdentity: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("GetCallerIdentity: reading response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Code    string `xml:"Error>Code"`
			Message string `xml:"Error>Message"`
		}
		if xml.Unmarshal(data, &e) == nil && e.Code != "" {
			return "", fmt.Errorf("GetCallerIdentity: %s: %s", e.Code, e.Message)
		}
		return "", fmt.Errorf("GetCallerIdentity: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Arn string `xml:"GetCallerIdentityResult>Arn"`
	}
	if err := xml.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("GetCallerIdentity: decoding response: %w", err)
	}
	if out.Arn == "" {
		return "", errors.New("GetCallerIdentity: response carried no Arn")
	}
	return out.Arn, nil
}

// sigV4Sign adds SigV4 headers (X-Amz-Date, X-Amz-Security-Token when
// there's a session token, Authorization) to req, signing Host plus every
// header already set on it.
func sigV4Sign(req *http.Request, body []byte, creds awsCredentials, region, service string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	day := amzDate[:8]
	req.Header.Set("X-Amz-Date", amzDate)
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers := map[string]string{"host": host}
	for k, vs := range req.Header {
		trimmed := make([]string, len(vs))
		for i, v := range vs {
			trimmed[i] = strings.Join(strings.Fields(v), " ")
		}
		headers[strings.ToLower(k)] = strings.Join(trimmed, ",")
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signedHeaders := strings.Join(names, ";")

	var query []string
	for k, vs := range req.URL.Query() {
		for _, v := range vs {
			query = append(query, sigV4Escape(k)+"="+sigV4Escape(v))
		}
	}
	sort.Strings(query)

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	payloadHash := sha256.Sum256(body)
	canonReq := strings.Join([]string{
		req.Method,
		path,
		strings.Join(query, "&"),
		canonHeaders.String(),
		signedHeaders,
		hex.EncodeToString(payloadHash[:]),
	}, "\n")

	scope := day + "/" + region + "/" + service + "/aws4_request"
	canonHash := sha256.Sum256([]byte(canonReq))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(canonHash[:])

	key := hmacSHA256([]byte("AWS4"+creds.SecretAccessKey), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))

	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		creds.AccessKeyID, scope, signedHeaders, sig))
}

// sigV4Escape is RFC 3986 escaping (%20, not +, for spaces), as SigV4's
// canonical query string requires.
func sigV4Escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
