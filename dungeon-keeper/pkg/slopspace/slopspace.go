// Package slopspace handles slopspace lifecycle management.
package slopspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dungeon-keeper/pkg/gitauth"
	"dungeon-keeper/pkg/types"
	"dungeon-keeper/pkg/writespace"

	"github.com/google/uuid"
)

// Directory names within a slopspace.
const (
	ReadSpacesDir       = "readspaces"
	WriteSpacesDir      = "writespaces"
	WriteSpacesSecure   = "writespaces-secure" // Secure storage for .git dirs (not copied to /agent)
	MetadataFile        = "SLOPSPACE.json"
	AgentRecordsDir     = "agent-records"
	DTTImagesDir        = "dtt-images"
	DTTCanvasDir        = "dtt-canvas"
	ReposDir            = "repos"
	FilesDir            = "files"
)

// Manager handles slopspace lifecycle operations.
type Manager struct {
	config *types.Config
}

// NewManager creates a new slopspace manager.
func NewManager(cfg *types.Config) *Manager {
	return &Manager{config: cfg}
}

// Create creates a new slopspace.
// Note: Slopspaces are NOT tied to an agent type at creation time.
// The agent type is specified during Deploy().
func (m *Manager) Create() (*types.SlopspaceMetadata, error) {
	id := uuid.New().String()
	now := time.Now()

	rootPath := filepath.Join(m.config.SlopspacesDir, id)

	// Create the slopspace directory structure
	readSpacesPath := filepath.Join(rootPath, ReadSpacesDir)
	writeSpacesPath := filepath.Join(rootPath, WriteSpacesDir)

	dirs := []string{
		rootPath,
		readSpacesPath,
		filepath.Join(readSpacesPath, AgentRecordsDir),
		filepath.Join(readSpacesPath, DTTImagesDir),
		filepath.Join(readSpacesPath, ReposDir),
		filepath.Join(readSpacesPath, FilesDir),
		writeSpacesPath,
		filepath.Join(writeSpacesPath, AgentRecordsDir),
		filepath.Join(writeSpacesPath, DTTCanvasDir),
		filepath.Join(writeSpacesPath, ReposDir),
		filepath.Join(writeSpacesPath, FilesDir),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	metadata := &types.SlopspaceMetadata{
		Slopspace: types.Slopspace{
			ID:        id,
			CreatedAt: now,
			RootPath:  rootPath,
			Deployed:  false,
		},
		Iteration: 0,
	}

	// Write metadata file
	if err := m.writeMetadata(rootPath, metadata); err != nil {
		return nil, err
	}

	return metadata, nil
}

// Get retrieves the metadata for a slopspace by ID.
func (m *Manager) Get(id string) (*types.SlopspaceMetadata, error) {
	rootPath := filepath.Join(m.config.SlopspacesDir, id)
	return m.readMetadata(rootPath)
}

// List returns all slopspaces.
func (m *Manager) List() ([]*types.SlopspaceMetadata, error) {
	entries, err := os.ReadDir(m.config.SlopspacesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read slopspaces dir: %w", err)
	}

	var slopspaces []*types.SlopspaceMetadata
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		metadata, err := m.readMetadata(filepath.Join(m.config.SlopspacesDir, entry.Name()))
		if err != nil {
			// Skip invalid slopspaces
			continue
		}
		slopspaces = append(slopspaces, metadata)
	}

	return slopspaces, nil
}

