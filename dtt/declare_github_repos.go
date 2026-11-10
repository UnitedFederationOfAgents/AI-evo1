package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// This file is the github provider's other path (see declare_github.go): an
// owner's set of repositories, which an assert can create and delete.
//
//	github.orgs.<owner>.repos:              (or github.users.<owner>.repos)
//	  assert:
//	    repos:                              (one, or a list)
//	      select:
//	        name: scratch-${{timestamp.date}}
//	      state:
//	        exists: true                    (the default, so it may be left out)
//	        private: true
//	        description: ...
//	        default_branch: main
//	        auto_init: true                 (create-time only: a first commit)
//	        approve_delete: <owner>/<name>  (needed beside exists: false)
//
// The statement's own object is the owner; each selected repository is a
// child object, <path>[<name>], with every field a repository on the other
// path has. auto_init only means something when the repository is created:
// it is applied then, and read back as false (so it is never asserted on a
// repository that exists). exists: false deletes the repository, which
// can't be undone, so it is refused unless approve_delete beside it names
// the repository as <owner>/<name>; the from rule then applies as for any
// field (exists: true isn't empty). Delete mode asserts exists: false on
// every selected repository, under the same approval and from rule, so a
// document whose repositories carry approve_delete can make them (assert)
// and remove them again (delete). approve_delete is only checked when
// deleting.

// repoFields are the fields a repository's state may assert.
var repoFields = []string{"exists", "private", "description", "default_branch", "auto_init", "approve_delete"}

type repoSpec struct {
	name   *yNode
	fields []fieldSpec
	line   int
}

// ghOwnerPath reads github.orgs|users.<owner>.repos.
func ghOwnerPath(path []string) (kind, owner string, ok bool) {
	if len(path) == 4 && (path[1] == "orgs" || path[1] == "users") && path[3] == "repos" {
		return path[1], path[2], true
	}
	return "", "", false
}

func parseRepoSpecs(body *yNode) ([]repoSpec, error) {
	if body.kind != yMap {
		return nil, fmt.Errorf("line %d: an owner's repositories' assert is a mapping with repos under it", body.line)
	}
	if err := onlyKeys(body, "an owner's repositories' assert", "repos"); err != nil {
		return nil, err
	}
	repos := body.get("repos")
	if repos == nil {
		return nil, fmt.Errorf("line %d: an owner's repositories' assert needs repos", body.line)
	}
	items := []*yNode{repos}
	if repos.kind == ySeq {
		items = repos.items
	}
	var out []repoSpec
	for _, n := range items {
		if n.kind != yMap {
			return nil, fmt.Errorf("line %d: each of repos is a mapping of select and state", n.line)
		}
		if err := onlyKeys(n, "a repository", "select", "state"); err != nil {
			return nil, err
		}
		sel := n.get("select")
		if sel == nil || sel.kind != yMap {
			return nil, fmt.Errorf("line %d: a repository needs a select mapping (name)", n.line)
		}
		if err := onlyKeys(sel, "a repository's select", "name"); err != nil {
			return nil, err
		}
		spec := repoSpec{name: sel.get("name"), line: n.line}
		if spec.name == nil || isEmpty(spec.name) || spec.name.kind != yScalar {
			return nil, fmt.Errorf("line %d: a repository's select needs name", sel.line)
		}
		var err error
		if spec.fields, err = assertFields(n.get("state"), "a repository's state", repoFields...); err != nil {
			return nil, err
		}
		removes, approved := false, false
		for _, f := range spec.fields {
			if f.want.kind != yScalar {
				return nil, fmt.Errorf("line %d: a repository's %s should be a single value", f.line, f.name)
			}
			switch f.name {
			case "exists":
				b, err := parseBool(f.want.str)
				removes = err == nil && !b
			case "approve_delete":
				approved = !isEmpty(f.want)
				if len(f.from) > 0 {
					return nil, fmt.Errorf("line %d: approve_delete isn't a field of the repository; it takes no from", f.line)
				}
			}
		}
		if removes && !approved {
			return nil, fmt.Errorf("line %d: exists: false deletes the repository, which can't be undone; it needs approve_delete: <owner>/<name> beside it", n.line)
		}
		out = append(out, spec)
	}
	return out, nil
}

// ghRepoJSON is the part of a repository dtt reads.
type ghRepoJSON struct {
	Name          string  `json:"name"`
	FullName      string  `json:"full_name"`
	DefaultBranch string  `json:"default_branch"`
	Private       bool    `json:"private"`
	Description   *string `json:"description"`
	HTMLURL       string  `json:"html_url"`
	CloneURL      string  `json:"clone_url"`
	Archived      bool    `json:"archived"`
}

