package slopspace

import (
	"os"
	"path/filepath"
	"testing"

	"heuristic-agent/pkg/types"
)

func TestManagerCreate(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = tmpDir
	cfg.AgentSlopspaceRoot = filepath.Join(tmpDir, "agent")

	mgr := NewManager(cfg)

	// Create without specifying agent type (key difference from attempt1)
	metadata, err := mgr.Create()
	if err != nil {
		t.Fatalf("failed to create slopspace: %v", err)
	}

	if metadata.ID == "" {
		t.Error("expected non-empty ID")
	}
	if metadata.Deployed {
		t.Error("expected deployed to be false")
	}
	if metadata.Iteration != 0 {
		t.Errorf("expected iteration 0, got %d", metadata.Iteration)
	}
	// Agent type should be empty at creation
	if metadata.DeployedAgentType != "" {
		t.Errorf("expected empty agent type at creation, got %s", metadata.DeployedAgentType)
	}

	// Check directory structure
	expectedDirs := []string{
		filepath.Join(metadata.RootPath, ReadSpacesDir),
		filepath.Join(metadata.RootPath, ReadSpacesDir, AgentRecordsDir),
		filepath.Join(metadata.RootPath, ReadSpacesDir, DTTImagesDir),
		filepath.Join(metadata.RootPath, ReadSpacesDir, ReposDir),
		filepath.Join(metadata.RootPath, ReadSpacesDir, FilesDir),
		filepath.Join(metadata.RootPath, WriteSpacesDir),
		filepath.Join(metadata.RootPath, WriteSpacesDir, AgentRecordsDir),
		filepath.Join(metadata.RootPath, WriteSpacesDir, DTTCanvasDir),
		filepath.Join(metadata.RootPath, WriteSpacesDir, ReposDir),
		filepath.Join(metadata.RootPath, WriteSpacesDir, FilesDir),
	}

	for _, dir := range expectedDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("expected directory to exist: %s", dir)
		}
	}

	// Check metadata file
	metadataPath := filepath.Join(metadata.RootPath, MetadataFile)
	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		t.Error("expected metadata file to exist")
	}
}

func TestManagerGetAndList(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = tmpDir
	cfg.AgentSlopspaceRoot = filepath.Join(tmpDir, "agent")

	mgr := NewManager(cfg)

	// Create multiple slopspaces
	metadata1, err := mgr.Create()
	if err != nil {
		t.Fatalf("failed to create slopspace 1: %v", err)
	}

	metadata2, err := mgr.Create()
	if err != nil {
		t.Fatalf("failed to create slopspace 2: %v", err)
	}

	// Get by ID
	retrieved, err := mgr.Get(metadata1.ID)
	if err != nil {
		t.Fatalf("failed to get slopspace: %v", err)
	}
	if retrieved.ID != metadata1.ID {
		t.Errorf("ID mismatch: expected %s, got %s", metadata1.ID, retrieved.ID)
	}

	// List all
	all, err := mgr.List()
	if err != nil {
		t.Fatalf("failed to list slopspaces: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 slopspaces, got %d", len(all))
	}

	// Verify both are present
	ids := make(map[string]bool)
	for _, s := range all {
		ids[s.ID] = true
	}
	if !ids[metadata1.ID] {
		t.Error("metadata1 not found in list")
	}
	if !ids[metadata2.ID] {
		t.Error("metadata2 not found in list")
	}
}

