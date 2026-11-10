package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// This file is the timestamp and filesystem providers (see declare.go).

// ---- timestamp ----

// timestampProvider is a moment, measured once per statement:
//
//	timestamp:
//	  select: now        (or an RFC 3339 time, or unix seconds)
//
// Its state is unix, iso, human-readable and date, for ${{timestamp.unix}}
// and the like. It can't be asserted. Deleting with an image, it is the
// moment that image measured. Unix seconds let a document calling another
// (dtt.calls) hand it its own moment: ${{the-time.unix}} into a variable
// the called document selects its timestamp by.
type timestampProvider struct{}

func (timestampProvider) checkSelect(st *statement) error {
	if len(st.path) != 1 {
		return fmt.Errorf("a timestamp's path is just timestamp")
	}
	if st.sel != nil && st.sel.kind != yScalar {
		return fmt.Errorf("a timestamp's select is now, an RFC 3339 time or unix seconds")
	}
	return nil
}

var unixSecondsRe = regexp.MustCompile(`^[0-9]{1,12}$`)

func (timestampProvider) checkAssert([]string, *yNode) error {
	return fmt.Errorf("a timestamp can only be read")
}

func (timestampProvider) resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	t, id := e.now(), "timestamp"
	if st.sel != nil {
		s, err := e.text(st.sel, "select")
		if err != nil {
			return nil, err
		}
		if s = strings.TrimSpace(s); s != "" && s != "now" {
			if unixSecondsRe.MatchString(s) {
				n, _ := strconv.ParseInt(s, 10, 64)
				t = time.Unix(n, 0)
			} else if t, err = time.Parse(time.RFC3339, s); err != nil {
				return nil, fmt.Errorf("select %q should be now, an RFC 3339 time or unix seconds", s)
			}
			id += "[" + s + "]"
		}
	}
	if p, ok := e.prior[id]; ok && e.deleting() && p.State["iso"] != nil {
		// Deleting with an image: the moment the image saw, so names made
		// from it (scratch-${{timestamp.date}}) are the ones it measured.
		if seen, err := time.Parse(time.RFC3339, fmt.Sprint(p.State["iso"])); err == nil {
			t = seen
		}
	}
	t = t.UTC()
	state := map[string]any{
		"unix":           t.Unix(),
		"iso":            t.Format(time.RFC3339),
		"human-readable": t.Format(time.RFC1123),
		"date":           t.Format("2006-01-02"),
	}
	return []*dobj{e.addObject(id, "timestamp", st.line, state, nil)}, nil
}

func (timestampProvider) assert(context.Context, *engine, *dobj, *yNode) error {
	return fmt.Errorf("a timestamp can only be read")
}

// ---- filesystem ----

// fsProvider is files on this host:
//
//	filesystem.items:
//	  select:
//	    abspath: /tmp/        (a directory, or a file)
//	  assert:
//	    exists: true          (optional: the selected directory itself; true
//	                           makes it, false removes it and all in it)
//	    copy_from: /tmp/a/    (optional: every file directly in this
//	                           directory is copied in, an item each)
//	    items:
//	      my.json:            (a path under the directory)
//	        type: json        (json or text; json if content is a mapping or list)
//	        content: ...
//	        exists: true      (the default; false removes it, as delete mode does)
//	        mode: "0644"      (optional: permission bits, in octal)
//	        owner: agent      (optional: a user's name or id)
//	        group: agent      (optional: a group's name or id)
//
// The selected directory's state is its kind, mode, owner, group and item
// names (a file's: its size instead of items). An item's fields are exists,
// content, mode, owner and group. A json item's content is compared as
// JSON, so spacing and key order don't count. An item this run creates had
// no mode, owner or group before, so asserting them on it always applies.
//
// The directory's exists follows the from rule like any field: making it
// is from empty, so always applies; removing it needs from.exists: true,
// an image that saw it or force. Deleting a document removes a directory
// its assert asserts exists (true or false), after its items. A copied
// file is a text item whose content is the source file's, so asserting
// again changes nothing until the source does (and then only from the
// rule); a source directory that isn't there copies nothing reading or
// deleting, and fails asserting.
type fsProvider struct{}

