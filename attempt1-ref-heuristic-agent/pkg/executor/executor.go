// Package executor provides clauditable-wrapped command execution.
package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"heuristic-agent/pkg/types"
)

// ClauditableBinary is the name of the clauditable binary.
const ClauditableBinary = "clauditable"

// Executor wraps commands with clauditable for recording.
type Executor struct {
	config        *types.Config
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
		config:        cfg,
		clauditablePath: clauditablePath,
	}, nil
}

// findClauditable searches for the clauditable binary.
func findClauditable() (string, error) {
	// Check if we're already in a clauditable context
	if os.Getenv("IS_CLAUDITABLE") == "true" {
		// We're nested - return empty to signal no wrapping needed
		return "", nil
	}

	// Check current directory first
	cwd, _ := os.Getwd()
	localPath := filepath.Join(cwd, ClauditableBinary)
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}

	// Check PATH
	path, err := exec.LookPath(ClauditableBinary)
	if err == nil {
		return path, nil
	}

	return "", fmt.Errorf("clauditable binary not found in PATH or current directory")
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
func (e *Executor) InvokeAgent(agent, model, mode, prompt, workdir string) error {
	args := []string{
		"--agent", agent,
		"--model", model,
	}

	switch mode {
	case "execute", "e":
		args = append(args, "-e", prompt)
	case "prompt", "p":
		args = append(args, "-p", prompt)
	case "read", "r":
		args = append(args, "-r", prompt)
	default:
		args = append(args, "-e", prompt)
	}

	// Find ambiguous-agent
	ambiguousAgentPath, err := findBinary("ambiguous-agent")
	if err != nil {
		return fmt.Errorf("ambiguous-agent not found: %w", err)
	}

	cmd := e.Command(ambiguousAgentPath, args...)
	cmd.Dir = workdir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

// InvokeAgentWithCapture invokes an agent and captures output.
func (e *Executor) InvokeAgentWithCapture(agent, model, mode, prompt, workdir string) ([]byte, error) {
	args := []string{
		"--agent", agent,
		"--model", model,
	}

	switch mode {
	case "execute", "e":
		args = append(args, "-e", prompt)
	case "prompt", "p":
		args = append(args, "-p", prompt)
	case "read", "r":
		args = append(args, "-r", prompt)
	default:
		args = append(args, "-e", prompt)
	}

	// Find ambiguous-agent
	ambiguousAgentPath, err := findBinary("ambiguous-agent")
	if err != nil {
		return nil, fmt.Errorf("ambiguous-agent not found: %w", err)
	}

	cmd := e.Command(ambiguousAgentPath, args...)
	cmd.Dir = workdir

	return cmd.CombinedOutput()
}

// findBinary searches for a binary in PATH and current directory.
func findBinary(name string) (string, error) {
	// Check current directory first
	cwd, _ := os.Getwd()
	localPath := filepath.Join(cwd, name)
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}

	// Check PATH
	path, err := exec.LookPath(name)
	if err == nil {
		return path, nil
	}

	return "", fmt.Errorf("binary %s not found", name)
}

// RunClod invokes the clod deterministic tool.
func (e *Executor) RunClod(permissionMode, prompt string) error {
	clodPath, err := findBinary("clod")
	if err != nil {
		return fmt.Errorf("clod not found: %w", err)
	}

	args := []string{
		"--permission-mode", permissionMode,
		"-p", prompt,
	}

	return e.Run(clodPath, args...)
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
