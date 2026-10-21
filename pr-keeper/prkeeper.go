// Package prkeeper raises, inspects and merges GitHub pull requests through
// the REST API, authenticating with a personal access token -- the same
// HTTPS + PAT path every other GitHub operation in this repo uses (see
// docs/SlopspaceControlScheme.md, S3).
//
// It is deliberately small and dependency-free so condoccer (the slopspace
// control scheme's propose-PR / PR / merge phases), dungeon-keeper and,
// later, claudomation can all import it. It knows nothing about condocs:
// callers decide which repos get PRs, in what order they merge, and what a
// partial failure means.
package prkeeper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultBaseURL is the public GitHub REST API.
const DefaultBaseURL = "https://api.github.com"

// Merge methods accepted by Merge.
const (
	MergeMethodMerge  = "merge"
	MergeMethodSquash = "squash"
	MergeMethodRebase = "rebase"
)

// TokenFromEnv returns the GitHub PAT from the environment: TF_VAR_github_pat
// (the name dungeon-keeper uses), falling back to the usual gh/Actions names.
func TokenFromEnv() string {
	for _, k := range []string{"TF_VAR_github_pat", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// Client talks to the GitHub REST API.
type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a Client for the public API using token.
func NewClient(token string) *Client {
	return &Client{
		Token:   token,
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError is a non-2xx response from the API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github api: %d %s", e.Status, e.Message)
}

// IsStatus reports whether err is an APIError with the given HTTP status.
func IsStatus(err error, status int) bool {
	ae, ok := err.(*APIError)
	return ok && ae.Status == status
}

// Ref is the head or base of a pull request.
type Ref struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// PullRequest is the subset of GitHub's pull request object callers need.
type PullRequest struct {
	Number         int    `json:"number"`
	URL            string `json:"html_url"`
	State          string `json:"state"` // "open" or "closed"
	Title          string `json:"title"`
	Body           string `json:"body"`
	Merged         bool   `json:"merged"`
	Mergeable      *bool  `json:"mergeable"`       // nil while GitHub is still computing it
	MergeableState string `json:"mergeable_state"` // clean, unstable, has_hooks, blocked, behind, dirty, draft, unknown
	Head           Ref    `json:"head"`
	Base           Ref    `json:"base"`
}

// Ready reports whether the PR is open and can be merged now: GitHub has
// computed it as mergeable and nothing (conflicts, required reviews or
// checks, a draft) blocks it. "unstable" (non-required checks failing) and
// "has_hooks" still allow merging.
func (pr *PullRequest) Ready() bool {
	if pr.State != "open" || pr.Merged || pr.Mergeable == nil || !*pr.Mergeable {
		return false
	}
	switch pr.MergeableState {
	case "clean", "unstable", "has_hooks":
		return true
	}
	return false
}

// Status summarizes the PR in one word for display: merged, closed,
// mergeable, pending (GitHub still computing), or the mergeable_state that
// blocks it (blocked, behind, dirty, draft, ...).
func (pr *PullRequest) Status() string {
	switch {
	case pr.Merged:
		return "merged"
	case pr.State == "closed":
		return "closed"
	case pr.Ready():
		return "mergeable"
	case pr.Mergeable == nil || pr.MergeableState == "" || pr.MergeableState == "unknown":
		return "pending"
	default:
		return pr.MergeableState
	}
}

// CreateRequest describes a new pull request. Head is a branch in the same
// repo.
type CreateRequest struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
}

// Create opens a pull request in repo (owner/name).
func (c *Client) Create(repo string, req CreateRequest) (*PullRequest, error) {
	var pr PullRequest
	if err := c.do(http.MethodPost, "/repos/"+repo+"/pulls", req, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// FindOpen returns the open pull request whose head is branch in repo, or
// nil if there is none. It lets a caller that was interrupted part-way
// through raising a set of PRs pick up again without opening duplicates.
func (c *Client) FindOpen(repo, branch string) (*PullRequest, error) {
	owner, _, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("repo %q: expected owner/name", repo)
	}
	q := url.Values{"state": {"open"}, "head": {owner + ":" + branch}}
	var prs []PullRequest
	if err := c.do(http.MethodGet, "/repos/"+repo+"/pulls?"+q.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

// Get fetches pull request number in repo, including its mergeability.
func (c *Client) Get(repo string, number int) (*PullRequest, error) {
	var pr PullRequest
	if err := c.do(http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), nil, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// UpdateBody replaces a pull request's body.
func (c *Client) UpdateBody(repo string, number int, body string) error {
	return c.do(http.MethodPatch, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), map[string]string{"body": body}, nil)
}

// Merge merges pull request number with method (MergeMethod*). A non-empty
// sha makes GitHub refuse the merge if the head moved since it was checked.
func (c *Client) Merge(repo string, number int, method, sha string) error {
	req := map[string]string{"merge_method": method}
	if sha != "" {
		req["sha"] = sha
	}
	return c.do(http.MethodPut, fmt.Sprintf("/repos/%s/pulls/%d/merge", repo, number), req, nil)
}

// DeleteBranch deletes branch from repo. A branch that is already gone is
// not an error.
func (c *Client) DeleteBranch(repo, branch string) error {
	segs := strings.Split(branch, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	err := c.do(http.MethodDelete, "/repos/"+repo+"/git/refs/heads/"+strings.Join(segs, "/"), nil, nil)
	if IsStatus(err, http.StatusNotFound) || IsStatus(err, http.StatusUnprocessableEntity) {
		return nil
	}
	return err
}

// DefaultBranch returns repo's default branch.
func (c *Client) DefaultBranch(repo string) (string, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.do(http.MethodGet, "/repos/"+repo, nil, &r); err != nil {
		return "", err
	}
	return r.DefaultBranch, nil
}

// do sends one API request with body JSON-encoded (nil for none) and decodes
// a 2xx response into out (nil to discard it).
func (c *Client) do(method, path string, body, out interface{}) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequest(method, strings.TrimSuffix(base, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Message != "" {
			msg = e.Message
			for _, d := range e.Errors {
				if d.Message != "" {
					msg += ": " + d.Message
				}
			}
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
