package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	dkslop "dungeon-keeper/pkg/slopspace"
	dktypes "dungeon-keeper/pkg/types"
	"dungeon-keeper/pkg/worksignal"
)

// This file is the slopspace provider (see declare.go): slopspaces, the
// files in them, and agent work done in them -- through dungeon-keeper's
// slopspaces and work signals on this host, as condocv2 does. Agent work a
// document asks for always runs in a slopspace:
//
//	slopspace.spaces:                 a slopspace, named by key
//	  select:
//	    key: <a name for it>          (dtt keeps it in the slopspace's DTT.json;
//	                                   the slopspace's own id is a uuid)
//	  assert:
//	    exists: true                  (false deletes it; so does delete mode)
//
//	slopspace.files:                  a file in a slopspace's files
//	  select:                         (writespaces/files, which the agent sees
//	    space: <its key, or its id>    and changes)
//	    path: story.md                (a path under files/)
//	  assert:
//	    content: |                    (text)
//	      ...
//	    exists: true                  (false removes it; so does delete mode)
//
//	slopspace.files:                  a directory in a slopspace's files,
//	  select:                         holding a tree's files
//	    space: <its key, or its id>
//	    path: repo                    (a path under files/)
//	  assert:
//	    copy_from: ${{source.tree}}   (a git.trees or git.commits object's
//	                                   tree: declare_git_trees.go)
//
//	slopspace.work.executions:        agent work in a slopspace, named by key,
//	  select:                         done in band: asserting submits it and
//	    space: <its key, or its id>    waits for it to complete
//	    key: <a name for this work>
//	  assert:
//	    prompt: |                     (what the agent is asked to do)
//	      ...
//	    agent: claude                 (optional: the agent, and its model --
//	    model: ""                      blank for the agent's own default)
//	    mode: write                   (optional: prompt, read, write or execute)
//	    timeout: 3600                 (optional: seconds the agent may run)
//	    wait: 1800                    (seconds to wait for it to complete)
//
//	slopspace.work.submissions:       the same work, done out of band:
//	  select:                         asserting submits it and is done --
//	    space: <its key, or its id>    a work completion waits for it
//	    key: <a name for this work>
//	  assert:
//	    prompt: | ...                 (and agent, model, mode, timeout as above)
//	    wait: 600                     (seconds to wait for the agent's slot)
//
//	slopspace.work.completions:       submitted work, by its id, complete
//	  select:
//	    id: ${{submitted.id}}         (a submission's or an execution's id)
//	    file: story.md                (optional: a file under its slopspace's
//	                                   files/ to grab once it completes)
//	  assert:
//	    completed: true               (waits for it to complete; failing is an
//	                                   error)
//	    wait: 1800                    (seconds to wait)
//
// A slopspace is made with dungeon-keeper's own code, its DTT proposals off
// (so it never waits for a review). A file is written where the slopspace's
// files are right now; a slopspace deployed for work isn't written to.
//
// A path that is a directory is measured as one: exists, dir: true,
// file_count and bytes. copy_from's field is tree_hash, the directory's
// files' git tree hash: a directory that holds the tree's files already is
// unchanged; one that doesn't exist is made; one holding other files
// (what an agent left in it, say) is held, by the from rule, and once
// changed is emptied and the tree's files written into it. A tree's files
// never include a .git, so a repository copied in comes without its
// history.
//
// Work is a work signal for the agent-worker worker, of slopspace type, role
// dtt-<key>. To run it in the selected slopspace, dtt deploys that slopspace
// for agent-worker first -- once no other slopspace is deployed there (a
// condoc's, say: dtt waits for it to be returned, as condocv2 waits for
// others) -- and the worker uses whatever slopspace is deployed. The
// slopspace stays deployed until the work completes: the execution, or the
// completion measuring it, returns it, so the agent's changes to its files
// come back with it. Its id is the signal's name, dtt-<key>-<unix>.
//
// Work exists once one with its key has: completed, for an execution;
// been submitted and not failed, for a submission. Asserting it again is
// then unchanged; work that failed is submitted again by the next assert.
// Work is an event, so deleting leaves it (kept). Its state is the newest
// signal with its key: id, status (none, pending, processing, completed or
// failed), its times, the slopspace it ran in, the agent's output and the
// worker's comment. A completion's is the same for its id, plus completed
// (true once it completed) and finished (completed or failed), and the file
// it grabs: file_path, file_exists and file_content (text, up to
// ssMaxContent).
//
// Slopspaces, work signals and the deploy path are where dungeon-keeper
// finds them: $SLOPSPACES_DIR, $WORK_SIGNALS_DIR and $AGENT_SLOPSPACE_ROOT,
// with its defaults (/host-agent-files/slopspaces, /host-agent-files/work,
// /agent).
type slopspaceProvider struct{}

const (
	ssSpaces      = "slopspace.spaces"
	ssFiles       = "slopspace.files"
	ssExecutions  = "slopspace.work.executions"
	ssSubmissions = "slopspace.work.submissions"
	ssCompletions = "slopspace.work.completions"

	// ssMarkerFile is dtt's note in a slopspace it made: its key, and the
	// work it deployed it for.
	ssMarkerFile = "DTT.json"
	ssRolePrefix = "dtt-"
	ssAgent      = "claude"
	ssAgentType  = dktypes.AgentTypeWorker

	ssDefaultWait     = 1800 // seconds an execution or a completion waits
	ssDefaultSlotWait = 600  // seconds a submission waits for the agent's slot
	ssMaxWait         = lrMaxWait
	ssDefaultTimeout  = 3600 // seconds the agent may run
	ssMaxContent      = 256 << 10
	ssMaxOutput       = 32 << 10 // the end of the agent's output kept in the state
)

