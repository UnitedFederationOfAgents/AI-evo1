.PHONY: build docker-build run clean test all deploy-dependencies-local clean-local-dependencies

BINARY_NAME=heuristic-agent

all: build

# Build the Go binary
build:
	go build -o $(BINARY_NAME) .

# Build Docker image
docker-build:
	docker build -t $(BINARY_NAME) .

# Run watch loop in agent-worker mode (default)
run: build
	@echo "Starting heuristic-agent watch loop..."
	./$(BINARY_NAME) watch --agent-type agent-worker

# Clean up build artifacts
clean:
	go clean
	rm -f $(BINARY_NAME)

# Run Go tests
test:
	go test -v ./...

# Build and deploy local dependencies from other sub-projects
deploy-dependencies-local:
	@echo "Building clauditable..."
	cd ../clauditable && go build -o ../heuristic-agent/clauditable .
	@echo "Building ambiguous-agent..."
	cd ../ambiguous-agent && go build -o ../heuristic-agent/ambiguous-agent .
	@echo "Building clod..."
	cd ../clod && go build -o ../heuristic-agent/clod .
	@echo "Dependencies deployed to local directory"
	@ls -la clauditable ambiguous-agent clod 2>/dev/null || true

# Clean local dependencies
clean-local-dependencies:
	@echo "Cleaning local dependencies..."
	rm -f clauditable ambiguous-agent clod
	@echo "Local dependencies cleaned"
