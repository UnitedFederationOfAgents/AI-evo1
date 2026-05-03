// heuristic-agent is a tool for managing asynchronous AI agent invocations
// through slopspaces and work signals.
//
// Usage:
//
//	heuristic-agent watch [--agent-type <type>]
//	heuristic-agent slopspace create
//	heuristic-agent slopspace deploy <id> [--agent-type <type>]
//	heuristic-agent slopspace return <id>
//	heuristic-agent slopspace list
//	heuristic-agent slopspace delete <id>
//	heuristic-agent slopspace status [--agent-type <type>]
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"heuristic-agent/pkg/executor"
	"heuristic-agent/pkg/slopspace"
	"heuristic-agent/pkg/types"
	"heuristic-agent/pkg/worksignal"

	"github.com/google/uuid"
)

const (
	checkInterval = 10 * time.Second
	version       = "0.1.0"
)

var backoffLevels = []time.Duration{
	30 * time.Second,
	5 * time.Minute,
	1 * time.Hour,
	24 * time.Hour,
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cfg := loadConfig()

	switch os.Args[1] {
	case "watch":
		runWatch(cfg, os.Args[2:])
	case "slopspace":
		runSlopspace(cfg, os.Args[2:])
	case "version":
		fmt.Printf("heuristic-agent %s\n", version)
	case "check-deps":
		if err := executor.CheckDependencies(); err != nil {
			log.Fatalf("Dependency check failed: %v", err)
		}
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`heuristic-agent - Manage asynchronous AI agent invocations

Usage:
  heuristic-agent <command> [options]

Commands:
  watch       Start the watch loop for processing work signals
  slopspace   Manage slopspaces (create, deploy, return, list, delete)
  version     Print version information
  check-deps  Verify dependencies (ambiguous-agent, clauditable) are available
  help        Print this help message

Watch command:
  heuristic-agent watch [--agent-type <type>]
    --agent-type    Agent type to run as: agent-worker or heuristic-request
                    (default: agent-worker)

Slopspace commands:
  heuristic-agent slopspace create
    Creates a new slopspace (agent type specified at deploy time)

  heuristic-agent slopspace deploy <id> [--agent-type <type>]
    Deploy a slopspace for a specific agent type (default: agent-worker)

  heuristic-agent slopspace return <id>
    Return a deployed slopspace

  heuristic-agent slopspace list
    List all slopspaces

  heuristic-agent slopspace delete <id>
    Delete a slopspace

  heuristic-agent slopspace status [--agent-type <type>]
    Show currently deployed slopspace for agent type

Environment variables:
  SLOPSPACES_DIR        Slopspaces directory (default: /host-agent-files/slopspaces)
  WORK_SIGNALS_DIR      Work signals directory (default: /host-agent-files/work)
  AGENT_SLOPSPACE_ROOT  Agent workspace root (default: /agent)
  AGENT_RECORDS_PATH    Agent records path (default: /host-agent-files/agent-records)`)
}

func loadConfig() *types.Config {
	cfg := types.DefaultConfig()

	if v := os.Getenv("SLOPSPACES_DIR"); v != "" {
		cfg.SlopspacesDir = v
	}
	if v := os.Getenv("WORK_SIGNALS_DIR"); v != "" {
		cfg.WorkSignalsDir = v
	}
	if v := os.Getenv("AGENT_SLOPSPACE_ROOT"); v != "" {
		cfg.AgentSlopspaceRoot = v
	}
	if v := os.Getenv("AGENT_RECORDS_PATH"); v != "" {
		cfg.AgentRecordsPath = v
	}

	cfg.WorkerID = uuid.New().String()[:8]

	return cfg
}

func runWatch(cfg *types.Config, args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	agentType := fs.String("agent-type", "agent-worker", "Agent type: agent-worker or heuristic-request")
	fs.Parse(args)

	switch *agentType {
	case "agent-worker":
		cfg.AgentType = types.AgentTypeWorker
	case "heuristic-request":
		cfg.AgentType = types.AgentTypeHeuristic
	default:
		log.Fatalf("Invalid agent type: %s", *agentType)
	}

	// Check dependencies at startup
	log.Printf("[%s] Checking dependencies...", cfg.WorkerID)
	if err := executor.CheckDependencies(); err != nil {
		log.Printf("[%s] Warning: %v", cfg.WorkerID, err)
	}

	worker := NewWorker(cfg)
	worker.Run()
}