// Deploy moves the slopspace contents to the agent workspace for the specified agent type.
// Metadata and sensitive files remain in the slopspaces directory.
func (m *Manager) Deploy(id string, agentType types.AgentType) error {
	metadata, err := m.Get(id)
	if err != nil {
		return fmt.Errorf("failed to get slopspace: %w", err)
	}

	if metadata.Deployed {
		return fmt.Errorf("slopspace %s is already deployed", id)
	}

	deployPath := m.config.DeployPathForAgentType(agentType)

	// Ensure deploy path exists
	if err := os.MkdirAll(deployPath, 0755); err != nil {
		return fmt.Errorf("failed to create deploy path: %w", err)
	}

	// Move readspaces and writespaces to deploy location
	srcReadSpaces := filepath.Join(metadata.RootPath, ReadSpacesDir)
	srcWriteSpaces := filepath.Join(metadata.RootPath, WriteSpacesDir)
	dstReadSpaces := filepath.Join(deployPath, ReadSpacesDir)
	dstWriteSpaces := filepath.Join(deployPath, WriteSpacesDir)

	// Remove any existing deployed content
	os.RemoveAll(dstReadSpaces)
	os.RemoveAll(dstWriteSpaces)

	// Move directories
	if err := os.Rename(srcReadSpaces, dstReadSpaces); err != nil {
		return fmt.Errorf("failed to move readspaces: %w", err)
	}

	if err := os.Rename(srcWriteSpaces, dstWriteSpaces); err != nil {
		// Try to roll back
		os.Rename(dstReadSpaces, srcReadSpaces)
		return fmt.Errorf("failed to move writespaces: %w", err)
	}

	// Write a marker file at deploy location for agent to identify context
	markerPath := filepath.Join(deployPath, "SLOPSPACE_ID")
	if err := os.WriteFile(markerPath, []byte(id), 0644); err != nil {
		return fmt.Errorf("failed to write marker file: %w", err)
	}

	// Update metadata
	metadata.Deployed = true
	metadata.Iteration++
	metadata.DeployPath = deployPath
	metadata.DeployedAgentType = agentType
	if err := m.writeMetadata(metadata.RootPath, metadata); err != nil {
		return fmt.Errorf("failed to update metadata: %w", err)
	}

	return nil
}

// Return moves the slopspace contents back from the agent workspace.
// Writespaces are moved back; readspaces are discarded and repopulated.
func (m *Manager) Return(id string) error {
	metadata, err := m.Get(id)
	if err != nil {
		return fmt.Errorf("failed to get slopspace: %w", err)
	}

	if !metadata.Deployed {
		return fmt.Errorf("slopspace %s is not deployed", id)
	}

	deployPath := metadata.DeployPath

	dstReadSpaces := filepath.Join(deployPath, ReadSpacesDir)
	dstWriteSpaces := filepath.Join(deployPath, WriteSpacesDir)
	srcReadSpaces := filepath.Join(metadata.RootPath, ReadSpacesDir)
	srcWriteSpaces := filepath.Join(metadata.RootPath, WriteSpacesDir)

	// Discard deployed readspaces (agent can't modify them meaningfully)
	os.RemoveAll(dstReadSpaces)

	// Move writespaces back to slopspace
	os.RemoveAll(srcWriteSpaces) // Remove any stale content
	if err := os.Rename(dstWriteSpaces, srcWriteSpaces); err != nil {
		return fmt.Errorf("failed to move writespaces back: %w", err)
	}

	// Recreate empty readspaces structure
	readSpaceDirs := []string{
		srcReadSpaces,
		filepath.Join(srcReadSpaces, AgentRecordsDir),
		filepath.Join(srcReadSpaces, DTTImagesDir),
		filepath.Join(srcReadSpaces, ReposDir),
		filepath.Join(srcReadSpaces, FilesDir),
	}
	for _, dir := range readSpaceDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to recreate readspaces dir %s: %w", dir, err)
		}
	}

	// Remove marker file
	os.Remove(filepath.Join(deployPath, "SLOPSPACE_ID"))

	// Update metadata
	metadata.Deployed = false
	metadata.DeployPath = ""
	if err := m.writeMetadata(metadata.RootPath, metadata); err != nil {
		return fmt.Errorf("failed to update metadata: %w", err)
	}

	return nil
}

// Delete removes a slopspace entirely.
func (m *Manager) Delete(id string) error {
	metadata, err := m.Get(id)
	if err != nil {
		return fmt.Errorf("failed to get slopspace: %w", err)
	}

	if metadata.Deployed {
		// Return it first
		if err := m.Return(id); err != nil {
			return fmt.Errorf("failed to return deployed slopspace: %w", err)
		}
	}

	return os.RemoveAll(metadata.RootPath)
}

