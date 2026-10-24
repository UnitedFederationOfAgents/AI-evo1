package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// This file is the github provider (see declare.go): a repository and its
// open pull requests, through GitHub's REST API.
//
//	github.orgs.<owner>.repos.<repo>:   (or github.users.<owner>.repos.<repo>)
//	  read: *                           (the repo's open PRs too)
//	  assert:
//	    prs:                            (one, or a list)
//	      select:
//	        from: <head branch>         (owner:branch for a fork's)
//	        to: <base branch>           (default: the repo's default branch)
//	        title: <a new PR's title>
//	      state:
//	        exists: true                (false closes it)
//	        title: ...
//	        body: ...
//
// A PR is matched by from and to among the open ones; select's title is
// only what a new one is called. The token is $GITHUB_TOKEN, $GH_TOKEN or
// "gh auth token"; $DTT_GITHUB_API points elsewhere than api.github.com.
type githubProvider struct{}

type ghRepo struct {
	owner, name, defaultBranch string
	exists                     bool
}

type prSpec struct {
	from, to, title *yNode
	fields          []fieldSpec
	line            int
}

func ghRepoPath(path []string) (owner, repo string, err error) {
	if len(path) == 5 && (path[1] == "orgs" || path[1] == "users") && path[3] == "repos" {
		return path[2], path[4], nil
	}
	return "", "", fmt.Errorf("github paths are github.orgs.<owner>.repos.<repo> (or github.users.<owner>.repos.<repo>)")
}

func (githubProvider) checkSelect(st *statement) error {
	if _, _, err := ghRepoPath(st.path); err != nil {
		return err
	}
	if st.sel != nil {
		return fmt.Errorf("a repository is named by its path, so it takes no select")
	}
	return nil
}

func (githubProvider) checkAssert(_ []string, body *yNode) error {
	_, err := parsePRSpecs(body)
	return err
}

