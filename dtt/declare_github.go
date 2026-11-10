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
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file is the github provider (see declare.go): a repository and its
// open pull requests, through GitHub's REST API. A repository's issues are in
// declare_github_issues.go; an owner's set of repositories, which an assert
// can create, and github.repos, any repository read by its full name, are in
// declare_github_repos.go.
//
//	github.orgs.<owner>.repos.<repo>:   (or github.users.<owner>.repos.<repo>)
//	  assert:
//	    prs:                            (one, or a list)
//	      select:
//	        from: <head branch>         (owner:branch for a fork's; or a
//	                                     pattern, condoc/*/main: every open
//	                                     PR whose head matches)
//	        to: <base branch>           (default: the repo's default branch)
//	        title: <a new PR's title>
//	      state:
//	        exists: true                (false closes it; so does delete mode, for a PR named by its branch)
//	        title: ...
//	        body: ...
//	        labels: [a, b]              (the whole list; or has: [...] and
//	        reviewers: {has: [x]}        lacks: [...], others left alone)
//	    issues:                         (one, or a list; see declare_github_issues.go)
//	      select:
//	        title: <the issue's title>
//	      state:
//	        exists: true                (false closes it; so does delete mode)
//	        body: ...
//	        labels: [a, b]
//	        assignees: [x]
//
// The repository's state is measured whole, its open PRs and issues included. A PR is
// matched by from and to among the open ones; select's title is only what a
// new one is called. A pattern (*, ? or [...] in from, path.Match's
// syntax) only matches PRs that are open: it never creates one, and
// matching none is fine. Reviewers are the requested ones: someone who has
// reviewed since drops off GitHub's list, so has: asks them again. The token
// is $GITHUB_TOKEN, $GH_TOKEN or "gh auth token"; $DTT_GITHUB_API points
// elsewhere than api.github.com.
type githubProvider struct{}

// prSetFields are a PR's list fields.
var prSetFields = []string{"labels", "reviewers"}

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
	if isGhReposPath(st.path) {
		return checkGhReposSelect(st)
	}
	if _, _, ok := ghOwnerPath(st.path); ok {
		if st.sel != nil {
			return fmt.Errorf("an owner's repositories take no select here; each one under assert's repos names its own (select: {name: ...})")
		}
		return nil
	}
	if _, _, err := ghRepoPath(st.path); err != nil {
		return fmt.Errorf("github paths are github.users.<owner>.repos.<repo>, or github.users.<owner>.repos to create repositories (orgs for users when the owner is an organization), or github.repos to read one by select: {repo: <owner>/<name>}")
	}
	if st.sel != nil {
		return fmt.Errorf("a repository is named by its path, so it takes no select")
	}
	return nil
}

func (githubProvider) checkAssert(path []string, body *yNode) error {
	if isGhReposPath(path) {
		return fmt.Errorf("github.repos is only read: assert a repository through github.users.<owner>.repos")
	}
	if _, _, ok := ghOwnerPath(path); ok {
		_, err := parseRepoSpecs(body)
		return err
	}
	if _, err := parsePRSpecs(body); err != nil {
		return err
	}
	_, err := parseIssueSpecs(body)
	return err
}