// PopulateReadSpace copies content into a slopspace's read-space.
func (m *Manager) PopulateReadSpace(id string, subdir string, sourcePath string) error {
	metadata, err := m.Get(id)
	if err != nil {
		return err
	}

	if metadata.Deployed {
		return fmt.Errorf("cannot populate readspace while deployed")
	}

	destPath := filepath.Join(metadata.RootPath, ReadSpacesDir, subdir)
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create parent dir: %w", err)
	}

	return copyDirContents(sourcePath, destPath)
}

// PopulateWriteSpace copies content into a slopspace's write-space.
func (m *Manager) PopulateWriteSpace(id string, subdir string, sourcePath string) error {
	metadata, err := m.Get(id)
	if err != nil {
		return err
	}

	if metadata.Deployed {
		return fmt.Errorf("cannot populate writespace while deployed")
	}

	destPath := filepath.Join(metadata.RootPath, WriteSpacesDir, subdir)
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create parent dir: %w", err)
	}

	return copyDirContents(sourcePath, destPath)
}

// AddReadspaceRepo copies a repository from readspaces into a slopspace's readspaces/repos.
// The repository is copied, switched to the specified ref (branch/tag/commit), and the .git directory is deleted.
func (m *Manager) AddReadspaceRepo(id string, owner, repo string, ref string, sourceRepoPath string) error {
	metadata, err := m.Get(id)
	if err != nil {
		return err
	}

	if metadata.Deployed {
		return fmt.Errorf("cannot add readspace repo while deployed")
	}

	destPath := filepath.Join(metadata.RootPath, ReadSpacesDir, ReposDir, owner, repo)

	// Copy the repository
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create parent dir: %w", err)
	}

	if err := copyDirContents(sourceRepoPath, destPath); err != nil {
		return fmt.Errorf("failed to copy repository: %w", err)
	}

	// If ref is specified, switch to it
	if ref != "" {
		if err := gitCheckout(destPath, ref); err != nil {
			os.RemoveAll(destPath)
			return fmt.Errorf("failed to checkout ref %s: %w", ref, err)
		}
	}

	// Delete the .git directory - readspaces don't need git history
	gitDir := filepath.Join(destPath, ".git")
	if err := os.RemoveAll(gitDir); err != nil {
		return fmt.Errorf("failed to remove .git directory: %w", err)
	}

	return nil
}

// AddWritespaceRepo copies a repository from writespaces into a slopspace's writespaces/repos.
// The repository is copied, switched to the specified ref (must be a branch),
// and the .git directory is moved to writespaces-secure (not deployed to /agent).
func (m *Manager) AddWritespaceRepo(id string, owner, repo string, ref string, sourceRepoPath string) error {
	metadata, err := m.Get(id)
	if err != nil {
		return err
	}

	if metadata.Deployed {
		return fmt.Errorf("cannot add writespace repo while deployed")
	}

	destPath := filepath.Join(metadata.RootPath, WriteSpacesDir, ReposDir, owner, repo)
	secureGitPath := filepath.Join(metadata.RootPath, WriteSpacesSecure, ReposDir, owner, repo)

	// Copy the repository
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create parent dir: %w", err)
	}

	if err := copyDirContents(sourceRepoPath, destPath); err != nil {
		return fmt.Errorf("failed to copy repository: %w", err)
	}

	// The shared clone keeps its bookkeeping file in its own working tree;
	// left here it would be swept into the first commit WriteRepoChanges
	// makes on the slopspace branch.
	os.Remove(filepath.Join(destPath, writespace.MetadataFile))

	// Create and switch to the new branch (ref is required for writespaces).
	if ref != "" {
		if err := gitCheckoutNewBranch(destPath, ref); err != nil {
			os.RemoveAll(destPath)
			return fmt.Errorf("failed to create branch %s: %w", ref, err)
		}
	}

	// Move the .git directory to the secure location
	gitDir := filepath.Join(destPath, ".git")
	if err := os.MkdirAll(filepath.Dir(secureGitPath), 0755); err != nil {
		return fmt.Errorf("failed to create secure git parent dir: %w", err)
	}

	if err := os.Rename(gitDir, secureGitPath); err != nil {
		// If rename fails (cross-device), fall back to copy + delete
		if err := copyDirContents(gitDir, secureGitPath); err != nil {
			return fmt.Errorf("failed to move .git directory: %w", err)
		}
		os.RemoveAll(gitDir)
	}

	return nil
}

