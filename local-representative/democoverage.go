package main

import (
	"path"
	"sort"
	"strings"
)

// A demo's declared coverage (PrivateRepoOps Step 5, Revision C): which of
// the project's files a demo exercises, as paths in the project protosurf
// maps (protosurf.go) -- "ianar/vision.go" a file, "session-manager/" a
// whole directory. Selecting a demo or a suite in the demo tab picks them
// out on the map: the focal coverage, what the demo is about, in one
// colour, and the supporting coverage, what it needs on the way, in
// another.
//
// It is declared, not measured. A demo's document says what it is about in
// its leading comment block, in lines the description leaves out:
//
//	#   focal: ianar/vision.go ianar/seqv2_ops.go
//	#   supporting: ianar/lrrun.go, ianar/screencast_linux.go
//
// (either may repeat; paths are split on spaces and commas). To those the
// demo's phases add what they call: a subject phase's documents are focal,
// a lead-in's or tear-down's supporting -- each dtt example
// (dtt/examples/compositions/<doc>) and, for a localrep-demo-<x> one, the control
// sequence it imports (dtt/examples/compositions/resources/demo-<x>.control.yaml). A
// built-in demo's own document, the machinery every demo runs through
// (demoCoverageMachinery) and, with a compose phase, composer are
// supporting. A path that's focal isn't also supporting. A suite's coverage
// is its demos' together.

// DemoCoverage is a demo's or a suite's coverage: project paths, those
// ending "/" directories.
type DemoCoverage struct {
	Focal      []string `json:"focal"`
	Supporting []string `json:"supporting"`
}

// demoCoverageMachinery is what every demo run goes through: the demo tab
// itself, LR's control runs, and dtt's declare tool with its localrep
// provider and its calls.
var demoCoverageMachinery = []string{
	"local-representative/demos.go",
	"local-representative/demoanalysis.go",
	"local-representative/tools.go",
	"local-representative/control.go",
	"local-representative/controlops.go",
	"local-representative/controlnodes.go",
	"dtt/declare.go",
	"dtt/declare_dtt.go",
	"dtt/declare_localrep.go",
	"dtt/calls.go",
}

// demoCoverageCompose is what a compose phase draws on.
const demoCoverageCompose = "composer/"

// demoCoverageLine reads a "#   focal: ..." or "#   supporting: ..." line
// of a demo's leading comment block: which list, and its paths.
func demoCoverageLine(line string) (kind string, paths []string, ok bool) {
	text := strings.TrimSpace(strings.TrimPrefix(line, "#"))
	key, rest, found := strings.Cut(text, ":")
	if !found || (key != "focal" && key != "supporting") || !strings.HasPrefix(line, "#   ") {
		return "", nil, false
	}
	for _, p := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' }) {
		if p = cleanCoveragePath(p); p != "" {
			paths = append(paths, p)
		}
	}
	return key, paths, true
}

// cleanCoveragePath makes p a project path: slash-separated, relative,
// without "." or ".." -- "" if it can't be one. A trailing "/" (a
// directory) is kept.
func cleanCoveragePath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if strings.HasPrefix(p, "/") {
		return ""
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return ""
		}
	}
	dir := strings.HasSuffix(p, "/")
	p = path.Clean("/" + p)[1:]
	if p == "" {
		return ""
	}
	if dir {
		p += "/"
	}
	return p
}

// demoCallPaths are the project's files behind a called dtt example: the
// document, and the control sequence a localrep-demo-<x> one imports.
func demoCallPaths(doc string) []string {
	doc = path.Base(strings.ReplaceAll(doc, "\\", "/"))
	out := []string{"dtt/examples/compositions/" + doc}
	if x, ok := strings.CutPrefix(strings.TrimSuffix(doc, ".dtt.yaml"), "localrep-demo-"); ok && x != "" {
		out = append(out, "dtt/examples/compositions/resources/demo-"+x+".control.yaml")
	}
	return out
}

// demoCoverage is d's coverage: what its document declares (focal,
// supporting) and what its phases add.
func demoCoverage(d DemoInfo, focal, supporting []string) *DemoCoverage {
	f := append([]string(nil), focal...)
	s := append([]string(nil), supporting...)
	for _, p := range d.Phases {
		for _, c := range p.Calls {
			if p.Role == "subject" {
				f = append(f, demoCallPaths(c)...)
			} else {
				s = append(s, demoCallPaths(c)...)
			}
		}
		if p.Role == "compose" {
			s = append(s, demoCoverageCompose)
		}
	}
	if d.Origin == demoOriginEmbedded {
		s = append(s, "local-representative/"+builtinDemosDir+"/"+d.Name+demoSuffix)
	}
	s = append(s, demoCoverageMachinery...)
	return tidyCoverage(f, s)
}

// suiteCoverage is its demos' coverage together: a path focal in any of
// them is the suite's focal.
func suiteCoverage(demos []DemoInfo) *DemoCoverage {
	var f, s []string
	for _, d := range demos {
		if d.Coverage == nil {
			continue
		}
		f = append(f, d.Coverage.Focal...)
		s = append(s, d.Coverage.Supporting...)
	}
	return tidyCoverage(f, s)
}

// tidyCoverage sorts and dedupes both lists and drops from supporting what
// focal covers already.
func tidyCoverage(focal, supporting []string) *DemoCoverage {
	f := uniqueSorted(focal)
	var s []string
	for _, p := range uniqueSorted(supporting) {
		if !coverageCovers(f, p) {
			s = append(s, p)
		}
	}
	if s == nil {
		s = []string{}
	}
	return &DemoCoverage{Focal: f, Supporting: s}
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range in {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// coverageCovers says whether file or directory p is within one of paths:
// the same file, or under a directory ("ianar/" covers "ianar/robot.go"
// and "ianar/frontend/").
func coverageCovers(paths []string, p string) bool {
	for _, c := range paths {
		if c == p || strings.HasSuffix(c, "/") && strings.HasPrefix(p, c) {
			return true
		}
	}
	return false
}