func repoState(owner, name string, r *ghRepoJSON) map[string]any {
	if r == nil {
		return map[string]any{"exists": false, "name": name, "full_name": owner + "/" + name, "auto_init": false}
	}
	desc := ""
	if r.Description != nil {
		desc = *r.Description
	}
	return map[string]any{"exists": true, "name": r.Name, "full_name": r.FullName, "private": r.Private, "description": desc,
		"default_branch": r.DefaultBranch, "url": r.HTMLURL, "clone_url": r.CloneURL, "archived": r.Archived, "auto_init": false}
}

// ---- github.repos: any repository, read by its full name ----
//
//	github.repos:
//	  select:
//	    repo: ${{var.repo}}                 (<owner>/<name>)
//	  alias: repo
//
// A repository named by a value rather than a path, so a variable can name
// it: its state is what an owner's repository's is (exists, full_name,
// default_branch, private, description, url, clone_url, archived) plus
// owner. It is only read. A public repository needs no token; without one
// GitHub allows 60 requests an hour. clone_url is what git.trees fetches
// (declare_git_trees.go): GitHub knows which repository, git its files.

func isGhReposPath(path []string) bool { return len(path) == 2 && path[1] == "repos" }

func checkGhReposSelect(st *statement) error {
	if st.sel == nil || st.sel.kind != yMap {
		return fmt.Errorf("github.repos needs select: {repo: <owner>/<name>}")
	}
	if err := onlyKeys(st.sel, "github.repos' select", "repo"); err != nil {
		return err
	}
	r, err := scalar(st.sel, "repo")
	if err != nil {
		return err
	}
	if !strings.Contains(r, "${{") {
		if _, _, err := splitFullName(r); err != nil {
			return err
		}
	}
	return nil
}

func splitFullName(s string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("select.repo %q should be <owner>/<name>", s)
	}
	return owner, name, nil
}

func resolveGhRepos(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	full, err := e.text(st.sel.get("repo"), "select.repo")
	if err != nil {
		return nil, err
	}
	owner, name, err := splitFullName(full)
	if err != nil {
		return nil, err
	}
	r, err := e.github().measureRepo(ctx, owner, name)
	if err != nil {
		return nil, err
	}
	state := repoState(owner, name, r)
	delete(state, "auto_init") // create-time only, and this path creates nothing
	state["owner"] = owner
	return []*dobj{e.addObject("github.repos["+owner+"/"+name+"]", st.pathString(), st.line, state, nil)}, nil
}

// measureRepo gets owner/name, nil when there's no such repository.
func (g *githubClient) measureRepo(ctx context.Context, owner, name string) (*ghRepoJSON, error) {
	var r ghRepoJSON
	status, err := g.call(ctx, "GET", ghRepoURL(owner, name), nil, &r)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	return &r, nil
}

// ghOwner is the provider's handle on an owner's set of repositories.
type ghOwner struct{ kind, owner string }

// ghOwnerTypes are the account types GitHub reports for each path kind.
var ghOwnerTypes = map[string]string{"users": "User", "orgs": "Organization"}

func resolveOwner(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	kind, owner, _ := ghOwnerPath(st.path)
	var acct struct {
		Login   string `json:"login"`
		Type    string `json:"type"`
		HTMLURL string `json:"html_url"`
	}
	// /users/<name> answers for an org too, with its type, so a path that
	// names the wrong kind can say so rather than "doesn't exist".
	status, err := e.github().call(ctx, "GET", "/users/"+url.PathEscape(owner), nil, &acct)
	if err != nil {
		return nil, err
	}
	state := map[string]any{"owner": owner, "kind": kind, "exists": status != http.StatusNotFound}
	if status != http.StatusNotFound {
		state["type"] = acct.Type
		state["url"] = acct.HTMLURL
	}
	return []*dobj{e.addObject(st.pathString(), st.pathString(), st.line, state, &ghOwner{kind, owner})}, nil
}

func assertOwner(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	specs, err := parseRepoSpecs(body)
	if err != nil {
		return err
	}
	own := o.handle.(*ghOwner)
	if o.img.State["exists"] != true {
		return fmt.Errorf("github %s %s doesn't exist (or this token can't see it)", own.kind, own.owner)
	}
	if typ, _ := o.img.State["type"].(string); typ != "" && typ != ghOwnerTypes[own.kind] {
		other := "orgs"
		if typ == "User" {
			other = "users"
		}
		return fmt.Errorf("%s is a GitHub %s, not %s; its path is github.%s.%s.repos", own.owner, typ, ghOwnerTypes[own.kind], other, own.owner)
	}
	for _, s := range specs {
		if err := own.assertRepo(ctx, e, o, s); err != nil {
			return err
		}
	}
	return nil
}

