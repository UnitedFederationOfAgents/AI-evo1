package main

import (
	"os"
	"path/filepath"
	"testing"
)

const testControlSeqYAML = `format: lr-control-v1
actions:
  - id: make-tag
    name: Make a tag
    do:
      - {op: random, save_as: tag}
sequences:
  - id: hello
    name: Hello
    steps:
      - action: make-tag
`

const testRobotSeqYAML = `format: ianar-sequence-v2
actions:
  - id: write-file
    name: Write a file
    controls:
      - name: path
        default: "{{home}}/x-{{timestamp}}.txt"
    do:
      - {op: create-file, path: "{{path}}", content: hi}
      - {op: print, text: "{{path|base}}"}
sequences:
  - id: roundtrip
    name: Round trip
    steps:
      - action: write-file
`

func TestClassifySequenceYAML(t *testing.T) {
	robotOps := robotControlOps([]ControlOpSpec{
		{Op: "create-file", Args: []ControlOpArg{{Name: "path", Required: true}, {Name: "content"}, {Name: "overwrite"}}},
		{Op: "print", Args: []ControlOpArg{{Name: "text", Required: true}, {Name: "label"}}},
	})
	actions, sequences := exampleControlLibrary()
	examples := encodeControlLib(controlLibDoc{Actions: actions, Sequences: sequences})

	for _, tc := range []struct {
		name, src string
		ops       []ControlOpSpec
		want      string
	}{
		{"control", testControlSeqYAML, nil, seqFileControl},
		{"control examples", examples, nil, seqFileControl},
		{"control without format", testControlSeqYAML[len("format: lr-control-v1\n"):], nil, seqFileControl},
		{"robot, ops unreported", testRobotSeqYAML, nil, seqFileRobot},
		{"robot, ops known", testRobotSeqYAML, robotOps, seqFileRobot},
		{"robot without format", testRobotSeqYAML[len("format: ianar-sequence-v2\n"):], nil, seqFileRobot},
		{"robot, unknown op", testRobotSeqYAML, robotOps[:1], ""},
		{"actions only", "format: lr-control-v1\nactions:\n  - id: a\n    name: A\n    do:\n      - {op: random, save_as: x}\n", nil, ""},
		{"step without its action", "format: ianar-sequence-v2\nsequences:\n  - id: s\n    name: S\n    steps:\n      - action: missing\n", nil, ""},
		{"other format", "format: something-else\nsequences: []\n", nil, ""},
		{"plain yaml", "name: not a sequence\nvalue: 3\n", nil, ""},
		{"not yaml", "{{{", nil, ""},
	} {
		if got := classifySequenceYAML(tc.src, tc.ops); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestScanCacheDirMarksSequences(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("aaaaaaaa_ctl.yaml", testControlSeqYAML)
	write("bbbbbbbb_robot.yml", testRobotSeqYAML)
	write("cccccccc_robot.txt", testRobotSeqYAML) // not .yaml: not checked
	write("dddddddd_plain.yaml", "a: b\n")

	s := &Server{fileCacheDir: dir}
	want := map[string]string{
		"aaaaaaaa_ctl.yaml":   seqFileControl,
		"bbbbbbbb_robot.yml":  seqFileRobot,
		"cccccccc_robot.txt":  "",
		"dddddddd_plain.yaml": "",
	}
	files := s.scanCacheDir()
	if len(files) != len(want) {
		t.Fatalf("got %d files, want %d", len(files), len(want))
	}
	for _, f := range files {
		if f.Sequence != want[f.ID] {
			t.Errorf("%s: Sequence %q, want %q", f.ID, f.Sequence, want[f.ID])
		}
		if f.Kind != "text" {
			t.Errorf("%s: Kind %q, want text", f.ID, f.Kind)
		}
	}
}