func parsePRSpecs(body *yNode) ([]prSpec, error) {
	if body.kind != yMap {
		return nil, fmt.Errorf("line %d: a repository's assert is a mapping with prs or issues under it", body.line)
	}
	if err := onlyKeys(body, "a repository's assert", "prs", "issues"); err != nil {
		return nil, err
	}
	prs := body.get("prs")
	if prs == nil {
		if body.get("issues") == nil {
			return nil, fmt.Errorf("line %d: a repository's assert needs prs or issues", body.line)
		}
		return nil, nil
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
		if spec.fields, err = assertFields(n.get("state"), "a pull request's state", "exists", "title", "body", "labels", "reviewers"); err != nil {
			return nil, err
		}
		for _, f := range spec.fields {
			if contains(prSetFields, f.name) {
				if err := checkSetField(f); err != nil {
					return nil, err
				}
			} else if f.want.kind != yScalar {
				return nil, fmt.Errorf("line %d: a pull request's %s should be a single value", f.line, f.name)
			}
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
		Ref   string `json:"ref"`
		Label string `json:"label"` // owner:branch
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	RequestedReviewers []struct {
		Login string `json:"login"`
	} `json:"requested_reviewers"`
}

func (p *ghPull) labels() []any {
	out := []any{}
	for _, l := range p.Labels {
		out = append(out, l.Name)
	}
	sortAny(out)
	return out
}

func (p *ghPull) reviewers() []any {
	out := []any{}
	for _, r := range p.RequestedReviewers {
		out = append(out, r.Login)
	}
	sortAny(out)
	return out
}

func sortAny(list []any) {
	sort.Slice(list, func(i, j int) bool { return fmt.Sprint(list[i]) < fmt.Sprint(list[j]) })
}

// openPulls lists a repository's open PRs, every page of them; q narrows
// them (head, base).
func (g *githubClient) openPulls(ctx context.Context, owner, repo string, q url.Values) ([]ghPull, error) {
	const perPage, maxPages = 100, 20
	var all []ghPull
	for page := 1; page <= maxPages; page++ {
		pq := url.Values{"state": {"open"}, "per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
		for k, v := range q {
			pq[k] = v
		}
		var pulls []ghPull
		if _, err := g.call(ctx, "GET", ghRepoURL(owner, repo)+"/pulls?"+pq.Encode(), nil, &pulls); err != nil {
			return nil, err
		}
		all = append(all, pulls...)
		if len(pulls) < perPage {
			break
		}
	}
	return all, nil
}

func (githubProvider) resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	if isGhReposPath(st.path) {
		return resolveGhRepos(ctx, e, st)
	}
	if _, _, ok := ghOwnerPath(st.path); ok {
		return resolveOwner(ctx, e, st)
	}
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
		pulls, err := g.openPulls(ctx, owner, name, nil)
		if err != nil {
			return nil, err
		}
		list := []any{}
		for _, p := range pulls {
			list = append(list, map[string]any{"number": p.Number, "from": p.Head.Ref, "to": p.Base.Ref, "title": p.Title, "url": p.HTMLURL,
				"labels": p.labels(), "reviewers": p.reviewers()})
		}
		state["prs"] = list
		issues, err := g.openIssues(ctx, owner, name)
		if err != nil {
			return nil, err
		}
		ilist := []any{}
		for _, is := range issues {
			ilist = append(ilist, map[string]any{"number": is.Number, "title": is.Title, "url": is.HTMLURL,
				"labels": is.labels(), "assignees": is.assignees()})
		}
		state["issues"] = ilist
	}
	return []*dobj{e.addObject(st.pathString(), st.pathString(), st.line, state, r)}, nil
}

func (githubProvider) assert(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	if _, ok := o.handle.(*ghOwner); ok {
		return assertOwner(ctx, e, o, body)
	}
	specs, err := parsePRSpecs(body)
	if err != nil {
		return err
	}
	issues, err := parseIssueSpecs(body)
	if err != nil {
		return err
	}
	r := o.handle.(*ghRepo)
	if !r.exists && e.deleting() {
		return nil // no repository, so no PRs or issues to close
	}
	if !r.exists {
		return fmt.Errorf("github.com/%s/%s doesn't exist (or this token can't see it); create it through github.users.%s.repos, or github.orgs.%s.repos for an organization (see examples/compositions/github-repo-dated.dtt.yaml)", r.owner, r.name, r.owner, r.owner)
	}
	for _, s := range specs {
		if err := r.assertPR(ctx, e, o, s); err != nil {
			return err
		}
	}
	for _, s := range issues {
		if err := r.assertIssue(ctx, e, o, s); err != nil {
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
	return map[string]any{"exists": true, "number": p.Number, "url": p.HTMLURL, "from": from, "to": to, "title": p.Title, "body": body,
		"labels": p.labels(), "reviewers": p.reviewers()}
}

// isPRPattern reports whether a PR select's from is a pattern.
func isPRPattern(from string) bool { return strings.ContainsAny(from, "*?[") }

// prFields are a PR assert's fields, evaluated: exists, the single-valued
// fields and the list fields.
type prFields struct {
	wantExists, explicit bool
	existsFrom           []any
	existsLine           int
	scalars              []prScalar
	sets                 []setSpec
}

type prScalar struct {
	name, want string
	from       []any
	line       int
}

func (e *engine) evalPRFields(s prSpec) (prFields, error) {
	return e.evalGithubFields(s.fields, s.line, prSetFields)
}

// evalGithubFields evaluates a PR's or an issue's fields; setFields are its
// list fields.
func (e *engine) evalGithubFields(fields []fieldSpec, line int, setFields []string) (prFields, error) {
	pf := prFields{wantExists: true, existsLine: line}
	for _, f := range fields {
		if contains(setFields, f.name) {
			set, err := e.set(f)
			if err != nil {
				return pf, err
			}
			pf.sets = append(pf.sets, set)
			continue
		}
		want, err := e.text(f.want, f.name)
		if err != nil {
			return pf, err
		}
		from, err := e.values(f.from)
		if err != nil {
			return pf, err
		}
		if f.name == "exists" {
			if pf.wantExists, err = parseBool(want); err != nil {
				return pf, fmt.Errorf("line %d: exists: %v", f.line, err)
			}
			pf.existsFrom, pf.explicit, pf.existsLine = from, true, f.line
			continue
		}
		pf.scalars = append(pf.scalars, prScalar{f.name, want, from, f.line})
	}
	return pf, nil
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
	pf, err := e.evalPRFields(s)
	if err != nil {
		return err
	}

	if isPRPattern(from) {
		// Every open PR into to whose head matches; none is fine.
		if _, err := path.Match(from, ""); err != nil {
			return fmt.Errorf("line %d: select.from %q isn't a pattern path.Match reads: %v", s.line, from, err)
		}
		pulls, err := g.openPulls(ctx, r.owner, r.name, url.Values{"base": {to}})
		if err != nil {
			return err
		}
		n := 0
		for i := range pulls {
			p := &pulls[i]
			name := p.Head.Ref
			if strings.Contains(from, ":") {
				name = p.Head.Label
			}
			if ok, _ := path.Match(from, name); !ok {
				continue
			}
			n++
			if err := r.assertOnePR(ctx, e, repoObj, s, pf, p, p.Head.Ref, to, "", ""); err != nil {
				return err
			}
		}
		if e.log != nil {
			fmt.Fprintf(e.log, "github: %d open PR(s) into %s match %s\n", n, to, from)
		}
		return nil
	}

	head := from
	if !strings.Contains(head, ":") {
		head = r.owner + ":" + from
	}
	pulls, err := g.openPulls(ctx, r.owner, r.name, url.Values{"head": {head}, "base": {to}})
	if err != nil {
		return err
	}
	var pr *ghPull
	if len(pulls) > 0 {
		pr = &pulls[0]
	}
	return r.assertOnePR(ctx, e, repoObj, s, pf, pr, from, to, head, newTitle)
}

// assertOnePR asserts pf on one PR: pr, or (pr nil) the one from head into
// to that would be created, called newTitle.
func (r *ghRepo) assertOnePR(ctx context.Context, e *engine, repoObj *dobj, s prSpec, pf prFields, pr *ghPull, from, to, head, newTitle string) error {
	g := e.github()
	o := e.addObject(repoObj.img.ID+".prs["+from+"->"+to+"]", repoObj.img.Path+".prs", s.line, prState(pr, from, to), nil)

	if e.deleting() {
		if head == "" {
			// Found by a pattern: an assert never makes these, so deleting
			// leaves them be.
			e.keep(o, s.line, "found by a pattern, which an assert never creates; delete leaves it open")
			return nil
		}
		// Delete mode: the PR is closed, whatever else is asserted on it.
		pf.wantExists, pf.explicit = false, true
	}
	exists := pr != nil
	changeExists := false
	if pf.explicit || !exists {
		changeExists = e.decide(o, []string{"exists"}, pf.existsLine, pf.wantExists, exists, pf.existsFrom)
	}
	pulls := ghRepoURL(r.owner, r.name) + "/pulls"
	if !pf.wantExists {
		if changeExists {
			if _, err := g.call(ctx, "PATCH", fmt.Sprintf("%s/%d", pulls, pr.Number), map[string]any{"state": "closed"}, nil); err != nil {
				return err
			}
			o.img.State = prState(nil, from, to)
		}
		return nil
	}
	patch := map[string]any{}
	for _, f := range pf.scalars {
		if e.decide(o, []string{f.name}, f.line, f.want, o.img.State[f.name], f.from) {
			patch[f.name] = f.want
		}
	}
	type setChange struct{ add, remove []string }
	changes := map[string]setChange{}
	for _, set := range pf.sets {
		add, remove := e.decideSet(o, set, stringList(o.img.State[set.name]))
		if len(add)+len(remove) > 0 {
			changes[set.name] = setChange{add, remove}
		}
	}

	number := 0
	if exists {
		number = pr.Number
	}
	switch {
	case !exists:
		if !changeExists {
			return nil
		}
		create := map[string]any{"title": newTitle, "head": head, "base": to, "body": ""}
		for k, v := range patch {
			create[k] = v
		}
		var got ghPull
		if _, err := g.call(ctx, "POST", pulls, create, &got); err != nil {
			return err
		}
		number = got.Number
	case len(patch) > 0:
		if _, err := g.call(ctx, "PATCH", fmt.Sprintf("%s/%d", pulls, number), patch, nil); err != nil {
			return err
		}
	case len(changes) == 0:
		return nil
	}

	// Labels are the PR's issue's; reviewers are requested on the PR.
	issue := fmt.Sprintf("%s/issues/%d", ghRepoURL(r.owner, r.name), number)
	if c, ok := changes["labels"]; ok {
		if len(c.add) > 0 {
			if _, err := g.call(ctx, "POST", issue+"/labels", map[string]any{"labels": c.add}, nil); err != nil {
				return err
			}
		}
		for _, l := range c.remove {
			if _, err := g.call(ctx, "DELETE", issue+"/labels/"+url.PathEscape(l), nil, nil); err != nil {
				return err
			}
		}
	}
	if c, ok := changes["reviewers"]; ok {
		reviewers := fmt.Sprintf("%s/%d/requested_reviewers", pulls, number)
		if len(c.add) > 0 {
			if _, err := g.call(ctx, "POST", reviewers, map[string]any{"reviewers": c.add}, nil); err != nil {
				return err
			}
		}
		if len(c.remove) > 0 {
			if _, err := g.call(ctx, "DELETE", reviewers, map[string]any{"reviewers": c.remove}, nil); err != nil {
				return err
			}
		}
	}

	// Measure it again, as the changes left it.
	var now ghPull
	if _, err := g.call(ctx, "GET", fmt.Sprintf("%s/%d", pulls, number), nil, &now); err != nil {
		return err
	}
	o.img.State = prState(&now, from, to)
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
