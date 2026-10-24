package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This file is the timestamp and filesystem providers (see declare.go).

// ---- timestamp ----

// timestampProvider is a moment, measured once per statement:
//
//	timestamp:
//	  select: now        (or an RFC 3339 time)
//
// Its state is unix, iso, human-readable and date, for ${{timestamp.unix}}
// and the like. It can't be asserted.
type timestampProvider struct{}

func (timestampProvider) checkSelect(st *statement) error {
	if len(st.path) != 1 {
		return fmt.Errorf("a timestamp's path is just timestamp")
	}
	if st.sel != nil && st.sel.kind != yScalar {
		return fmt.Errorf("a timestamp's select is now or an RFC 3339 time")
	}
	return nil
}

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
		if s != "" && s != "now" {
			if t, err = time.Parse(time.RFC3339, s); err != nil {
				return nil, fmt.Errorf("select %q should be now or an RFC 3339 time", s)
			}
			id += "[" + s + "]"
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
//	  read: *                 (a directory's item names; a file's size)
//	  assert:
//	    items:
//	      my.json:            (a path under the directory)
//	        type: json        (json or text; json if content is a mapping or list)
//	        content: ...
//	        exists: true      (the default; false removes it)
//
// An item's fields are exists and content. A json item's content is
// compared as JSON, so spacing and key order don't count.
type fsProvider struct{}

type fsItemSpec struct {
	name   string
	typ    string
	fields []fieldSpec
	line   int
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

func parseFSAssert(body *yNode) ([]fsItemSpec, error) {
	if body.kind != yMap {
		return nil, fmt.Errorf("line %d: a filesystem assert is a mapping with items under it", body.line)
	}
	if err := onlyKeys(body, "a filesystem assert", "items"); err != nil {
		return nil, err
	}
	items := body.get("items")
	if items == nil || items.kind != yMap {
		return nil, fmt.Errorf("line %d: items should be a mapping of path: {type, content, exists}", body.line)
	}
	var out []fsItemSpec
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
		all, err := assertFields(n, "item "+name, "type", "content", "exists")
		if err != nil {
			return nil, err
		}
		for _, f := range all {
			if f.name != "type" {
				spec.fields = append(spec.fields, f)
			}
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
		out = append(out, spec)
	}
	return out, nil
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
		if st.reads("items") && info.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				return nil, err
			}
			names := []any{}
			for _, en := range entries {
				names = append(names, en.Name())
			}
			state["items"] = names
		}
		if st.reads("size") && !info.IsDir() {
			state["size"] = info.Size()
		}
	}
	return []*dobj{e.addObject("filesystem.items["+p+"]", st.pathString(), st.line, state, p)}, nil
}

func (fsProvider) assert(ctx context.Context, e *engine, o *dobj, body *yNode) error {
	specs, err := parseFSAssert(body)
	if err != nil {
		return err
	}
	dir := o.handle.(string)
	if o.img.State["kind"] == "file" {
		return fmt.Errorf("%s is a file; items go under a directory", dir)
	}
	for _, spec := range specs {
		if err := assertFSItem(e, dir, spec); err != nil {
			return err
		}
	}
	return nil
}

func assertFSItem(e *engine, dir string, spec fsItemSpec) error {
	p := filepath.Join(dir, spec.name)
	exists, have, err := readFSItem(p, spec.typ)
	if err != nil {
		return err
	}
	state := map[string]any{"abspath": p, "exists": exists, "type": spec.typ}
	if exists {
		state["content"] = have
	}
	o := e.addObject("filesystem.items["+p+"]", "filesystem.items", spec.line, state, nil)

	wantExists, existsFrom, explicit, line := true, []any(nil), false, spec.line
	var content *fieldSpec
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
		}
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
	var want any
	if content != nil {
		if want, err = e.value(content.want); err != nil {
			return err
		}
		from, err := e.values(content.from)
		if err != nil {
			return err
		}
		if e.decide(o, []string{"content"}, content.line, want, have, from) {
			write = true
		} else {
			want = have
		}
	}
	if !write {
		return nil
	}
	if err := writeFSItem(p, spec.typ, want); err != nil {
		return err
	}
	_, now, err := readFSItem(p, spec.typ)
	if err != nil {
		return err
	}
	o.img.State = map[string]any{"abspath": p, "exists": true, "type": spec.typ, "content": now}
	return nil
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