// fsMetaFields are an item's optional fields beside content.
var fsMetaFields = []string{"mode", "owner", "group"}

type fsItemSpec struct {
	name   string
	typ    string
	fields []fieldSpec
	line   int

	// copied: an item copy_from makes, with source's content.
	copied bool
	source string
}

// fsAssert is a filesystem assert: the selected directory's exists, the
// directory copy_from copies, and the items.
type fsAssert struct {
	exists   *fieldSpec
	copyFrom *yNode
	items    []fsItemSpec
}

func (fsProvider) checkSelect(st *statement) error {
	if len(st.path) != 2 || st.path[1] != "items" {
		return fmt.Errorf("filesystem paths are filesystem.items")
	}
	if st.sel == nil || st.sel.kind != yMap {
		return fmt.Errorf("select a directory or file by its abspath (select: {abspath: /tmp/})")
	}
	if err := onlyKeys(st.sel, "filesystem.items' select", "abspath"); err != nil {
		return err
	}
	p, err := scalar(st.sel, "abspath")
	if err != nil {
		return err
	}
	if p == "" || (!strings.Contains(p, "${{") && !filepath.IsAbs(p)) {
		return fmt.Errorf("select.abspath %q should be an absolute path", p)
	}
	return nil
}

func (fsProvider) checkAssert(_ []string, body *yNode) error {
	_, err := parseFSAssert(body)
	return err
}

func parseFSAssert(body *yNode) (*fsAssert, error) {
	if body.kind != yMap {
		return nil, fmt.Errorf("line %d: a filesystem assert is a mapping with items, exists or copy_from under it", body.line)
	}
	if err := onlyKeys(body, "a filesystem assert", "items", "exists", "from.exists", "copy_from"); err != nil {
		return nil, err
	}
	a := &fsAssert{copyFrom: body.get("copy_from")}
	dir := &yNode{kind: yMap, line: body.line}
	for i, k := range body.keys {
		if k == "exists" || k == "from.exists" {
			dir.keys, dir.vals = append(dir.keys, k), append(dir.vals, body.vals[i])
		}
	}
	if len(dir.keys) > 0 {
		fields, err := assertFields(dir, "a filesystem assert", "exists")
		if err != nil {
			return nil, err
		}
		if len(fields) == 1 {
			a.exists = &fields[0]
			if a.exists.want.kind != yScalar {
				return nil, fmt.Errorf("line %d: exists should be true or false", a.exists.line)
			}
		}
	}
	if c := a.copyFrom; c != nil {
		if c.kind != yScalar || c.str == "" || (!strings.Contains(c.str, "${{") && !filepath.IsAbs(c.str)) {
			return nil, fmt.Errorf("line %d: copy_from should be a directory's absolute path", c.line)
		}
	}
	items := body.get("items")
	if items == nil {
		if a.exists == nil && a.copyFrom == nil {
			return nil, fmt.Errorf("line %d: a filesystem assert needs items, exists or copy_from", body.line)
		}
		return a, nil
	}
	if items.kind != yMap {
		return nil, fmt.Errorf("line %d: items should be a mapping of path: {type, content, exists}", items.line)
	}
	for i, name := range items.keys {
		n := items.vals[i]
		if p := filepath.ToSlash(filepath.Clean(name)); filepath.IsAbs(name) || p == "." || p == ".." || strings.HasPrefix(p, "../") {
			return nil, fmt.Errorf("line %d: item %q should be a relative path inside the selected directory", n.line, name)
		}
		if n.kind != yMap {
			return nil, fmt.Errorf("line %d: item %s should be a mapping (type, content, exists)", n.line, name)
		}
		spec := fsItemSpec{name: name, line: n.line}
		var err error
		if spec.typ, err = scalar(n, "type"); err != nil {
			return nil, err
		}
		if spec.typ != "" && spec.typ != "json" && spec.typ != "text" {
			return nil, fmt.Errorf("line %d: item %s: type %q should be json or text", n.line, name, spec.typ)
		}
		if n.get("from.type") != nil {
			return nil, fmt.Errorf("line %d: item %s: type says how to read content; it takes no from.type", n.line, name)
		}
		all, err := assertFields(n, "item "+name, "type", "content", "exists", "mode", "owner", "group")
		if err != nil {
			return nil, err
		}
		for _, f := range all {
			if f.name == "type" {
				continue
			}
			if contains(fsMetaFields, f.name) {
				if f.want.kind != yScalar {
					return nil, fmt.Errorf("line %d: item %s: %s should be a single value", f.line, name, f.name)
				}
				if f.name == "mode" && !strings.Contains(f.want.str, "${{") {
					if _, err := parseMode(f.want.str); err != nil {
						return nil, fmt.Errorf("line %d: item %s: %v", f.line, name, err)
					}
				}
			}
			spec.fields = append(spec.fields, f)
		}
		for _, f := range spec.fields {
			if f.name == "content" && f.want.kind != yScalar {
				if spec.typ == "text" {
					return nil, fmt.Errorf("line %d: item %s is text, so its content is a single value (a | block for lines)", f.line, name)
				}
				spec.typ = "json"
			}
		}
		if spec.typ == "" {
			spec.typ = "text"
		}
		a.items = append(a.items, spec)
	}
	return a, nil
}

