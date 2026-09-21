package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ufa-loader/restartsignal"
)

// announceLoopScript is a small POSIX shell program used as a stand-in
// sub-application: each launch bumps a counter persisted in $COUNTFILE and,
// for as long as that counter is at or under $LIMIT, prints a restart
// announcement before exiting 0; past the limit it just exits 0 with no
// announcement, ending the restart loop.
const announceLoopScript = `
n=$(cat "$COUNTFILE" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$COUNTFILE"
if [ "$n" -le "$LIMIT" ]; then
	echo "$BANNER"
	echo '{"app":"test-app","reason":"loop","pid":1,"time":"t"}'
	echo "$FOOTER"
else
	echo "final run ($n)"
fi
`

// echoStateLoopScript is announceLoopScript's sibling for the restart-state
// carryover feature: each launch records the UFA_LOADER_STATE it was handed
// (one line per launch, in $SEENFILE) and announces a restart carrying its
// own launch count as state, for as long as $LIMIT allows -- so a test can
// verify launch N+1 saw launch N's announced state.
const echoStateLoopScript = `
n=$(cat "$COUNTFILE" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$COUNTFILE"
echo "${UFA_LOADER_STATE:-<none>}" >> "$SEENFILE"
if [ "$n" -le "$LIMIT" ]; then
	echo "$BANNER"
	echo "{\"app\":\"test-app\",\"reason\":\"loop\",\"pid\":1,\"time\":\"t\",\"state\":{\"n\":$n}}"
	echo "$FOOTER"
else
	echo "final run ($n)"
fi
`

func newAnnounceLoopLoader(t *testing.T, limit int) *loader {
	t.Helper()
	t.Setenv("BANNER", restartsignal.Banner)
	t.Setenv("FOOTER", restartsignal.Footer)
	t.Setenv("LIMIT", strconv.Itoa(limit))
	t.Setenv("COUNTFILE", filepath.Join(t.TempDir(), "count"))
	return &loader{
		bin:          "sh",
		binArgs:      []string{"-c", announceLoopScript},
		restartDelay: time.Millisecond,
	}
}

func readCount(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("COUNTFILE"))
	if err != nil {
		t.Fatalf("reading counter file: %v", err)
	}
	n, err := strconv.Atoi(string(data[:len(data)-1])) // strip trailing newline
	if err != nil {
		t.Fatalf("parsing counter file %q: %v", data, err)
	}
	return n
}

// TestLoaderRestartsUntilChildStopsAnnouncing verifies the core loop: each
// announced exit triggers a relaunch, and a plain (non-announcing) exit ends
// it, with the child's own exit code propagated.
func TestLoaderRestartsUntilChildStopsAnnouncing(t *testing.T) {
	l := newAnnounceLoopLoader(t, 2)

	code := l.run()
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := readCount(t); got != 3 {
		t.Fatalf("launch count = %d, want 3 (2 announced restarts + 1 final run)", got)
	}
}

// TestLoaderRespectsMaxRestarts verifies a child that keeps announcing
// restarts forever is still capped at max-restarts relaunches.
func TestLoaderRespectsMaxRestarts(t *testing.T) {
	l := newAnnounceLoopLoader(t, 1000) // effectively "always announces"
	l.maxRestarts = 1

	l.run()
	if got := readCount(t); got != 2 {
		t.Fatalf("launch count = %d, want 2 (1 initial launch + 1 restart, then capped)", got)
	}
}

// TestLoaderPropagatesExitCodeWithoutRestarting verifies a child that exits
// without ever announcing is not relaunched, and its exit code is propagated
// as ufa-loader's own.
func TestLoaderPropagatesExitCodeWithoutRestarting(t *testing.T) {
	l := &loader{bin: "sh", binArgs: []string{"-c", "exit 7"}, restartDelay: time.Millisecond}

	if code := l.run(); code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
}

// TestLoaderCarriesStateForward verifies each relaunch is handed the
// previous launch's announced state (via UFA_LOADER_STATE), and that the
// very first launch sees none.
func TestLoaderCarriesStateForward(t *testing.T) {
	t.Setenv("BANNER", restartsignal.Banner)
	t.Setenv("FOOTER", restartsignal.Footer)
	t.Setenv("LIMIT", "3")
	t.Setenv("COUNTFILE", filepath.Join(t.TempDir(), "count"))
	seenFile := filepath.Join(t.TempDir(), "seen")
	t.Setenv("SEENFILE", seenFile)

	l := &loader{
		bin:          "sh",
		binArgs:      []string{"-c", echoStateLoopScript},
		restartDelay: time.Millisecond,
	}
	if code := l.run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	data, err := os.ReadFile(seenFile)
	if err != nil {
		t.Fatalf("reading seen file: %v", err)
	}
	seen := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	want := []string{"<none>", `{"n":1}`, `{"n":2}`, `{"n":3}`}
	if len(seen) != len(want) {
		t.Fatalf("UFA_LOADER_STATE seen per launch = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("UFA_LOADER_STATE seen per launch = %v, want %v", seen, want)
		}
	}
}

// TestLoaderPassesThroughStdout verifies ordinary output from the child
// (not just the restart announcement) still reaches ufa-loader's own stdout,
// by redirecting os.Stdout for the duration of the run.
func TestLoaderPassesThroughStdout(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	l := &loader{bin: "sh", binArgs: []string{"-c", "echo hello-from-child"}, restartDelay: time.Millisecond}
	l.run()
	os.Stdout = orig
	w.Close()

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	if got := string(buf[:n]); got != "hello-from-child\n" {
		t.Fatalf("passed-through stdout = %q, want %q", got, "hello-from-child\n")
	}
}
