// Package executor provides clauditable-wrapped command execution.
package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"dungeon-keeper/pkg/types"
)

// ClauditableBinary is the name of the clauditable binary.
const ClauditableBinary = "clauditable"

// AmbiguousAgentBinary is the name of the ambiguous-agent binary.
const AmbiguousAgentBinary = "ambiguous-agent"

// Executor wraps commands with clauditable for recording.
type Executor struct {
	config          *types.Config
	clauditablePath string
}

// NewExecutor creates a new executor.
// It searches for clauditable in PATH and current directory.
func NewExecutor(cfg *types.Config) (*Executor, error) {
	clauditablePath, err := findClauditable()
	if err != nil {
		return nil, fmt.Errorf("clauditable not found: %w", err)
	}

	return &Executor{
		config:          cfg,
		clauditablePath: clauditablePath,
	}, nil
}

// findClauditable searches for the clauditable binary.
func findClauditable() (string, error) {
	// Check if we're already in a clauditable context
	if os.Getenv("CLAUDITABLE_ALREADY_ACTIVE") == "true" {
		// We're nested - return empty to signal no wrapping needed
		return "", nil
	}

	path, err := findBinary(ClauditableBinary)
	if err != nil {
		return "", fmt.Errorf("clauditable binary not found in PATH or current directory")
	}
	return path, nil
}

// Run executes a command wrapped with clauditable.
// If we're already in a clauditable context, runs the command directly.
func (e *Executor) Run(name string, args ...string) error {
	cmd := e.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// RunWithOutput executes a command and returns its combined output.
func (e *Executor) RunWithOutput(name string, args ...string) ([]byte, error) {
	cmd := e.Command(name, args...)
	return cmd.CombinedOutput()
}

// Command creates an exec.Cmd wrapped with clauditable.
func (e *Executor) Command(name string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd

	if e.clauditablePath == "" {
		// Already in clauditable context, run directly
		cmd = exec.Command(name, args...)
	} else {
		// Wrap with clauditable
		allArgs := append([]string{name}, args...)
		cmd = exec.Command(e.clauditablePath, allArgs...)
	}

	// Set up environment for agent records
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("AGENT_RECORDS_PATH=%s", e.config.AgentRecordsPath),
	)

	return cmd
}

// InvokeAgent invokes an agent via ambiguous-agent.
// Supports modes: prompt (p), read (r), write (w), execute (x/e)
func (e *Executor) InvokeAgent(agent, model, mode, prompt, workdir string) error {
	args := []string{
		"-a", agent,
		"-m", model,
	}

	switch mode {
	case "execute", "e", "x":
		args = append(args, "-x", prompt)
	case "write", "w":
		args = append(args, "-w", prompt)
	case "read", "r":
		args = append(args, "-r", prompt)
	case "prompt", "p":
		args = append(args, "-p", prompt)
	default:
		// Default to execute for backwards compatibility
		args = append(args, "-x", prompt)
	}

	// Find ambiguous-agent
	ambiguousAgentPath, err := findBinary(AmbiguousAgentBinary)
	if err != nil {
		return fmt.Errorf("ambiguous-agent not found: %w", err)
	}

	cmd := e.Command(ambiguousAgentPath, args...)
	cmd.Env = append(cmd.Env, agentRecordEnv(agent, model)...)
	cmd.Dir = workdir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

// InvokeAgentWithCapture invokes an agent and captures output.
// Supports modes: prompt (p), read (r), write (w), execute (x/e)
func (e *Executor) InvokeAgentWithCapture(agent, model, mode, prompt, workdir string) ([]byte, error) {
	args := []string{
		"-a", agent,
		"-m", model,
	}

	switch mode {
	case "execute", "e", "x":
		args = append(args, "-x", prompt)
	case "write", "w":
		args = append(args, "-w", prompt)
	case "read", "r":
		args = append(args, "-r", prompt)
	case "prompt", "p":
		args = append(args, "-p", prompt)
	default:
		// Default to execute for backwards compatibility
		args = append(args, "-x", prompt)
	}

	// Find ambiguous-agent
	ambiguousAgentPath, err := findBinary(AmbiguousAgentBinary)
	if err != nil {
		return nil, fmt.Errorf("ambiguous-agent not found: %w", err)
	}

	cmd := e.Command(ambiguousAgentPath, args...)
	cmd.Env = append(cmd.Env, agentRecordEnv(agent, model)...)
	cmd.Dir = workdir

	return cmd.CombinedOutput()
}

// agentRecordEnv names the agent and model for clauditable's record of an
// agent run. The outer clauditable (from Command) is the one that records it --
// ambiguous-agent's own clauditable passes straight through -- so without
// these it would log the run as a plain command_execution with no agent.
// UFA_EVENT_TYPE covers a signal that leaves the agent to ambiguous-agent's
// default.
func agentRecordEnv(agent, model string) []string {
	env := []string{"UFA_EVENT_TYPE=ai_invocation", "UFA_AGENT=" + agent}
	if model != "" {
		env = append(env, "UFA_MODEL="+model)
	}
	return env
}

// findBinary searches for a binary in PATH, current directory, and executable directory.
// Local candidates must be executable regular files: run from the repo root,
// "clauditable" and "ambiguous-agent" name source directories, not binaries.
func findBinary(name string) (string, error) {
	// Check current directory first
	cwd, _ := os.Getwd()
	localPath := filepath.Join(cwd, name)
	if isExecutableFile(localPath) {
		return localPath, nil
	}

	// Check directory of executable (for deployed binaries)
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		localPath := filepath.Join(exeDir, name)
		if isExecutableFile(localPath) {
			return localPath, nil
		}
	}

	// Check PATH
	path, err := exec.LookPath(name)
	if err == nil {
		return path, nil
	}

	return "", fmt.Errorf("binary %s not found", name)
}

