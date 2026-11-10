package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractFilePath(t *testing.T) {
	tests := []struct {
		name     string
		prompt   string
		trigger  string
		expected string
	}{
		{
			name:     "create trigger with simple path",
			prompt:   "Our nice agent should create the file test.txt",
			trigger:  "Our nice agent should create the file",
			expected: "test.txt",
		},
		{
			name:     "create trigger with full path",
			prompt:   "Our nice agent should create the file /tmp/myfile.txt",
			trigger:  "Our nice agent should create the file",
			expected: "/tmp/myfile.txt",
		},
		{
			name:     "create trigger embedded in text",
			prompt:   "Please run: Our nice agent should create the file output.log and verify",
			trigger:  "Our nice agent should create the file",
			expected: "output.log",
		},
		{
			name:     "modify trigger with path",
			prompt:   "Our nice agent should modify the file existing.txt",
			trigger:  "Our nice agent should modify the file",
			expected: "existing.txt",
		},
		{
			name:     "quoted path with single quotes",
			prompt:   "Our nice agent should create the file 'my file.txt'",
			trigger:  "Our nice agent should create the file",
			expected: "my file.txt",
		},
		{
			name:     "quoted path with double quotes",
			prompt:   `Our nice agent should create the file "my file.txt"`,
			trigger:  "Our nice agent should create the file",
			expected: "my file.txt",
		},
		{
			name:     "no trigger found",
			prompt:   "Some random text without trigger",
			trigger:  "Our nice agent should create the file",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractFilePath(tt.prompt, tt.trigger)
			if result != tt.expected {
				t.Errorf("ExtractFilePath() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestIsTextFile(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"file.txt", true},
		{"file.md", true},
		{"file.go", true},
		{"file.py", true},
		{"file.json", true},
		{"file.yaml", true},
		{"file.yml", true},
		{"file.sh", true},
		{"file", true}, // no extension
		{"file.png", false},
		{"file.jpg", false},
		{"file.jpeg", false},
		{"file.gif", false},
		{"file.mp4", false},
		{"file.mov", false},
		{"file.avi", false},
		{"file.pdf", false},
		{"file.exe", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := IsTextFile(tt.path)
			if result != tt.expected {
				t.Errorf("IsTextFile(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestHandleCreate(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "clod_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Run("create text file", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "cat_story.txt")

		HandleCreate(testFile)

		// Verify file was created
		if _, err := os.Stat(testFile); os.IsNotExist(err) {
			t.Error("Expected file to be created, but it doesn't exist")
		}

		// Verify content contains cat sentences
		content, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("Failed to read created file: %v", err)
		}

		contentStr := string(content)
		if !strings.Contains(contentStr, "cat") && !strings.Contains(contentStr, "Cat") {
			t.Error("Expected file to contain cat-themed content")
		}

		// Verify it contains content from CatSentences
		if !strings.Contains(contentStr, CatSentences[0]) {
			t.Error("Expected file to contain first cat sentence")
		}
	})

	t.Run("create file in nested directory", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "nested", "dir", "story.txt")

		HandleCreate(testFile)

		// Verify file was created
		if _, err := os.Stat(testFile); os.IsNotExist(err) {
			t.Error("Expected file to be created in nested directory")
		}
	})

	t.Run("non-text file returns stub", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "image.png")

		HandleCreate(testFile)

		// Verify file was NOT created (stub behavior)
		if _, err := os.Stat(testFile); !os.IsNotExist(err) {
			t.Error("Expected non-text file to NOT be created")
		}
	})
}

func TestHandleModify(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "clod_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Run("modify existing text file", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "existing.txt")
		initialContent := "Initial content.\n"

		// Create the file first
		if err := os.WriteFile(testFile, []byte(initialContent), 0644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		HandleModify(testFile)

		// Verify content was appended
		content, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("Failed to read modified file: %v", err)
		}

		contentStr := string(content)
		if !strings.Contains(contentStr, "Initial content") {
			t.Error("Expected original content to be preserved")
		}

		// Verify cat content was added
		if !strings.Contains(contentStr, CatSentences[3]) {
			t.Error("Expected file to contain appended cat sentence")
		}
	})
}

func TestCatSentences(t *testing.T) {
	// Verify we have the expected number of sentences
	if len(CatSentences) != 5 {
		t.Errorf("Expected 5 cat sentences, got %d", len(CatSentences))
	}

	// Verify each sentence mentions a cat
	for i, sentence := range CatSentences {
		lower := strings.ToLower(sentence)
		if !strings.Contains(lower, "cat") && !strings.Contains(lower, "feline") && !strings.Contains(lower, "tabby") {
			t.Errorf("Sentence %d doesn't appear to be cat-themed: %s", i, sentence)
		}
	}
}
