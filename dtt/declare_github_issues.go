package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// This file is a repository's issues, for the github provider
// (declare_github.go):
//
//	github.users.<owner>.repos.<repo>:   (or github.orgs.<owner>.repos.<repo>)
//	  assert:
//	    issues:                         (one, or a list)
//	      select:
//	        title: <the issue's title>
//	      state:
//	        exists: true                (false closes it; so does delete mode)
//	        body: ...
//	        labels: [a, b]              (the whole list; or has: [...] and
//	        assignees: {has: [x]}        lacks: [...], others left alone)
//
// An issue is matched by its title among the repository's open issues (the
// oldest, if several share it); select's title is also what a new one is
// called. A closed issue is never matched, so asserting exists: true after
// closing one opens a new issue. GitHub's REST API can't delete an issue, so
// exists: false and delete mode close it.

// issueSetFields are an issue's list fields.
var issueSetFields = []string{"labels", "assignees"}

type issueSpec struct {
	title  *yNode
	fields []fieldSpec
	line   int
}

func parseIssueSpecs(body *yNode) ([]issueSpec, error) {
	if body.kind != yMap {
		return nil, nil // parsePRSpecs says what's wrong
	}
	issues := body.get("issues")
	if issues == nil {
		return nil, nil
	}
	items := []*yNode{issues}
	if issues.kind == ySeq {
		items = issues.items
	}
	var out []issueSpec
	for _, n := range items {
		if n.kind != yMap {
			return nil, fmt.Errorf("line %d: each of issues is a mapping of select and state", n.line)
		}
		if err := onlyKeys(n, "an issue", "select", "state"); err != nil {
			return nil, err
		}
		sel := n.get("select")
		if sel == nil || sel.kind != yMap {
			return nil, fmt.Errorf("line %d: an issue needs a select mapping (title)", n.line)
		}
		if err := onlyKeys(sel, "an issue's select", "title"); err != nil {
			return nil, err
		}
		spec := issueSpec{title: sel.get("title"), line: n.line}
		if spec.title == nil || isEmpty(spec.title) {
			return nil, fmt.Errorf("line %d: an issue's select needs title", sel.line)
		}
		var err error
		if spec.fields, err = assertFields(n.get("state"), "an issue's state", "exists", "body", "labels", "assignees"); err != nil {
			return nil, err
		}
		for _, f := range spec.fields {
			if contains(issueSetFields, f.name) {
				if err := checkSetField(f); err != nil {
					return nil, err
				}
			} else if f.want.kind != yScalar {
				return nil, fmt.Errorf("line %d: an issue's %s should be a single value", f.line, f.name)
			}
		}
		out = append(out, spec)
	}
	return out, nil
}

// ghIssue is the part of an issue dtt reads.
type ghIssue struct {
	Number  int     `json:"number"`
	Title   string  `json:"title"`
	Body    *string `json:"body"`
	HTMLURL string  `json:"html_url"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	PullRequest any `json:"pull_request"` // set when this "issue" is a PR
}

func (is *ghIssue) labels() []any {
	out := []any{}
	for _, l := range is.Labels {
		out = append(out, l.Name)
	}
	sortAny(out)
	return out
}

func (is *ghIssue) assignees() []any {
	out := []any{}
	for _, a := range is.Assignees {
		out = append(out, a.Login)
	}
	sortAny(out)
	return out
}

// openIssues lists a repository's open issues, every page of them, leaving
// out pull requests (which GitHub lists as issues too).
func (g *githubClient) openIssues(ctx context.Context, owner, repo string) ([]ghIssue, error) {
	const perPage, maxPages = 100, 20
	var all []ghIssue
	for page := 1; page <= maxPages; page++ {
		q := url.Values{"state": {"open"}, "per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
		var issues []ghIssue
		if _, err := g.call(ctx, "GET", ghRepoURL(owner, repo)+"/issues?"+q.Encode(), nil, &issues); err != nil {
			return nil, err
		}
		for _, is := range issues {
			if is.PullRequest == nil {
				all = append(all, is)
			}
		}
		if len(issues) < perPage {
			break
		}
	}
	return all, nil
}

func issueState(is *ghIssue, title string) map[string]any {
	if is == nil {
		return map[string]any{"exists": false, "title": title}
	}
	body := ""
	if is.Body != nil {
		body = *is.Body
	}
	return map[string]any{"exists": true, "number": is.Number, "url": is.HTMLURL, "title": is.Title, "body": body,
		"labels": is.labels(), "assignees": is.assignees()}
}

func (r *ghRepo) assertIssue(ctx context.Context, e *engine, repoObj *dobj, s issueSpec) error {
	g := e.github()
	title, err := e.text(s.title, "select.title")
	if err != nil {
		return err
	}
	pf, err := e.evalGithubFields(s.fields, s.line, issueSetFields)
	if err != nil {
		return err
	}
	open, err := g.openIssues(ctx, r.owner, r.name)
	if err != nil {
		return err
	}
	var issue *ghIssue
	for i := range open {
		if open[i].Title == title && (issue == nil || open[i].Number < issue.Number) {
			issue = &open[i]
		}
	}
	o := e.addObject(repoObj.img.ID+".issues["+title+"]", repoObj.img.Path+".issues", s.line, issueState(issue, title), nil)

	if e.deleting() {
		pf.wantExists, pf.explicit = false, true
	}
	exists := issue != nil
	changeExists := false
	if pf.explicit || !exists {
		changeExists = e.decide(o, []string{"exists"}, pf.existsLine, pf.wantExists, exists, pf.existsFrom)
	}
	issues := ghRepoURL(r.owner, r.name) + "/issues"
	if !pf.wantExists {
		if changeExists {
			if _, err := g.call(ctx, "PATCH", fmt.Sprintf("%s/%d", issues, issue.Number), map[string]any{"state": "closed"}, nil); err != nil {
				return err
			}
			o.img.State = issueState(nil, title)
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
		number = issue.Number
	}
	switch {
	case !exists:
		if !changeExists {
			return nil
		}
		create := map[string]any{"title": title, "body": ""}
		for k, v := range patch {
			create[k] = v
		}
		var got ghIssue
		if _, err := g.call(ctx, "POST", issues, create, &got); err != nil {
			return err
		}
		number = got.Number
	case len(patch) > 0:
		if _, err := g.call(ctx, "PATCH", fmt.Sprintf("%s/%d", issues, number), patch, nil); err != nil {
			return err
		}
	case len(changes) == 0:
		return nil
	}

	one := fmt.Sprintf("%s/%d", issues, number)
	if c, ok := changes["labels"]; ok {
		if len(c.add) > 0 {
			if _, err := g.call(ctx, "POST", one+"/labels", map[string]any{"labels": c.add}, nil); err != nil {
				return err
			}
		}
		for _, l := range c.remove {
			if _, err := g.call(ctx, "DELETE", one+"/labels/"+url.PathEscape(l), nil, nil); err != nil {
				return err
			}
		}
	}
	if c, ok := changes["assignees"]; ok {
		if len(c.add) > 0 {
			if _, err := g.call(ctx, "POST", one+"/assignees", map[string]any{"assignees": c.add}, nil); err != nil {
				return err
			}
		}
		if len(c.remove) > 0 {
			if _, err := g.call(ctx, "DELETE", one+"/assignees", map[string]any{"assignees": c.remove}, nil); err != nil {
				return err
			}
		}
	}

	// Measure it again, as the changes left it.
	var now ghIssue
	if _, err := g.call(ctx, "GET", one, nil, &now); err != nil {
		return err
	}
	o.img.State = issueState(&now, title)
	return nil
}