// ssPoll is how often work is checked on while waiting (a var so tests can
// shorten it).
var ssPoll = 5 * time.Second

var (
	ssKeyRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)
	ssWorkRe  = regexp.MustCompile(`^dtt-[a-z0-9][a-z0-9_-]{0,79}-[0-9]+$`)
	ssModesRe = regexp.MustCompile(`^(prompt|read|write|execute)$`)
)

// ssSelectKeys are each path's select keys, the required ones first.
var ssSelectKeys = map[string]struct{ required, optional []string }{
	ssSpaces:      {[]string{"key"}, nil},
	ssFiles:       {[]string{"space", "path"}, nil},
	ssExecutions:  {[]string{"space", "key"}, nil},
	ssSubmissions: {[]string{"space", "key"}, nil},
	ssCompletions: {[]string{"id"}, []string{"file"}},
}

// ssAssertFields are each path's assert fields.
var ssAssertFields = map[string][]string{
	ssSpaces:      {"exists"},
	ssFiles:       {"content", "copy_from", "exists"},
	ssExecutions:  {"prompt", "agent", "model", "mode", "timeout", "wait", "exists"},
	ssSubmissions: {"prompt", "agent", "model", "mode", "timeout", "wait", "exists"},
	ssCompletions: {"completed", "wait"},
}

func (slopspaceProvider) checkSelect(st *statement) error {
	keys, ok := ssSelectKeys[st.pathString()]
	if !ok {
		return fmt.Errorf("slopspace paths are %s, %s, %s, %s and %s", ssSpaces, ssFiles, ssExecutions, ssSubmissions, ssCompletions)
	}
	if st.sel == nil || st.sel.kind != yMap {
		return fmt.Errorf("select it by %s", strings.Join(keys.required, " and "))
	}
	if err := onlyKeys(st.sel, st.key+"'s select", append(append([]string{}, keys.required...), keys.optional...)...); err != nil {
		return err
	}
	for _, k := range keys.required {
		v, err := scalar(st.sel, k)
		if err != nil {
			return err
		}
		// A completion's id is usually a submission's, which reading leaves
		// empty: it is checked when measured.
		if strings.TrimSpace(v) == "" && k != "id" {
			return fmt.Errorf("select.%s is needed", k)
		}
		if strings.Contains(v, "${{") {
			continue
		}
		if k == "key" && !ssKeyRe.MatchString(v) {
			return fmt.Errorf("select.key %q: keys are lower-case letters, digits, '_' and '-'", v)
		}
		if k == "path" {
			if _, err := ssRelPath(v); err != nil {
				return fmt.Errorf("select.path: %v", err)
			}
		}
	}
	for _, k := range keys.optional {
		if _, err := scalar(st.sel, k); err != nil {
			return err
		}
	}
	return nil
}

// ssRelPath cleans p, a path under a slopspace's files/.
func ssRelPath(p string) (string, error) {
	c := filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
	if p == "" || filepath.IsAbs(p) || c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%q should be a relative path under the slopspace's files/", p)
	}
	return c, nil
}

func (slopspaceProvider) checkAssert(p []string, body *yNode) error {
	where := strings.Join(p, ".")
	fields, err := assertFields(body, where+"'s assert", ssAssertFields[where]...)
	if err != nil {
		return err
	}
	for _, f := range fields {
		if f.want.kind != yScalar {
			return fmt.Errorf("line %d: %s should be a single value (a | block for text)", f.line, f.name)
		}
		if f.name == "copy_from" && contains(body.keys, "content") {
			return fmt.Errorf("line %d: a path is a file (content) or a directory (copy_from), not both", f.line)
		}
		if f.from != nil && f.name != "exists" && f.name != "content" && f.name != "copy_from" {
			return fmt.Errorf("line %d: %s takes no from.%s", f.line, f.name, f.name)
		}
		v := strings.TrimSpace(f.want.str)
		if strings.Contains(v, "${{") {
			continue
		}
		switch f.name {
		case "wait", "timeout":
			if n, err := strconv.Atoi(v); err != nil || n <= 0 {
				return fmt.Errorf("line %d: %s is a number of seconds", f.line, f.name)
			}
		case "mode":
			if !ssModesRe.MatchString(v) {
				return fmt.Errorf("line %d: mode is prompt, read, write or execute", f.line)
			}
		case "exists":
			b, err := parseBool(v)
			if err != nil {
				return fmt.Errorf("line %d: exists is true or false", f.line)
			}
			if !b && (where == ssExecutions || where == ssSubmissions) {
				return fmt.Errorf("line %d: work can only be asserted to exist -- work that has been done can't be undone", f.line)
			}
		case "completed":
			if b, err := parseBool(v); err != nil || !b {
				return fmt.Errorf("line %d: completed can only be asserted true -- it waits for the work", f.line)
			}
		}
	}
	return nil
}

// ---- dungeon-keeper ----

// slopEnv is dungeon-keeper's slopspaces and work signals, as dtt reaches
// them.
type slopEnv struct {
	cfg     *dktypes.Config
	spaces  *dkslop.Manager
	signals *worksignal.Manager
}

