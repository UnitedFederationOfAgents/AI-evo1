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
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	ufahostid "ufa-hostid"
	"ufa-loader/awsprincipal"
	"ufa-loader/restartsignal"
	ufaversion "ufa-version"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("ufa-loader: ")

	maxRestarts := flag.Int("max-restarts", 0, "stop relaunching after this many restarts (0 = unlimited)")
	restartDelay := flag.Duration("restart-delay", 500*time.Millisecond, "pause before relaunching, giving an in-flight binary replacement time to land")
	showVersion := flag.Bool("version", false, "print ufa-loader's own version and exit (a wrapped binary's --version goes to the binary, not here)")
	noAWSPrincipal := flag.Bool("no-aws-principal", false, "don't create a temporary AWS principal; pass the starting AWS credentials (if any) straight through to <binary>")
	awsIdleDays := flag.Int("aws-principal-idle-days", 2, "delete temporary AWS principals from previous days once unused for this many days")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println(ufaversion.Version)
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	if !*noAWSPrincipal {
		provisionAWSPrincipal(filepath.Base(args[0]), *awsIdleDays)
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

// provisionAWSPrincipal swaps the AWS credentials ufa-loader started with
// for a fresh temporary IAM user's (see ufa-loader/awsprincipal), so every
// launch of app inherits only the new keys through the environment. With no
// starting credentials it does nothing and says nothing; if creating the
// principal fails, it logs why and leaves the starting credentials in
// place. The new keys live only in this process's environment -- nothing is
// written to disk.
func provisionAWSPrincipal(app string, idleDays int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := awsprincipal.Provision(ctx, awsprincipal.Options{
		HostID:   ufahostid.GetHostID(),
		App:      app,
		IdleDays: idleDays,
	})
	if errors.Is(err, awsprincipal.ErrNoCredentials) {
		return
	}
	if err != nil {
		log.Printf("no temporary AWS principal (keeping starting credentials): %v", err)
		return
	}
	awsprincipal.ApplyToEnv(res.Credentials)

	var notes []string
	if !res.Confirmed {
		notes = append(notes, "key not yet accepted by AWS")
	}
	if res.Removed > 0 {
		notes = append(notes, fmt.Sprintf("removed %d stale", res.Removed))
	}
	if res.CleanupErrs > 0 {
		notes = append(notes, fmt.Sprintf("%d stale cleanup failures", res.CleanupErrs))
	}
	suffix := ""
	if len(notes) > 0 {
		suffix = " (" + strings.Join(notes, ", ") + ")"
	}
	log.Printf("created temporary AWS principal %s for %s%s", res.UserName, app, suffix)
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
	var state json.RawMessage // carried forward from the previous announcement's State, if any
	for {
		code, ann := l.runOnce(sigCh, state)
		if ann == nil {
			return code
		}
		if l.maxRestarts > 0 && restarts >= l.maxRestarts {
			log.Printf("%s asked for a restart but the %d restart limit was reached — exiting", l.bin, l.maxRestarts)
			return code
		}
		restarts++
		state = ann.State
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
// exit. state is the previous instance's disclosed restart State, if any
// (see restartsignal.PreviousState); pass nil on the first launch. It
// returns the exit code to propagate if this turns out to be the final
// launch, and the announcement the child made before exiting, if any (nil
// for a plain exit).
func (l *loader) runOnce(sigCh <-chan os.Signal, state json.RawMessage) (exitCode int, ann *restartsignal.Announcement) {
	cmd := exec.Command(l.bin, l.binArgs...)
	// Let the sub-application detect it is loader-managed (see
	// restartsignal.IsLoaderManaged) without needing to know anything else
	// about how it was invoked.
	env := append(childEnviron(), restartsignal.InitEnvVar+"=1")
	if len(state) > 0 {
		// Hand the previous instance's disclosed state back to this one
		// (see restartsignal.PreviousState) — opaque to ufa-loader itself.
		env = append(env, restartsignal.StateEnvVar+"="+string(state))
	}
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("%s: %v", l.bin, err)
		return 1, nil
	}

	if err := cmd.Start(); err != nil {
		log.Printf("%s: %v", l.bin, err)
		return 1, nil
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
	ann = restartsignal.ScanReader(stdout, os.Stdout)

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
		return code, ann
	}
	log.Printf("%s (pid %d) exited %d", l.bin, cmd.Process.Pid, code)
	return code, nil
}

// childEnviron returns ufa-loader's own environment minus the loader
// variables it sets itself (see restartsignal.InitEnvVar / StateEnvVar).
// ufa-loader may itself run inside a loader-managed process tree (e.g. a
// shell opened from local-representative), and an inherited StateEnvVar
// would otherwise hand that unrelated app's state to our child's very first
// launch.
func childEnviron() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, restartsignal.InitEnvVar+"=") || strings.HasPrefix(kv, restartsignal.StateEnvVar+"=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}