// isExecutableFile reports whether path is a regular file with an execute bit set.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

// IsClauditableWrapped returns true if commands are being wrapped.
func (e *Executor) IsClauditableWrapped() bool {
	return e.clauditablePath != ""
}

// ExecutorOption is a functional option for configuring the executor.
type ExecutorOption func(*Executor)

// WithClauditablePath sets a specific path for clauditable.
func WithClauditablePath(path string) ExecutorOption {
	return func(e *Executor) {
		e.clauditablePath = path
	}
}

// NewExecutorWithOptions creates an executor with options.
func NewExecutorWithOptions(cfg *types.Config, opts ...ExecutorOption) *Executor {
	e := &Executor{
		config: cfg,
	}

	// Try to find clauditable but don't fail
	e.clauditablePath, _ = findClauditable()

	for _, opt := range opts {
		opt(e)
	}

	return e
}

// FormatPromptForAgent formats a prompt for agent invocation.
func FormatPromptForAgent(signal *types.WorkSignal, workdir string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Role: %s\n\n", signal.Role))
	sb.WriteString(fmt.Sprintf("Working Directory: %s\n\n", workdir))
	sb.WriteString("Instructions:\n")
	sb.WriteString(signal.Prompt)

	return sb.String()
}

// CheckDependencies verifies that required dependencies are available and prints version info.
func CheckDependencies() error {
	// Check ambiguous-agent
	ambiguousAgentPath, err := findBinary(AmbiguousAgentBinary)
	if err != nil {
		return fmt.Errorf("ambiguous-agent not found: %w", err)
	}
	fmt.Printf("Found ambiguous-agent at: %s\n", ambiguousAgentPath)

	// Try to get version
	cmd := exec.Command(ambiguousAgentPath, "--help")
	output, err := cmd.CombinedOutput()
	if err == nil && len(output) > 0 {
		// Just confirm it runs
		fmt.Println("ambiguous-agent is executable")
	}

	// Check clauditable
	clauditablePath, err := findClauditable()
	if err != nil {
		fmt.Println("clauditable not found (will run without wrapping if CLAUDITABLE_ALREADY_ACTIVE is set)")
	} else if clauditablePath == "" {
		fmt.Println("Running in clauditable context (CLAUDITABLE_ALREADY_ACTIVE=true)")
	} else {
		fmt.Printf("Found clauditable at: %s\n", clauditablePath)
	}

	return nil
}