func TestManagerDeployAndReturn(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = tmpDir
	cfg.AgentSlopspaceRoot = filepath.Join(tmpDir, "agent")
	cfg.AgentType = types.AgentTypeWorker

	mgr := NewManager(cfg)

	// Create slopspace without agent type
	metadata, err := mgr.Create()
	if err != nil {
		t.Fatalf("failed to create slopspace: %v", err)
	}

	// Create a test file in write-spaces
	testFile := filepath.Join(metadata.RootPath, WriteSpacesDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Deploy with agent type specified
	if err := mgr.Deploy(metadata.ID, types.AgentTypeWorker); err != nil {
		t.Fatalf("failed to deploy: %v", err)
	}

	// Check deployed state
	metadata, err = mgr.Get(metadata.ID)
	if err != nil {
		t.Fatalf("failed to get metadata after deploy: %v", err)
	}
	if !metadata.Deployed {
		t.Error("expected deployed to be true")
	}
	if metadata.Iteration != 1 {
		t.Errorf("expected iteration 1, got %d", metadata.Iteration)
	}
	if metadata.DeployedAgentType != types.AgentTypeWorker {
		t.Errorf("expected DeployedAgentType %s, got %s", types.AgentTypeWorker, metadata.DeployedAgentType)
	}

	// Check files are at deploy location
	deployPath := cfg.DeployPathForAgentType(types.AgentTypeWorker)
	deployedFile := filepath.Join(deployPath, WriteSpacesDir, "test.txt")
	if _, err := os.Stat(deployedFile); os.IsNotExist(err) {
		t.Error("expected test file to be deployed")
	}

	// Check marker file
	markerFile := filepath.Join(deployPath, "SLOPSPACE_ID")
	data, err := os.ReadFile(markerFile)
	if err != nil {
		t.Fatalf("failed to read marker file: %v", err)
	}
	if string(data) != metadata.ID {
		t.Errorf("expected marker to contain ID %s, got %s", metadata.ID, string(data))
	}

	// Check GetDeployedID
	deployedID, err := mgr.GetDeployedID(types.AgentTypeWorker)
	if err != nil {
		t.Fatalf("failed to get deployed ID: %v", err)
	}
	if deployedID != metadata.ID {
		t.Errorf("expected deployed ID %s, got %s", metadata.ID, deployedID)
	}

	// Modify file at deploy location
	if err := os.WriteFile(deployedFile, []byte("modified content"), 0644); err != nil {
		t.Fatalf("failed to modify deployed file: %v", err)
	}

	// Return
	if err := mgr.Return(metadata.ID); err != nil {
		t.Fatalf("failed to return: %v", err)
	}

	// Check returned state
	metadata, err = mgr.Get(metadata.ID)
	if err != nil {
		t.Fatalf("failed to get metadata after return: %v", err)
	}
	if metadata.Deployed {
		t.Error("expected deployed to be false")
	}

	// Check file is back with modifications
	returnedFile := filepath.Join(metadata.RootPath, WriteSpacesDir, "test.txt")
	data, err = os.ReadFile(returnedFile)
	if err != nil {
		t.Fatalf("failed to read returned file: %v", err)
	}
	if string(data) != "modified content" {
		t.Errorf("expected modified content, got %s", string(data))
	}

	// Check GetDeployedID returns empty
	deployedID, err = mgr.GetDeployedID(types.AgentTypeWorker)
	if err != nil {
		t.Fatalf("failed to get deployed ID after return: %v", err)
	}
	if deployedID != "" {
		t.Errorf("expected empty deployed ID, got %s", deployedID)
	}
}

func TestManagerDeployToDifferentAgentTypes(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = tmpDir
	cfg.AgentSlopspaceRoot = filepath.Join(tmpDir, "agent")

	mgr := NewManager(cfg)

	// Create two slopspaces
	metadata1, err := mgr.Create()
	if err != nil {
		t.Fatalf("failed to create slopspace 1: %v", err)
	}
	metadata2, err := mgr.Create()
	if err != nil {
		t.Fatalf("failed to create slopspace 2: %v", err)
	}

	// Deploy one to agent-worker
	if err := mgr.Deploy(metadata1.ID, types.AgentTypeWorker); err != nil {
		t.Fatalf("failed to deploy to agent-worker: %v", err)
	}

	// Deploy another to heuristic-request
	if err := mgr.Deploy(metadata2.ID, types.AgentTypeHeuristic); err != nil {
		t.Fatalf("failed to deploy to heuristic-request: %v", err)
	}

	// Check both are deployed to their respective locations
	workerID, err := mgr.GetDeployedID(types.AgentTypeWorker)
	if err != nil {
		t.Fatalf("failed to get worker deployed ID: %v", err)
	}
	if workerID != metadata1.ID {
		t.Errorf("expected worker deployed ID %s, got %s", metadata1.ID, workerID)
	}

	heuristicID, err := mgr.GetDeployedID(types.AgentTypeHeuristic)
	if err != nil {
		t.Fatalf("failed to get heuristic deployed ID: %v", err)
	}
	if heuristicID != metadata2.ID {
		t.Errorf("expected heuristic deployed ID %s, got %s", metadata2.ID, heuristicID)
	}

	// Clean up
	if err := mgr.Return(metadata1.ID); err != nil {
		t.Fatalf("failed to return metadata1: %v", err)
	}
	if err := mgr.Return(metadata2.ID); err != nil {
		t.Fatalf("failed to return metadata2: %v", err)
	}
}

func TestManagerDeployAlreadyDeployed(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = tmpDir
	cfg.AgentSlopspaceRoot = filepath.Join(tmpDir, "agent")
	cfg.AgentType = types.AgentTypeWorker

	mgr := NewManager(cfg)

	metadata, err := mgr.Create()
	if err != nil {
		t.Fatalf("failed to create slopspace: %v", err)
	}

	// Deploy first time
	if err := mgr.Deploy(metadata.ID, types.AgentTypeWorker); err != nil {
		t.Fatalf("failed to deploy: %v", err)
	}

	// Try to deploy again
	err = mgr.Deploy(metadata.ID, types.AgentTypeWorker)
	if err == nil {
		t.Error("expected error when deploying already deployed slopspace")
	}
}

func TestManagerDelete(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = tmpDir
	cfg.AgentSlopspaceRoot = filepath.Join(tmpDir, "agent")
	cfg.AgentType = types.AgentTypeWorker

	mgr := NewManager(cfg)

	metadata, err := mgr.Create()
	if err != nil {
		t.Fatalf("failed to create slopspace: %v", err)
	}

	// Delete
	if err := mgr.Delete(metadata.ID); err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// Check it's gone
	if _, err := os.Stat(metadata.RootPath); !os.IsNotExist(err) {
		t.Error("expected slopspace directory to be deleted")
	}

	// Try to get it
	_, err = mgr.Get(metadata.ID)
	if err == nil {
		t.Error("expected error when getting deleted slopspace")
	}
}

func TestManagerDeleteDeployed(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = tmpDir
	cfg.AgentSlopspaceRoot = filepath.Join(tmpDir, "agent")
	cfg.AgentType = types.AgentTypeWorker

	mgr := NewManager(cfg)

	metadata, err := mgr.Create()
	if err != nil {
		t.Fatalf("failed to create slopspace: %v", err)
	}

	// Deploy
	if err := mgr.Deploy(metadata.ID, types.AgentTypeWorker); err != nil {
		t.Fatalf("failed to deploy: %v", err)
	}

	// Delete should return it first, then delete
	if err := mgr.Delete(metadata.ID); err != nil {
		t.Fatalf("failed to delete deployed slopspace: %v", err)
	}

	// Check it's gone
	if _, err := os.Stat(metadata.RootPath); !os.IsNotExist(err) {
		t.Error("expected slopspace directory to be deleted")
	}
}

func TestManagerPopulateSpaces(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = tmpDir
	cfg.AgentSlopspaceRoot = filepath.Join(tmpDir, "agent")

	mgr := NewManager(cfg)

	metadata, err := mgr.Create()
	if err != nil {
		t.Fatalf("failed to create slopspace: %v", err)
	}

	// Create source directory with content
	srcDir := filepath.Join(tmpDir, "source")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("failed to create source dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	// Populate read-space
	if err := mgr.PopulateReadSpace(metadata.ID, "files/mydir", srcDir); err != nil {
		t.Fatalf("failed to populate read-space: %v", err)
	}

	// Check file exists
	readFile := filepath.Join(metadata.RootPath, ReadSpacesDir, "files/mydir", "file.txt")
	if _, err := os.Stat(readFile); os.IsNotExist(err) {
		t.Error("expected read file to exist")
	}

	// Populate write-space
	if err := mgr.PopulateWriteSpace(metadata.ID, "files/mydir", srcDir); err != nil {
		t.Fatalf("failed to populate write-space: %v", err)
	}

	// Check file exists
	writeFile := filepath.Join(metadata.RootPath, WriteSpacesDir, "files/mydir", "file.txt")
	if _, err := os.Stat(writeFile); os.IsNotExist(err) {
		t.Error("expected write file to exist")
	}
}

func TestManagerPopulateWhileDeployed(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := types.DefaultConfig()
	cfg.SlopspacesDir = tmpDir
	cfg.AgentSlopspaceRoot = filepath.Join(tmpDir, "agent")
	cfg.AgentType = types.AgentTypeWorker

	mgr := NewManager(cfg)

	metadata, err := mgr.Create()
	if err != nil {
		t.Fatalf("failed to create slopspace: %v", err)
	}

	// Deploy
	if err := mgr.Deploy(metadata.ID, types.AgentTypeWorker); err != nil {
		t.Fatalf("failed to deploy: %v", err)
	}

	// Try to populate while deployed
	srcDir := filepath.Join(tmpDir, "source")
	os.MkdirAll(srcDir, 0755)

	err = mgr.PopulateReadSpace(metadata.ID, "files", srcDir)
	if err == nil {
		t.Error("expected error when populating while deployed")
	}

	err = mgr.PopulateWriteSpace(metadata.ID, "files", srcDir)
	if err == nil {
		t.Error("expected error when populating while deployed")
	}
}
