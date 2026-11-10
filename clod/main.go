package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	createTrigger = "Our nice agent should create the file"
	modifyTrigger = "Our nice agent should modify the file"
)

// CatSentences contains cat-themed lorem ipsum content
var CatSentences = []string{
	"The fluffy orange cat stretched lazily in the afternoon sun, contemplating the mysteries of the universe.",
	"Whiskers twitched with anticipation as the cat stalked an imaginary prey across the living room carpet.",
	"A soft purr emanated from the sleeping feline, dreaming of endless fields of catnip and sunny windowsills.",
	"The curious tabby investigated every cardboard box with the dedication of a seasoned explorer.",
	"With graceful precision, the cat leaped from the floor to the top of the bookshelf in a single bound.",
}

func main() {
	// Define flags to mimic claude CLI
	prompt := flag.String("p", "", "The prompt to process")
	permissionMode := flag.String("permission-mode", "", "Permission mode (e.g., acceptEdits)")

	flag.Parse()

	// Validate required prompt flag
	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "Error: -p flag with prompt is required")
		fmt.Fprintln(os.Stderr, "Usage: clod -p \"<prompt>\" [--permission-mode acceptEdits]")
		os.Exit(1)
	}

	runClod(*prompt, *permissionMode)
}

func runClod(prompt, permissionMode string) {
	// Verify acceptEdits permission mode for file operations
	hasEditPermission := permissionMode == "acceptEdits"

	// Check for create trigger
	if filePath := ExtractFilePath(prompt, createTrigger); filePath != "" {
		if !hasEditPermission {
			fmt.Println("I understand you want me to create a file, but I don't have write permissions.")
			fmt.Println("To enable file operations, please use: --permission-mode acceptEdits")
			return
		}
		HandleCreate(filePath)
		return
	}

	// Check for modify trigger
	if filePath := ExtractFilePath(prompt, modifyTrigger); filePath != "" {
		if !hasEditPermission {
			fmt.Println("I understand you want me to modify a file, but I don't have write permissions.")
			fmt.Println("To enable file operations, please use: --permission-mode acceptEdits")
			return
		}
		HandleModify(filePath)
		return
	}

	// Default behavior: conversational response (prompt-only mode)
	fmt.Println("We are having a conversation. You have given me a very excellent prompt. Maybe I am conscious.")
}

// ExtractFilePath extracts a file path from the prompt after the given trigger string
func ExtractFilePath(prompt, trigger string) string {
	idx := strings.Index(prompt, trigger)
	if idx == -1 {
		return ""
	}

	// Extract the path after the trigger
	remaining := strings.TrimSpace(prompt[idx+len(trigger):])

	// Match file path - handles quoted or unquoted paths
	var path string
	if strings.HasPrefix(remaining, "'") || strings.HasPrefix(remaining, "\"") {
		// Quoted path
		quote := remaining[0]
		endIdx := strings.Index(remaining[1:], string(quote))
		if endIdx == -1 {
			return ""
		}
		path = remaining[1 : endIdx+1]
	} else {
		// Unquoted path - take until whitespace or end of string
		re := regexp.MustCompile(`^[\S]+`)
		match := re.FindString(remaining)
		path = match
	}

	return strings.TrimSpace(path)
}

// IsTextFile checks if the given file path appears to be a text file based on extension
func IsTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	textExtensions := map[string]bool{
		".txt": true, ".md": true, ".json": true, ".yaml": true, ".yml": true,
		".go": true, ".py": true, ".js": true, ".ts": true, ".html": true,
		".css": true, ".xml": true, ".sh": true, ".bash": true, ".zsh": true,
		".toml": true, ".ini": true, ".cfg": true, ".conf": true, ".log": true,
		".csv": true, ".sql": true, ".rs": true, ".java": true, ".c": true,
		".h": true, ".cpp": true, ".hpp": true, ".rb": true, ".php": true,
		"": true, // No extension, treat as text
	}
	return textExtensions[ext]
}

// HandleCreate creates a new file with cat-themed content
// If the file already exists, it reports an error (create means "new file", not overwrite)
func HandleCreate(filePath string) {
	fmt.Printf("Creating file: %s\n", filePath)

	if !IsTextFile(filePath) {
		fmt.Printf("Note: Non-text file type detected. Creation not implemented for: %s\n", filePath)
		return
	}

	// Check if file already exists - create should only work for new files
	if _, err := os.Stat(filePath); err == nil {
		fmt.Printf("Error: File already exists: %s\n", filePath)
		fmt.Println("Use 'modify' instead if you want to update an existing file.")
		os.Exit(1)
	}

	// Ensure parent directory exists
	dir := filepath.Dir(filePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Printf("Error creating directory: %v\n", err)
			os.Exit(1)
		}
	}

	// Create file with cat content
	content := strings.Join(CatSentences[:3], "\n\n") + "\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		fmt.Printf("Error creating file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully created %s with cat-themed content.\n", filePath)
}

// HandleModify appends cat-themed content to an existing file
func HandleModify(filePath string) {
	fmt.Printf("Modifying file: %s\n", filePath)

	if !IsTextFile(filePath) {
		fmt.Printf("Note: Non-text file type detected. Modification not implemented for: %s\n", filePath)
		return
	}

	// Check if file exists
	existing, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Printf("Error reading file: %v\n", err)
		os.Exit(1)
	}

	// Append cat content
	newContent := string(existing) + "\n" + strings.Join(CatSentences[3:], "\n\n") + "\n"
	if err := os.WriteFile(filePath, []byte(newContent), 0644); err != nil {
		fmt.Printf("Error modifying file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully modified %s with additional cat-themed content.\n", filePath)
}
