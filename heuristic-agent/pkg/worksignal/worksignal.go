// Package worksignal handles work signal file operations.
package worksignal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"heuristic-agent/pkg/types"

	"github.com/google/uuid"
)

// FilePrefix constants for work signal filenames.
const (
	WorkingPrefix  = "WORKING-"
	CompletePrefix = "COMPLETE-"
	FileSuffix     = ".jsonl"
)

// Manager handles work signal file operations.
type Manager struct {
	config *types.Config
}

// NewManager creates a new work signal manager.
func NewManager(cfg *types.Config) *Manager {
	return &Manager{config: cfg}
}

// GenerateFilename creates a filename for a work signal.
func GenerateFilename(name string, createdAt time.Time, complete bool) string {
	prefix := WorkingPrefix
	if complete {
		prefix = CompletePrefix
	}
	timestamp := createdAt.Unix()
	// Sanitize name: replace spaces with underscores, remove special chars
	safeName := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		if r == ' ' {
			return '_'
		}
		return -1
	}, name)
	return fmt.Sprintf("%s%s-%d%s", prefix, safeName, timestamp, FileSuffix)
}

// ParseFilename extracts name and timestamp from a work signal filename.
func ParseFilename(filename string) (name string, timestamp int64, complete bool, err error) {
	basename := filepath.Base(filename)

	if strings.HasPrefix(basename, CompletePrefix) {
		complete = true
		basename = strings.TrimPrefix(basename, CompletePrefix)
	} else if strings.HasPrefix(basename, WorkingPrefix) {
		complete = false
		basename = strings.TrimPrefix(basename, WorkingPrefix)
	} else {
		return "", 0, false, fmt.Errorf("invalid filename prefix: %s", filename)
	}

	basename = strings.TrimSuffix(basename, FileSuffix)

	// Find last dash followed by timestamp
	lastDash := strings.LastIndex(basename, "-")
	if lastDash == -1 {
		return "", 0, false, fmt.Errorf("invalid filename format: %s", filename)
	}

	name = basename[:lastDash]
	_, err = fmt.Sscanf(basename[lastDash+1:], "%d", &timestamp)
	if err != nil {
		return "", 0, false, fmt.Errorf("invalid timestamp in filename: %s", filename)
	}

	return name, timestamp, complete, nil
}

