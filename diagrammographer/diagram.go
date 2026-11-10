package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A diagram is written as a DTT document (see dtt/README.md): one statement
// whose path is diagrammographer.diagrams, selecting the diagram by name and
// asserting its contents. dtt has no diagrammographer tool yet, so nothing
// runs these documents; they are the diagram's source, read and written
// here. For example:
//
//	# A comment block, kept with the diagram.
//
//	diagrammographer.diagrams:
//	  select:
//	    name: hello
//	  assert:
//	    title: Hello
//	    size: [480, 200]
//	    nodes:
//	      a:
//	        label: A
//	        at: [40, 70]
//	      b:
//	        label: B
//	        shape: round
//	        color: "#2d4a6b"
//	        at: [300, 70]
//	    edges:
//	      - from: a
//	        to: b
//	        label: says hello
//	    steps:
//	      - caption: A says hello to B.
//	        highlight: [a, a->b]
//
// A diagram with steps is dynamic: each step highlights some nodes and
// edges (an edge is named "from->to") and dims the rest. One without is
// static.

// docPath is the statement path every diagram document uses.
const docPath = "diagrammographer.diagrams"

const (
	defaultNodeW = 140
	defaultNodeH = 56
	sizeMargin   = 40 // around the nodes, when size is left out
)

// shapes are the node shapes the canvas draws and D2 export maps.
var shapes = map[string]bool{"rect": true, "round": true, "circle": true, "cylinder": true}

