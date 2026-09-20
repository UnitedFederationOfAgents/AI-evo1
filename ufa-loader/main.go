// Command ufa-loader is a wrapping executable that launches an inner
// sub-application and restarts it whenever that sub-application announces
// (via the restartsignal protocol) that it wants one — see
// condocs/InitialDistributedDevelopment.md Step 2 and docs/DevMode.md's
// "Loader" entry. It's deliberately generic: it knows nothing about which
// sub-application it's wrapping, only how to launch a binary and watch its
// stdout, so any future sub-application that adopts restartsignal gets
// restart/version-switching for free.
//
// Usage: ufa-loader [flags] <binary> [binary-args...]
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"ufa-loader/restartsignal"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("ufa-loader: ")

	maxRestarts := flag.Int("max-restarts", 0, "stop relaunching after this many restarts (0 = unlimited)")
	restartDelay := flag.Duration("restart-delay", 500*time.Millisecond, "pause before relaunching, giving an in-flight binary replacement time to land")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	l := &loader{
		bin:          args[0],
		binArgs:      args[1:],
		restartDelay: *restartDelay,
		maxRestarts:  *maxRestarts,
	}
	os.Exit(l.run())
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: ufa-loader [flags] <binary> [binary-args...]\n\n"+
		"Launches <binary>, watching its stdout for the restart announcement\n"+
		"defined by ufa-loader/restartsignal (see docs/DevMode.md). When seen,\n"+
		"ufa-loader relaunches <binary> with the same arguments — expecting the\n"+
		"file on disk to have been replaced with a newer build — instead of\n"+
		"exiting. A plain exit (no announcement) is propagated as-is: ufa-loader\n"+
		"exits with the same code.\n\n"+
		"example: ufa-loader local-representative --dev-mode\n\nflags:\n")
	flag.PrintDefaults()
}

// loader supervises repeated launches of one binary.
type loader struct {
	bin          string
	binArgs      []string
	restartDelay time.Duration
	maxRestarts  int // 0 = unlimited
}

// run launches bin, relaunching it for as long as each exit is accompanied
// by a restart announcement, and returns the code ufa-loader itself should
// exit with.
func (l *loader) run() int {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	restarts := 0
	for {
		code, announced := l.runOnce(sigCh)
		if !announced {
			return code
		}
		if l.maxRestarts > 0 && restarts >= l.maxRestarts {
			log.Printf("%s asked for a restart but the %d restart limit was reached — exiting", l.bin, l.maxRestarts)
			return code
		}
		restarts++
		log.Printf("%s announced a restart (#%d) — relaunching in %s", l.bin, restarts, l.restartDelay)

		select {
		case sig := <-sigCh:
			log.Printf("received %s while waiting to relaunch %s — not restarting", sig, l.bin)
			return code
		case <-time.After(l.restartDelay):
		}
	}
}

// runOnce launches one instance of bin, forwards SIGINT/SIGTERM received by
// ufa-loader on to it while it runs, streams its stdout through to our own
// (watching for the restart announcement along the way), and waits for it to
// exit. It returns the exit code to propagate if this turns out to be the
// final launch, and whether the child announced a restart before exiting.
func (l *loader) runOnce(sigCh <-chan os.Signal) (exitCode int, announced bool) {
	cmd := exec.Command(l.bin, l.binArgs...)
	// Let the sub-application detect it is loader-managed (see
	// restartsignal.IsLoaderManaged) without needing to know anything else
	// about how it was invoked.
	cmd.Env = append(os.Environ(), restartsignal.InitEnvVar+"=1")
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("%s: %v", l.bin, err)
		return 1, false
	}

	if err := cmd.Start(); err != nil {
		log.Printf("%s: %v", l.bin, err)
		return 1, false
	}
	log.Printf("launched %s (pid %d)", l.bin, cmd.Process.Pid)

	// Forward stop signals to the child for as long as it runs; the
	// restart trigger itself (e.g. SIGHUP to local-representative) is sent
	// directly to the child's own pid, not through ufa-loader.
	stopForwarding := make(chan struct{})
	forwardingDone := make(chan struct{})
	go func() {
		defer close(forwardingDone)
		select {
		case sig := <-sigCh:
			log.Printf("forwarding %s to %s (pid %d)", sig, l.bin, cmd.Process.Pid)
			_ = cmd.Process.Signal(sig)
		case <-stopForwarding:
		}
	}()

	// All reads from the stdout pipe must finish before Wait is called.
	ann := restartsignal.ScanReader(stdout, os.Stdout)

	err = cmd.Wait()
	close(stopForwarding)
	<-forwardingDone

	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			log.Printf("%s: %v", l.bin, err)
			code = 1
		}
	}
	if ann != nil {
		log.Printf("%s (pid %d) exited %d after announcing a restart (%s)", l.bin, cmd.Process.Pid, code, ann.Reason)
		return code, true
	}
	log.Printf("%s (pid %d) exited %d", l.bin, cmd.Process.Pid, code)
	return code, false
}