// RepoWriteResult is the outcome of writing one writespace repo's changes.
type RepoWriteResult struct {
	Repo    string `json:"repo"`    // owner/name
	SHA     string `json:"sha"`     // HEAD after the write: the new commit when Changed, else the existing HEAD
	Changed bool   `json:"changed"` // a commit was made and pushed
}

// WriteRepoChanges pushes changes from a slopspace's writespace repo back to the remote.
// The .git directory is restored from writespaces-secure, changes are committed and pushed.
// It reports the resulting HEAD so callers can link the commit (see
// docs/SlopspaceControlScheme.md, D6). The branch is pushed even with nothing to
// commit, so a commit left unpushed by an earlier failed write still reaches the
// remote; Changed is true only when this write made a commit. Commits pushed to
// the branch from outside the slopspace are rebased under the local ones first
// (see gitIntegrateRemote), so the push is a fast-forward.
func (m *Manager) WriteRepoChanges(id string, owner, repo string, commitMessage string) (RepoWriteResult, error) {
	result := RepoWriteResult{Repo: owner + "/" + repo}

	repoPath, restore, err := m.openWritespaceRepo(id, owner, repo, "write repo changes")
	if err != nil {
		return result, err
	}
	defer restore()

	// Add all changes
	if err := gitAddAll(repoPath); err != nil {
		return result, fmt.Errorf("failed to add changes: %w", err)
	}

	// Check if there are changes to commit
	hasChanges, err := gitHasChanges(repoPath)
	if err != nil {
		return result, fmt.Errorf("failed to check for changes: %w", err)
	}

	if hasChanges {
		if commitMessage == "" {
			commitMessage = "Automated commit from dungeon-keeper"
		}
		if err := gitCommit(repoPath, commitMessage); err != nil {
			return result, fmt.Errorf("failed to commit changes: %w", err)
		}
		result.Changed = true
	}

	// The rebase may rewrite the local (still unpushed) commits, so HEAD is read
	// after it.
	syncErr := gitIntegrateRemote(repoPath)
	result.SHA, _ = gitHead(repoPath)
	if syncErr != nil {
		result.Changed = false
		return result, fmt.Errorf("failed to sync with the remote: %w", syncErr)
	}

	// Push even with nothing new to commit: an earlier write may have committed
	// and then failed to push, and a retry would otherwise strand that commit.
	// When the remote is already up to date this does nothing.
	if err := gitPush(repoPath); err != nil {
		result.Changed = false
		return result, fmt.Errorf("failed to push changes: %w", err)
	}

	return result, nil
}

// SyncRepo brings commits pushed to a writespace repo's branch from outside the
// slopspace (a .ufabuild request, a hand-made fix) into the writespace, so the
// next agent turn starts from them and its write pushes cleanly. Uncommitted
// changes are carried across with --autostash. It returns the HEAD afterwards.
func (m *Manager) SyncRepo(id string, owner, repo string) (string, error) {
	repoPath, restore, err := m.openWritespaceRepo(id, owner, repo, "sync a repo")
	if err != nil {
		return "", err
	}
	defer restore()

	if err := gitIntegrateRemote(repoPath); err != nil {
		return "", fmt.Errorf("sync %s/%s: %w", owner, repo, err)
	}
	return gitHead(repoPath)
}

