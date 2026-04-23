.PHONY: build docker-build run clean test all

BINARY_NAME=clod
TEST_FILE=cat_story.txt

all: build

# Build the Go binary
build:
	go build -o $(BINARY_NAME) .

# Build Docker image
docker-build:
	docker build -t $(BINARY_NAME) .

# Run with sample args - creates cat_story.txt
run: build
	@echo "Running clod to create $(TEST_FILE)..."
	./$(BINARY_NAME) --permission-mode acceptEdits -p "Our nice agent should create the file $(TEST_FILE)"

# Clean up build artifacts and test files
clean:
	go clean
	rm -f $(BINARY_NAME) $(TEST_FILE)

# Run Go tests
test:
	go test -v ./...
