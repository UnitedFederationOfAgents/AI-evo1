package main

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

// This file is "dtt examples": the examples in examples/, embedded in this
// binary so callers (local-representative's tools tab) get the ones that
// match the built-in tools they run. They come in two sets.
//
// examples/compositions/ holds tool call definitions on their own -- what
// the tools tab's examples sub-tab imports into its runner, and what a
// document may call or a specification include. A composition is one of two
// kinds:
//
//   - a DTT document, <name>.dtt.yaml, run by the built-in declarative tool
//     of the provider it uses (its document input);
//   - a tool call, <tool>-<rest>.call.yaml: a flat mapping of a built-in
//     tool's inputs (input name: value, a | block for text), exactly what
//     "dtt run <tool> --input <file>" reads. Its tool is the file name's
//     first '-'-separated part.
//
// Either way the leading comment block describes it: the first line is its
// title, the lines indented under "#   " are commands to run it, and the
// rest is its description.
//
// examples/compositions/resources/ holds files compositions name rather than
// hold (a control sequence a localrep document imports, say). They're
// embedded too, so an example imported into a runner as text still finds
// them (see readDocFile), but they aren't examples themselves.
//
// examples/specifications/ holds tool call specifications: each directory is
// an in-place call (calls.go) -- its .dtttc, its document (described by its
// leading comments, as a composition is), and a .gitignore keeping the
// images its executions leave there out of git. A .dtttc may include
// compositions (include:), so its document can call one though it isn't in
// the directory. "dtt examples --specifications" unpacks them under
// examples-dir (~/.ufa/dtt/examples/specifications/<name>), so the tools
// tab can call them by their directory like any other in-place call.

//go:embed examples/compositions/*.dtt.yaml examples/compositions/*.call.yaml examples/compositions/resources
//go:embed examples/specifications/*/.dtttc examples/specifications/*/.gitignore examples/specifications/*/*.dtt.yaml
var examplesFS embed.FS

const (
	examplesRoot       = "examples"
	compositionsRoot   = "examples/compositions"
	specificationsRoot = "examples/specifications"
	exampleSuffix      = ".dtt.yaml"
	callSuffix         = ".call.yaml"
	exampleCmdHead     = "#   "
)

// Example is one entry of "dtt examples --json".
type Example struct {
	Name        string            `json:"name"`        // the file name without .dtt.yaml or .call.yaml
	Kind        string            `json:"kind"`        // "document" or "call"
	Title       string            `json:"title"`       // the first comment line
	Description string            `json:"description"` // the rest of the leading comments, commands left out
	Tool        string            `json:"tool"`        // the built-in tool that runs it, or "" for none
	Providers   []string          `json:"providers"`   // a document's providers, timestamp included; empty for a call
	Inputs      map[string]string `json:"inputs,omitempty"`
	Commands    []string          `json:"commands"` // the commands its comments give, e.g. "dtt read examples/x.dtt.yaml"
	Path        string            `json:"path"`     // examples/compositions/<file>, relative to dtt's source
	Source      string            `json:"source"`   // the file, comments included
	Error       string            `json:"error,omitempty"`
}