func parsePRSpecs(body *yNode) ([]prSpec, error) {
	if body.kind != yMap {
		return nil, fmt.Errorf("line %d: a repository's assert is a mapping with prs under it", body.line)
	}
	if err := onlyKeys(body, "a repository's assert", "prs"); err != nil {
		return nil, err
	}
	prs := body.get("prs")
	if prs == nil {
		return nil, fmt.Errorf("line %d: a repository's assert needs prs", body.line)
	}
	items := []*yNode{prs}
	if prs.kind == ySeq {
		items = prs.items
	}
	var out []prSpec
	for _, n := range items {
		if n.kind != yMap {
			return nil, fmt.Errorf("line %d: each of prs is a mapping of select and state", n.line)
		}
		if err := onlyKeys(n, "a pull request", "select", "state"); err != nil {
			return nil, err
		}
		sel := n.get("select")
		if sel == nil || sel.kind != yMap {
			return nil, fmt.Errorf("line %d: a pull request needs a select mapping (from, to, title)", n.line)
		}
		if err := onlyKeys(sel, "a pull request's select", "from", "to", "title"); err != nil {
			return nil, err
		}
		spec := prSpec{from: sel.get("from"), to: sel.get("to"), title: sel.get("title"), line: n.line}
		if spec.from == nil || isEmpty(spec.from) {
			return nil, fmt.Errorf("line %d: a pull request's select needs from (its head branch)", sel.line)
		}
		var err error
		if spec.fields, err = assertFields(n.get("state"), "a pull request's state", "exists", "title", "body"); err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}

// ghPull is the part of a pull request dtt reads.
type ghPull struct {
	Number  int     `json:"number"`
	Title   string  `json:"title"`
	Body    *string `json:"body"`
	HTMLURL string  `json:"html_url"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (githubProvider) resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	owner, name, _ := ghRepoPath(st.path)
	g := e.github()
	var repo struct {
		FullName      string  `json:"full_name"`
		DefaultBranch string  `json:"default_branch"`
		Private       bool    `json:"private"`
		Description   *string `json:"description"`
		HTMLURL       string  `json:"html_url"`
	}
	status, err := g.call(ctx, "GET", ghRepoURL(owner, name), nil, &repo)
	if err != nil {
		return nil, err
	}
	r := &ghRepo{owner: owner, name: name, defaultBranch: repo.DefaultBranch, exists: status != http.StatusNotFound}
	state := map[string]any{"exists": r.exists}
	if r.exists {
		state["full_name"] = repo.FullName
		state["default_branch"] = repo.DefaultBranch
		state["private"] = repo.Private
		state["url"] = repo.HTMLURL
		if repo.Description != nil {
			state["description"] = *repo.Description
		}
		if st.reads("prs") {
			var pulls []ghPull
			if _, err := g.call(ctx, "GET", ghRepoURL(owner, name)+"/pulls?state=open&per_page=100", nil, &pulls); err != nil {
				return nil, err
			}
			list := []any{}
			for _, p := range pulls {
				list = append(list, map[string]any{"number": p.Number, "from": p.Head.Ref, "to": p.Base.Ref, "title": p.Title, "url": p.HTMLURL})
			}
			state["prs"] = list
		}
	}
	return []*dobj{e.addObject(st.pathString(), st.pathString(), st.line, state, r)}, nil
}

func (githubProvider) assert(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	specs, err := parsePRSpecs(body)
	if err != nil {
		return err
	}
	r := o.handle.(*ghRepo)
	if !r.exists {
		return fmt.Errorf("github.com/%s/%s doesn't exist (or this token can't see it); dtt doesn't create repositories", r.owner, r.name)
	}
	for _, s := range specs {
		if err := r.assertPR(ctx, e, o, s); err != nil {
			return err
		}
	}
	return nil
}

func prState(p *ghPull, from, to string) map[string]any {
	if p == nil {
		return map[string]any{"exists": false, "from": from, "to": to}
	}
	body := ""
	if p.Body != nil {
		body = *p.Body
	}
	return map[string]any{"exists": true, "number": p.Number, "url": p.HTMLURL, "from": from, "to": to, "title": p.Title, "body": body}
}

func (r *ghRepo) assertPR(ctx context.Context, e *engine, repoObj *dobj, s prSpec) error {
	g := e.github()
	from, err := e.text(s.from, "select.from")
	if err != nil {
		return err
	}
	to := r.defaultBranch
	if s.to != nil && !isEmpty(s.to) {
		if to, err = e.text(s.to, "select.to"); err != nil {
			return err
		}
	}
	newTitle := from
	if s.title != nil && !isEmpty(s.title) {
		if newTitle, err = e.text(s.title, "select.title"); err != nil {
			return err
		}
	}
	head := from
	if !strings.Contains(head, ":") {
		head = r.owner + ":" + from
	}
	var pulls []ghPull
	q := url.Values{"state": {"open"}, "head": {head}, "base": {to}}
	if _, err := g.call(ctx, "GET", ghRepoURL(r.owner, r.name)+"/pulls?"+q.Encode(), nil, &pulls); err != nil {
		return err
	}
	var pr *ghPull
	if len(pulls) > 0 {
		pr = &pulls[0]
	}
	o := e.addObject(repoObj.img.ID+".prs["+from+"->"+to+"]", repoObj.img.Path+".prs", s.line, prState(pr, from, to), nil)

	wantExists, explicit, line := true, false, s.line
	var existsFrom []any
	type wanted struct {
		f    fieldSpec
		want string
		from []any
	}
	var fields []wanted
	for _, f := range s.fields {
		want, err := e.text(f.want, f.name)
		if err != nil {
			return err
		}
		from, err := e.values(f.from)
		if err != nil {
			return err
		}
		if f.name == "exists" {
			if wantExists, err = parseBool(want); err != nil {
				return fmt.Errorf("line %d: exists: %v", f.line, err)
			}
			existsFrom, explicit, line = from, true, f.line
			continue
		}
		fields = append(fields, wanted{f, want, from})
	}

	exists := pr != nil
	changeExists := false
	if explicit || !exists {
		changeExists = e.decide(o, []string{"exists"}, line, wantExists, exists, existsFrom)
	}
	path := ghRepoURL(r.owner, r.name) + "/pulls"
	if !wantExists {
		if changeExists {
			if _, err := g.call(ctx, "PATCH", fmt.Sprintf("%s/%d", path, pr.Number), map[string]any{"state": "closed"}, nil); err != nil {
				return err
			}
			o.img.State = prState(nil, from, to)
		}
		return nil
	}
	patch := map[string]any{}
	for _, w := range fields {
		if e.decide(o, []string{w.f.name}, w.f.line, w.want, o.img.State[w.f.name], w.from) {
			patch[w.f.name] = w.want
		}
	}
	var got ghPull
	switch {
	case !exists:
		if !changeExists {
			return nil
		}
		create := map[string]any{"title": newTitle, "head": head, "base": to, "body": ""}
		for k, v := range patch {
			create[k] = v
		}
		if _, err := g.call(ctx, "POST", path, create, &got); err != nil {
			return err
		}
	case len(patch) > 0:
		if _, err := g.call(ctx, "PATCH", fmt.Sprintf("%s/%d", path, pr.Number), patch, &got); err != nil {
			return err
		}
	default:
		return nil
	}
	o.img.State = prState(&got, from, to)
	return nil
}

func ghRepoURL(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// ---- The REST client ----

type githubClient struct {
	api, token string
	http       *http.Client
}

func (e *engine) github() *githubClient {
	if e.gh == nil {
		e.gh = newGithubClient()
	}
	return e.gh
}

func newGithubClient() *githubClient {
	g := &githubClient{api: strings.TrimRight(os.Getenv("DTT_GITHUB_API"), "/"), http: &http.Client{Timeout: 30 * time.Second}}
	if g.api == "" {
		g.api = "https://api.github.com"
	}
	g.token = os.Getenv("GITHUB_TOKEN")
	if g.token == "" {
		g.token = os.Getenv("GH_TOKEN")
	}
	if g.token == "" {
		if gh, err := exec.LookPath("gh"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if out, err := exec.CommandContext(ctx, gh, "auth", "token").Output(); err == nil {
				g.token = strings.TrimSpace(string(out))
			}
		}
	}
	return g
}

// call sends one request, decoding the reply into out. A GET that finds
// nothing returns 404 and no error.
func (g *githubClient) call(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.api+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusNotFound && method == "GET" {
		return resp.StatusCode, nil
	}
	if resp.StatusCode >= 300 {
		var msg struct {
			Message string `json:"message"`
		}
		json.Unmarshal(data, &msg)
		err := fmt.Errorf("github %s %s: %s: %s", method, path, resp.Status, msg.Message)
		if g.token == "" {
			err = fmt.Errorf("%v (no token: set GITHUB_TOKEN or log in with gh auth login)", err)
		}
		return resp.StatusCode, err
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("github %s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode, nil
}
