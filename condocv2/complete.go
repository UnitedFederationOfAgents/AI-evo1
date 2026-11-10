package main

// Completing a v2 condoc and re-integrating its branches:
//
//	complete  (condoc + pointer commits) -> the timeline record is saved
//	          (record.go), then the agent drafts one PR per repo with changes
//	          (an in-place turn writing prs.yaml)
//	drafted   (pointer commit)  -> the human edits the drafts, or redrafts
//	prs       (condoc + pointer commits) -> the PRs are raised, targets
//	          first, the condoc repo last
//	merged    (pointer commit)  -> Merge, once every PR is mergeable, merges
//	          them in that order, awaits GitHub's confirmation of each, and
//	          commits the finalization (the completion's timeline nodes and
//	          links, see Finalization); then it removes the slopspace (the
//	          branches stay)
//
// The drafts are asked for with the human's PR guidance, which defaults to
// defaultPRGuidance. A local repo (local/<name>, localrepo.go) has no PR:
// raising records the local merge to come, and Merge merges the branch into
// its base in the repo itself.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dungeon-keeper/pkg/gitauth"
	dktypes "dungeon-keeper/pkg/types"
	prkeeper "pr-keeper"
)

const (
	prTemplateFile = "prs.yaml"
	prDiffLimit    = 200_000
	prLiveMaxAge   = 15 * time.Second
)

// defaultPRGuidance is what the PR drafts are asked for unless the human
// says otherwise.
const defaultPRGuidance = "Be extremely concise. Title: under 60 characters, imperative mood. " +
	"Body: at most three short bullet points on what changed -- no headings, no background, " +
	"no restating the condoc, no test plan."

// ---- complete ----

// slopComplete ends the condoc's interactions: it marks the main file
// completed and saves the timeline record (the condoc's complete commit), and
// queues the PR draft turn (the
// pointer's complete commit).
func (s *Server) slopComplete(stem, guidance string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != PhaseAwaitingPrompt || spec.Lock != nil || spec.Error != "" {
		return fmt.Errorf("%s can only be completed between interactions (phase %s)", spec.Name, spec.Phase)
	}
	if spec.Interactions == 0 {
		return fmt.Errorf("%s has no interactions to complete", spec.Name)
	}
	meta, main, err := s.idleMainFile(&spec)
	if err != nil {
		return err
	}
	content, err := readFileString(main)
	if err != nil {
		return err
	}
	if !strings.Contains(content, "\n## Completed\n") {
		content = strings.TrimRight(content, "\n") + fmt.Sprintf("\n\n## Completed\n\nAfter %d interaction(s). Timeline record: [%s](%s).\n",
			spec.Interactions, recordFileName, filepath.ToSlash(filepath.Join(filepath.Base(implDir(main)), recordFileName)))
		if err := writeFileString(main, content); err != nil {
			return err
		}
	}
	// The visual tab's model, for once Merge has deleted the slopspace (see
	// record.go).
	if err := s.writeTimelineRecord(&spec, meta, main); err != nil {
		return fmt.Errorf("timeline record: %v", err)
	}
	if err := s.commitCondocRepo(&spec, commitMsg(&spec, "complete", 0)); err != nil {
		return err
	}
	return s.queueDraft(&spec, guidance, "complete")
}

// slopRedraft asks the agent for new drafts, given the current (possibly
// edited) ones and new guidance.
func (s *Server) slopRedraft(stem, guidance, template string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != PhasePRProposed {
		return fmt.Errorf("%s has no PR drafts to redraft (phase %s)", spec.Name, spec.Phase)
	}
	if strings.TrimSpace(template) != "" {
		spec.PRTemplate = template
	}
	return s.queueDraft(&spec, guidance, "redraft")
}

func (s *Server) queueDraft(spec *PointerSpec, guidance, kind string) error {
	if guidance = strings.TrimSpace(guidance); guidance == "" {
		guidance = defaultPRGuidance
	}
	spec.PRGuidance = guidance
	spec.PRRole, spec.PRSignalAt, spec.PRWorkdir = "", 0, ""
	spec.Error = ""
	spec.Phase = PhasePRDrafting
	if err := s.writeSpec(spec, commitMsg(spec, kind, 0)); err != nil {
		return err
	}
	s.enqueueTurn(spec.Stem())
	return nil
}

