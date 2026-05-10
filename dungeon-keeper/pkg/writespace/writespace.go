// Package writespace handles writespace lifecycle management for repository clones.
package writespace

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dungeon-keeper/pkg/types"
)

const (
	// ReposDir is the subdirectory for repository writespaces.
	ReposDir     = "repos"
	MetadataFile = "WRITESPACE.json"
)

// RepoMetadata stores information about a cloned repository writespace.
type RepoMetadata struct {
	Owner     string    `json:"owner"`
	Repo      string    `json:"repo"`
	ClonedAt  time.Time `json:"cloned_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Branch    string    `json:"branch"` // Current branch (usually main)
	RootPath  string    `json:"root_path"`
}

// Manager handles writespace lifecycle operations.
type Manager struct {
	config *types.Config
}

// NewManager creates a new writespace manager.
func NewManager(cfg *types.Config) *Manager {
	return &Manager{config: cfg}
}

// WritespacesDir returns the base writespaces directory.
func (m *Manager) WritespacesDir() string {
	// Writespaces dir is beside slopspaces dir
	return filepath.Join(filepath.Dir(m.config.SlopspacesDir), "writespaces")
}

// ReposPath returns the path to the repos subdirectory.
func (m *Manager) ReposPath() string {
	return filepath.Join(m.WritespacesDir(), ReposDir)
}

// RepoPath returns the path to a specific repo writespace.
func (m *Manager) RepoPath(owner, repo string) string {
	return filepath.Join(m.ReposPath(), owner, repo)
}

// CloneRepo clones a repository into the writespaces directory.
// Uses the TF_VAR_github_pat environment variable for authentication.
func (m *Manager) CloneRepo(ownerRepo string) (*RepoMetadata, error) {
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid owner/repo format: %s (expected owner/repo)", ownerRepo)
	}
	owner, repo := parts[0], parts[1]

	repoPath := m.RepoPath(owner, repo)

	// Check if already exists
	if _, err := os.Stat(repoPath); err == nil {
		// Repo exists, do a pull --rebase
		return m.updateRepo(owner, repo)
	}

	// Ensure parent directories exist
	if err := os.MkdirAll(filepath.Dir(repoPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create parent directory: %w", err)
	}

	// Get GitHub PAT from environment
	pat := os.Getenv("TF_VAR_github_pat")
	if pat == "" {
		return nil, fmt.Errorf("TF_VAR_github_pat environment variable not set")
	}

	// Clone using git with PAT authentication
	cloneURL := fmt.Sprintf("https://%s@github.com/%s/%s.git", pat, owner, repo)
	cmd := exec.Command("git", "clone", cloneURL, repoPath)
	cmd.Env = append(os.Environ(), fmt.Sprintf("GH_TOKEN=%s", pat))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to clone repository: %w\nOutput: %s", err, string(output))
	}

	// Get the default branch
	branch, err := m.getDefaultBranch(repoPath)
	if err != nil {
		branch = "main" // Fallback
	}

	now := time.Now()
	metadata := &RepoMetadata{
		Owner:     owner,
		Repo:      repo,
		ClonedAt:  now,
		UpdatedAt: now,
		Branch:    branch,
		RootPath:  repoPath,
	}

	// Write metadata
	if err := m.writeMetadata(repoPath, metadata); err != nil {
		return nil, err
	}

	return metadata, nil
}

// updateRepo updates an existing repo with pull --rebase.
func (m *Manager) updateRepo(owner, repo string) (*RepoMetadata, error) {
	repoPath := m.RepoPath(owner, repo)

	// Read existing metadata
	metadata, err := m.readMetadata(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	// Pull --rebase
	cmd := exec.Command("git", "-C", repoPath, "pull", "--rebase")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to pull --rebase: %w\nOutput: %s", err, string(output))
	}

	// Update metadata
	metadata.UpdatedAt = time.Now()
	if err := m.writeMetadata(repoPath, metadata); err != nil {
		return nil, err
	}

	return metadata, nil
}

// Get retrieves metadata for a repo writespace.
func (m *Manager) Get(ownerRepo string) (*RepoMetadata, error) {
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid owner/repo format: %s", ownerRepo)
	}
	return m.readMetadata(m.RepoPath(parts[0], parts[1]))
}

// List returns all repo writespaces.
func (m *Manager) List() ([]*RepoMetadata, error) {
	reposPath := m.ReposPath()
	if _, err := os.Stat(reposPath); os.IsNotExist(err) {
		return nil, nil
	}

	var repos []*RepoMetadata

	// Walk through owner directories
	owners, err := os.ReadDir(reposPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read repos directory: %w", err)
	}

	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		ownerPath := filepath.Join(reposPath, owner.Name())
		repoEntries, err := os.ReadDir(ownerPath)
		if err != nil {
			continue
		}

		for _, repoEntry := range repoEntries {
			if !repoEntry.IsDir() {
				continue
			}
			repoPath := filepath.Join(ownerPath, repoEntry.Name())
			metadata, err := m.readMetadata(repoPath)
			if err != nil {
				continue
			}
			repos = append(repos, metadata)
		}
	}

	return repos, nil
}

// Delete removes a repo writespace.
func (m *Manager) Delete(ownerRepo string) error {
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid owner/repo format: %s", ownerRepo)
	}
	repoPath := m.RepoPath(parts[0], parts[1])

	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		return fmt.Errorf("repo writespace does not exist: %s", ownerRepo)
	}

	if err := os.RemoveAll(repoPath); err != nil {
		return fmt.Errorf("failed to delete repo writespace: %w", err)
	}

	// Try to clean up empty owner directory
	ownerPath := filepath.Dir(repoPath)
	entries, _ := os.ReadDir(ownerPath)
	if len(entries) == 0 {
		os.Remove(ownerPath)
	}

	return nil
}

// getDefaultBranch gets the default branch name for a repo.
func (m *Manager) getDefaultBranch(repoPath string) (string, error) {
	cmd := exec.Command("git", "-C", repoPath, "symbolic-ref", "--short", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// readMetadata reads repo writespace metadata from disk.
func (m *Manager) readMetadata(repoPath string) (*RepoMetadata, error) {
	metadataPath := filepath.Join(repoPath, MetadataFile)
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var metadata RepoMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	return &metadata, nil
}

// writeMetadata writes repo writespace metadata to disk.
func (m *Manager) writeMetadata(repoPath string, metadata *RepoMetadata) error {
	metadataPath := filepath.Join(repoPath, MetadataFile)
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	data = append(data, '\n')

	if err := os.WriteFile(metadataPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}
