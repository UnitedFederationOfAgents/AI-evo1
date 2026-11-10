package main

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// The diagrams the sidebar starts with: every examples/*.dtt.yaml, embedded.
// Edits made in the browser stay in the browser; nothing is saved back.

//go:embed examples/*.dtt.yaml
var embeddedExamples embed.FS

// example is one embedded diagram: its document as written, and parsed.
type example struct {
	source  string
	diagram *Diagram
}

// loadExamples parses every embedded example, in file-name order. A bad one
// is an error: the examples ship with the binary and are tested.
func loadExamples() ([]example, error) {
	names, err := fs.Glob(embeddedExamples, "examples/*.dtt.yaml")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []example
	for _, name := range names {
		b, err := embeddedExamples.ReadFile(name)
		if err != nil {
			return nil, err
		}
		d, err := parseDiagram(string(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		if want := strings.TrimSuffix(strings.TrimPrefix(name, "examples/"), ".dtt.yaml"); d.Name != want {
			return nil, fmt.Errorf("%s: select.name is %q, but the file is named for %q", name, d.Name, want)
		}
		out = append(out, example{source: string(b), diagram: d})
	}
	return out, nil
}
