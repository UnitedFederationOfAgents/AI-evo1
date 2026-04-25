.PHONY: build docker-build run clean test all deploy-dependencies-local

BINARY_NAME=ambiguous-agent

all: build

# Build the Go binary
build:
	go build -o $(BINARY_NAME) .

# Build Docker image
docker-build:
	docker build -t $(BINARY_NAME) .

# Run with sample prompt (read mode by default)
run: build
	@echo "Running ambiguous-agent..."
	./$(BINARY_NAME) -r "What files are in this directory?"

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
	cd ../clauditable && go build -o ../ambiguous-agent/clauditable .
	@echo "Building clod..."
	cd ../clod && go build -o ../ambiguous-agent/clod .
	@echo "Dependencies deployed to local directory"
	@ls -la clauditable clod 2>/dev/null || true
