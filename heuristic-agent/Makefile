.PHONY: build docker-build run clean test all deploy-dev-binary

BINARY_NAME=heuristic-agent

all: build

# Build the Go binary
build:
	go build -o $(BINARY_NAME) .

# Build Docker image (from parent directory context)
docker-build:
	@echo "Building Docker image..."
	@echo "Note: Run from parent AI-evo1 directory with: docker build -f heuristic-agent/Dockerfile -t heuristic-agent ."
	cd .. && docker build -f heuristic-agent/Dockerfile -t $(BINARY_NAME) .

# Run watch loop in agent-worker mode (default)
run: build
	@echo "Starting heuristic-agent (agent-worker) watch loop..."
	./$(BINARY_NAME) watch --agent-type agent-worker

# Run watch loop in heuristic-request mode
run-heuristic: build
	@echo "Starting heuristic-agent (heuristic-request) watch loop..."
	./$(BINARY_NAME) watch --agent-type heuristic-request

# Clean up build artifacts
clean:
	go clean
	rm -f $(BINARY_NAME)

# Run Go tests
test:
	go test -v ./...

DEV_BIN_DIR=/AI-evo1-dev/bin

# Build and deploy this binary to the shared dev bin directory
deploy-dev-binary: build
	@mkdir -p $(DEV_BIN_DIR)
	cp $(BINARY_NAME) $(DEV_BIN_DIR)/$(BINARY_NAME)
	@echo "$(BINARY_NAME) deployed to $(DEV_BIN_DIR)"

# Check dependencies (ambiguous-agent availability)
check-deps: build
	./$(BINARY_NAME) check-deps