func newSlopEnv(cfg *dktypes.Config) *slopEnv {
	cfg.AgentType = ssAgentType
	return &slopEnv{cfg: cfg, spaces: dkslop.NewManager(cfg), signals: worksignal.NewManager(cfg)}
}

// slopConfigFromEnv reads the environment dungeon-keeper does, so dtt and
// the worker agree on where slopspaces, work signals and the deploy path
// are (as condocv2's dungeonKeeperConfig does).
func slopConfigFromEnv() *dktypes.Config {
	cfg := dktypes.DefaultConfig()
	if v := os.Getenv("SLOPSPACES_DIR"); v != "" {
		cfg.SlopspacesDir = v
	}
	if v := os.Getenv("WORK_SIGNALS_DIR"); v != "" {
		cfg.WorkSignalsDir = v
	}
	if v := os.Getenv("AGENT_SLOPSPACE_ROOT"); v != "" {
		cfg.AgentSlopspaceRoot = v
	}
	if v := os.Getenv("AGENT_RECORDS_PATH"); v != "" {
		cfg.AgentRecordsPath = v
	}
	return cfg
}

// ssSleep waits d while work or a slopspace is waited for.
func (e *engine) ssSleep(ctx context.Context, d time.Duration) error {
	if e.sleep == nil {
		return sleepCtx(ctx, d)
	}
	return e.sleep(ctx, d)
}

func (e *engine) slopspaces() *slopEnv {
	if e.slop == nil {
		e.slop = newSlopEnv(slopConfigFromEnv())
	}
	return e.slop
}

// ssMarker is a slopspace's DTT.json.
type ssMarker struct {
	Key    string `json:"key"`
	MadeAt string `json:"made_at,omitempty"`
	// DeployedFor is the work dtt deployed the slopspace for, not yet seen
	// complete; once none is left, dtt returns it.
	DeployedFor []string `json:"deployed_for,omitempty"`
}

func readMarker(root string) *ssMarker {
	data, err := os.ReadFile(filepath.Join(root, ssMarkerFile))
	if err != nil {
		return nil
	}
	var m ssMarker
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return &m
}

func writeMarker(root string, m *ssMarker) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, ssMarkerFile), append(data, '\n'), 0o644)
}

// findSpace finds the slopspace ref names: one dtt made with key ref (the
// newest, if several), or the one whose id is ref. nil when there's none.
func (s *slopEnv) findSpace(ref string) (*dktypes.SlopspaceMetadata, *ssMarker, error) {
	if ref == "" {
		return nil, nil, nil
	}
	all, err := s.spaces.List()
	if err != nil {
		return nil, nil, err
	}
	var found *dktypes.SlopspaceMetadata
	var marker *ssMarker
	for _, m := range all {
		mk := readMarker(m.RootPath)
		if m.ID != ref && (mk == nil || mk.Key != ref) {
			continue
		}
		if found == nil || m.CreatedAt.After(found.CreatedAt) {
			found, marker = m, mk
		}
	}
	if found != nil && marker == nil {
		marker = &ssMarker{}
	}
	return found, marker, nil
}

// ssFilesDir is where a slopspace's files are right now: under its deploy
// path while deployed, else under its root.
func ssFilesDir(m *dktypes.SlopspaceMetadata) string {
	base := m.RootPath
	if m.Deployed && m.DeployPath != "" {
		base = m.DeployPath
	}
	return filepath.Join(base, dkslop.WriteSpacesDir, dkslop.FilesDir)
}

// agentFiles is where the agent sees a deployed slopspace's files.
func (s *slopEnv) agentFiles() string {
	return filepath.Join(s.cfg.DeployPathForAgentType(ssAgentType), dkslop.WriteSpacesDir, dkslop.FilesDir)
}

// ---- Measuring ----

func (slopspaceProvider) resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	sel := map[string]string{}
	for i, k := range st.sel.keys {
		v, err := e.text(st.sel.vals[i], "select."+k)
		if err != nil {
			return nil, err
		}
		sel[k] = strings.TrimSpace(v)
	}
	s := e.slopspaces()
	p := st.pathString()
	switch p {
	case ssSpaces:
		if !ssKeyRe.MatchString(sel["key"]) {
			return nil, fmt.Errorf("select.key %q: keys are lower-case letters, digits, '_' and '-'", sel["key"])
		}
		state, err := s.measureSpace(sel["key"])
		if err != nil {
			return nil, err
		}
		return []*dobj{e.addObject(p+"["+sel["key"]+"]", p, st.line, state, sel["key"])}, nil
	case ssFiles:
		rel, err := ssRelPath(sel["path"])
		if err != nil {
			return nil, fmt.Errorf("select.path: %v", err)
		}
		h := &ssFile{space: sel["space"], path: rel}
		state, err := s.measureFile(h)
		if err != nil {
			return nil, err
		}
		return []*dobj{e.addObject(p+"["+h.space+":"+rel+"]", p, st.line, state, h)}, nil
	case ssExecutions, ssSubmissions:
		if !ssKeyRe.MatchString(sel["key"]) {
			return nil, fmt.Errorf("select.key %q: keys are lower-case letters, digits, '_' and '-'", sel["key"])
		}
		h := &ssWork{space: sel["space"], key: sel["key"], inBand: p == ssExecutions}
		state, err := s.measureWork(h)
		if err != nil {
			return nil, err
		}
		return []*dobj{e.addObject(p+"["+h.key+"]", p, st.line, state, h)}, nil
	default: // ssCompletions
		h := &ssCompletion{id: sel["id"]}
		if f := sel["file"]; f != "" {
			rel, err := ssRelPath(f)
			if err != nil {
				return nil, fmt.Errorf("select.file: %v", err)
			}
			h.file = rel
		}
		if h.id != "" && !ssWorkRe.MatchString(h.id) {
			return nil, fmt.Errorf("select.id %q isn't work dtt submitted (dtt-<key>-<unix>, a submission's or an execution's id)", h.id)
		}
		state, err := s.measureCompletion(h)
		if err != nil {
			return nil, err
		}
		id := h.id
		if id == "" {
			id = fmt.Sprintf("line %d", st.line)
		}
		return []*dobj{e.addObject(p+"["+id+"]", p, st.line, state, h)}, nil
	}
}

