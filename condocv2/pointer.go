package main

// Pointer repo: the top-level setting of condocv2 -- see
// docs/SlopspaceControlScheme.md. A git repo condocv2 keeps checked out at a
// workspace path and regularly pulls on a target branch, holding one pointer
// file per condoc (pointers/*.yaml) that records where that condoc's
// progression is and which repos it targets. The pointers/ directory is
// shared with condoccer (v1); each lists only its own version's pointers
// (see isCondocV2Pointer).
//
// This file configures the repo/branch/path, clones it when missing, keeps it
// pulled (continuously -- there is no manual pull) and lists the v2 pointers
// it holds. Proposing, beginning and driving condocs through their pointers
// is slopspace.go.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultPointerRepo   = "je-sidestuff/sloppo"
	defaultPointerBranch = "main"
	defaultPointerPath   = "../sloppo"

	// pointerPullInterval is how often a ready pointer repo is pulled in the
	// background so pointers written by other condoccers show up. It is the
	// only pull there is, so it runs often.
	pointerPullInterval = 15 * time.Second

	// pointerDir holds the per-condoc pointer files inside the pointer repo.
	pointerDir = "pointers"
)

// pointerRepoPattern is an owner/name GitHub repo.
var pointerRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// PointerConfig is the configured pointer repo. Path may be relative, in
// which case it resolves against the pointer base (see resolvePointerPath).
type PointerConfig struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
}

// PointerEntry is one v2 pointer file's summary, as listed in the sidebar
// (see entryFromSpec and decoratePointerEntry).
type PointerEntry struct {
	File         string   `json:"file"` // relative to the pointer repo root
	Stem         string   `json:"stem"` // what the UI subscribes to
	Name         string   `json:"name"`
	Phase        string   `json:"phase"`
	Interactions int      `json:"interactions"` // completed interactions
	Branch       string   `json:"branch,omitempty"`
	CondocRepo   string   `json:"condocRepo,omitempty"`
	Targets      []string `json:"targets,omitempty"`
	CondocPath   string   `json:"condocPath,omitempty"`
	CondocURL    string   `json:"condocUrl,omitempty"`
	Description  string   `json:"description,omitempty"`
	Agent        string   `json:"agent,omitempty"`
	Model        string   `json:"model,omitempty"`
	StartTime    int64    `json:"startTime,omitempty"`
	SlopspaceID  string   `json:"slopspaceId,omitempty"`
	Holder       string   `json:"holder,omitempty"`
	Error        string   `json:"error,omitempty"`

	// The interaction holding the lock, if any.
	LockInteraction int    `json:"lockInteraction,omitempty"`
	LockStage       string `json:"lockStage,omitempty"`
	AutoSubmit      bool   `json:"autoSubmit,omitempty"`

	// Known only to this condocv2.
	HeldHere bool `json:"heldHere"`
	QueuePos int  `json:"queuePos,omitempty"` // 1-based position in the turn queue
	Running  bool `json:"running,omitempty"`  // its turn is the one running
	Deployed bool `json:"deployed,omitempty"` // its slopspace is deployed
}

// PointerStatus is the "pointer" WebSocket payload, sent on connect and
// broadcast whenever the config or the repo's state changes.
//
// State is one of:
//   - "missing": nothing at the resolved path; the UI offers to clone
//   - "cloning": a clone is in progress
//   - "invalid": something is at the path but it isn't the configured repo
//     on the configured branch (Message says why)
//   - "ready":   the pointer repo is usable; Pointers lists its pointer files
type PointerStatus struct {
	PointerConfig
	ResolvedPath   string         `json:"resolvedPath"`
	State          string         `json:"state"`
	Message        string         `json:"message,omitempty"`
	LastPull       string         `json:"lastPull,omitempty"` // RFC3339 time of the last successful pull
	PullError      string         `json:"pullError,omitempty"`
	PushError      string         `json:"pushError,omitempty"` // the last pointer push failed; retried after each pull
	TokenAvailable bool           `json:"tokenAvailable"`
	Holder         string         `json:"holder"` // this condocv2's holder ID (see slopHolder)
	DefaultAgent   string         `json:"defaultAgent,omitempty"`
	DefaultModel   string         `json:"defaultModel,omitempty"`
	Pointers       []PointerEntry `json:"pointers"`
}

// pointerState is the Server's pointer repo bookkeeping, guarded by mu.
type pointerState struct {
	mu        sync.Mutex
	cfg       PointerConfig
	base      string // what a relative cfg.Path resolves against
	cloning   bool
	cloneErr  string
	lastPull  time.Time
	pullErr   string
	pushErr   string
	opRunning bool // a clone, pull or pointer write is running; serializes git work on the repo
}

