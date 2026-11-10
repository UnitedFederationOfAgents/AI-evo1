package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunGroupCancelKillsGroup: cancelling stops the leader and the children
// it started, even one that ignores SIGTERM, and the error carries the cause.
func TestRunGroupCancelKillsGroup(t *testing.T) {
	defer func(g time.Duration) { KillGrace = g }(KillGrace)
	KillGrace = 500 * time.Millisecond

	cause := errors.New("cancelled: test")
	ctx, cancel := context.WithCancelCause(context.Background())
	// The child ignores SIGTERM, so only the SIGKILL stops it.
	cmd := exec.Command("sh", "-c", `sh -c 'trap "" TERM; sleep 60' & echo $!; wait`)
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel(cause)
	}()

	start := time.Now()
	out, err := runGroup(ctx, cmd)
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want it to wrap the cause", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %s to stop", elapsed)
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if perr != nil {
		t.Fatalf("output %q: %v", out, perr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d outlived the cancel", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// alive reports whether pid is running: a zombie waiting for a reaper that
// may never come (no init in a container) counts as gone.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	// The state follows the parenthesised command name.
	s := string(stat)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2] != 'Z'
	}
	return true
}

// TestRunGroupUncancelled: a run that isn't cancelled returns its output and
// exit status as CombinedOutput would.
func TestRunGroupUncancelled(t *testing.T) {
	out, err := runGroup(context.Background(), exec.Command("sh", "-c", "echo out; echo err >&2"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "out\nerr\n" {
		t.Errorf("output %q", got)
	}
	_, err = runGroup(context.Background(), exec.Command("sh", "-c", "exit 3"))
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Errorf("err = %v, want exit status 3", err)
	}
}