func (s *slopEnv) measureSpace(ref string) (map[string]any, error) {
	m, mk, err := s.findSpace(ref)
	if err != nil {
		return nil, err
	}
	return s.spaceState(ref, m, mk), nil
}

func (s *slopEnv) spaceState(ref string, m *dktypes.SlopspaceMetadata, mk *ssMarker) map[string]any {
	state := map[string]any{"key": ref, "exists": m != nil, "agent_files": s.agentFiles()}
	if m == nil {
		return state
	}
	state["id"], state["root"], state["files"] = m.ID, m.RootPath, ssFilesDir(m)
	state["deployed"], state["iteration"] = m.Deployed, m.Iteration
	state["created_at"] = m.CreatedAt.UTC().Format(time.RFC3339)
	if m.Deployed {
		state["deploy_path"], state["deployed_agent_type"] = m.DeployPath, string(m.DeployedAgentType)
	}
	if len(mk.DeployedFor) > 0 {
		state["deployed_for"] = ssAnys(mk.DeployedFor)
	}
	return state
}

func ssAnys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

type ssFile struct{ space, path string }

func (s *slopEnv) measureFile(h *ssFile) (map[string]any, error) {
	state := map[string]any{"space": h.space, "path": h.path, "exists": false}
	m, _, err := s.findSpace(h.space)
	if err != nil {
		return nil, err
	}
	state["space_exists"] = m != nil
	if m == nil {
		return state, nil
	}
	p := filepath.Join(ssFilesDir(m), h.path)
	state["abspath"] = p
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		return state, ssReadDir(state, p)
	}
	if err := ssReadText(state, "", p); err != nil {
		return nil, err
	}
	return state, nil
}

// ssReadDir puts what directory p holds in state: exists, dir, and its
// files' count and bytes.
func ssReadDir(state map[string]any, p string) error {
	count, size := 0, int64(0)
	err := filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		count, size = count+1, size+info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	state["exists"], state["dir"], state["file_count"], state["bytes"] = true, true, count, size
	return nil
}

// ssReadText puts what file p holds in state, its fields named with prefix:
// exists, size, and content when it's text no bigger than ssMaxContent
// (else content_omitted says why).
func ssReadText(state map[string]any, prefix, p string) error {
	info, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		state[prefix+"exists"] = false
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", p)
	}
	state[prefix+"exists"], state[prefix+"size"] = true, info.Size()
	if info.Size() > ssMaxContent {
		state[prefix+"content_omitted"] = fmt.Sprintf("%d bytes, over %d", info.Size(), ssMaxContent)
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if !utf8.Valid(data) {
		state[prefix+"content_omitted"] = "not text"
		return nil
	}
	state[prefix+"content"] = string(data)
	return nil
}

// ssWork is an execution or a submission.
type ssWork struct {
	space, key string
	inBand     bool // an execution: asserting waits for it
}

func (h *ssWork) role() string { return ssRolePrefix + h.key }

// ssSignal is one work signal file.
type ssSignal struct {
	id       string // dtt-<key>-<unix>
	path     string
	complete bool
	at       int64
}