var (
	nameRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	nodeRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	colorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// Diagram is one diagram, as the frontend and the exports see it.
type Diagram struct {
	Name        string `json:"name"`
	Comment     string `json:"comment,omitempty"` // the document's leading # comment block, without the #s
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Nodes       []Node `json:"nodes"`
	Edges       []Edge `json:"edges"`
	Steps       []Step `json:"steps"`
}

// Node is a box on the canvas. X, Y is its top-left corner.
type Node struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Shape string `json:"shape"`
	Color string `json:"color,omitempty"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
}

// Edge is an arrow from one node to another.
type Edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

// ID is the name a step highlights the edge by.
func (e Edge) ID() string { return e.From + "->" + e.To }

// Step is one frame of a dynamic diagram.
type Step struct {
	Caption   string   `json:"caption,omitempty"`
	Highlight []string `json:"highlight"`
}

// ---- Reading ----

// parseDiagram reads a diagram's DTT document.
func parseDiagram(src string) (*Diagram, error) {
	root, err := parseYAML(src)
	if err != nil {
		return nil, err
	}
	if root.kind != yMap {
		return nil, fmt.Errorf("the document should be a mapping with one %s statement", docPath)
	}
	if len(root.keys) != 1 || root.keys[0] != docPath {
		return nil, fmt.Errorf("the document should have one statement, %s (it has %s)", docPath, strings.Join(root.keys, ", "))
	}
	stmt := root.vals[0]
	if err := onlyKeys(stmt, docPath, "select", "assert"); err != nil {
		return nil, err
	}

	d := &Diagram{Comment: leadingComment(src)}
	sel := stmt.get("select")
	if sel == nil {
		return nil, fmt.Errorf("%s: select.name is required", docPath)
	}
	if err := onlyKeys(sel, "select", "name"); err != nil {
		return nil, err
	}
	if d.Name, err = scalar(sel.get("name"), "select.name"); err != nil {
		return nil, err
	}

	a := stmt.get("assert")
	if a == nil {
		return nil, fmt.Errorf("%s: assert is required", docPath)
	}
	if err := onlyKeys(a, "assert", "title", "description", "size", "nodes", "edges", "steps"); err != nil {
		return nil, err
	}
	if d.Title, err = scalar(a.get("title"), "title"); err != nil {
		return nil, err
	}
	if d.Description, err = scalar(a.get("description"), "description"); err != nil {
		return nil, err
	}
	if n := a.get("size"); n != nil {
		if d.Width, d.Height, err = intPair(n, "size"); err != nil {
			return nil, err
		}
	}

	if nodes := a.get("nodes"); nodes != nil {
		if nodes.kind != yMap {
			return nil, fmt.Errorf("line %d: nodes should be a mapping of node id to node", nodes.line)
		}
		for i, id := range nodes.keys {
			nd, err := parseNode(id, nodes.vals[i])
			if err != nil {
				return nil, err
			}
			d.Nodes = append(d.Nodes, nd)
		}
	}

	if edges := a.get("edges"); edges != nil {
		if edges.kind != ySeq {
			return nil, fmt.Errorf("line %d: edges should be a list", edges.line)
		}
		for _, item := range edges.items {
			if err := onlyKeys(item, "edge", "from", "to", "label"); err != nil {
				return nil, err
			}
			var e Edge
			if e.From, err = scalar(item.get("from"), "edge from"); err != nil {
				return nil, err
			}
			if e.To, err = scalar(item.get("to"), "edge to"); err != nil {
				return nil, err
			}
			if e.Label, err = scalar(item.get("label"), "edge label"); err != nil {
				return nil, err
			}
			d.Edges = append(d.Edges, e)
		}
	}

	if steps := a.get("steps"); steps != nil {
		if steps.kind != ySeq {
			return nil, fmt.Errorf("line %d: steps should be a list", steps.line)
		}
		for _, item := range steps.items {
			if err := onlyKeys(item, "step", "caption", "highlight"); err != nil {
				return nil, err
			}
			var s Step
			if s.Caption, err = scalar(item.get("caption"), "step caption"); err != nil {
				return nil, err
			}
			if h := item.get("highlight"); h != nil {
				if h.kind != ySeq {
					return nil, fmt.Errorf("line %d: highlight should be a list of node ids and from->to edges", h.line)
				}
				for _, x := range h.items {
					v, err := scalar(x, "highlight")
					if err != nil {
						return nil, err
					}
					s.Highlight = append(s.Highlight, v)
				}
			}
			d.Steps = append(d.Steps, s)
		}
	}

	d.normalize()
	if err := d.validate(); err != nil {
		return nil, err
	}
	return d, nil
}

func parseNode(id string, n *yNode) (Node, error) {
	nd := Node{ID: id}
	if n.kind != yMap {
		return nd, fmt.Errorf("line %d: node %s should be a mapping", n.line, id)
	}
	what := "node " + id
	if err := onlyKeys(n, what, "label", "shape", "color", "at", "size"); err != nil {
		return nd, err
	}
	var err error
	if nd.Label, err = scalar(n.get("label"), what+" label"); err != nil {
		return nd, err
	}
	if nd.Shape, err = scalar(n.get("shape"), what+" shape"); err != nil {
		return nd, err
	}
	if nd.Color, err = scalar(n.get("color"), what+" color"); err != nil {
		return nd, err
	}
	at := n.get("at")
	if at == nil {
		return nd, fmt.Errorf("line %d: %s: at: [x, y] is required", n.line, what)
	}
	if nd.X, nd.Y, err = intPair(at, what+" at"); err != nil {
		return nd, err
	}
	if sz := n.get("size"); sz != nil {
		if nd.W, nd.H, err = intPair(sz, what+" size"); err != nil {
			return nd, err
		}
	}
	return nd, nil
}

// onlyKeys checks n is a mapping holding none but the given keys.
func onlyKeys(n *yNode, what string, keys ...string) error {
	if n.kind != yMap {
		return fmt.Errorf("line %d: %s should be a mapping", n.line, what)
	}
	for _, k := range n.keys {
		ok := false
		for _, want := range keys {
			ok = ok || k == want
		}
		if !ok {
			return fmt.Errorf("line %d: %s: unknown key %q (expected one of %s)", n.line, what, k, strings.Join(keys, ", "))
		}
	}
	return nil
}

// scalar returns n's string value; "" when n is nil.
func scalar(n *yNode, what string) (string, error) {
	if n == nil {
		return "", nil
	}
	if n.kind != yScalar {
		return "", fmt.Errorf("line %d: %s should be a single value, not %s", n.line, what, n.kind)
	}
	return n.str, nil
}

// intPair reads [a, b].
func intPair(n *yNode, what string) (int, int, error) {
	if n.kind != ySeq || len(n.items) != 2 || n.items[0].kind != yScalar || n.items[1].kind != yScalar {
		return 0, 0, fmt.Errorf("line %d: %s should be [a, b]", n.line, what)
	}
	a, err1 := strconv.Atoi(n.items[0].str)
	b, err2 := strconv.Atoi(n.items[1].str)
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("line %d: %s should be two whole numbers", n.line, what)
	}
	return a, b, nil
}

// leadingComment returns the # lines a document starts with, without the #s.
func leadingComment(src string) string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		if t == "" && len(out) == 0 {
			continue
		}
		if !strings.HasPrefix(t, "#") {
			break
		}
		t = strings.TrimPrefix(t, "#")
		out = append(out, strings.TrimPrefix(t, " "))
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// ---- Defaults and checks ----

// normalize fills in what a document may leave out: a node's label (its
// id), shape (rect) and size, and the diagram's size (the nodes' extent
// plus a margin). It also makes the lists non-nil, so they reach the
// frontend as [] rather than null.
func (d *Diagram) normalize() {
	if d.Title == "" {
		d.Title = d.Name
	}
	maxX, maxY := 0, 0
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.Label == "" {
			n.Label = n.ID
		}
		if n.Shape == "" {
			n.Shape = "rect"
		}
		if n.W <= 0 {
			n.W = defaultNodeW
		}
		if n.H <= 0 {
			n.H = defaultNodeH
		}
		maxX, maxY = max(maxX, n.X+n.W), max(maxY, n.Y+n.H)
	}
	if d.Width <= 0 {
		d.Width = max(maxX+sizeMargin, 200)
	}
	if d.Height <= 0 {
		d.Height = max(maxY+sizeMargin, 120)
	}
	if d.Nodes == nil {
		d.Nodes = []Node{}
	}
	if d.Edges == nil {
		d.Edges = []Edge{}
	}
	if d.Steps == nil {
		d.Steps = []Step{}
	}
	for i := range d.Steps {
		if d.Steps[i].Highlight == nil {
			d.Steps[i].Highlight = []string{}
		}
	}
}

// validate checks names, shapes and colors, and that every edge and
// highlight refers to something in the diagram.
func (d *Diagram) validate() error {
	if !nameRE.MatchString(d.Name) {
		return fmt.Errorf("name %q: use lower-case letters, digits and dashes", d.Name)
	}
	ids := map[string]bool{}
	for _, n := range d.Nodes {
		if !nodeRE.MatchString(n.ID) {
			return fmt.Errorf("node id %q: use letters, digits, _ and -, starting with a letter or _", n.ID)
		}
		if strings.HasPrefix(n.ID, "dg_") {
			return fmt.Errorf("node id %q: ids starting dg_ are kept for the D2 export", n.ID)
		}
		if ids[n.ID] {
			return fmt.Errorf("node %s appears twice", n.ID)
		}
		ids[n.ID] = true
		if !shapes[n.Shape] {
			return fmt.Errorf("node %s: shape %q isn't one of rect, round, circle, cylinder", n.ID, n.Shape)
		}
		if n.Color != "" && !colorRE.MatchString(n.Color) {
			return fmt.Errorf("node %s: color %q should be #rrggbb", n.ID, n.Color)
		}
	}
	edges := map[string]bool{}
	for _, e := range d.Edges {
		if !ids[e.From] || !ids[e.To] {
			return fmt.Errorf("edge %s: both ends must be nodes", e.ID())
		}
		edges[e.ID()] = true
	}
	for i, s := range d.Steps {
		for _, h := range s.Highlight {
			if !ids[h] && !edges[h] {
				return fmt.Errorf("step %d: highlight %q is neither a node nor a from->to edge", i+1, h)
			}
		}
	}
	return nil
}

// ---- Writing ----

// DTT writes d as its DTT document: the comment block, then the statement,
// leaving out what normalize would fill back in.
func (d *Diagram) DTT() string {
	w := &yamlWriter{}
	if d.Comment != "" {
		for _, l := range strings.Split(d.Comment, "\n") {
			w.line(0, strings.TrimRight("# "+l, " "))
		}
		w.line(0, "")
	}
	w.line(0, docPath+":")
	w.line(2, "select:")
	w.field(4, "name", d.Name)
	w.line(2, "assert:")
	w.field(4, "title", d.Title)
	w.field(4, "description", d.Description)
	w.line(4, fmt.Sprintf("size: [%d, %d]", d.Width, d.Height))
	if len(d.Nodes) > 0 {
		w.line(4, "nodes:")
		for _, n := range d.Nodes {
			w.line(6, yamlScalar(n.ID)+":")
			if n.Label != n.ID {
				w.field(8, "label", n.Label)
			}
			if n.Shape != "rect" {
				w.field(8, "shape", n.Shape)
			}
			w.field(8, "color", n.Color)
			w.line(8, fmt.Sprintf("at: [%d, %d]", n.X, n.Y))
			if n.W != defaultNodeW || n.H != defaultNodeH {
				w.line(8, fmt.Sprintf("size: [%d, %d]", n.W, n.H))
			}
		}
	}
	if len(d.Edges) > 0 {
		w.line(4, "edges:")
		for _, e := range d.Edges {
			w.line(6, "- from: "+yamlScalar(e.From))
			w.field(8, "to", e.To)
			w.field(8, "label", e.Label)
		}
	}
	if len(d.Steps) > 0 {
		w.line(4, "steps:")
		for _, s := range d.Steps {
			hl := make([]string, len(s.Highlight))
			for i, h := range s.Highlight {
				hl[i] = flowScalar(h)
			}
			if s.Caption != "" {
				w.line(6, "- caption: "+yamlScalar(s.Caption))
				w.line(8, "highlight: ["+strings.Join(hl, ", ")+"]")
			} else {
				w.line(6, "- highlight: ["+strings.Join(hl, ", ")+"]")
			}
		}
	}
	return w.b.String()
}

// flowScalar renders a highlight inside [...]: plain for a node id or a
// from->to edge (both read back unchanged there), quoted otherwise.
func flowScalar(s string) string {
	if nodeRE.MatchString(s) {
		return s
	}
	if from, to, ok := strings.Cut(s, "->"); ok && nodeRE.MatchString(from) && nodeRE.MatchString(to) {
		return s
	}
	return yamlScalar(s)
}