func (fsProvider) resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	p, err := e.text(st.sel.get("abspath"), "select.abspath")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(p) {
		return nil, fmt.Errorf("select.abspath %q isn't absolute", p)
	}
	p = filepath.Clean(p)
	state, err := fsDirState(p)
	if err != nil {
		return nil, err
	}
	return []*dobj{e.addObject("filesystem.items["+p+"]", st.pathString(), st.line, state, p)}, nil
}

// fsDirState measures the selected directory (or file) p.
func fsDirState(p string) (map[string]any, error) {
	state := map[string]any{"abspath": p, "exists": false, "kind": "missing"}
	info, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		state["exists"] = true
		state["kind"] = "file"
		if info.IsDir() {
			state["kind"] = "dir"
		}
		for k, v := range fsMeta(info) {
			state[k] = v
		}
		if info.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				return nil, err
			}
			names := []any{}
			for _, en := range entries {
				names = append(names, en.Name())
			}
			state["items"] = names
		} else {
			state["size"] = info.Size()
		}
	}
	return state, nil
}

func (fsProvider) assert(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	a, err := parseFSAssert(body)
	if err != nil {
		return err
	}
	dir := o.handle.(string)
	if o.img.State["kind"] == "file" {
		return fmt.Errorf("%s is a file; items go under a directory", dir)
	}

	// The directory itself: made before its items, removed after them.
	wantDir, removeDir := true, false
	if a.exists != nil {
		s, err := e.text(a.exists.want, "exists")
		if err != nil {
			return err
		}
		if wantDir, err = parseBool(s); err != nil {
			return fmt.Errorf("line %d: exists: %v", a.exists.line, err)
		}
		if e.deleting() {
			wantDir = false
		}
		if wantDir {
			if e.decide(o, []string{"exists"}, a.exists.line, true, o.img.State["exists"] == true, nil) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
			}
		}
	}

	specs := a.items
	if a.copyFrom != nil {
		copies, err := e.fsCopies(a.copyFrom)
		if err != nil {
			return err
		}
		specs = append(copies, specs...)
	}
	for _, spec := range specs {
		if err := assertFSItem(e, dir, spec); err != nil {
			return err
		}
	}

	if a.exists != nil && !wantDir {
		from, err := e.values(a.exists.from)
		if err != nil {
			return err
		}
		have, err := fsDirState(dir)
		if err != nil {
			return err
		}
		removeDir = e.decide(o, []string{"exists"}, a.exists.line, false, have["exists"] == true, from)
	}
	if removeDir {
		if err := removeFSDir(dir); err != nil {
			return err
		}
	}
	if e.changes() {
		now, err := fsDirState(dir)
		if err != nil {
			return err
		}
		o.img.State = now
	}
	return nil
}

