package main

import (
	"os"
	"path/filepath"
	"testing"
)

const testCondocHeader = "# Foo\n\n<!--\n```condoc-yaml\ncondoc:\n  startTime: 1\n  controlScheme: same-repo\n  branch: condoc/Foo-1/main\n  callerPath: ..\n```\n-->\n"

// TestFindCondocsSkipsNonCondocs checks that only real condoc main files are
// listed: snapshot copies (<file>.snpN.md) and files that only mention
// "condoc-yaml" in prose are skipped.
func TestFindCondocsSkipsNonCondocs(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"Foo.md":                      testCondocHeader,
		"examples/Simple.snp0.md":     testCondocHeader,
		"examples/Simple.snp12.md":    testCondocHeader,
		"notes/Mentions.md":           "We parse the condoc-yaml header here.\n",
		"notes/Fenced.md":             "```condoc-yaml\ncondoc: {}\n```\n",
		"examples/explanation.md":     "Snapshot files are <file>.snpN.md.\n",
		"examples/Simple.snp0.md.bak": testCondocHeader,
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	infos, err := findCondocs(root)
	if err != nil {
		t.Fatalf("findCondocs: %v", err)
	}
	if len(infos) != 1 || infos[0].Path != "Foo.md" {
		t.Fatalf("expected only Foo.md, got %+v", infos)
	}
}