// signalsFor finds the work signals with role, oldest first.
func (s *slopEnv) signalsFor(role string) ([]ssSignal, error) {
	var out []ssSignal
	for _, d := range []struct {
		dir      string
		complete bool
	}{{s.cfg.OngoingWorkDir(), false}, {s.cfg.CompleteWorkDir(), true}} {
		entries, err := os.ReadDir(d.dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, en := range entries {
			name, at, complete, err := worksignal.ParseFilename(en.Name())
			if err != nil || name != role || complete != d.complete {
				continue
			}
			out = append(out, ssSignal{id: fmt.Sprintf("%s-%d", role, at), path: filepath.Join(d.dir, en.Name()), complete: complete, at: at})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out, nil
}

// signalByID finds work id's signal: complete/ first, then ongoing/. nil
// when neither has it.
func (s *slopEnv) signalByID(id string) (*ssSignal, error) {
	name, at, _, err := worksignal.ParseFilename(worksignal.WorkingPrefix + id + worksignal.FileSuffix)
	if err != nil {
		return nil, fmt.Errorf("work id %q: %v", id, err)
	}
	created := time.Unix(at, 0)
	for _, c := range []bool{true, false} {
		dir := s.cfg.OngoingWorkDir()
		if c {
			dir = s.cfg.CompleteWorkDir()
		}
		p := filepath.Join(dir, worksignal.GenerateFilename(name, created, c))
		if ssExists(p) {
			return &ssSignal{id: id, path: p, complete: c, at: at}, nil
		}
	}
	return nil, nil
}

func ssExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// signalState reads sig: its status, times, slopspace, the agent's output
// (its end, up to ssMaxOutput) and the worker's comment.
func (s *slopEnv) signalState(sig *ssSignal) (map[string]any, error) {
	h, events, err := s.signals.Read(sig.path)
	if err != nil {
		// Moved from ongoing/ to complete/ as it was read: read it there.
		if again, aerr := s.signalByID(sig.id); aerr == nil && again != nil && again.path != sig.path {
			return s.signalState(again)
		}
		return nil, err
	}
	state := map[string]any{"id": sig.id, "status": string(h.Status), "signal": sig.path,
		"agent": h.Agent, "model": h.Model, "created_at": h.CreatedAt.UTC().Format(time.RFC3339)}
	if h.StartedAt != nil {
		state["started_at"] = h.StartedAt.UTC().Format(time.RFC3339)
	}
	if h.CompletedAt != nil {
		state["completed_at"] = h.CompletedAt.UTC().Format(time.RFC3339)
	}
	for _, ev := range events {
		if ev.SlopspaceID != "" {
			state["slopspace_id"] = ev.SlopspaceID
		}
		if ev.StatusUpdate == "output" {
			out := ev.Output
			if len(out) > ssMaxOutput {
				out = out[len(out)-ssMaxOutput:]
				state["output_truncated"] = true
			}
			state["output"] = out
		}
		if ev.Comment != "" {
			state["comment"] = ev.Comment
		}
	}
	return state, nil
}

// measureWork: the newest signal with h's key. An execution exists once one
// has completed, a submission once one hasn't failed.
func (s *slopEnv) measureWork(h *ssWork) (map[string]any, error) {
	sigs, err := s.signalsFor(h.role())
	if err != nil {
		return nil, err
	}
	state := map[string]any{"space": h.space, "key": h.key, "role": h.role(), "exists": false, "status": "none", "id": ""}
	exists := false
	for i := range sigs {
		st, err := s.signalState(&sigs[i])
		if err != nil {
			return nil, err
		}
		switch st["status"] {
		case string(dktypes.WorkStatusCompleted):
			exists = true
		case string(dktypes.WorkStatusFailed):
		default:
			exists = exists || !h.inBand
		}
		if i == len(sigs)-1 {
			for k, v := range st {
				state[k] = v
			}
		}
	}
	state["exists"] = exists
	return state, nil
}

type ssCompletion struct{ id, file string }

// measureCompletion: work id's signal as it is (no waiting), and the file
// it grabs from the slopspace it ran in.
func (s *slopEnv) measureCompletion(h *ssCompletion) (map[string]any, error) {
	state := map[string]any{"id": h.id, "exists": false, "status": "none", "completed": false, "finished": false}
	if h.file != "" {
		state["file"] = h.file
	}
	if h.id == "" {
		return state, nil
	}
	sig, err := s.signalByID(h.id)
	if err != nil || sig == nil {
		return state, err
	}
	st, err := s.signalState(sig)
	if err != nil {
		return nil, err
	}
	for k, v := range st {
		state[k] = v
	}
	state["exists"] = true
	state["completed"] = st["status"] == string(dktypes.WorkStatusCompleted)
	state["finished"] = sig.complete
	if h.file == "" {
		return state, nil
	}
	m, err := s.workSpace(h.id, st)
	if err != nil || m == nil {
		return state, err
	}
	state["file_path"] = filepath.Join(ssFilesDir(m), h.file)
	if err := ssReadText(state, "file_", state["file_path"].(string)); err != nil {
		return nil, err
	}
	return state, nil
}

// workSpace is the slopspace work id ran in (the worker notes it), or the
// one dtt deployed for it; nil when neither is known.
func (s *slopEnv) workSpace(id string, st map[string]any) (*dktypes.SlopspaceMetadata, error) {
	if ref, _ := st["slopspace_id"].(string); ref != "" {
		m, err := s.spaces.Get(ref)
		if err != nil {
			return nil, nil // deleted since
		}
		return m, nil
	}
	all, err := s.spaces.List()
	if err != nil {
		return nil, err
	}
	for _, m := range all {
		if mk := readMarker(m.RootPath); mk != nil && contains(mk.DeployedFor, id) {
			return m, nil
		}
	}
	return nil, nil
}

// ---- Asserting ----

func (slopspaceProvider) assert(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	fields, err := assertFields(body, "assert", ssAssertFields[o.img.Path]...)
	if err != nil {
		return err
	}
	f := lrFields{}
	for i := range fields {
		f[fields[i].name] = &fields[i]
	}
	switch o.img.Path {
	case ssSpaces:
		return e.assertSpace(o, f, body.line)
	case ssFiles:
		return e.assertSpaceFile(ctx, o, f, body.line)
	case ssExecutions, ssSubmissions:
		return e.assertWork(ctx, o, f, body.line)
	default:
		return e.assertCompletion(ctx, o, f, body.line)
	}
}

// waitFor is the assert's wait, in seconds (def without one), as a deadline.
func (e *engine) waitFor(f lrFields, def int) (time.Time, error) {
	n := def
	if s, ok, err := f.text(e, "wait"); err != nil {
		return time.Time{}, err
	} else if ok {
		if n, err = strconv.Atoi(strings.TrimSpace(s)); err != nil || n <= 0 {
			return time.Time{}, fmt.Errorf("line %d: wait %q is a number of seconds", f["wait"].line, s)
		}
	}
	if n > ssMaxWait {
		n = ssMaxWait
	}
	return e.now().Add(time.Duration(n) * time.Second), nil
}

func (e *engine) assertSpace(o *dobj, f lrFields, line int) error {
	key, s := o.handle.(string), e.slopspaces()
	want, change, err := e.decideExists(o, f, line)
	if err != nil || !change {
		return err
	}
	m, _, err := s.findSpace(key)
	if err != nil {
		return err
	}
	if want && m == nil {
		made, err := s.spaces.Create()
		if err != nil {
			return err
		}
		// Proposals would hold its next deploy for a review no one is
		// asked for.
		if err := s.spaces.SetDTTRequests(made.ID, false); err != nil {
			return err
		}
		if err := writeMarker(made.RootPath, &ssMarker{Key: key, MadeAt: e.now().UTC().Format(time.RFC3339)}); err != nil {
			return err
		}
		if e.log != nil {
			fmt.Fprintf(e.log, "slopspace %s made (key %s)\n", made.ID, key)
		}
	}
	if !want && m != nil {
		if m.Deployed {
			return fmt.Errorf("line %d: slopspace %s (%s) is deployed for work -- delete it once the work completes and it's returned", line, key, m.ID)
		}
		if err := s.spaces.Delete(m.ID); err != nil {
			return err
		}
	}
	state, err := s.measureSpace(key)
	if err != nil {
		return err
	}
	o.img.State = state
	return nil
}

func (e *engine) assertSpaceFile(ctx context.Context, o *dobj, f lrFields, line int) error {
	h, s := o.handle.(*ssFile), e.slopspaces()
	wantExists, existsFrom, explicit := true, []any(nil), false
	if spec := f["exists"]; spec != nil {
		v, err := e.text(spec.want, "exists")
		if err != nil {
			return err
		}
		if wantExists, err = parseBool(v); err != nil {
			return fmt.Errorf("line %d: exists: %v", spec.line, err)
		}
		if existsFrom, err = e.values(spec.from); err != nil {
			return err
		}
		explicit, line = true, spec.line
	}
	if e.deleting() {
		wantExists, explicit = false, true
	}
	have := o.img.State["exists"] == true
	if o.img.State["space_exists"] != true {
		if !wantExists || !e.changes() {
			// Nothing to remove; reading, the slopspace is made first.
			e.decide(o, []string{"exists"}, line, wantExists, have, existsFrom)
			return nil
		}
		return fmt.Errorf("line %d: slopspace %s doesn't exist -- assert it (slopspace.spaces) first", line, h.space)
	}
	m, _, err := s.findSpace(h.space)
	if err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("line %d: slopspace %s is gone", line, h.space)
	}
	p := filepath.Join(ssFilesDir(m), h.path)
	guard := func() error {
		if m.Deployed {
			return fmt.Errorf("line %d: slopspace %s is deployed for work -- its files are changed once the work completes and it's returned", line, h.space)
		}
		return nil
	}
	changeExists := false
	if explicit || !have {
		changeExists = e.decide(o, []string{"exists"}, line, wantExists, have, existsFrom)
	}
	if !wantExists {
		if changeExists {
			if err := guard(); err != nil {
				return err
			}
			remove := os.Remove
			if o.img.State["dir"] == true {
				remove = os.RemoveAll
			}
			if err := remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	} else {
		write := !have && changeExists
		if spec := f["copy_from"]; spec != nil {
			if err := e.copyIntoSpace(ctx, o, spec, p, write, guard); err != nil {
				return err
			}
		} else if spec := f["content"]; spec != nil {
			want, err := e.text(spec.want, "content")
			if err != nil {
				return err
			}
			from, err := e.values(spec.from)
			if err != nil {
				return err
			}
			var cur any = o.img.State["content"]
			if why, ok := o.img.State["content_omitted"].(string); ok {
				cur = "(" + why + ")" // not read: held, unless from or force
			}
			if e.decide(o, []string{"content"}, spec.line, want, cur, from) {
				write = true
			}
			if write {
				if err := guard(); err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
					return err
				}
			}
		} else if write {
			return fmt.Errorf("line %d: %s isn't in slopspace %s's files -- give its content to make it", line, h.path, h.space)
		}
	}
	if e.changes() {
		state, err := s.measureFile(h)
		if err != nil {
			return err
		}
		o.img.State = state
	}
	return nil
}

// copyIntoSpace asserts copy_from (spec): directory p holds the files of a
// git.trees or git.commits object's tree. write: p is to be made anyway.
func (e *engine) copyIntoSpace(ctx context.Context, o *dobj, spec *fieldSpec, p string, write bool, guard func() error) error {
	src, err := e.text(spec.want, "copy_from")
	if err != nil {
		return err
	}
	src = strings.TrimSpace(src)
	from, err := e.values(spec.from)
	if err != nil {
		return err
	}
	t := e.gitTreeByID(src)
	if t == nil {
		if unresolved(src) && !e.changes() {
			// Reading, with the tree not made yet.
			e.decide(o, []string{"tree_hash"}, spec.line, "(the files of "+src+", once made)", nil, from)
			return nil
		}
		return fmt.Errorf("line %d: copy_from %q isn't a tree measured above -- give a git.trees or git.commits object's tree (${{<its alias>.tree}})", spec.line, src)
	}
	var have any
	switch {
	case o.img.State["dir"] == true:
		if have, err = gitDirTree(ctx, t.cache, p); err != nil {
			return err
		}
	case o.img.State["exists"] == true:
		have = "(a file, not a directory)"
	}
	if e.decide(o, []string{"tree_hash"}, spec.line, t.hash, have, from) {
		write = true
	}
	if !write || !e.changes() {
		return nil
	}
	if err := guard(); err != nil {
		return err
	}
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	return gitCheckoutTree(ctx, t, p)
}

func (e *engine) assertWork(ctx context.Context, o *dobj, f lrFields, line int) error {
	h, s := o.handle.(*ssWork), e.slopspaces()
	if e.deleting() {
		e.keep(o, line, "work has been done; deleting leaves it")
		return nil
	}
	def := ssDefaultWait
	if !h.inBand {
		def = ssDefaultSlotWait
	}
	deadline, err := e.waitFor(f, def)
	if err != nil {
		return err
	}
	// An execution under way (an earlier assert gave up waiting, say): wait
	// for it rather than submitting another.
	if id, _ := o.img.State["id"].(string); h.inBand && e.changes() && id != "" &&
		(o.img.State["status"] == string(dktypes.WorkStatusPending) || o.img.State["status"] == string(dktypes.WorkStatusProcessing)) {
		if err := e.awaitWork(ctx, id, deadline); err != nil {
			return err
		}
		return e.finishExecution(o, h, id)
	}
	_, change, err := e.decideExists(o, f, line)
	if err != nil || !change {
		return err
	}
	prompt, ok, err := f.text(e, "prompt")
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("line %d: give the prompt -- what the agent is asked to do", line)
	}
	sig := &dktypes.WorkSignal{WorkType: dktypes.WorkTypeSlopspace, AgentType: ssAgentType, Role: h.role(),
		Prompt: prompt, Agent: ssAgent, Mode: "write", TimeoutSeconds: ssDefaultTimeout}
	if v, ok, err := f.text(e, "agent"); err != nil {
		return err
	} else if ok && strings.TrimSpace(v) != "" {
		sig.Agent = strings.TrimSpace(v)
	}
	model, _, err := f.text(e, "model")
	if err != nil {
		return err
	}
	sig.Model = strings.TrimSpace(model)
	if v, ok, err := f.text(e, "mode"); err != nil {
		return err
	} else if ok {
		if v = strings.TrimSpace(v); !ssModesRe.MatchString(v) {
			return fmt.Errorf("line %d: mode %q is prompt, read, write or execute", f["mode"].line, v)
		}
		sig.Mode = v
	}
	if v, ok, err := f.text(e, "timeout"); err != nil {
		return err
	} else if ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n <= 0 {
			return fmt.Errorf("line %d: timeout %q is a number of seconds", f["timeout"].line, v)
		}
		sig.TimeoutSeconds = int64(n)
	}
	id, err := s.submit(ctx, e, h.space, sig, deadline)
	if err != nil {
		return fmt.Errorf("line %d: %v", line, err)
	}
	if !h.inBand {
		state, err := s.measureWork(h)
		if err != nil {
			return err
		}
		o.img.State = state
		return nil
	}
	if err := e.awaitWork(ctx, id, deadline); err != nil {
		return err
	}
	return e.finishExecution(o, h, id)
}