// fsCopies are the items copy_from (n) makes: a text item for each file
// directly in the directory it names, with that file's content. None for
// a directory that isn't there, except asserting.
func (e *engine) fsCopies(n *yNode) ([]fsItemSpec, error) {
	src, err := e.text(n, "copy_from")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(src) {
		return nil, fmt.Errorf("line %d: copy_from %q isn't absolute", n.line, src)
	}
	src = filepath.Clean(src)
	entries, err := os.ReadDir(src)
	if errors.Is(err, fs.ErrNotExist) && !(e.changes() && !e.deleting()) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("line %d: copy_from: %v", n.line, err)
	}
	var out []fsItemSpec
	for _, en := range entries {
		if !en.Type().IsRegular() {
			continue
		}
		out = append(out, fsItemSpec{name: en.Name(), typ: "text", line: n.line, copied: true, source: filepath.Join(src, en.Name())})
	}
	return out, nil
}

// removeFSDir removes directory p and everything in it. It won't remove
// the root or a directory directly under it (/tmp, /home), which nothing
// a document makes should be.
func removeFSDir(p string) error {
	p = filepath.Clean(p)
	if strings.Count(strings.Trim(p, "/"), "/") < 1 {
		return fmt.Errorf("%s: dtt removes no directory as near the root as that", p)
	}
	return os.RemoveAll(p)
}

// fsItemState measures item p: whether it exists, and if so its content
// (read as typ) and fsMeta.
func fsItemState(p, typ string) (map[string]any, error) {
	exists, content, err := readFSItem(p, typ)
	if err != nil {
		return nil, err
	}
	state := map[string]any{"abspath": p, "exists": exists, "type": typ}
	if !exists {
		return state, nil
	}
	state["content"] = content
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	for k, v := range fsMeta(info) {
		state[k] = v
	}
	return state, nil
}

func assertFSItem(e *engine, dir string, spec fsItemSpec) error {
	p := filepath.Join(dir, spec.name)
	state, err := fsItemState(p, spec.typ)
	if err != nil {
		return err
	}
	exists := state["exists"] == true
	o := e.addObject("filesystem.items["+p+"]", "filesystem.items", spec.line, state, nil)

	wantExists, existsFrom, explicit, line := true, []any(nil), false, spec.line
	var content *fieldSpec
	var meta []fieldSpec
	for i := range spec.fields {
		f := &spec.fields[i]
		switch f.name {
		case "exists":
			s, err := e.text(f.want, "exists")
			if err != nil {
				return err
			}
			if wantExists, err = parseBool(s); err != nil {
				return fmt.Errorf("line %d: exists: %v", f.line, err)
			}
			if existsFrom, err = e.values(f.from); err != nil {
				return err
			}
			explicit, line = true, f.line
		case "content":
			content = f
		default:
			meta = append(meta, *f)
		}
	}
	if e.deleting() {
		// Delete mode: the item goes, whatever else is asserted on it. An item
		// already gone is unchanged.
		wantExists, explicit = false, true
	}
	changeExists := false
	if explicit || !exists {
		changeExists = e.decide(o, []string{"exists"}, line, wantExists, exists, existsFrom)
	}
	if !wantExists {
		if changeExists {
			if err := os.Remove(p); err != nil {
				return err
			}
			o.img.State = map[string]any{"abspath": p, "exists": false, "type": spec.typ}
		}
		return nil
	}

	write := !exists && changeExists
	have := state["content"]
	var want any
	if content != nil || spec.copied {
		var from []any
		contentLine := spec.line
		if spec.copied {
			data, err := os.ReadFile(spec.source)
			if err != nil {
				return fmt.Errorf("line %d: copy_from: %v", spec.line, err)
			}
			want = string(data)
		} else {
			if want, err = e.value(content.want); err != nil {
				return err
			}
			if from, err = e.values(content.from); err != nil {
				return err
			}
			contentLine = content.line
		}
		if e.decide(o, []string{"content"}, contentLine, want, have, from) {
			write = true
		} else {
			want = have
		}
	}
	if write {
		if err := writeFSItem(p, spec.typ, want); err != nil {
			return err
		}
	}

	// mode, owner and group: an item that didn't exist had none.
	changed := write
	for _, f := range meta {
		s, err := e.text(f.want, f.name)
		if err != nil {
			return err
		}
		from, err := e.values(f.from)
		if err != nil {
			return err
		}
		if f.name == "mode" {
			if s, err = parseMode(s); err != nil {
				return fmt.Errorf("line %d: %v", f.line, err)
			}
			for i, v := range from {
				if m, err := parseMode(fmt.Sprint(v)); err == nil {
					from[i] = m
				}
			}
		}
		var was any
		if exists {
			was = state[f.name]
		}
		if !e.decide(o, []string{f.name}, f.line, s, was, from) {
			continue
		}
		if !exists && !write {
			continue // not there to change
		}
		if err := setFSMeta(p, f.name, s); err != nil {
			return fmt.Errorf("line %d: %s: %v", f.line, f.name, err)
		}
		changed = true
	}
	if !changed {
		return nil
	}
	now, err := fsItemState(p, spec.typ)
	if err != nil {
		return err
	}
	o.img.State = now
	return nil
}