// readExamples reads every embedded composition, by name. One that doesn't
// parse is still listed, with Error.
func readExamples() ([]Example, error) {
	entries, err := fs.ReadDir(examplesFS, compositionsRoot)
	if err != nil {
		return nil, err
	}
	var out []Example
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		parse := parseExample
		name, isDoc := strings.CutSuffix(e.Name(), exampleSuffix)
		if !isDoc {
			var isCall bool
			if name, isCall = strings.CutSuffix(e.Name(), callSuffix); !isCall {
				continue
			}
			parse = parseCallExample
		}
		data, err := examplesFS.ReadFile(path.Join(compositionsRoot, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, parse(name, string(data)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// parseCallExample describes tool-call example name (<tool>-<rest>) from its
// source: its comments as parseExample reads them, its inputs checked
// against the tool's schema.
func parseCallExample(name, src string) Example {
	ex := Example{Name: name, Kind: "call", Path: path.Join(compositionsRoot, name+callSuffix), Source: src,
		Providers: []string{}, Commands: []string{}}
	readExampleComments(&ex, src)
	tool, _, _ := strings.Cut(name, "-")
	t, err := readBuiltin(tool)
	if err != nil {
		ex.Error = fmt.Sprintf("a tool-call example is named <tool>-<rest>, and %q isn't a built-in tool", tool)
		return ex
	}
	ex.Tool = tool
	inputs, err := readInputFile("-", strings.NewReader(src))
	if err != nil {
		ex.Error = err.Error()
		return ex
	}
	if _, _, err := t.checkInputs(inputs); err != nil {
		ex.Error = err.Error()
		return ex
	}
	ex.Inputs = inputs
	return ex
}

// parseExample describes document example name from its source, as the
// file comment above says. Its tool is the one non-timestamp provider its
// statements use; a document using several (or none) has no single tool,
// and runs through dtt read/assert instead.
func parseExample(name, src string) Example {
	ex := Example{Name: name, Kind: "document", Path: path.Join(compositionsRoot, name+exampleSuffix), Source: src,
		Providers: []string{}, Commands: []string{}}
	readExampleComments(&ex, src)

	doc, err := parseDocument(src)
	if err != nil {
		ex.Error = err.Error()
		return ex
	}
	seen := map[string]bool{}
	for _, st := range doc {
		p := st.path[0]
		if _, ok := dttProviders[p]; !ok || seen[p] {
			continue // an alias, or a provider already counted
		}
		seen[p] = true
		ex.Providers = append(ex.Providers, p)
	}
	sort.Strings(ex.Providers)
	var tools []string
	for _, p := range ex.Providers {
		if p == "timestamp" {
			continue
		}
		if _, err := readBuiltin(p); err == nil {
			tools = append(tools, p)
		}
	}
	switch {
	case len(tools) == 1:
		ex.Tool = tools[0]
	case len(tools) > 1:
		ex.Tool = allProvidersTool // several providers: the declare tool reads them all
	}
	return ex
}

// readExampleComments fills ex's title, commands and description from the
// leading comment block of src.
func readExampleComments(ex *Example, src string) {
	var desc []string
	for i, l := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "#") {
			if t == "" && i == 0 {
				continue
			}
			break
		}
		switch {
		case ex.Title == "":
			// "DTT example -- files (see ../README.md ...)." is titled
			// "DTT example -- files".
			title, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(t, "#")), " (see ")
			ex.Title = strings.TrimSuffix(title, ".")
		case strings.HasPrefix(t, exampleCmdHead):
			ex.Commands = append(ex.Commands, strings.TrimSpace(t[len(exampleCmdHead):]))
		default:
			desc = append(desc, strings.TrimSpace(strings.TrimPrefix(t, "#")))
		}
	}
	ex.Description = joinParagraphs(desc)
	if ex.Title == "" {
		ex.Title = ex.Name
	}
}

// joinParagraphs joins comment lines into paragraphs: lines run together,
// and an empty line starts a new paragraph.
func joinParagraphs(lines []string) string {
	var paras []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			paras = append(paras, strings.Join(cur, " "))
			cur = nil
		}
	}
	for _, l := range lines {
		if l == "" {
			flush()
			continue
		}
		cur = append(cur, l)
	}
	flush()
	return strings.Join(paras, "\n\n")
}

// ---- Specifications ----

// SpecExample is one of "dtt examples --specifications --json": an example
// tool call specification, an in-place call unpacked at Dir.
type SpecExample struct {
	Name        string   `json:"name"`        // its directory's name
	Title       string   `json:"title"`       // its document's first comment line
	Description string   `json:"description"` // the rest of its document's leading comments, commands left out
	Commands    []string `json:"commands"`
	Path        string   `json:"path"`     // examples/specifications/<name>, relative to dtt's source
	Dir         string   `json:"dir"`      // where it's unpacked on this host ("" if it couldn't be)
	Document    string   `json:"document"` // its document's file name, in Dir
	Includes    []string `json:"includes"` // the compositions its .dtttc includes
	Source      string   `json:"source"`   // its document
	Error       string   `json:"error,omitempty"`
}

