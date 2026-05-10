// dungeon-keeper is a tool for managing asynchronous AI agent invocations
// through slopspaces and work signals.
//
// Usage:
//
//	dungeon-keeper watch [--agent-type <type>]
//	dungeon-keeper slopspace create
//	dungeon-keeper slopspace deploy <id> [--agent-type <type>]
//	dungeon-keeper slopspace return <id>
//	dungeon-keeper slopspace list
//	dungeon-keeper slopspace delete <id>
//	dungeon-keeper slopspace status [--agent-type <type>]
//	dungeon-keeper slopspace add-readspace repo <id> <owner/repo> [--ref <branch|tag|commit>]
//	dungeon-keeper slopspace add-writespace repo <id> <owner/repo> --ref <branch>
//	dungeon-keeper slopspace write <id> all
//	dungeon-keeper slopspace write repo <id> <owner/repo>
//	dungeon-keeper readspace repo clone <owner/repo>
//	dungeon-keeper readspace repo delete <owner/repo>
//	dungeon-keeper readspace repo list
//	dungeon-keeper writespace repo clone <owner/repo>
//	dungeon-keeper writespace repo delete <owner/repo>
//	dungeon-keeper writespace repo list
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"dungeon-keeper/pkg/executor"
	"dungeon-keeper/pkg/readspace"
	"dungeon-keeper/pkg/slopspace"
	"dungeon-keeper/pkg/types"
	"dungeon-keeper/pkg/worksignal"
	"dungeon-keeper/pkg/writespace"

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
	case "readspace":
		runReadspace(cfg, os.Args[2:])
	case "writespace":
		runWritespace(cfg, os.Args[2:])
	case "version":
		fmt.Printf("dungeon-keeper %s\n", version)
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
	fmt.Println(`dungeon-keeper - Manage asynchronous AI agent invocations

Usage:
  dungeon-keeper <command> [options]

Commands:
  watch       Start the watch loop for processing work signals
  slopspace   Manage slopspaces (create, deploy, return, list, delete, add-readspace, add-writespace, write)
  readspace   Manage readspaces (repo clone, repo delete, repo list)
  writespace  Manage writespaces (repo clone, repo delete, repo list)
  version     Print version information
  check-deps  Verify dependencies (ambiguous-agent, clauditable) are available
  help        Print this help message

Watch command:
  dungeon-keeper watch [--agent-type <type>]
    --agent-type    Agent type to run as: agent-worker or heuristic-request
                    (default: agent-worker)

Slopspace commands:
  dungeon-keeper slopspace create
    Creates a new slopspace (agent type specified at deploy time)

  dungeon-keeper slopspace deploy <id> [--agent-type <type>]
    Deploy a slopspace for a specific agent type (default: agent-worker)

  dungeon-keeper slopspace return <id>
    Return a deployed slopspace

  dungeon-keeper slopspace list
    List all slopspaces

  dungeon-keeper slopspace delete <id>
    Delete a slopspace

  dungeon-keeper slopspace status [--agent-type <type>]
    Show currently deployed slopspace for agent type

  dungeon-keeper slopspace add-readspace repo <id> <owner/repo> [--ref <branch|tag|commit>]
    Add a repository from readspaces to a slopspace (copies repo, switches to ref, deletes .git)

  dungeon-keeper slopspace add-writespace repo <id> <owner/repo> --ref <branch>
    Add a repository from writespaces to a slopspace (copies repo, creates new branch, moves .git to secure location)

  dungeon-keeper slopspace write <id> all
    Push changes from all writespace repos in a slopspace

  dungeon-keeper slopspace write repo <id> <owner/repo>
    Push changes from a specific writespace repo in a slopspace

Readspace commands:
  dungeon-keeper readspace repo clone <owner/repo>
    Clone a repository into the readspaces directory (or update if exists)

  dungeon-keeper readspace repo delete <owner/repo>
    Delete a repository from the readspaces directory

  dungeon-keeper readspace repo list
    List all repositories in the readspaces directory

Writespace commands:
  dungeon-keeper writespace repo clone <owner/repo>
    Clone a repository into the writespaces directory (or update if exists)

  dungeon-keeper writespace repo delete <owner/repo>
    Delete a repository from the writespaces directory

  dungeon-keeper writespace repo list
    List all repositories in the writespaces directory

Environment variables:
  SLOPSPACES_DIR        Slopspaces directory (default: /host-agent-files/slopspaces)
  WORK_SIGNALS_DIR      Work signals directory (default: /host-agent-files/work)
  AGENT_SLOPSPACE_ROOT  Agent workspace root (default: /agent)
  AGENT_RECORDS_PATH    Agent records path (default: /host-agent-files/agent-records)
  TF_VAR_github_pat     GitHub Personal Access Token for cloning repos`)
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
		fmt.Fprintln(os.Stderr, "slopspace subcommand required: create, deploy, return, list, delete, status, add-readspace, add-writespace, write")
		os.Exit(1)
	}

	mgr := slopspace.NewManager(cfg)
	readspaceMgr := readspace.NewManager(cfg)
	writespaceMgr := writespace.NewManager(cfg)

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
		id, ok := requiredSlopspaceID(args)
		if !ok {
			log.Fatal("slopspace deploy requires an ID")
		}

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
		id, ok := requiredSlopspaceID(args)
		if !ok {
			log.Fatal("slopspace return requires an ID")
		}
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
		id, ok := requiredSlopspaceID(args)
		if !ok {
			log.Fatal("slopspace delete requires an ID")
		}
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

	case "add-readspace":
		// slopspace add-readspace repo <id> <owner/repo> [--ref <ref>]
		if len(args) < 4 || args[1] != "repo" {
			log.Fatal("Usage: slopspace add-readspace repo <slopspace-id> <owner/repo> [--ref <branch|tag|commit>]")
		}
		slopspaceID := args[2]
		ownerRepo := args[3]

		// Parse --ref flag
		fs := flag.NewFlagSet("add-readspace", flag.ExitOnError)
		ref := fs.String("ref", "", "Branch, tag, or commit to checkout")
		fs.Parse(args[4:])

		// Validate owner/repo format
		parts := strings.SplitN(ownerRepo, "/", 2)
		if len(parts) != 2 {
			log.Fatalf("Invalid owner/repo format: %s (expected owner/repo)", ownerRepo)
		}
		owner, repo := parts[0], parts[1]

		// Get the source repo from readspaces
		repoMeta, err := readspaceMgr.Get(ownerRepo)
		if err != nil {
			log.Fatalf("Repository not found in readspaces: %s\nRun 'readspace repo clone %s' first", ownerRepo, ownerRepo)
		}

		// Add to slopspace
		if err := mgr.AddReadspaceRepo(slopspaceID, owner, repo, *ref, repoMeta.RootPath); err != nil {
			log.Fatalf("Failed to add readspace repo: %v", err)
		}
		fmt.Printf("Added %s to slopspace %s readspaces\n", ownerRepo, slopspaceID)
		if *ref != "" {
			fmt.Printf("  Checked out ref: %s\n", *ref)
		}

	case "add-writespace":
		// slopspace add-writespace repo <id> <owner/repo> --ref <branch>
		if len(args) < 4 || args[1] != "repo" {
			log.Fatal("Usage: slopspace add-writespace repo <slopspace-id> <owner/repo> --ref <branch>")
		}
		slopspaceID := args[2]
		ownerRepo := args[3]

		// Parse --ref flag
		fs := flag.NewFlagSet("add-writespace", flag.ExitOnError)
		ref := fs.String("ref", "", "Branch to create (required; prevents accidental use of main)")
		fs.Parse(args[4:])

		if *ref == "" {
			log.Fatal("--ref is required for add-writespace to prevent accidental use of main")
		}

		// Validate owner/repo format
		parts := strings.SplitN(ownerRepo, "/", 2)
		if len(parts) != 2 {
			log.Fatalf("Invalid owner/repo format: %s (expected owner/repo)", ownerRepo)
		}
		owner, repo := parts[0], parts[1]

		// Get the source repo from writespaces
		repoMeta, err := writespaceMgr.Get(ownerRepo)
		if err != nil {
			log.Fatalf("Repository not found in writespaces: %s\nRun 'writespace repo clone %s' first", ownerRepo, ownerRepo)
		}

		// Add to slopspace
		if err := mgr.AddWritespaceRepo(slopspaceID, owner, repo, *ref, repoMeta.RootPath); err != nil {
			log.Fatalf("Failed to add writespace repo: %v", err)
		}
		fmt.Printf("Added %s to slopspace %s writespaces\n", ownerRepo, slopspaceID)
		fmt.Printf("  Created branch: %s\n", *ref)
		fmt.Println("  .git directory moved to writespaces-secure (will not be deployed to agent)")

	case "write":
		// slopspace write <id> all
		// slopspace write repo <id> <owner/repo>
		if len(args) < 2 {
			log.Fatal("Usage: slopspace write <slopspace-id> all | slopspace write repo <slopspace-id> <owner/repo>")
		}

		if args[1] == "all" {
			// slopspace write <id> all
			if len(args) < 3 {
				log.Fatal("Usage: slopspace write <slopspace-id> all")
			}
			slopspaceID := args[2]

			// Parse optional --message flag
			fs := flag.NewFlagSet("write-all", flag.ExitOnError)
			message := fs.String("message", "", "Commit message")
			fs.Parse(args[3:])

			if err := mgr.WriteAllRepoChanges(slopspaceID, *message); err != nil {
				log.Fatalf("Failed to write changes: %v", err)
			}
			fmt.Printf("Pushed changes from all writespace repos in slopspace %s\n", slopspaceID)
		} else if args[1] == "repo" {
			// slopspace write repo <id> <owner/repo>
			if len(args) < 4 {
				log.Fatal("Usage: slopspace write repo <slopspace-id> <owner/repo> [--message <msg>]")
			}
			slopspaceID := args[2]
			ownerRepo := args[3]

			// Parse optional --message flag
			fs := flag.NewFlagSet("write-repo", flag.ExitOnError)
			message := fs.String("message", "", "Commit message")
			fs.Parse(args[4:])

			parts := strings.SplitN(ownerRepo, "/", 2)
			if len(parts) != 2 {
				log.Fatalf("Invalid owner/repo format: %s", ownerRepo)
			}

			if err := mgr.WriteRepoChanges(slopspaceID, parts[0], parts[1], *message); err != nil {
				log.Fatalf("Failed to write changes: %v", err)
			}
			fmt.Printf("Pushed changes from %s in slopspace %s\n", ownerRepo, slopspaceID)
		} else {
			// Assume slopspace write <id> all format with id as args[1]
			slopspaceID := args[1]
			if len(args) < 3 || args[2] != "all" {
				log.Fatal("Usage: slopspace write <slopspace-id> all | slopspace write repo <slopspace-id> <owner/repo>")
			}

			// Parse optional --message flag
			fs := flag.NewFlagSet("write-all", flag.ExitOnError)
			message := fs.String("message", "", "Commit message")
			fs.Parse(args[3:])

			if err := mgr.WriteAllRepoChanges(slopspaceID, *message); err != nil {
				log.Fatalf("Failed to write changes: %v", err)
			}
			fmt.Printf("Pushed changes from all writespace repos in slopspace %s\n", slopspaceID)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown slopspace subcommand: %s\n", args[0])
		os.Exit(1)
	}

	// Silence unused variable warnings
	_ = readspaceMgr
	_ = writespaceMgr
}