// fsMeta is a file's mode (its permission bits, in octal), owner and group
// (names where this host knows them, ids otherwise).
func fsMeta(info fs.FileInfo) map[string]any {
	m := map[string]any{"mode": fmt.Sprintf("%04o", info.Mode().Perm())}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		uid, gid := strconv.FormatUint(uint64(st.Uid), 10), strconv.FormatUint(uint64(st.Gid), 10)
		m["owner"], m["group"] = uid, gid
		if u, err := user.LookupId(uid); err == nil {
			m["owner"] = u.Username
		}
		if g, err := user.LookupGroupId(gid); err == nil {
			m["group"] = g.Name
		}
	}
	return m
}

// parseMode reads permission bits written in octal ("0644", "644", "0o644")
// as fsMeta writes them.
func parseMode(s string) (string, error) {
	t := strings.TrimPrefix(strings.TrimSpace(s), "0o")
	n, err := strconv.ParseUint(t, 8, 32)
	if err != nil || n > 0o777 {
		return "", fmt.Errorf("mode %q should be permission bits in octal, 0644 say", s)
	}
	return fmt.Sprintf("%04o", n), nil
}

// setFSMeta sets p's mode (as parseMode gives it), owner or group (a name,
// or a numeric id).
func setFSMeta(p, field, v string) error {
	switch field {
	case "mode":
		n, _ := strconv.ParseUint(v, 8, 32)
		return os.Chmod(p, os.FileMode(n))
	case "owner":
		uid := v
		if _, err := strconv.Atoi(v); err != nil {
			u, err := user.Lookup(v)
			if err != nil {
				return err
			}
			uid = u.Uid
		}
		n, _ := strconv.Atoi(uid)
		return os.Lchown(p, n, -1)
	case "group":
		gid := v
		if _, err := strconv.Atoi(v); err != nil {
			g, err := user.LookupGroup(v)
			if err != nil {
				return err
			}
			gid = g.Gid
		}
		n, _ := strconv.Atoi(gid)
		return os.Lchown(p, -1, n)
	}
	return fmt.Errorf("no field %s", field)
}

// readFSItem reads p's content: parsed for json (as text if it isn't JSON,
// so it compares unequal), as it is for text.
func readFSItem(p, typ string) (bool, any, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if typ == "json" {
		var v any
		if json.Unmarshal(data, &v) == nil {
			return true, v, nil
		}
	}
	return true, string(data), nil
}

// writeFSItem replaces p (through a temporary file beside it, so a reader
// never sees half of it), making its directory if need be.
func writeFSItem(p, typ string, content any) error {
	var data []byte
	switch typ {
	case "json":
		b, err := json.MarshalIndent(content, "", "  ")
		if err != nil {
			return err
		}
		data = append(b, '\n')
	default:
		s, _ := content.(string)
		data = []byte(s)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".dtt-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	mode := os.FileMode(0o644)
	if info, err := os.Stat(p); err == nil {
		mode = info.Mode().Perm()
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