func (own *ghOwner) assertRepo(ctx context.Context, e *engine, ownerObj *dobj, s repoSpec) error {
	g := e.github()
	name, err := e.text(s.name, "select.name")
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("line %d: select.name is empty", s.line)
	}
	now, err := g.measureRepo(ctx, own.owner, name)
	if err != nil {
		return err
	}
	o := e.addObject(ownerObj.img.ID+"["+name+"]", ownerObj.img.Path, s.line, repoState(own.owner, name, now), nil)
	exists := now != nil

	wantExists, explicit, existsLine := true, false, s.line
	var existsFrom []any
	approval := ""
	type scalarField struct {
		name, want string
		from       []any
		line       int
	}
	var fields []scalarField
	for _, f := range s.fields {
		want, err := e.text(f.want, f.name)
		if err != nil {
			return err
		}
		from, err := e.values(f.from)
		if err != nil {
			return err
		}
		switch f.name {
		case "exists":
			if wantExists, err = parseBool(want); err != nil {
				return fmt.Errorf("line %d: exists: %v", f.line, err)
			}
			existsFrom, explicit, existsLine = from, true, f.line
		case "approve_delete":
			approval = want
		default:
			if f.name == "private" || f.name == "auto_init" {
				b, err := parseBool(want)
				if err != nil {
					return fmt.Errorf("line %d: %s: %v", f.line, f.name, err)
				}
				want = fmt.Sprint(b)
			}
			fields = append(fields, scalarField{f.name, want, from, f.line})
		}
	}

	if e.deleting() {
		// Delete mode: the repository goes, whatever else is asserted on it.
		wantExists = false
	}

	repoURL := ghRepoURL(own.owner, name)
	if !wantExists {
		if approval != own.owner+"/"+name {
			if approval == "" {
				return fmt.Errorf("line %d: deleting %s/%s can't be undone; it needs approve_delete: %s/%s in its state", s.line, own.owner, name, own.owner, name)
			}
			return fmt.Errorf("line %d: approve_delete %q should be %s/%s to delete that repository", existsLine, approval, own.owner, name)
		}
		if exists && e.decide(o, []string{"exists"}, existsLine, false, true, existsFrom) {
			if _, err := g.call(ctx, "DELETE", repoURL, nil, nil); err != nil {
				return err
			}
			o.img.State = repoState(own.owner, name, nil)
		} else if !exists {
			e.decide(o, []string{"exists"}, existsLine, false, false, existsFrom)
		}
		return nil
	}

	changeExists := false
	if explicit || !exists {
		changeExists = e.decide(o, []string{"exists"}, existsLine, true, exists, existsFrom)
	}
	// Decide every field against what's there now (nothing, for a
	// repository not made yet).
	patch := map[string]any{}
	autoInit := false
	for _, f := range fields {
		if f.name == "auto_init" {
			// Create-time only: asserted on a repository being made, never
			// on one that exists (it reads back false).
			if !exists && e.decide(o, []string{"auto_init"}, f.line, f.want, false, f.from) {
				autoInit = f.want == "true"
			}
			continue
		}
		if !e.decide(o, []string{f.name}, f.line, f.want, o.img.State[f.name], f.from) {
			continue
		}
		if f.name == "private" {
			patch[f.name] = f.want == "true"
		} else {
			patch[f.name] = f.want
		}
	}

	switch {
	case !exists:
		if !changeExists {
			return nil
		}
		create := map[string]any{"name": name, "auto_init": autoInit}
		for _, k := range []string{"private", "description"} {
			if v, ok := patch[k]; ok {
				create[k] = v
				delete(patch, k)
			}
		}
		createURL := "/orgs/" + url.PathEscape(own.owner) + "/repos"
		if own.kind == "users" {
			// /user/repos makes it for whoever the token is: refuse if that
			// isn't the owner named.
			var me struct {
				Login string `json:"login"`
			}
			if _, err := g.call(ctx, "GET", "/user", nil, &me); err != nil {
				return err
			}
			if me.Login != own.owner {
				return fmt.Errorf("the GitHub token here is %s's, so it can't create a repository for user %s", me.Login, own.owner)
			}
			createURL = "/user/repos"
		}
		var made ghRepoJSON
		if _, err := g.call(ctx, "POST", createURL, create, &made); err != nil {
			return err
		}
		if b, ok := patch["default_branch"]; ok && b == made.DefaultBranch {
			delete(patch, "default_branch")
		}
	case len(patch) == 0:
		return nil
	}
	if len(patch) > 0 {
		if _, err := g.call(ctx, "PATCH", repoURL, patch, nil); err != nil {
			return err
		}
	}

	// Measure it again, as the changes left it.
	if now, err = g.measureRepo(ctx, own.owner, name); err != nil {
		return err
	}
	o.img.State = repoState(own.owner, name, now)
	return nil
}