func requiredSlopspaceID(args []string) (string, bool) {
	if len(args) < 2 || strings.HasPrefix(args[1], "-") {
		return "", false
	}
	return args[1], true
}

func runReadspace(cfg *types.Config, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "readspace subcommand required: repo clone, repo delete, repo list")
		os.Exit(1)
	}

	if args[0] != "repo" {
		fmt.Fprintln(os.Stderr, "readspace subcommand must be 'repo'")
		os.Exit(1)
	}

	mgr := readspace.NewManager(cfg)

	switch args[1] {
	case "clone":
		if len(args) < 3 {
			log.Fatal("Usage: readspace repo clone <owner/repo>")
		}
		ownerRepo := args[2]

		metadata, err := mgr.CloneRepo(ownerRepo)
		if err != nil {
			log.Fatalf("Failed to clone repository: %v", err)
		}
		fmt.Printf("Cloned %s/%s to readspaces\n", metadata.Owner, metadata.Repo)
		fmt.Printf("  Path: %s\n", metadata.RootPath)
		fmt.Printf("  Branch: %s\n", metadata.Branch)

	case "delete":
		if len(args) < 3 {
			log.Fatal("Usage: readspace repo delete <owner/repo>")
		}
		ownerRepo := args[2]

		if err := mgr.Delete(ownerRepo); err != nil {
			log.Fatalf("Failed to delete repository: %v", err)
		}
		fmt.Printf("Deleted %s from readspaces\n", ownerRepo)

	case "list":
		repos, err := mgr.List()
		if err != nil {
			log.Fatalf("Failed to list repositories: %v", err)
		}

		if len(repos) == 0 {
			fmt.Println("No repositories in readspaces")
			return
		}

		fmt.Printf("%-30s  %-12s  %-20s\n", "REPOSITORY", "BRANCH", "UPDATED")
		fmt.Println("------------------------------------------------------------------------")
		for _, r := range repos {
			fmt.Printf("%-30s  %-12s  %-20s\n",
				r.Owner+"/"+r.Repo,
				r.Branch,
				r.UpdatedAt.Format("2006-01-02 15:04:05"))
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown readspace repo subcommand: %s\n", args[1])
		os.Exit(1)
	}
}

