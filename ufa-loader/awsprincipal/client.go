package awsprincipal

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	// IAM and STS are both reached through their global endpoints, which
	// sign as us-east-1. Only the standard "aws" partition is supported.
	defaultIAMEndpoint = "https://iam.amazonaws.com/"
	defaultSTSEndpoint = "https://sts.amazonaws.com/"
	signingRegion      = "us-east-1"

	iamAPIVersion = "2010-05-08"
	stsAPIVersion = "2011-06-15"
)

// client is a minimal AWS query-protocol client: form-encoded POSTs signed
// with SigV4, XML responses.
type client struct {
	creds       Credentials
	iamEndpoint string
	stsEndpoint string
	http        *http.Client
	now         func() time.Time
}

func newClient(creds Credentials) *client {
	return &client{
		creds:       creds,
		iamEndpoint: defaultIAMEndpoint,
		stsEndpoint: defaultSTSEndpoint,
		http:        &http.Client{Timeout: 20 * time.Second},
		now:         time.Now,
	}
}

// withCreds returns a copy of c that signs with creds instead.
func (c *client) withCreds(creds Credentials) *client {
	cp := *c
	cp.creds = creds
	return &cp
}

// apiError is an AWS <ErrorResponse>.
type apiError struct {
	Action  string
	Status  int
	Code    string `xml:"Error>Code"`
	Message string `xml:"Error>Message"`
}

func (e *apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("%s: HTTP %d", e.Action, e.Status)
	}
	return fmt.Sprintf("%s: %s: %s", e.Action, e.Code, e.Message)
}

func (c *client) iam(ctx context.Context, action string, params url.Values, out any) error {
	return c.call(ctx, "iam", c.iamEndpoint, iamAPIVersion, action, params, out)
}

func (c *client) sts(ctx context.Context, action string, params url.Values, out any) error {
	return c.call(ctx, "sts", c.stsEndpoint, stsAPIVersion, action, params, out)
}

func (c *client) call(ctx context.Context, service, endpoint, version, action string, params url.Values, out any) error {
	form := url.Values{}
	for k, v := range params {
		form[k] = v
	}
	form.Set("Action", action)
	form.Set("Version", version)
	body := []byte(form.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sign(req, body, c.creds, signingRegion, service, c.now())

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%s: reading response: %w", action, err)
	}
	if resp.StatusCode/100 != 2 {
		e := &apiError{Action: action, Status: resp.StatusCode}
		_ = xml.Unmarshal(data, e)
		return e
	}
	if out == nil {
		return nil
	}
	if err := xml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: decoding response: %w", action, err)
	}
	return nil
}

// sign adds SigV4 headers (X-Amz-Date, X-Amz-Security-Token when there's a
// session token, Authorization) to req. Every header already set on req is
// signed, plus Host.
func sign(req *http.Request, body []byte, creds Credentials, region, service string, now time.Time) {
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

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	payloadHash := sha256.Sum256(body)
	canonReq := strings.Join([]string{
		req.Method,
		path,
		canonicalQuery(req.URL.Query()),
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

// canonicalQuery encodes q with keys (then values) sorted and RFC 3986
// escaping (%20, not +, for spaces), as SigV4 requires.
func canonicalQuery(q url.Values) string {
	var pairs []string
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, awsEscape(k)+"="+awsEscape(v))
		}
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

func awsEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
