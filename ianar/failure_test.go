package main

import (
	"image"
	"strings"
	"testing"
)

// stubTesseractVersion keeps tests from running tesseract.
func stubTesseractVersion(t *testing.T) {
	t.Helper()
	orig := tesseractVersion
	t.Cleanup(func() { tesseractVersion = orig })
	tesseractVersion = func() string { return "tesseract 5.3.0 (test)" }
}

// TestFailedRunKeepsDetailedEvidence: the step a run fails at keeps its last
// screen reading, and the run's .zip gains a failure/ folder and a report
// section on it -- including words tesseract read but dropped.
func TestFailedRunKeepsDetailedEvidence(t *testing.T) {
	stubSequence(t)
	stubTesseractVersion(t)
	kept := OCRWord{Text: "hello", Conf: 90, X: 0, Y: 0, W: 40, H: 10}
	dropped := OCRWord{Text: "world!", Conf: 12, X: 45, Y: 0, W: 50, H: 10}
	orig := readScreen
	t.Cleanup(func() { readScreen = orig })
	reads := 0
	readScreen = func() (*screenReading, error) {
		reads++
		words := []OCRWord{kept}
		return &screenReading{
			img: image.NewRGBA(image.Rect(0, 0, 1280, 720)), from: "test",
			words: words, lines: groupLines(words), scale: 2,
			raw: [2][]OCRWord{{kept, dropped}, nil},
		}, nil
	}

	res := runV2(t, "fc-hello-world", nil)
	if res.Success || res.Failure == nil {
		t.Fatalf("expected a failure with evidence, got %+v", res)
	}
	f := res.Failure
	if f.Step != res.FailedStep || f.Want != "hello world!" || f.Reading == nil || f.Reads < 1 {
		t.Errorf("failure = %+v", f)
	}

	data, err := sequenceArtifact(fcHelloWorld(t).def(), res).build()
	if err != nil {
		t.Fatal(err)
	}
	files := unzip(t, data)
	for _, name := range []string{"failure/capture.png", "failure/capture-boxed.png", "failure/closest.png", "failure/lines.txt", "failure/words.tsv"} {
		if len(files[name]) == 0 {
			t.Errorf("missing %s (have %v)", name, keys(files))
		}
	}
	report := string(files["report.txt"])
	for _, want := range []string{
		"failure detail (step",
		"IANAR build:",
		"tesseract 5.3.0 (test)",
		"read at 2x",
		`looking for "hello world!"`,
		`"world": as-is pass read "world!" at (45, 0) 50x10, 12% confident -- dropped: low confidence`,
		`"hello": as-is pass read "hello"`,
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report.txt lacks %q:\n%s", want, report)
		}
	}
	if !strings.Contains(string(files["failure/words.tsv"]), "as-is\t12\tfalse\t45\t0\t50\t10\tworld!") {
		t.Errorf("words.tsv = %s", files["failure/words.tsv"])
	}
}

// TestSucceededRunHasNoFailureDetail: a successful run's .zip is as before.
func TestSucceededRunHasNoFailureDetail(t *testing.T) {
	stubSequence(t)
	stubScreenLines(t, func(n int) []OCRLine {
		lines := []OCRLine{{Text: `$ echo "hello world!"`, X: 0, Y: 0, W: 100, H: 10}}
		for i := 0; i <= min(n, 1); i++ {
			lines = append(lines, OCRLine{Text: "hello world!", X: 0, Y: 20 + 20*i, W: 100, H: 10})
		}
		return lines
	})
	res := runV2(t, "fc-hello-world", nil)
	if !res.Success || res.Failure != nil {
		t.Fatalf("result = %+v", res)
	}
	data, err := sequenceArtifact(fcHelloWorld(t).def(), res).build()
	if err != nil {
		t.Fatal(err)
	}
	files := unzip(t, data)
	for name := range files {
		if strings.HasPrefix(name, "failure/") {
			t.Errorf("a successful run's .zip has %s", name)
		}
	}
	if strings.Contains(string(files["report.txt"]), "failure detail") {
		t.Errorf("report.txt = %s", files["report.txt"])
	}
}

func TestNearWord(t *testing.T) {
	for _, c := range []struct {
		got, want string
		near      bool
	}{
		{"autosubmit", "autosubmit", true},
		{"mautosubmit", "autosubmit", true}, // a checkbox read as a letter
		{"auto", "autosubmit", true},        // split in two
		{"autosubrnit", "autosubmit", true}, // misread
		{"once", "autosubmit", false},
		{"on", "once", false},
	} {
		if got := nearWord(c.got, c.want); got != c.near {
			t.Errorf("nearWord(%q, %q) = %v, want %v", c.got, c.want, got, c.near)
		}
	}
}
