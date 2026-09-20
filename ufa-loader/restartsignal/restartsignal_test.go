package restartsignal

import (
	"bytes"
	"strings"
	"testing"
)

// TestAnnounceScannerRoundTrip verifies a Scanner fed Announce's own output,
// line by line, recovers the same app/reason/pid that were announced.
func TestAnnounceScannerRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := Announce(&buf, "local-representative", "sighup"); err != nil {
		t.Fatalf("Announce: %v", err)
	}

	sc := NewScanner()
	var got *Announcement
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if a, ok := sc.Feed(line); ok {
			got = a
		}
	}
	if got == nil {
		t.Fatal("Scanner did not recognize the announcement")
	}
	if got.App != "local-representative" || got.Reason != "sighup" {
		t.Errorf("got App=%q Reason=%q, want local-representative/sighup", got.App, got.Reason)
	}
	if got.PID <= 0 {
		t.Errorf("got PID=%d, want a positive pid", got.PID)
	}
	if got.Time == "" {
		t.Error("got empty Time")
	}
}

// TestScannerIgnoresOrdinaryLines verifies plain log output never triggers a
// false positive, including a line that merely contains the banner text as a
// substring.
func TestScannerIgnoresOrdinaryLines(t *testing.T) {
	sc := NewScanner()
	lines := []string{
		"2026/09/20 12:00:00 starting up",
		"2026/09/20 12:00:01 listening on :8081",
		"note: === UFA-LOADER-RESTART === is not a real banner here",
	}
	for _, line := range lines {
		if a, ok := sc.Feed(line); ok {
			t.Fatalf("unexpected announcement from ordinary line %q: %+v", line, a)
		}
	}
}

// TestScannerRecoversFromMalformedBody verifies one bad sequence (body that
// doesn't parse as JSON) doesn't wedge the scanner — a subsequent well-formed
// sequence is still recognized.
func TestScannerRecoversFromMalformedBody(t *testing.T) {
	sc := NewScanner()

	feed := func(lines ...string) (a *Announcement, ok bool) {
		for _, l := range lines {
			a, ok = sc.Feed(l)
		}
		return
	}

	if a, ok := feed(Banner, "not json", Footer); ok {
		t.Fatalf("expected malformed body to be discarded, got %+v", a)
	}

	var buf bytes.Buffer
	_ = Announce(&buf, "local-representative", "sighup")
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	a, ok := feed(lines...)
	if !ok || a == nil || a.App != "local-representative" {
		t.Fatalf("expected the following well-formed announcement to be recognized, got %+v ok=%v", a, ok)
	}
}

// TestScanReaderPassesThroughAndDetects verifies ScanReader echoes every line
// it reads and returns the announcement embedded among ordinary output.
func TestScanReaderPassesThroughAndDetects(t *testing.T) {
	var ann bytes.Buffer
	_ = Announce(&ann, "local-representative", "sighup")

	input := "starting up\nlistening on :8081\n" + ann.String()
	var echoed bytes.Buffer

	got := ScanReader(strings.NewReader(input), &echoed)
	if got == nil {
		t.Fatal("ScanReader did not detect the embedded announcement")
	}
	if got.App != "local-representative" {
		t.Errorf("got App=%q, want local-representative", got.App)
	}
	if !strings.Contains(echoed.String(), "starting up") || !strings.Contains(echoed.String(), Banner) {
		t.Errorf("expected all input lines echoed through, got:\n%s", echoed.String())
	}
}

// TestScanReaderNoAnnouncement verifies a plain exit (no banner ever seen)
// returns nil rather than a zero-value Announcement.
func TestScanReaderNoAnnouncement(t *testing.T) {
	got := ScanReader(strings.NewReader("just some output\nand more\n"), nil)
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}