// finishExecution, once work id completes: return its slopspace, measure
// the execution again, and fail if the work did.
func (e *engine) finishExecution(o *dobj, h *ssWork, id string) error {
	s := e.slopspaces()
	if err := e.returnFor(id); err != nil {
		return err
	}
	state, err := s.measureWork(h)
	if err != nil {
		return err
	}
	o.img.State = state
	sig, err := s.signalByID(id)
	if err != nil || sig == nil {
		return fmt.Errorf("work %s: its signal is gone", id)
	}
	st, err := s.signalState(sig)
	if err != nil {
		return err
	}
	if st["status"] != string(dktypes.WorkStatusCompleted) {
		return fmt.Errorf("work %s %s: %v", id, st["status"], st["comment"])
	}
	return nil
}

// submit deploys the slopspace ref names for the agent -- once no other
// slopspace is deployed there, waiting until deadline for that -- and
// writes sig, the work, for it. It returns the work's id.
func (s *slopEnv) submit(ctx context.Context, e *engine, ref string, sig *dktypes.WorkSignal, deadline time.Time) (string, error) {
	m, mk, err := s.findSpace(ref)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", fmt.Errorf("slopspace %s doesn't exist -- assert it (slopspace.spaces) first", ref)
	}
	// The signal's name, a second later than any with its role.
	created := e.now()
	for ssExists(filepath.Join(s.cfg.OngoingWorkDir(), worksignal.GenerateFilename(sig.Role, created, false))) ||
		ssExists(filepath.Join(s.cfg.CompleteWorkDir(), worksignal.GenerateFilename(sig.Role, created, true))) {
		created = created.Add(time.Second)
	}
	sig.CreatedAt = created
	id := fmt.Sprintf("%s-%d", sig.Role, created.Unix())

	waiting, deployedNow := false, false
	for {
		deployed, err := s.spaces.GetDeployedID(ssAgentType)
		if err != nil {
			return "", err
		}
		if deployed == m.ID {
			break // deployed already (for work before this, which the worker does first)
		}
		if deployed == "" {
			if err := s.spaces.Deploy(m.ID, ssAgentType); err != nil {
				return "", fmt.Errorf("deploying slopspace %s: %v", ref, err)
			}
			if e.log != nil {
				fmt.Fprintf(e.log, "slopspace %s (%s) deployed for %s\n", ref, m.ID, ssAgentType)
			}
			deployedNow = true
			break
		}
		if !e.now().Before(deadline) {
			return "", fmt.Errorf("slopspace %s is deployed for %s (another's work) and wasn't returned in time -- assert again (with a longer wait) once it is", deployed, ssAgentType)
		}
		if !waiting && e.log != nil {
			fmt.Fprintf(e.log, "waiting for slopspace %s, deployed for %s, to be returned\n", deployed, ssAgentType)
		}
		waiting = true
		if err := e.ssSleep(ctx, ssPoll); err != nil {
			return "", err
		}
	}
	// dtt returns what it deployed: this deploy, or one for earlier work
	// of dtt's still to complete. A slopspace someone else deployed is
	// theirs to return.
	if !deployedNow && len(mk.DeployedFor) == 0 {
		if _, err := s.signals.Create(sig); err != nil {
			return "", err
		}
		if e.log != nil {
			fmt.Fprintf(e.log, "work %s submitted in slopspace %s (deployed already)\n", id, ref)
		}
		return id, nil
	}
	mk.DeployedFor = append(mk.DeployedFor, id)
	if err := writeMarker(m.RootPath, mk); err != nil {
		return "", err
	}
	if _, err := s.signals.Create(sig); err != nil {
		_ = e.returnFor(id)
		return "", err
	}
	if e.log != nil {
		fmt.Fprintf(e.log, "work %s submitted in slopspace %s\n", id, ref)
	}
	return id, nil
}