// githubToken returns the PAT used for pointer repo git operations, read from
// the same env var dungeon-keeper uses for writespaces (TF_VAR_github_pat),
// falling back to the usual gh/Actions names.
func githubToken() string {
	for _, k := range []string{"TF_VAR_github_pat", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// pointerRepoURL is the HTTPS clone URL for an owner/name repo. It carries no
// credentials: the PAT is supplied per command by gitAuthEnv, so it is never
// written into the clone's .git/config.
func pointerRepoURL(repo string) string {
	return "https://github.com/" + repo + ".git"
}

// gitAuthEnv returns the environment for a git command against GitHub over
// HTTPS: the PAT as an Authorization header via GIT_CONFIG_* env (git >=
// 2.31), which keeps it out of both argv and .git/config. Without a token it
// is just the plain environment, which still works for public repos.
func gitAuthEnv() []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	tok := githubToken()
	if tok == "" {
		return env
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	return append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+basic,
	)
}

// normalizeGitHubRemote reduces a GitHub remote URL (https, with or without
// embedded credentials, or ssh) to owner/name, or "" if it isn't one.
func normalizeGitHubRemote(url string) string {
	u := strings.TrimSpace(url)
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	switch {
	case strings.HasPrefix(u, "git@github.com:"):
		u = strings.TrimPrefix(u, "git@github.com:")
	case strings.HasPrefix(u, "ssh://git@github.com/"):
		u = strings.TrimPrefix(u, "ssh://git@github.com/")
	case strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://"):
		u = u[strings.Index(u, "://")+3:]
		if at := strings.LastIndex(u, "@"); at >= 0 {
			u = u[at+1:]
		}
		if !strings.HasPrefix(u, "github.com/") {
			return ""
		}
		u = strings.TrimPrefix(u, "github.com/")
	default:
		return ""
	}
	if !pointerRepoPattern.MatchString(u) {
		return ""
	}
	return u
}

// validatePointerConfig trims cfg and checks it is well-formed.
func validatePointerConfig(cfg PointerConfig) (PointerConfig, error) {
	cfg.Repo = strings.TrimSpace(cfg.Repo)
	cfg.Branch = strings.TrimSpace(cfg.Branch)
	cfg.Path = strings.TrimSpace(cfg.Path)
	if n := normalizeGitHubRemote(cfg.Repo); n != "" {
		cfg.Repo = n // accept a pasted URL as well as owner/name
	}
	if !pointerRepoPattern.MatchString(cfg.Repo) {
		return cfg, fmt.Errorf("pointer repo %q: expected owner/name", cfg.Repo)
	}
	if cfg.Branch == "" {
		return cfg, fmt.Errorf("pointer branch is empty")
	}
	if out, err := exec.Command("git", "check-ref-format", "--branch", cfg.Branch).CombinedOutput(); err != nil {
		return cfg, fmt.Errorf("pointer branch %q: %s", cfg.Branch, strings.TrimSpace(string(out)))
	}
	if cfg.Path == "" {
		return cfg, fmt.Errorf("pointer path is empty")
	}
	return cfg, nil
}

// resolvePointerPath makes path absolute: ~ expands to the home dir and a
// relative path resolves against base (the git repo condocv2 was started
// in, so the default ../sloppo is a sibling checkout in the same workspace).
func resolvePointerPath(base, path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}

// initPointer seeds the pointer config at startup. base is what a relative
// path resolves against; it stays fixed for the life of the process.
func (s *Server) initPointer(cfg PointerConfig, base string) {
	s.pointer.mu.Lock()
	s.pointer.cfg = cfg
	s.pointer.base = base
	s.pointer.mu.Unlock()
}

// pointerStatus inspects the configured pointer repo on disk and lists its
// pointers.
func (s *Server) pointerStatus() PointerStatus {
	st := s.pointerRepoStatus()
	st.Holder = s.slopHolder()
	st.DefaultAgent, st.DefaultModel = s.slop.agent, s.slop.model
	if st.State == "ready" {
		for _, e := range readPointers(st.ResolvedPath) {
			st.Pointers = append(st.Pointers, s.decoratePointerEntry(e))
		}
	}
	return st
}

// pointerRepoStatus is pointerStatus without the pointer listing -- the
// cheap check slopspace.go makes before every pointer read or write.
func (s *Server) pointerRepoStatus() PointerStatus {
	p := &s.pointer
	p.mu.Lock()
	cfg, base := p.cfg, p.base
	cloning, cloneErr := p.cloning, p.cloneErr
	lastPull, pullErr, pushErr := p.lastPull, p.pullErr, p.pushErr
	p.mu.Unlock()

	st := PointerStatus{
		PointerConfig:  cfg,
		ResolvedPath:   resolvePointerPath(base, cfg.Path),
		TokenAvailable: githubToken() != "",
		PullError:      pullErr,
		PushError:      pushErr,
		Pointers:       []PointerEntry{},
	}
	if !lastPull.IsZero() {
		st.LastPull = lastPull.Format(time.RFC3339)
	}
	if cloning {
		st.State = "cloning"
		st.Message = fmt.Sprintf("cloning %s into %s…", cfg.Repo, st.ResolvedPath)
		return st
	}

	fi, err := os.Stat(st.ResolvedPath)
	if os.IsNotExist(err) {
		st.State = "missing"
		st.Message = cloneErr
		return st
	}
	if err != nil {
		st.State = "invalid"
		st.Message = err.Error()
		return st
	}
	if !fi.IsDir() {
		st.State = "invalid"
		st.Message = st.ResolvedPath + " is not a directory"
		return st
	}
	if _, err := os.Stat(filepath.Join(st.ResolvedPath, ".git")); err != nil {
		st.State = "invalid"
		st.Message = st.ResolvedPath + " exists but is not a git repo"
		return st
	}

	remote, err := exec.Command("git", "-C", st.ResolvedPath, "remote", "get-url", "origin").Output()
	if err != nil {
		st.State = "invalid"
		st.Message = "no origin remote"
		return st
	}
	if got := normalizeGitHubRemote(string(remote)); !strings.EqualFold(got, cfg.Repo) {
		st.State = "invalid"
		if got == "" {
			got = strings.TrimSpace(string(remote))
		}
		st.Message = fmt.Sprintf("origin is %s, expected %s", got, cfg.Repo)
		return st
	}
	head, err := exec.Command("git", "-C", st.ResolvedPath, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		st.State = "invalid"
		st.Message = "cannot read HEAD"
		return st
	}
	if h := strings.TrimSpace(string(head)); h != cfg.Branch {
		st.State = "invalid"
		st.Message = fmt.Sprintf("checked out on %s, expected %s", h, cfg.Branch)
		return st
	}

	st.State = "ready"
	return st
}

// readPointers lists and summarizes the v2 pointers among pointers/*.yaml
// (or .yml). Everything else there -- condoccer's v1 pointers, hand-written
// files -- is filtered out: v1 and v2 condocs are incompatible.
func readPointers(repoRoot string) []PointerEntry {
	var files []string
	for _, pat := range []string{"*.yaml", "*.yml"} {
		m, _ := filepath.Glob(filepath.Join(repoRoot, pointerDir, pat))
		files = append(files, m...)
	}
	sort.Strings(files)
	entries := []PointerEntry{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		spec, err := parsePointerSpec(string(b))
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(repoRoot, f)
		e := entryFromSpec(spec, filepath.ToSlash(rel))
		if e.Name == "" {
			e.Name = e.Stem
		}
		entries = append(entries, e)
	}
	return entries
}

// setPointer changes the pointer repo config ("Set Pointer Repo") and, if the
// new one is already checked out, pulls it.
func (s *Server) setPointer(cfg PointerConfig) error {
	cfg, err := validatePointerConfig(cfg)
	if err != nil {
		return err
	}
	s.pointer.mu.Lock()
	changed := s.pointer.cfg != cfg
	if changed {
		s.pointer.cfg = cfg
		s.pointer.cloneErr = ""
		s.pointer.pullErr = ""
		s.pointer.pushErr = ""
		s.pointer.lastPull = time.Time{}
	}
	s.pointer.mu.Unlock()
	if !changed {
		return nil
	}
	log.Printf("pointer repo set to %s@%s at %s", cfg.Repo, cfg.Branch, cfg.Path)
	s.broadcastPointer()
	go s.pullPointer()
	return nil
}

// beginPointerOp claims the pointer repo for one git operation, so a clone
// and the periodic pull never run over each other.
func (s *Server) beginPointerOp() (PointerConfig, string, bool) {
	s.pointer.mu.Lock()
	defer s.pointer.mu.Unlock()
	if s.pointer.opRunning {
		return PointerConfig{}, "", false
	}
	s.pointer.opRunning = true
	return s.pointer.cfg, resolvePointerPath(s.pointer.base, s.pointer.cfg.Path), true
}

func (s *Server) endPointerOp() {
	s.pointer.mu.Lock()
	s.pointer.opRunning = false
	s.pointer.mu.Unlock()
}

// clonePointer clones the configured pointer repo over HTTPS (PAT auth, see
// gitAuthEnv) into its resolved path, checked out on the target branch. Only
// valid while the path is missing.
func (s *Server) clonePointer() error {
	if st := s.pointerRepoStatus(); st.State != "missing" {
		return fmt.Errorf("pointer repo is %s, not missing -- nothing to clone", st.State)
	}
	cfg, path, ok := s.beginPointerOp()
	if !ok {
		return fmt.Errorf("a pointer repo operation is already running")
	}
	defer s.endPointerOp()

	s.pointer.mu.Lock()
	s.pointer.cloning = true
	s.pointer.cloneErr = ""
	s.pointer.mu.Unlock()
	s.broadcastPointer()

	err := os.MkdirAll(filepath.Dir(path), 0755)
	if err == nil {
		cmd := exec.Command("git", "clone", "--branch", cfg.Branch, pointerRepoURL(cfg.Repo), path)
		cmd.Env = gitAuthEnv()
		if out, cerr := cmd.CombinedOutput(); cerr != nil {
			err = fmt.Errorf("clone %s: %v: %s", cfg.Repo, cerr, strings.TrimSpace(string(out)))
		}
	}

	s.pointer.mu.Lock()
	s.pointer.cloning = false
	if err != nil {
		s.pointer.cloneErr = err.Error()
		if githubToken() == "" {
			s.pointer.cloneErr += " (no GitHub token: set TF_VAR_github_pat)"
		}
	} else {
		s.pointer.lastPull = time.Now()
		s.pointer.pullErr = ""
	}
	s.pointer.mu.Unlock()
	if err != nil {
		log.Printf("pointer repo: %v", err)
	} else {
		log.Printf("pointer repo: cloned %s into %s", cfg.Repo, path)
	}
	s.broadcastPointer()
	return err
}

// pullPointer fast-forwards (rebasing any local commits) the pointer repo
// onto origin's target branch, then pushes any local commits an earlier push
// failed to deliver. Uncommitted turn-stage writes (see writeSpec) are
// stashed around the rebase. A no-op unless the repo is ready.
func (s *Server) pullPointer() {
	if s.pointerRepoStatus().State != "ready" {
		return
	}
	cfg, path, ok := s.beginPointerOp()
	if !ok {
		return
	}
	defer s.endPointerOp()

	cmd := exec.Command("git", "-C", path, "pull", "-q", "--rebase", "--autostash", "origin", cfg.Branch)
	cmd.Env = gitAuthEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		ahead, _ := exec.Command("git", "-C", path, "rev-list", "--count", "origin/"+cfg.Branch+"..HEAD").Output()
		if n := strings.TrimSpace(string(ahead)); n != "" && n != "0" {
			s.pushPointerLocked(cfg, path)
		}
	}

	s.pointer.mu.Lock()
	if s.pointer.cfg != cfg {
		s.pointer.mu.Unlock() // config changed underneath us; this result is stale
		return
	}
	if err != nil {
		s.pointer.pullErr = fmt.Sprintf("pull: %v: %s", err, strings.TrimSpace(string(out)))
	} else {
		s.pointer.pullErr = ""
		s.pointer.lastPull = time.Now()
	}
	s.pointer.mu.Unlock()
	s.broadcastPointer()
}

// pointerLoop keeps the pointer repo pulled and re-broadcasts its status
// when the pointers on disk change (a pull, or a local edit).
func (s *Server) pointerLoop() {
	s.pullPointer()
	pull := time.NewTicker(pointerPullInterval)
	defer pull.Stop()
	scan := time.NewTicker(2 * time.Second)
	defer scan.Stop()
	last := ""
	for {
		select {
		case <-pull.C:
			s.pullPointer()
		case <-scan.C:
			b, _ := json.Marshal(s.pointerStatus())
			if string(b) != last {
				last = string(b)
				s.broadcastPointer()
				s.pushState() // mirror the summary up to local-representative
			}
		}
	}
}

func (s *Server) sendPointer(c *wsClient) {
	s.sendToClient(c, "pointer", s.pointerStatus())
}

func (s *Server) broadcastPointer() {
	msg := s.marshalMsg("pointer", s.pointerStatus())
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}