func runWritespace(cfg *types.Config, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "writespace subcommand required: repo clone, repo delete, repo list")
		os.Exit(1)
	}

	if args[0] != "repo" {
		fmt.Fprintln(os.Stderr, "writespace subcommand must be 'repo'")
		os.Exit(1)
	}

	mgr := writespace.NewManager(cfg)

	switch args[1] {
	case "clone":
		if len(args) < 3 {
			log.Fatal("Usage: writespace repo clone <owner/repo>")
		}
		ownerRepo := args[2]

		metadata, err := mgr.CloneRepo(ownerRepo)
		if err != nil {
			log.Fatalf("Failed to clone repository: %v", err)
		}
		fmt.Printf("Cloned %s/%s to writespaces\n", metadata.Owner, metadata.Repo)
		fmt.Printf("  Path: %s\n", metadata.RootPath)
		fmt.Printf("  Branch: %s\n", metadata.Branch)

	case "delete":
		if len(args) < 3 {
			log.Fatal("Usage: writespace repo delete <owner/repo>")
		}
		ownerRepo := args[2]

		if err := mgr.Delete(ownerRepo); err != nil {
			log.Fatalf("Failed to delete repository: %v", err)
		}
		fmt.Printf("Deleted %s from writespaces\n", ownerRepo)

	case "list":
		repos, err := mgr.List()
		if err != nil {
			log.Fatalf("Failed to list repositories: %v", err)
		}

		if len(repos) == 0 {
			fmt.Println("No repositories in writespaces")
			return
		}

		fmt.Printf("%-30s  %-12s  %-20s\n", "REPOSITORY", "BRANCH", "UPDATED")
		fmt.Println("------------------------------------------------------------------------")
		for _, r := range repos {
			fmt.Printf("%-30s  %-12s  %-20s\n",
				r.Owner+"/"+r.Repo,
				r.Branch,
				r.UpdatedAt.Format("2006-01-02 15:04:05"))
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown writespace repo subcommand: %s\n", args[1])
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
	log.Printf("[%s] dungeon-keeper started (agent-type: %s)", w.workerID, w.config.AgentType)
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