// awaitWork waits for work id to complete (or fail), until deadline.
func (e *engine) awaitWork(ctx context.Context, id string, deadline time.Time) error {
	s := e.slopspaces()
	logged := false
	for {
		sig, err := s.signalByID(id)
		if err != nil {
			return err
		}
		if sig == nil {
			// The worker writes complete/ before removing ongoing/.
			if sig, err = s.signalByID(id); err != nil || sig == nil {
				return fmt.Errorf("work %s vanished without completing", id)
			}
		}
		if sig.complete {
			return nil
		}
		if !e.now().Before(deadline) {
			return fmt.Errorf("work %s didn't complete in time -- it goes on, its slopspace deployed; assert again (with a longer wait) to wait for it", id)
		}
		if !logged && e.log != nil {
			fmt.Fprintf(e.log, "waiting for work %s to complete\n", id)
			logged = true
		}
		if err := e.ssSleep(ctx, ssPoll); err != nil {
			return err
		}
	}
}

// returnFor notes that work id is complete, and returns its slopspace when
// dtt deployed it and no work dtt deployed it for is still to complete.
func (e *engine) returnFor(id string) error {
	s := e.slopspaces()
	all, err := s.spaces.List()
	if err != nil {
		return err
	}
	for _, m := range all {
		mk := readMarker(m.RootPath)
		if mk == nil || !contains(mk.DeployedFor, id) {
			continue
		}
		var left []string
		for _, w := range mk.DeployedFor {
			if sig, err := s.signalByID(w); err == nil && sig != nil && !sig.complete {
				left = append(left, w)
			}
		}
		mk.DeployedFor = left
		if err := writeMarker(m.RootPath, mk); err != nil {
			return err
		}
		if len(left) > 0 || !m.Deployed {
			return nil
		}
		if err := s.spaces.Return(m.ID); err != nil {
			return fmt.Errorf("returning slopspace %s: %v", m.ID, err)
		}
		if e.log != nil {
			fmt.Fprintf(e.log, "slopspace %s (%s) returned\n", mk.Key, m.ID)
		}
		return nil
	}
	return nil
}

