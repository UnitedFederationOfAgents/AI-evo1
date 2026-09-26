package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveCondocLockFile covers the shared helper introduced in Step5Prompt.md
// Revision H: every commit sequence that lands a condoc at "awaiting action" or
// "completed" must remove the '.condoc' lock file immediately beforehand so its
// removal is captured by that same commit, rather than left as a dangling,
// uncommitted deletion in the working tree.
func TestRemoveCondocLockFile(t *testing.T) {
	t.Run("removes an existing lock file", func(t *testing.T) {
		dir := t.TempDir()
		lockPath := filepath.Join(dir, ".condoc")
		if err := os.WriteFile(lockPath, []byte("Condoccer began work on Foo at ... (0)\n"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		removeCondocLockFile(dir)

		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Fatalf(".condoc still present after removeCondocLockFile: err=%v", err)
		}
	})

	t.Run("no-ops when the lock file is already absent", func(t *testing.T) {
		dir := t.TempDir()

		// Must not panic or otherwise error when there's nothing to remove --
		// e.g. condoccer's own watchLoop already noticed the transition first.
		removeCondocLockFile(dir)

		if _, err := os.Stat(filepath.Join(dir, ".condoc")); !os.IsNotExist(err) {
			t.Fatalf("expected no .condoc file, got err=%v", err)
		}
	})
}