// openWritespaceRepo moves a writespace repo's .git back from writespaces-secure
// so git can run in its working tree. The returned restore puts it back; the
// caller must call it. what names the operation in the "while deployed" error.
func (m *Manager) openWritespaceRepo(id, owner, repo, what string) (repoPath string, restore func(), err error) {
	metadata, err := m.Get(id)
	if err != nil {
		return "", nil, err
	}

	if metadata.Deployed {
		return "", nil, fmt.Errorf("cannot %s while deployed - return the slopspace first", what)
	}

	repoPath = filepath.Join(metadata.RootPath, WriteSpacesDir, ReposDir, owner, repo)
	secureGitPath := filepath.Join(metadata.RootPath, WriteSpacesSecure, ReposDir, owner, repo)

	// Check if the repo exists
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		return "", nil, fmt.Errorf("writespace repo does not exist: %s/%s", owner, repo)
	}

	// Check if the secure git directory exists
	if _, err := os.Stat(secureGitPath); os.IsNotExist(err) {
		return "", nil, fmt.Errorf("secure git directory not found for: %s/%s", owner, repo)
	}

	// Temporarily restore .git directory
	gitDir := filepath.Join(repoPath, ".git")
	if err := os.Rename(secureGitPath, gitDir); err != nil {
		// Fall back to copy
		if err := copyDirContents(secureGitPath, gitDir); err != nil {
			return "", nil, fmt.Errorf("failed to restore .git directory: %w", err)
		}
	}

	// Cleanup: ensure .git goes back to secure location
	return repoPath, func() {
		if _, err := os.Stat(gitDir); err == nil {
			os.Rename(gitDir, secureGitPath)
		}
	}, nil
}

// WritespaceRepos lists the owner/name of every writespace repo in a slopspace.
// It reads writespaces-secure, which never moves, so it works while deployed too.
func (m *Manager) WritespaceRepos(id string) ([]string, error) {
	metadata, err := m.Get(id)
	if err != nil {
		return nil, err
	}

	reposPath := filepath.Join(metadata.RootPath, WriteSpacesSecure, ReposDir)
	owners, err := os.ReadDir(reposPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read repos directory: %w", err)
	}

	var repos []string
	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(reposPath, owner.Name()))
		if err != nil {
			continue
		}
		for _, repo := range entries {
			if repo.IsDir() {
				repos = append(repos, owner.Name()+"/"+repo.Name())
			}
		}
	}
	return repos, nil
}

// WriteAllRepoChanges pushes changes from all writespace repos in a slopspace. It
// writes every repo even when one fails, returning each repo's result and every
// error joined together.
func (m *Manager) WriteAllRepoChanges(id string, commitMessage string) ([]RepoWriteResult, error) {
	repos, err := m.WritespaceRepos(id)
	if err != nil {
		return nil, err
	}

	var results []RepoWriteResult
	var errs []error
	for _, ownerRepo := range repos {
		owner, repo, _ := strings.Cut(ownerRepo, "/")
		res, err := m.WriteRepoChanges(id, owner, repo, commitMessage)
		results = append(results, res)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ownerRepo, err))
		}
	}

	return results, errors.Join(errs...)
}

