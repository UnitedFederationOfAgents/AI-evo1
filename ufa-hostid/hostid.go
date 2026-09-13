// Package ufahostid resolves a stable per-host identifier for UFA sub-applications.
//
// Resolution order:
//  1. Read id from ~/.ufa/host.yaml
//  2. If the file is absent, generate <hostname>-<4-char-random-alphanumeric>,
//     write it to host.yaml (with a first_configured timestamp), and return it.
//  3. If host.yaml is inaccessible for any other reason, return the raw hostname.
//  4. If hostname is also unavailable, return "unknown".
package ufahostid

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const hostYAMLName = "host.yaml"

// HostDetails holds the resolved host identity and metadata.
type HostDetails struct {
	ID              string
	FirstConfigured string // empty if unavailable
	Created         bool   // true if host.yaml was just created by this call
	AccessError     bool   // true if host.yaml exists but is inaccessible
}

// GetHostDetails resolves the host identity with full status information.
// It idempotently creates ~/.ufa/host.yaml when absent and sets Created=true.
// It sets AccessError=true when the file exists but cannot be read.
func GetHostDetails() HostDetails {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return HostDetails{ID: rawHostname(), AccessError: true}
	}

	hostFile := filepath.Join(home, ".ufa", hostYAMLName)

	data, err := os.ReadFile(hostFile)
	if err == nil {
		id := parseID(string(data))
		if id == "" {
			return HostDetails{ID: rawHostname()}
		}
		return HostDetails{
			ID:              id,
			FirstConfigured: parseFirstConfigured(string(data)),
		}
	}

	if !os.IsNotExist(err) {
		return HostDetails{ID: rawHostname(), AccessError: true}
	}

	// File absent – generate a new ID and attempt to persist it.
	hostname := rawHostname()
	id := hostname + "-" + randomAlphanumeric(4)
	created := writeHostYAML(hostFile, id) == nil
	return HostDetails{
		ID:              id,
		FirstConfigured: time.Now().Format(time.RFC3339),
		Created:         created,
	}
}

// GetHostID returns the stable host identifier for this machine.
func GetHostID() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return rawHostname()
	}

	hostFile := filepath.Join(home, ".ufa", hostYAMLName)

	data, err := os.ReadFile(hostFile)
	if err == nil {
		if id := parseID(string(data)); id != "" {
			return id
		}
		// File present but unparseable – fall through to hostname
		return rawHostname()
	}

	if !os.IsNotExist(err) {
		// File exists but inaccessible
		return rawHostname()
	}

	// File absent – generate a new ID and attempt to persist it.
	hostname := rawHostname()
	id := hostname + "-" + randomAlphanumeric(4)
	_ = writeHostYAML(hostFile, id) // best-effort; ignore write errors
	return id
}

func rawHostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown"
	}
	return name
}

func parseID(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "id:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		val = strings.Trim(val, `"'`)
		if val != "" {
			return val
		}
	}
	return ""
}

func parseFirstConfigured(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "first_configured:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, "first_configured:"))
		return strings.Trim(val, `"'`)
	}
	return ""
}

func writeHostYAML(path, id string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	content := fmt.Sprintf("id: %s\nfirst_configured: %s\n", id, time.Now().Format(time.RFC3339))
	return os.WriteFile(path, []byte(content), 0644)
}

func randomAlphanumeric(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[r.Intn(len(chars))]
	}
	return string(b)
}