// Create creates a new work signal file with the initial header.
func (m *Manager) Create(signal *types.WorkSignal) (string, error) {
	if signal.ID == "" {
		signal.ID = uuid.New().String()
	}
	if signal.CreatedAt.IsZero() {
		signal.CreatedAt = time.Now()
	}
	signal.UpdatedAt = signal.CreatedAt
	if signal.Status == "" {
		signal.Status = types.WorkStatusPending
	}

	filename := GenerateFilename(signal.Role, signal.CreatedAt, false)
	path := filepath.Join(m.config.OngoingWorkDir(), filename)

	// Ensure directory exists
	if err := os.MkdirAll(m.config.OngoingWorkDir(), 0755); err != nil {
		return "", fmt.Errorf("failed to create ongoing work dir: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("failed to create work signal file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	if err := encoder.Encode(signal); err != nil {
		return "", fmt.Errorf("failed to write work signal header: %w", err)
	}

	return path, nil
}

// Read reads a work signal file and returns the header and all events.
func (m *Manager) Read(path string) (*types.WorkSignal, []types.WorkEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open work signal file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	// First line is the header
	if !scanner.Scan() {
		return nil, nil, fmt.Errorf("empty work signal file")
	}

	var signal types.WorkSignal
	if err := json.Unmarshal(scanner.Bytes(), &signal); err != nil {
		return nil, nil, fmt.Errorf("failed to parse work signal header: %w", err)
	}

	// Remaining lines are events
	var events []types.WorkEvent
	for scanner.Scan() {
		var event types.WorkEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			// Skip malformed event lines
			continue
		}
		events = append(events, event)
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("error reading work signal file: %w", err)
	}

	return &signal, events, nil
}

// AppendEvent appends an event to a work signal file.
func (m *Manager) AppendEvent(path string, event *types.WorkEvent) error {
	if event.EventID == "" {
		event.EventID = uuid.New().String()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open work signal file for append: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	if err := encoder.Encode(event); err != nil {
		return fmt.Errorf("failed to append event: %w", err)
	}

	return nil
}

// UpdateStatus updates the status of a work signal and appends an event.
func (m *Manager) UpdateStatus(path string, status types.WorkStatus, comment string) error {
	event := &types.WorkEvent{
		StatusUpdate: string(status),
		Comment:      comment,
	}
	return m.AppendEvent(path, event)
}

// TakeOwnership sets the holder field on a work signal.
func (m *Manager) TakeOwnership(path string, holderID string) error {
	signal, events, err := m.Read(path)
	if err != nil {
		return err
	}

	// Update signal with new holder
	signal.Holder = holderID
	signal.UpdatedAt = time.Now()
	if signal.Status == types.WorkStatusPending {
		signal.Status = types.WorkStatusProcessing
		now := time.Now()
		signal.StartedAt = &now
	}

	// Rewrite the file with updated header
	return m.rewrite(path, signal, events)
}

// ReleaseOwnership clears the holder field.
func (m *Manager) ReleaseOwnership(path string) error {
	signal, events, err := m.Read(path)
	if err != nil {
		return err
	}

	signal.Holder = ""
	signal.UpdatedAt = time.Now()

	return m.rewrite(path, signal, events)
}

// Complete marks a work signal as complete and moves it to the complete directory.
func (m *Manager) Complete(path string, success bool, comment string) error {
	signal, events, err := m.Read(path)
	if err != nil {
		return err
	}

	now := time.Now()
	signal.CompletedAt = &now
	signal.UpdatedAt = now
	signal.Holder = ""

	if success {
		signal.Status = types.WorkStatusCompleted
	} else {
		signal.Status = types.WorkStatusFailed
	}

	// Add completion event
	events = append(events, types.WorkEvent{
		EventID:      uuid.New().String(),
		StatusUpdate: string(signal.Status),
		Comment:      comment,
		Timestamp:    now,
	})

	// Write to complete directory
	if err := os.MkdirAll(m.config.CompleteWorkDir(), 0755); err != nil {
		return fmt.Errorf("failed to create complete work dir: %w", err)
	}

	newFilename := GenerateFilename(signal.Role, signal.CreatedAt, true)
	newPath := filepath.Join(m.config.CompleteWorkDir(), newFilename)

	if err := m.rewrite(newPath, signal, events); err != nil {
		return err
	}

	// Remove original file
	return os.Remove(path)
}

// rewrite rewrites a work signal file with the given header and events.
func (m *Manager) rewrite(path string, signal *types.WorkSignal, events []types.WorkEvent) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create work signal file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	if err := encoder.Encode(signal); err != nil {
		return fmt.Errorf("failed to write work signal header: %w", err)
	}

	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("failed to write event: %w", err)
		}
	}

	return nil
}

// ListOngoing returns all ongoing work signals.
func (m *Manager) ListOngoing() ([]string, error) {
	return m.listDir(m.config.OngoingWorkDir())
}

// ListComplete returns all completed work signals.
func (m *Manager) ListComplete() ([]string, error) {
	return m.listDir(m.config.CompleteWorkDir())
}

// listDir lists all jsonl files in a directory.
func (m *Manager) listDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), FileSuffix) {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}

	return files, nil
}

// FindPendingForAgentType finds pending work signals for a specific agent type.
func (m *Manager) FindPendingForAgentType(agentType types.AgentType) ([]string, error) {
	files, err := m.ListOngoing()
	if err != nil {
		return nil, err
	}

	var pending []string
	for _, path := range files {
		signal, _, err := m.Read(path)
		if err != nil {
			continue
		}

		// Check if this signal is for our agent type and is available
		if signal.AgentType == agentType && signal.Holder == "" && signal.Status == types.WorkStatusPending {
			pending = append(pending, path)
		}
	}

	return pending, nil
}
