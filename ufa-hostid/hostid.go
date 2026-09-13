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