// idleMainFile is the condoc's main file, which condocv2 only edits while
// the slopspace isn't deployed.
func (s *Server) idleMainFile(spec *PointerSpec) (*dktypes.SlopspaceMetadata, string, error) {
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return nil, "", err
	}
	if meta.Deployed {
		return nil, "", fmt.Errorf("%s's slopspace is deployed", spec.Name)
	}
	return meta, filepath.Join(slopCondocRoot(meta, spec.CondocRepo), filepath.FromSlash(spec.CondocPath)), nil
}

// ---- the PR draft turn ----

// runDraftTurn runs the in-place turn that drafts the PRs: a tempdir in the
// slopspace holding the condoc, each repo's diff and log and the guidance,
// in which the agent writes prs.yaml. An unusable prs.yaml falls back to a
// default draft. The caller holds the claim.
func (s *Server) runDraftTurn(spec *PointerSpec) error {
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		return err
	}
	repos := s.prRepos(spec, meta)
	if spec.PRRole == "" {
		dir, err := s.prepareDraftDir(spec, meta, repos)
		if err != nil {
			return err
		}
		role, at, err := s.submitSignal(spec, dktypes.WorkTypeInPlace, dir, draftPrompt(dir, repos, spec.PRTemplate != ""))
		if err != nil {
			os.RemoveAll(dir)
			return err
		}
		spec.PRRole, spec.PRSignalAt, spec.PRWorkdir = role, at, dir
		if err := s.writeSpec(spec, ""); err != nil {
			return err
		}
	}
	reply, err := s.awaitSignal(spec.PRRole, spec.PRSignalAt)
	if err != nil {
		return err
	}
	template, terr := readFileString(filepath.Join(spec.PRWorkdir, prTemplateFile))
	if terr == nil {
		if drafts, perr := parsePRTemplate(template); perr != nil {
			terr = perr
		} else {
			_, terr = orderPRDrafts(repos, drafts)
		}
	}
	if terr != nil {
		template = defaultPRTemplate(spec, repos)
		spec.Error = fmt.Sprintf("the agent's %s was unusable (%v), so a default draft was filled in. Agent reply: %s",
			prTemplateFile, terr, firstLine(reply))
	}
	os.RemoveAll(spec.PRWorkdir)
	spec.PRRole, spec.PRSignalAt, spec.PRWorkdir = "", 0, ""
	spec.PRTemplate = template
	spec.Phase = PhasePRProposed
	return s.writeSpec(spec, commitMsg(spec, "drafted", 0))
}

// prRepos is every repo with something to merge: the targets whose branch
// is ahead of their base, then the condoc repo (always, it holds the condoc).
func (s *Server) prRepos(spec *PointerSpec, meta *dktypes.SlopspaceMetadata) []string {
	var out []string
	for _, repo := range spec.Targets {
		n, err := exec.Command("git", "--git-dir", slopGitDir(meta, repo), "rev-list", "--count",
			"origin/"+slopBaseBranch(meta, repo)+"..HEAD").Output()
		if err != nil || strings.TrimSpace(string(n)) != "0" {
			out = append(out, repo)
		}
	}
	return append(out, spec.CondocRepo)
}

// slopBaseBranch is a writespace repo's default branch, from its clone's
// origin/HEAD.
func slopBaseBranch(meta *dktypes.SlopspaceMetadata, repo string) string {
	out, err := exec.Command("git", "--git-dir", slopGitDir(meta, repo), "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
	if err == nil {
		if b := strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/"); b != "" {
			return b
		}
	}
	return "main"
}

