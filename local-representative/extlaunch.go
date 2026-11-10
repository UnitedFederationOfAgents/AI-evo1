package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

// This file is a plug-in dev repo's lifecycle beyond its build, per
// user-1/sloppo's condocs/privateRepoOpsImpls/Step6Prompt.md Revision
// F: its plug-in is a managed application, launched from the system tab's
// row for the repo and restarted after each build, just as cats-ufa-plugin
// (the in-repo example) is a managed application the plugins tab launches.
//
// Deployment is systematic: LR, not the plug-in repo, decides where the
// binary goes. 'make deploy-dev-binaries' is run with
//
//	DEV_PLUGIN_DIR=/AI-evo1-dev/plugin
//	DEV_PLUGIN_BIN=/AI-evo1-dev/plugin/.staging/<app-id>
//	PLUGIN_APP_ID=<app-id>
//
// on its command line (so they override the Makefile's own), and its recipe
// builds the plug-in to $(DEV_PLUGIN_BIN). Once it passes, LR moves that
// staged binary to /AI-evo1-dev/plugin/<app-id> -- a failed build leaves the
// deployed one untouched, like the top-level Makefile's staging dir -- and
// restarts the plug-in if it's running. The directory is
// $AI_EVO1_DEV_PLUGIN when set, beside $AI_EVO1_DEV_BIN.
//
// Launched, the plug-in gets the same arguments as cats-ufa-plugin:
// --lr-host, --lr-port, --port (a free loopback port, picked per launch),
// --name <app-id> and, when LR has it, --dev-mode. It says hello with its
// port, as any plug-in does, so the plugins tab routes /apps/<app-id>/ to
// wherever it came up.

// defaultDevPluginDir is where plug-in dev repos' builds are deployed unless
// $AI_EVO1_DEV_PLUGIN says otherwise.
const defaultDevPluginDir = "/AI-evo1-dev/plugin"

// devPluginDir is the directory plug-in dev repos' builds are deployed to.
func devPluginDir() string {
	if d := os.Getenv("AI_EVO1_DEV_PLUGIN"); d != "" {
		return d
	}
	return defaultDevPluginDir
}

// devPluginBinary is where appID's deployed binary lives.
func devPluginBinary(appID string) string {
	return filepath.Join(devPluginDir(), appID)
}

// devPluginStagedBinary is where 'make deploy-dev-binaries' builds appID's
// binary to, before LR deploys it: in the same directory tree, so the move
// is a rename.
func devPluginStagedBinary(appID string) string {
	return filepath.Join(devPluginDir(), ".staging", appID)
}

// devPluginMakeArgs is the make command line a plug-in dev repo's build
// runs: its hook target, and where to put what it builds.
func devPluginMakeArgs(appID string) []string {
	return []string{
		"deploy-dev-binaries",
		"DEV_PLUGIN_DIR=" + devPluginDir(),
		"DEV_PLUGIN_BIN=" + devPluginStagedBinary(appID),
		"PLUGIN_APP_ID=" + appID,
	}
}

// prepareDevPluginStaging makes the staging directory and clears whatever an
// earlier build left there, so a recipe that doesn't build to
// $(DEV_PLUGIN_BIN) can't deploy a stale binary.
func prepareDevPluginStaging(appID string) error {
	staged := devPluginStagedBinary(appID)
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return fmt.Errorf("making %s: %v", filepath.Dir(staged), err)
	}
	if err := os.Remove(staged); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clearing %s: %v", staged, err)
	}
	return nil
}

// deployDevPlugin moves a passed build's staged binary into place. It fails
// when the recipe didn't build one: the repo's hook ignored DEV_PLUGIN_BIN.
func deployDevPlugin(appID string) error {
	staged, bin := devPluginStagedBinary(appID), devPluginBinary(appID)
	fi, err := os.Stat(staged)
	if err != nil {
		return fmt.Errorf("make deploy-dev-binaries passed but built nothing at DEV_PLUGIN_BIN=%s: build the plug-in there", staged)
	}
	if fi.IsDir() {
		return fmt.Errorf("DEV_PLUGIN_BIN=%s is a directory, not the plug-in's binary", staged)
	}
	if err := os.Chmod(staged, fi.Mode()|0o111); err != nil {
		return fmt.Errorf("making %s executable: %v", staged, err)
	}
	if err := os.Rename(staged, bin); err != nil {
		return fmt.Errorf("deploying %s: %v", bin, err)
	}
	log.Printf("dev-repo: deployed %s", bin)
	return nil
}

// devPluginSpec is the launch spec for a plug-in dev repo's plug-in: a
// singleton, run from its deployed binary with cats-ufa-plugin's arguments.
func devPluginSpec(appID string) launchSpec {
	return launchSpec{
		binName:   appID,
		binPath:   devPluginBinary(appID),
		singleton: true,  // one per box, like cats-ufa-plugin
		terminal:  false, // plain HTTP server -- no TTY needed
		buildArgs: func(s *Server, instanceID string) []string {
			args := []string{
				"--lr-host", "localhost",
				"--lr-port", s.heartbeatPort,
				"--port", freeLoopbackPort(),
				"--name", appID,
			}
			if s.devMode {
				// Cascade this LR's dev mode to every instance it launches — see
				// docs/DevMode.md.
				args = append(args, "--dev-mode")
			}
			return args
		},
	}
}

// freeLoopbackPort picks a port nothing is listening on for a plug-in to
// serve on. "0" if none could be had -- the plug-in then picks its own, if
// it will, and says so in its hello.
func freeLoopbackPort() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "0"
	}
	defer l.Close()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

// launchSpecFor is app's launch spec: one of managedApps, or a registered
// plug-in dev repo's plug-in.
func (s *Server) launchSpecFor(app string) (launchSpec, bool) {
	if spec, ok := managedApps[app]; ok {
		return spec, true
	}
	if s.repoWatch != nil && s.repoWatch.isPluginDevApp(app) {
		return devPluginSpec(app), true
	}
	return launchSpec{}, false
}

// isPluginDevApp reports whether appID is a registered plug-in dev repo's
// plug-in.
func (w *repoWatch) isPluginDevApp(appID string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.pluginAppAbsLocked(appID) != ""
}

// restartDevPlugin restarts appID's running instance after a build deployed
// a new binary, returning the note for the repo's row. One not launched is
// left so: the row's "launch" starts it.
func (s *Server) restartDevPlugin(appID string) string {
	s.procMu.Lock()
	id := ""
	for iid, p := range s.managed {
		if p.app == appID && p.state() == "running" {
			id = iid
			break
		}
	}
	s.procMu.Unlock()
	if id == "" {
		return "not launched"
	}
	if err := s.restartManaged(id); err != nil {
		return "restarting after the build: " + err.Error()
	}
	return "restarted after the build at " + time.Now().Format("15:04:05")
}