func runSlopspace(cfg *types.Config, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "slopspace subcommand required: create, deploy, return, list, delete, status")
		os.Exit(1)
	}

	mgr := slopspace.NewManager(cfg)

	switch args[0] {
	case "create":
		// No agent-type needed at creation time
		metadata, err := mgr.Create()
		if err != nil {
			log.Fatalf("Failed to create slopspace: %v", err)
		}
		fmt.Printf("Created slopspace: %s\n", metadata.ID)
		fmt.Printf("  Path: %s\n", metadata.RootPath)
		fmt.Println("  Agent type will be specified at deploy time")

	case "deploy":
		if len(args) < 2 {
			log.Fatal("slopspace deploy requires an ID")
		}
		id := args[1]

		// Parse remaining args for --agent-type
		fs := flag.NewFlagSet("deploy", flag.ExitOnError)
		agentType := fs.String("agent-type", "agent-worker", "Agent type for deployment")
		fs.Parse(args[2:])

		var at types.AgentType
		switch *agentType {
		case "agent-worker":
			at = types.AgentTypeWorker
		case "heuristic-request":
			at = types.AgentTypeHeuristic
		default:
			log.Fatalf("Invalid agent type: %s", *agentType)
		}

		if err := mgr.Deploy(id, at); err != nil {
			log.Fatalf("Failed to deploy slopspace: %v", err)
		}
		fmt.Printf("Deployed slopspace %s to %s\n", id, cfg.DeployPathForAgentType(at))

	case "return":
		if len(args) < 2 {
			log.Fatal("slopspace return requires an ID")
		}
		id := args[1]
		if err := mgr.Return(id); err != nil {
			log.Fatalf("Failed to return slopspace: %v", err)
		}
		fmt.Printf("Returned slopspace %s\n", id)

	case "list":
		slopspaces, err := mgr.List()
		if err != nil {
			log.Fatalf("Failed to list slopspaces: %v", err)
		}

		if len(slopspaces) == 0 {
			fmt.Println("No slopspaces found")
			return
		}

		fmt.Printf("%-36s  %-18s  %-8s  %-4s\n", "ID", "DEPLOYED AGENT", "DEPLOYED", "ITER")
		fmt.Println("------------------------------------------------------------------------")
		for _, s := range slopspaces {
			deployed := "no"
			deployedAgent := "-"
			if s.Deployed {
				deployed = "yes"
				deployedAgent = string(s.DeployedAgentType)
			}
			fmt.Printf("%-36s  %-18s  %-8s  %-4d\n", s.ID, deployedAgent, deployed, s.Iteration)
		}

	case "delete":
		if len(args) < 2 {
			log.Fatal("slopspace delete requires an ID")
		}
		id := args[1]
		if err := mgr.Delete(id); err != nil {
			log.Fatalf("Failed to delete slopspace: %v", err)
		}
		fmt.Printf("Deleted slopspace %s\n", id)

	case "status":
		// Parse --agent-type flag
		fs := flag.NewFlagSet("status", flag.ExitOnError)
		agentType := fs.String("agent-type", "agent-worker", "Agent type to check")
		fs.Parse(args[1:])

		var at types.AgentType
		switch *agentType {
		case "agent-worker":
			at = types.AgentTypeWorker
		case "heuristic-request":
			at = types.AgentTypeHeuristic
		default:
			log.Fatalf("Invalid agent type: %s", *agentType)
		}

		id, err := mgr.GetDeployedID(at)
		if err != nil {
			log.Fatalf("Failed to get deployed status: %v", err)
		}
		if id == "" {
			fmt.Printf("No slopspace currently deployed for %s\n", at)
		} else {
			metadata, err := mgr.Get(id)
			if err != nil {
				log.Fatalf("Failed to get slopspace metadata: %v", err)
			}
			fmt.Printf("Currently deployed slopspace for %s:\n", at)
			fmt.Printf("  ID: %s\n", metadata.ID)
			fmt.Printf("  Iteration: %d\n", metadata.Iteration)
			fmt.Printf("  Deploy Path: %s\n", cfg.DeployPathForAgentType(at))
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown slopspace subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// Worker handles the watch loop for processing work signals.
type Worker struct {
	config         *types.Config
	workerID       string
	slopspaceMgr   *slopspace.Manager
	workSignalMgr  *worksignal.Manager
	executor       *executor.Executor
	lastActivity   time.Time
	backoffIndex   int
	nextBackoffLog time.Time
}

// NewWorker creates a new worker.
func NewWorker(cfg *types.Config) *Worker {
	exec := executor.NewExecutorWithOptions(cfg)

	return &Worker{
		config:         cfg,
		workerID:       cfg.WorkerID,
		slopspaceMgr:   slopspace.NewManager(cfg),
		workSignalMgr:  worksignal.NewManager(cfg),
		executor:       exec,
		lastActivity:   time.Now(),
		backoffIndex:   0,
		nextBackoffLog: time.Now().Add(backoffLevels[0]),
	}
}

// Run starts the watch loop.
func (w *Worker) Run() {
	log.Printf("[%s] heuristic-agent started (agent-type: %s)", w.workerID, w.config.AgentType)
	log.Printf("[%s] Watching for work signals in: %s", w.workerID, w.config.OngoingWorkDir())
	log.Printf("[%s] Slopspaces directory: %s", w.workerID, w.config.SlopspacesDir)
	log.Printf("[%s] Deploy path: %s", w.workerID, w.config.DeployPath())

	// Ensure directories exist
	if err := w.ensureDirectories(); err != nil {
		log.Fatalf("[%s] Failed to ensure directories: %v", w.workerID, err)
	}

	for {
		signals, err := w.checkForWork()
		if err != nil {
			log.Printf("[%s] Error checking for work: %v", w.workerID, err)
		}

		if len(signals) > 0 {
			// Reset backoff on activity
			w.lastActivity = time.Now()
			w.backoffIndex = 0
			w.nextBackoffLog = w.lastActivity.Add(backoffLevels[0])

			// Process the first available signal
			for _, signalPath := range signals {
				if err := w.processWorkSignal(signalPath); err != nil {
					log.Printf("[%s] Error processing signal: %v", w.workerID, err)
				}
				break // Process one at a time
			}
		} else {
			// No activity - check if we should log with backoff
			now := time.Now()
			if now.After(w.nextBackoffLog) {
				timeSinceActivity := now.Sub(w.lastActivity)
				log.Printf("[%s] No activity for %s", w.workerID, formatDuration(timeSinceActivity))

				// Advance to next backoff level
				if w.backoffIndex < len(backoffLevels)-1 {
					w.backoffIndex++
				}
				w.nextBackoffLog = now.Add(backoffLevels[w.backoffIndex])
			}
		}

		time.Sleep(checkInterval)
	}
}

func (w *Worker) ensureDirectories() error {
	dirs := []string{
		w.config.SlopspacesDir,
		w.config.OngoingWorkDir(),
		w.config.CompleteWorkDir(),
		w.config.AgentRecordsPath,
		w.config.DeployPath(),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}

func (w *Worker) checkForWork() ([]string, error) {
	return w.workSignalMgr.FindPendingForAgentType(w.config.AgentType)
}

func (w *Worker) processWorkSignal(signalPath string) error {
	log.Printf("[%s] Processing work signal: %s", w.workerID, signalPath)

	// Take ownership
	if err := w.workSignalMgr.TakeOwnership(signalPath, w.workerID); err != nil {
		return fmt.Errorf("failed to take ownership: %w", err)
	}

	// Read the signal
	signal, _, err := w.workSignalMgr.Read(signalPath)
	if err != nil {
		w.workSignalMgr.ReleaseOwnership(signalPath)
		return fmt.Errorf("failed to read signal: %w", err)
	}

	log.Printf("[%s] Work signal details:", w.workerID)
	log.Printf("[%s]   Role: %s", w.workerID, signal.Role)
	log.Printf("[%s]   Agent: %s", w.workerID, signal.Agent)
	log.Printf("[%s]   Model: %s", w.workerID, signal.Model)
	log.Printf("[%s]   Work Type: %s", w.workerID, signal.WorkType)

	var workErr error

	switch signal.WorkType {
	case types.WorkTypeSlopspace:
		workErr = w.processSlopspaceWork(signal, signalPath)
	case types.WorkTypeInPlace:
		workErr = w.processInPlaceWork(signal, signalPath)
	default:
		workErr = fmt.Errorf("unknown work type: %s", signal.WorkType)
	}

	// Complete the signal
	if workErr != nil {
		log.Printf("[%s] Work failed: %v", w.workerID, workErr)
		if err := w.workSignalMgr.Complete(signalPath, false, workErr.Error()); err != nil {
			log.Printf("[%s] Failed to mark signal as failed: %v", w.workerID, err)
		}
		return workErr
	}

	log.Printf("[%s] Work completed successfully", w.workerID)
	if err := w.workSignalMgr.Complete(signalPath, true, "Work completed"); err != nil {
		log.Printf("[%s] Failed to mark signal as complete: %v", w.workerID, err)
	}

	return nil
}

func (w *Worker) processSlopspaceWork(signal *types.WorkSignal, signalPath string) error {
	// Check if there's already a deployed slopspace for this agent type
	deployedID, err := w.slopspaceMgr.GetDeployedID(w.config.AgentType)
	if err != nil {
		return fmt.Errorf("failed to check deployed slopspace: %w", err)
	}

	var metadata *types.SlopspaceMetadata

	if deployedID != "" {
		// Use existing deployed slopspace
		log.Printf("[%s] Using existing deployed slopspace: %s", w.workerID, deployedID)
		metadata, err = w.slopspaceMgr.Get(deployedID)
		if err != nil {
			return fmt.Errorf("failed to get deployed slopspace: %w", err)
		}
	} else {
		// Create and deploy a new slopspace
		log.Printf("[%s] Creating new slopspace", w.workerID)
		metadata, err = w.slopspaceMgr.Create()
		if err != nil {
			return fmt.Errorf("failed to create slopspace: %w", err)
		}

		log.Printf("[%s] Deploying slopspace: %s for agent-type: %s", w.workerID, metadata.ID, w.config.AgentType)
		if err := w.slopspaceMgr.Deploy(metadata.ID, w.config.AgentType); err != nil {
			return fmt.Errorf("failed to deploy slopspace: %w", err)
		}

		// Update metadata after deploy
		metadata, _ = w.slopspaceMgr.Get(metadata.ID)
	}

	// Invoke the agent
	workdir := w.config.DeployPath()
	prompt := executor.FormatPromptForAgent(signal, workdir)

	log.Printf("[%s] Invoking agent %s with model %s", w.workerID, signal.Agent, signal.Model)
	output, err := w.executor.InvokeAgentWithCapture(signal.Agent, signal.Model, "execute", prompt, workdir)
	if err != nil {
		log.Printf("[%s] Agent output:\n%s", w.workerID, string(output))
		return fmt.Errorf("agent invocation failed: %w", err)
	}

	log.Printf("[%s] Agent output:\n%s", w.workerID, string(output))

	// Return the slopspace
	log.Printf("[%s] Returning slopspace: %s", w.workerID, metadata.ID)
	if err := w.slopspaceMgr.Return(metadata.ID); err != nil {
		return fmt.Errorf("failed to return slopspace: %w", err)
	}

	return nil
}

func (w *Worker) processInPlaceWork(signal *types.WorkSignal, signalPath string) error {
	workdir := signal.WorkLocation
	if workdir == "" {
		return fmt.Errorf("work_location required for in-place work")
	}

	prompt := executor.FormatPromptForAgent(signal, workdir)

	log.Printf("[%s] Invoking agent %s with model %s in-place at %s", w.workerID, signal.Agent, signal.Model, workdir)
	output, err := w.executor.InvokeAgentWithCapture(signal.Agent, signal.Model, "execute", prompt, workdir)
	if err != nil {
		log.Printf("[%s] Agent output:\n%s", w.workerID, string(output))
		return fmt.Errorf("agent invocation failed: %w", err)
	}

	log.Printf("[%s] Agent output:\n%s", w.workerID, string(output))

	return nil
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