// prepareDraftDir builds the draft turn's tempdir inside the slopspace's own
// directory, which the worker shares with condocv2.
func (s *Server) prepareDraftDir(spec *PointerSpec, meta *dktypes.SlopspaceMetadata, repos []string) (string, error) {
	dir, err := os.MkdirTemp(meta.RootPath, "pr-draft-")
	if err != nil {
		return "", err
	}
	main := filepath.Join(slopCondocRoot(meta, spec.CondocRepo), filepath.FromSlash(spec.CondocPath))
	files := map[string]string{"guidance.txt": spec.PRGuidance + "\n"}
	if spec.PRTemplate != "" {
		files["previous-prs.yaml"] = spec.PRTemplate
	}
	// The main file, and every scope's step file (steps.go).
	for _, src := range append([]string{main}, scopeFiles(spec, main)...) {
		content, err := readFileString(src)
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		files[filepath.Join("condoc", filepath.Base(src))] = content
	}
	for _, repo := range repos {
		gitDir, base := slopGitDir(meta, repo), slopBaseBranch(meta, repo)
		slug := strings.ReplaceAll(repo, "/", "__")
		diff, _ := exec.Command("git", "--git-dir", gitDir, "diff", "origin/"+base+"...HEAD").CombinedOutput()
		if len(diff) > prDiffLimit {
			diff = append(diff[:prDiffLimit], []byte("\n... (diff truncated)\n")...)
		}
		logOut, _ := exec.Command("git", "--git-dir", gitDir, "log", "--oneline", "origin/"+base+"..HEAD").CombinedOutput()
		files[filepath.Join("repos", slug+".diff")] = string(diff)
		files[filepath.Join("repos", slug+".log")] = string(logOut)
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err == nil {
			err = writeFileString(path, content)
		}
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func draftPrompt(dir string, repos []string, redraft bool) string {
	prompt := fmt.Sprintf("You are drafting pull requests for a completed condoc. Everything you need is in '%s': "+
		"condoc/ holds the condoc and its step files (Step1.md, and Step-<path>.md for each step it went into), repos/ holds each repo's diff (<owner>__<name>.diff) and log "+
		"(<owner>__<name>.log) against its base branch, and guidance.txt says how to write the PRs -- follow it. ", dir)
	if redraft {
		prompt += "The previous drafts, possibly edited by the human, are in previous-prs.yaml. "
	}
	return prompt + "Write a file named " + prTemplateFile + " in that directory, in exactly this YAML shape:\n\n" +
		"prs:\n  - repo: <owner/name>\n    title: <PR title>\n    body: |\n      <PR description in markdown>\n\n" +
		"Include exactly one entry for each of these repos: " + strings.Join(repos, ", ") + ". " +
		"Do not modify any other file. When you have finished, reply with one sentence saying the drafts are written."
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// ---- the drafts (prs.yaml) ----

type prDraft struct {
	Repo  string
	Title string
	Body  string
}

// parsePRTemplate reads
//
//	prs:
//	  - repo: owner/name
//	    title: ...
//	    body: |
//	      ...
func parsePRTemplate(text string) ([]prDraft, error) {
	m, err := parseMiniYAML(text)
	if err != nil {
		return nil, err
	}
	var drafts []prDraft
	for i, item := range yamlList(m, "prs") {
		pm, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("prs[%d]: expected a mapping", i)
		}
		d := prDraft{
			Repo:  strings.TrimSpace(yamlString(pm, "repo")),
			Title: strings.TrimSpace(yamlString(pm, "title")),
			Body:  strings.TrimSpace(yamlString(pm, "body")),
		}
		if d.Title == "" {
			return nil, fmt.Errorf("prs[%d] (%s): title is empty", i, d.Repo)
		}
		drafts = append(drafts, d)
	}
	if len(drafts) == 0 {
		return nil, fmt.Errorf("no prs listed")
	}
	return drafts, nil
}

// orderPRDrafts checks the drafts cover exactly repos, and puts them in
// repos' order -- the order they are raised and merged in.
func orderPRDrafts(repos []string, drafts []prDraft) ([]prDraft, error) {
	byRepo := map[string]prDraft{}
	for _, d := range drafts {
		key := strings.ToLower(d.Repo)
		if _, dup := byRepo[key]; dup {
			return nil, fmt.Errorf("%s is listed twice", d.Repo)
		}
		byRepo[key] = d
	}
	var ordered []prDraft
	for _, repo := range repos {
		d, ok := byRepo[strings.ToLower(repo)]
		if !ok {
			return nil, fmt.Errorf("no PR for %s", repo)
		}
		delete(byRepo, strings.ToLower(repo))
		d.Repo = repo
		ordered = append(ordered, d)
	}
	var extra []string
	for _, d := range byRepo {
		extra = append(extra, d.Repo)
	}
	if len(extra) > 0 {
		return nil, fmt.Errorf("nothing to merge, or not one of this condoc's repos: %s", strings.Join(extra, ", "))
	}
	return ordered, nil
}

// defaultPRTemplate is the fallback when the agent's drafts are unusable.
func defaultPRTemplate(spec *PointerSpec, repos []string) string {
	var w yamlWriter
	w.line(0, "prs:")
	for _, repo := range repos {
		w.line(2, "- repo: "+yamlScalar(repo))
		w.field(4, "title", spec.Name, true)
		w.field(4, "body", fmt.Sprintf("Changes from condoc %s.", spec.Name), true)
	}
	return w.String()
}

// ---- raising the PRs ----

func prClient() (*prkeeper.Client, error) {
	tok := githubToken()
	if tok == "" {
		return nil, fmt.Errorf("no GitHub token (TF_VAR_github_pat) to raise or merge PRs with")
	}
	return prkeeper.NewClient(tok), nil
}

// slopRaisePRs raises the drafts' PRs, targets first and the condoc repo
// last, and lists them in the condoc (its prs commit) and the pointer. Raising
// again after a partial failure picks up the PRs already open.
func (s *Server) slopRaisePRs(stem, template string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != PhasePRProposed {
		return fmt.Errorf("%s has no PR drafts to raise (phase %s)", spec.Name, spec.Phase)
	}
	if strings.TrimSpace(template) == "" {
		template = spec.PRTemplate
	}
	meta, main, err := s.idleMainFile(&spec)
	if err != nil {
		return err
	}
	drafts, err := parsePRTemplate(template)
	if err == nil {
		drafts, err = orderPRDrafts(s.prRepos(&spec, meta), drafts)
	}
	if err != nil {
		return fmt.Errorf("drafts: %v", err)
	}
	// A local repo's PR is the local merge to come (localrepo.go); only
	// GitHub's need a client.
	var client *prkeeper.Client
	if !allLocal(drafts) {
		if client, err = prClient(); err != nil {
			return err
		}
	}
	spec.PRTemplate = template
	fail := func(e error) error {
		spec.Error = e.Error()
		if werr := s.writeSpec(&spec, commitMsg(&spec, "error", 0)); werr != nil {
			log.Printf("condocv2 %s: %v", stem, werr)
		}
		return e
	}

	var prs []PointerPR
	for _, d := range drafts {
		if gitauth.IsLocal(d.Repo) {
			if err := gitauth.CanClone(d.Repo); err != nil {
				spec.PRs = prs
				return fail(err)
			}
			prs = append(prs, localPR(d.Repo, spec.Branch, slopBaseBranch(meta, d.Repo)))
			continue
		}
		body := d.Body + fmt.Sprintf("\n\n---\nRaised by condocv2 for condoc [%s](%s).", spec.Name, spec.CondocURL)
		pr, err := client.FindOpen(d.Repo, spec.Branch)
		if err == nil && pr == nil {
			pr, err = client.Create(d.Repo, prkeeper.CreateRequest{Title: d.Title, Head: spec.Branch, Base: slopBaseBranch(meta, d.Repo), Body: body})
		}
		if err != nil {
			spec.PRs = prs
			return fail(fmt.Errorf("%s: %v", d.Repo, err))
		}
		prs = append(prs, PointerPR{Repo: d.Repo, Number: pr.Number, URL: pr.URL, State: "open"})
	}
	spec.PRs = prs

	if content, err := readFileString(main); err == nil && !strings.Contains(content, "\n## Pull Requests\n") {
		var lines []string
		for _, pr := range prs {
			if gitauth.IsLocal(pr.Repo) {
				lines = append(lines, fmt.Sprintf("- %s: a local merge, [%s](%s)", pr.Repo, strings.TrimPrefix(pr.URL, "ufa-local:"), pr.URL))
				continue
			}
			lines = append(lines, fmt.Sprintf("- %s: [#%d](%s)", pr.Repo, pr.Number, pr.URL))
		}
		content = strings.TrimRight(content, "\n") + "\n\n## Pull Requests\n\n" + strings.Join(lines, "\n") + "\n"
		if err := writeFileString(main, content); err != nil {
			return fail(err)
		}
	}
	if err := s.commitCondocRepo(&spec, commitMsg(&spec, "prs", 0)); err != nil {
		return fail(err)
	}
	spec.Error = ""
	spec.Phase = PhasePROpen
	s.forgetPRLive(stem)
	return s.writeSpec(&spec, commitMsg(&spec, "prs", 0))
}

// ---- the PRs' live state ----

// PRLive is a raised PR as last seen on GitHub.
type PRLive struct {
	PointerPR
	Status string `json:"status"` // GitHub's mergeability, in words
	Ready  bool   `json:"ready"`  // mergeable now
}

type prLiveCache struct {
	at         time.Time
	prs        []PRLive
	refreshing bool
}

func (s *Server) forgetPRLive(stem string) {
	s.slop.mu.Lock()
	delete(s.slop.prLive, stem)
	s.slop.mu.Unlock()
}

// prLiveFor is the condoc's PRs for the view: as recorded, and while they
// are open, as last seen on GitHub -- refreshed in the background once the
// last look is older than prLiveMaxAge (the watch loop pushes the change).
func (s *Server) prLiveFor(spec *PointerSpec) []PRLive {
	recorded := make([]PRLive, len(spec.PRs))
	for i, pr := range spec.PRs {
		recorded[i] = PRLive{PointerPR: pr, Status: pr.State}
	}
	if spec.Phase != PhasePROpen {
		return recorded
	}
	stem := spec.Stem()
	s.slop.mu.Lock()
	c := s.slop.prLive[stem]
	stale := !c.refreshing && time.Since(c.at) > prLiveMaxAge
	if stale {
		c.refreshing = true
		s.slop.prLive[stem] = c
	}
	s.slop.mu.Unlock()
	if stale {
		go s.refreshPRLive(stem, spec.PRs)
	}
	if len(c.prs) == len(recorded) {
		return c.prs
	}
	return recorded
}

func (s *Server) refreshPRLive(stem string, prs []PointerPR) {
	live := make([]PRLive, len(prs))
	client, cerr := prClient()
	for i, pr := range prs {
		live[i] = PRLive{PointerPR: pr, Status: pr.State}
		if gitauth.IsLocal(pr.Repo) {
			live[i] = localPRLive(pr)
			continue
		}
		if cerr != nil {
			live[i].Status = cerr.Error()
			continue
		}
		got, err := client.Get(pr.Repo, pr.Number)
		if err != nil {
			live[i].Status = "unknown: " + err.Error()
			continue
		}
		live[i].Status, live[i].Ready = got.Status(), got.Ready()
		if got.Merged {
			live[i].State = "merged"
		}
	}
	s.slop.mu.Lock()
	s.slop.prLive[stem] = prLiveCache{at: time.Now(), prs: live}
	s.slop.mu.Unlock()
}

// ---- merge ----

// slopMerge merges every PR once all are mergeable -- targets first, the
// condoc repo last, with merge commits so the condoc's record links keep
// pointing at real history -- awaits each merge, commits the finalization
// with the merged commit, then deletes the slopspace. The condoc branches
// are kept.
// A failure stops the run; Merge again resumes past the merged ones.
func (s *Server) slopMerge(stem string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	defer s.forgetPRLive(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != PhasePROpen {
		return fmt.Errorf("%s has no open PRs (phase %s)", spec.Name, spec.Phase)
	}
	var client *prkeeper.Client
	if !allLocalPRs(spec.PRs) {
		if client, err = prClient(); err != nil {
			return err
		}
	}
	fail := func(e error) error {
		spec.Error = e.Error()
		if werr := s.writeSpec(&spec, commitMsg(&spec, "error", 0)); werr != nil {
			log.Printf("condocv2 %s: %v", stem, werr)
		}
		return e
	}

	live := make([]*prkeeper.PullRequest, len(spec.PRs))
	for i, pr := range spec.PRs {
		if gitauth.IsLocal(pr.Repo) {
			// Merged below, in its local repo; here only checked mergeable.
			if l := localPRLive(pr); !l.Ready && l.State != "merged" {
				return fail(fmt.Errorf("%s can't be merged locally (%s)", pr.Repo, l.Status))
			}
			continue
		}
		got, err := client.Get(pr.Repo, pr.Number)
		if err != nil {
			return fail(fmt.Errorf("%s#%d: %v", pr.Repo, pr.Number, err))
		}
		if !got.Merged && !got.Ready() {
			return fail(fmt.Errorf("%s#%d isn't mergeable yet (%s)", pr.Repo, pr.Number, got.Status()))
		}
		live[i] = got
	}
	for i := range spec.PRs {
		pr := &spec.PRs[i]
		if gitauth.IsLocal(pr.Repo) {
			got, err := localMerge(*pr, spec.Name)
			if err != nil {
				return fail(err)
			}
			live[i], pr.State = got, "merged"
			if pr.Repo == spec.CondocRepo {
				spec.CondocURL = repoBlobURL(spec.CondocRepo, got.Base.Ref, spec.CondocPath)
			}
			continue
		}
		if !live[i].Merged {
			if err := client.Merge(pr.Repo, pr.Number, prkeeper.MergeMethodMerge, live[i].Head.SHA); err != nil {
				return fail(fmt.Errorf("merging %s#%d: %v", pr.Repo, pr.Number, err))
			}
		}
		pr.State = "merged"
		if pr.Repo == spec.CondocRepo && live[i].Base.Ref != "" {
			spec.CondocURL = repoBlobURL(spec.CondocRepo, live[i].Base.Ref, spec.CondocPath)
		}
	}
	// Await every merge before the final commit: it records each one's merge
	// commit. A local merge is done as soon as it is made.
	merges, err := awaitMerges(client, spec.PRs, live)
	if err != nil {
		return fail(err)
	}

	// The final commit keeps the completion nodes the timeline record doesn't
	// have, read from the slopspace before it goes.
	meta, err := s.slop.ss.Get(spec.SlopspaceID)
	if err != nil {
		meta = nil
	}
	spec.Finalization = s.finalization(&spec, meta, merges)
	spec.Error = ""
	spec.Phase = PhaseMerged
	if err := s.writeSpec(&spec, commitMsg(&spec, "merged", 0)); err != nil {
		return err
	}
	if meta != nil && !meta.Deployed {
		if err := s.slop.ss.Delete(spec.SlopspaceID); err != nil {
			log.Printf("condocv2 %s: deleting slopspace %s: %v", stem, spec.SlopspaceID, err)
		}
	}
	return nil
}

const (
	mergeAwaitTimeout = 2 * time.Minute
	mergeAwaitEvery   = 3 * time.Second
)

// awaitMerges waits until GitHub reports each PR merged with its merge
// commit, and returns them in the PRs' order. A local repo's (localMerge's,
// in local) is merged already.
func awaitMerges(client *prkeeper.Client, prs []PointerPR, local []*prkeeper.PullRequest) ([]*prkeeper.PullRequest, error) {
	out := make([]*prkeeper.PullRequest, len(prs))
	deadline := time.Now().Add(mergeAwaitTimeout)
	for i, pr := range prs {
		if gitauth.IsLocal(pr.Repo) {
			out[i] = local[i]
			continue
		}
		for {
			got, err := client.Get(pr.Repo, pr.Number)
			if err == nil && got.Merged && got.MergeCommitSHA != "" {
				out[i] = got
				break
			}
			if time.Now().After(deadline) {
				if err == nil {
					err = fmt.Errorf("not reported merged yet (%s)", got.Status())
				}
				return nil, fmt.Errorf("awaiting the merge of %s#%d: %v", pr.Repo, pr.Number, err)
			}
			time.Sleep(mergeAwaitEvery)
		}
	}
	return out, nil
}

// finalization is the merged commit's Finalization: the condoc lane's
// completion commits (from the slopspace, when meta is still there) and each
// PR's merge commit, on its repo's lane.
func (s *Server) finalization(spec *PointerSpec, meta *dktypes.SlopspaceMetadata, merges []*prkeeper.PullRequest) *Finalization {
	f := &Finalization{State: PhaseMerged, At: time.Now().UTC().Format(time.RFC3339), RecordURL: recordURL(spec)}
	if meta != nil {
		for _, e := range s.buildTimeline(spec, meta).Events {
			if _, ok := completionKindSlot[e.Kind]; ok && e.Lane != "pointer" && !e.Pending {
				f.Events = append(f.Events, FinalEvent{Lane: e.Lane, Repo: spec.CondocRepo, Kind: e.Kind, SHA: e.SHA, Time: e.Time})
			}
		}
	}
	for i, pr := range spec.PRs {
		m := merges[i]
		lane := pr.Repo
		if pr.Repo == spec.CondocRepo {
			lane = "condoc"
		}
		e := FinalEvent{Lane: lane, Repo: pr.Repo, Kind: "merged", SHA: m.MergeCommitSHA, PR: pr.URL}
		if t, err := time.Parse(time.RFC3339, m.MergedAt); err == nil {
			e.Time = t.Unix()
		}
		f.Events = append(f.Events, e)
	}
	return f
}
