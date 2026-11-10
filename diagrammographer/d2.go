package main

import (
	"fmt"
	"strconv"
	"strings"
)

// D2 export (https://d2lang.com). D2 lays a diagram out itself, so node
// positions and the canvas size don't carry over; nodes, labels, shapes,
// colors and edges do. A dynamic diagram's steps become D2 scenarios, each
// layered on the base diagram alone (unlike D2 steps, which build on the
// previous one), so a step only has to say what it highlights.

// highlightColor is the stroke a step's highlighted nodes and edges get --
// the canvas's highlight color too (see frontend/src/Diagram.tsx).
const highlightColor = "#e0a060"

var d2Shapes = map[string]string{"rect": "rectangle", "round": "rectangle", "circle": "circle", "cylinder": "cylinder"}

// D2 renders d as a D2 document.
func (d *Diagram) D2() string {
	var b strings.Builder
	line := func(indent int, s string) {
		b.WriteString(strings.Repeat("  ", indent) + s + "\n")
	}

	line(0, "# "+d.Title+" -- exported by diagrammographer")
	if d.Description != "" {
		line(0, "# "+d.Description)
	}
	line(0, "")

	for _, n := range d.Nodes {
		line(0, n.ID+": "+strconv.Quote(n.Label)+" {")
		line(1, "shape: "+d2Shapes[n.Shape])
		if n.Shape == "round" {
			line(1, "style.border-radius: 8")
		}
		if n.Color != "" {
			line(1, "style.fill: "+strconv.Quote(n.Color))
		}
		line(0, "}")
	}
	if len(d.Edges) > 0 {
		line(0, "")
	}
	for _, e := range d.Edges {
		s := e.From + " -> " + e.To
		if e.Label != "" {
			s += ": " + strconv.Quote(e.Label)
		}
		line(0, s)
	}

	if len(d.Steps) == 0 {
		return b.String()
	}

	// D2 names the i'th edge between the same two nodes (a -> b)[i]; a
	// highlighted from->to means every one of them.
	edgeRefs := map[string][]string{}
	for _, e := range d.Edges {
		id := e.ID()
		edgeRefs[id] = append(edgeRefs[id], fmt.Sprintf("(%s -> %s)[%d]", e.From, e.To, len(edgeRefs[id])))
	}

	line(0, "")
	line(0, "scenarios: {")
	for i, s := range d.Steps {
		line(1, fmt.Sprintf("%d: {", i+1))
		for _, h := range s.Highlight {
			refs, isEdge := edgeRefs[h]
			if !isEdge {
				refs = []string{h}
			}
			for _, ref := range refs {
				line(2, ref+".style.stroke: "+strconv.Quote(highlightColor))
				line(2, ref+".style.stroke-width: 4")
			}
		}
		if s.Caption != "" {
			line(2, "dg_caption: "+strconv.Quote(s.Caption)+" {")
			line(3, "shape: text")
			line(3, "near: bottom-center")
			line(2, "}")
		}
		line(1, "}")
	}
	line(0, "}")
	return b.String()
}
