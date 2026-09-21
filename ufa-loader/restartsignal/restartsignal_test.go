package restartsignal

import (
	"bytes"
	"encoding/json"
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

// TestAnnounceStateRoundTrip verifies AnnounceState's attached state survives
// the Banner/JSON/Footer round trip intact, and that a plain Announce (no
// state) comes back with an empty State field.
func TestAnnounceStateRoundTrip(t *testing.T) {
	type payload struct {
		AutoRebuild bool `json:"auto_rebuild"`
	}

	var buf bytes.Buffer
	if err := AnnounceState(&buf, "local-representative", "restart", payload{AutoRebuild: true}); err != nil {
		t.Fatalf("AnnounceState: %v", err)
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
	var decoded payload
	if err := json.Unmarshal(got.State, &decoded); err != nil {
		t.Fatalf("unmarshaling State: %v", err)
	}
	if !decoded.AutoRebuild {
		t.Errorf("got AutoRebuild=false after round trip, want true")
	}

	buf.Reset()
	if err := Announce(&buf, "local-representative", "sighup"); err != nil {
		t.Fatalf("Announce: %v", err)
	}
	sc = NewScanner()
	got = nil
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if a, ok := sc.Feed(line); ok {
			got = a
		}
	}
	if got == nil {
		t.Fatal("Scanner did not recognize the plain announcement")
	}
	if len(got.State) != 0 {
		t.Errorf("got State=%q for a plain Announce, want empty", got.State)
	}
}

// TestPreviousState verifies PreviousState reflects StateEnvVar, and reports
// false when it's unset or empty (a fresh launch, or a prior announcement
// with no state).
func TestPreviousState(t *testing.T) {
	if _, ok := PreviousState(); ok {
		t.Fatal("expected ok=false with StateEnvVar unset")
	}

	t.Setenv(StateEnvVar, `{"auto_rebuild":true}`)
	raw, ok := PreviousState()
	if !ok {
		t.Fatal("expected ok=true with StateEnvVar set")
	}
	var decoded struct {
		AutoRebuild bool `json:"auto_rebuild"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshaling PreviousState: %v", err)
	}
	if !decoded.AutoRebuild {
		t.Errorf("got AutoRebuild=false, want true")
	}

	t.Setenv(StateEnvVar, "")
	if _, ok := PreviousState(); ok {
		t.Fatal("expected ok=false with StateEnvVar set to empty string")
	}
}