// RepoHead returns the HEAD commit of a slopspace writespace repo. It reads the
// .git dir in writespaces-secure directly, so it works while deployed too.
func (m *Manager) RepoHead(id string, owner, repo string) (string, error) {
	metadata, err := m.Get(id)
	if err != nil {
		return "", err
	}
	secureGitPath := filepath.Join(metadata.RootPath, WriteSpacesSecure, ReposDir, owner, repo)
	out, err := exec.Command("git", "--git-dir", secureGitPath, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// PushRepo pushes a slopspace writespace repo's branch as it stands, whether or
// not anything new was committed -- e.g. to publish a freshly created branch on
// every repo of a slopspace at once. It works while deployed too.
func (m *Manager) PushRepo(id string, owner, repo string) error {
	metadata, err := m.Get(id)
	if err != nil {
		return err
	}
	secureGitPath := filepath.Join(metadata.RootPath, WriteSpacesSecure, ReposDir, owner, repo)
	cmd := gitauth.Command("", "--git-dir", secureGitPath, "push", "--set-upstream", "origin", "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("push %s/%s: %w: %s", owner, repo, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitCheckout checks out an existing ref in a repository.
func gitCheckout(repoPath, ref string) error {
	cmd := newGitCommand(repoPath, "checkout", ref)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}
	return nil
}

// gitCheckoutNewBranch creates and checks out a new branch in a repository.
func gitCheckoutNewBranch(repoPath, branch string) error {
	cmd := newGitCommand(repoPath, "checkout", "-b", branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}
	return nil
}

// gitAddAll stages all changes.
func gitAddAll(repoPath string) error {
	cmd := newGitCommand(repoPath, "add", "-A")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}
	return nil
}

// gitHasChanges checks if there are staged changes.
func gitHasChanges(repoPath string) (bool, error) {
	cmd := newGitCommand(repoPath, "diff", "--cached", "--quiet")
	err := cmd.Run()
	if err != nil {
		// Exit code 1 means there are changes
		return true, nil
	}
	return false, nil
}

// gitCommit commits staged changes.
func gitCommit(repoPath, message string) error {
	cmd := newGitCommand(repoPath, "commit", "-m", message)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}
	return nil
}

// gitPush pushes changes to remote, setting upstream if the branch has none.
func gitPush(repoPath string) error {
	cmd := newGitCommand(repoPath, "push", "--set-upstream", "origin", "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}
	return nil
}

// gitIntegrateRemote rebases the current branch's local commits onto whatever
// has been pushed to origin's copy of the branch from elsewhere, so that a
// following push is a fast-forward. Only unpushed commits are rewritten; the
// remote's history never is. A branch not on the remote yet, or one the local
// branch already contains, is left alone. On a conflict the rebase is aborted,
// leaving the repo as it was, and the error says so.
func gitIntegrateRemote(repoPath string) error {
	out, err := newGitCommand(repoPath, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("HEAD isn't on a branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))

	// --exit-code makes ls-remote exit 2 when the branch isn't there.
	cmd := newGitCommand(repoPath, "ls-remote", "--exit-code", "--heads", "origin", "refs/heads/"+branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			return nil
		}
		return fmt.Errorf("ls-remote: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := newGitCommand(repoPath, "fetch", "-q", "origin", "refs/heads/"+branch).CombinedOutput(); err != nil {
		return fmt.Errorf("fetch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if newGitCommand(repoPath, "merge-base", "--is-ancestor", "FETCH_HEAD", "HEAD").Run() == nil {
		return nil
	}
	if out, err := newGitCommand(repoPath, "rebase", "--autostash", "FETCH_HEAD").CombinedOutput(); err != nil {
		newGitCommand(repoPath, "rebase", "--abort").Run()
		return fmt.Errorf("origin/%s has commits that conflict with the slopspace's unpushed ones; "+
			"rebase them by hand in the slopspace's writespaces-secure git dir (see docs/SlopspaceControlScheme.md): %w: %s",
			branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitHead returns a repository's HEAD commit.
func gitHead(repoPath string) (string, error) {
	out, err := newGitCommand(repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// newGitCommand creates a new git command with the PAT supplied as an auth
// header (see gitauth), so pushes authenticate without the token ever being
// written into the repo.
func newGitCommand(repoPath string, args ...string) *exec.Cmd {
	return gitauth.Command(repoPath, args...)
}

// GetDeployedID returns the ID of the currently deployed slopspace for the given agent type, if any.
func (m *Manager) GetDeployedID(agentType types.AgentType) (string, error) {
	deployPath := m.config.DeployPathForAgentType(agentType)
	markerPath := filepath.Join(deployPath, "SLOPSPACE_ID")
	data, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// GetDeployedIDForCurrentAgent returns the deployed slopspace ID for the config's agent type.
func (m *Manager) GetDeployedIDForCurrentAgent() (string, error) {
	return m.GetDeployedID(m.config.AgentType)
}

// readMetadata reads slopspace metadata from disk.
func (m *Manager) readMetadata(rootPath string) (*types.SlopspaceMetadata, error) {
	metadataPath := filepath.Join(rootPath, MetadataFile)
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var metadata types.SlopspaceMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	return &metadata, nil
}

// writeMetadata writes slopspace metadata to disk.
func (m *Manager) writeMetadata(rootPath string, metadata *types.SlopspaceMetadata) error {
	metadataPath := filepath.Join(rootPath, MetadataFile)
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Ensure proper JSON termination with newline
	data = append(data, '\n')

	if err := os.WriteFile(metadataPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

// copyDirContents copies the contents of src directory to dst.
func copyDirContents(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if !srcInfo.IsDir() {
		// It's a file, copy directly
		return copyFile(src, dst)
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.Type()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(linkTarget, dstPath); err != nil {
				return err
			}
		} else if entry.IsDir() {
			if err := copyDirContents(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// copyFile copies a single file from src to dst.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