func (e *engine) assertCompletion(ctx context.Context, o *dobj, f lrFields, line int) error {
	h, s := o.handle.(*ssCompletion), e.slopspaces()
	if e.deleting() {
		e.keep(o, line, "work has been done; deleting leaves it")
		return nil
	}
	if spec := f["completed"]; spec != nil {
		line = spec.line
	}
	if h.id == "" {
		if !e.changes() {
			e.decide(o, []string{"completed"}, line, true, false, nil)
			return nil
		}
		return fmt.Errorf("line %d: select.id is empty -- name the work (a submission's id)", line)
	}
	if o.img.State["exists"] != true && e.changes() {
		return fmt.Errorf("line %d: there's no work %s", line, h.id)
	}
	have := o.img.State["completed"] == true
	if !e.decide(o, []string{"completed"}, line, true, have, nil) && !(have && e.changes()) {
		return nil
	}
	deadline, err := e.waitFor(f, ssDefaultWait)
	if err != nil {
		return err
	}
	if err := e.awaitWork(ctx, h.id, deadline); err != nil {
		return err
	}
	if err := e.returnFor(h.id); err != nil {
		return err
	}
	state, err := s.measureCompletion(h)
	if err != nil {
		return err
	}
	o.img.State = state
	if state["completed"] != true {
		return fmt.Errorf("work %s %s: %v", h.id, state["status"], state["comment"])
	}
	if h.file != "" && state["file_exists"] != true {
		return fmt.Errorf("work %s completed, but left no %s in its slopspace's files", h.id, h.file)
	}
	return nil
}