// specFileNames are the files of an example specification, as embedded.
func specFileNames(name string) ([]string, error) {
	entries, err := fs.ReadDir(examplesFS, path.Join(specificationsRoot, name))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// readSpecExamples reads every embedded specification, by name, unpacking
// each under root/specifications/<name> ("" leaves them packed). One that
// doesn't parse, or can't be unpacked, is still listed, with Error.
func readSpecExamples(root string) ([]SpecExample, error) {
	entries, err := fs.ReadDir(examplesFS, specificationsRoot)
	if err != nil {
		return nil, err
	}
	out := []SpecExample{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out = append(out, readSpecExample(e.Name(), root))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func readSpecExample(name, root string) SpecExample {
	ex := SpecExample{Name: name, Path: path.Join(specificationsRoot, name), Commands: []string{}, Includes: []string{}}
	fail := func(err error) SpecExample {
		ex.Error = err.Error()
		if ex.Title == "" {
			ex.Title = name
		}
		return ex
	}
	tc, err := examplesFS.ReadFile(path.Join(ex.Path, dtttcFile))
	if err != nil {
		return fail(err)
	}
	spec, err := parseDtttc(string(tc))
	if err != nil {
		return fail(fmt.Errorf("%s: %v", dtttcFile, err))
	}
	ex.Includes = append(ex.Includes, spec.includes...)
	names, err := specFileNames(name)
	if err != nil {
		return fail(err)
	}
	if ex.Document = spec.document; ex.Document == "" {
		for _, n := range names {
			if strings.HasSuffix(n, docFileSuffix) {
				if ex.Document != "" {
					return fail(fmt.Errorf("it holds several *%s documents -- name one in %s", docFileSuffix, dtttcFile))
				}
				ex.Document = n
			}
		}
	}
	src, err := examplesFS.ReadFile(path.Join(ex.Path, ex.Document))
	if err != nil {
		return fail(fmt.Errorf("its document: %v", err))
	}
	ex.Source = string(src)
	desc := Example{Name: name}
	readExampleComments(&desc, ex.Source)
	ex.Title, ex.Description, ex.Commands = desc.Title, desc.Description, desc.Commands
	if _, err := parseDocument(ex.Source); err != nil {
		return fail(err)
	}
	if root != "" {
		dir := filepath.Join(root, "specifications", name)
		if err := unpackSpec(name, names, dir); err != nil {
			return fail(fmt.Errorf("unpacking it into %s: %v", dir, err))
		}
		ex.Dir = dir
	}
	return ex
}

// unpackSpec writes the embedded files of specification name into dir:
// each one that's missing or differs from dtt's copy (an edit there is
// undone -- copy the directory elsewhere to keep your own). Anything else
// in dir, the images its executions kept included, is left as it is.
func unpackSpec(name string, files []string, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		want, err := examplesFS.ReadFile(path.Join(specificationsRoot, name, f))
		if err != nil {
			return err
		}
		p := filepath.Join(dir, f)
		if have, err := os.ReadFile(p); err == nil && bytes.Equal(have, want) {
			continue
		}
		if err := os.WriteFile(p, want, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (c *cli) examples(cfg config, args []string) int {
	flags := newFlags("examples")
	specs := flags.Bool("specifications", false, "list the example tool call specifications, unpacked under examples-dir")
	pos, err := parseArgs(flags, args)
	if err != nil {
		return c.fail(exitBadInput, err)
	}
	if len(pos) > 1 || (*specs && len(pos) > 0) {
		return c.fail(exitBadInput, fmt.Errorf("usage: dtt examples [<name>] | dtt examples --specifications"))
	}
	if *specs {
		return c.specExamples(cfg)
	}
	all, err := readExamples()
	if err != nil {
		return c.fail(exitToolFailed, err)
	}
	if len(pos) == 1 {
		name := strings.TrimSuffix(strings.TrimSuffix(pos[0], exampleSuffix), callSuffix)
		for _, ex := range all {
			if ex.Name != name {
				continue
			}
			if c.json {
				c.emit(map[string]any{"ok": true, "example": ex})
			} else {
				fmt.Fprint(c.stdout, ex.Source)
			}
			return exitOK
		}
		return c.fail(exitBadInput, fmt.Errorf("no example %q (dtt examples lists them)", pos[0]))
	}
	if c.json {
		if all == nil {
			all = []Example{}
		}
		c.emit(map[string]any{"ok": true, "examples": all})
		return exitOK
	}
	if len(all) == 0 {
		fmt.Fprintln(c.stdout, "no examples")
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	for _, ex := range all {
		tool := ex.Tool
		if tool == "" {
			tool = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", ex.Name, tool, ex.Title)
	}
	tw.Flush()
	return exitOK
}

// specExamples lists the example specifications, unpacked under
// examples-dir.
func (c *cli) specExamples(cfg config) int {
	all, err := readSpecExamples(cfg.examplesDir)
	if err != nil {
		return c.fail(exitToolFailed, err)
	}
	if c.json {
		c.emit(map[string]any{"ok": true, "dir": filepath.Join(cfg.examplesDir, "specifications"), "specifications": all})
		return exitOK
	}
	if len(all) == 0 {
		fmt.Fprintln(c.stdout, "no example specifications")
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	for _, ex := range all {
		where := ex.Dir
		if ex.Error != "" {
			where = "error: " + ex.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", ex.Name, ex.Title, where)
	}
	tw.Flush()
	return exitOK
}
