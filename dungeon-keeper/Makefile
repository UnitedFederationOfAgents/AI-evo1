.PHONY: build docker-build run run-loader clean test all deploy-dev-binary

BINARY_NAME=dungeon-keeper
LOADER_DIR=../ufa-loader

# Repo-level version, embedded into the binary below -- see docs/DevMode.md
# "Versioning" and scripts/compute-version.sh.
VERSION := $(shell bash ../scripts/compute-version.sh)

all: build

# Build the Go binary
build:
	go build -ldflags "-X ufa-version.Version=$(VERSION)" -o $(BINARY_NAME) .

# Build Docker image (from parent directory context)
docker-build:
	@echo "Building Docker image..."
	@echo "Note: Run from parent AI-evo1 directory with: docker build -f dungeon-keeper/Dockerfile -t dungeon-keeper ."
	cd .. && docker build -f dungeon-keeper/Dockerfile -t $(BINARY_NAME) .

# Run watch loop in agent-worker mode (default)
run: build
	@echo "Starting dungeon-keeper (agent-worker) watch loop..."
	./$(BINARY_NAME) watch --agent-type agent-worker

# Run watch loop in heuristic-request mode
run-heuristic: build
	@echo "Starting dungeon-keeper (heuristic-request) watch loop..."
	./$(BINARY_NAME) watch --agent-type heuristic-request

# Run the agent-worker watch loop under ufa-loader: it relaunches
# $(BINARY_NAME) whenever a SIGHUP sent to its pid makes it announce a
# restart on stdout before exiting (see ufa-loader/README.md /
# docs/DevMode.md). Extra flags: `make run-loader ARGS="--dev-mode"`.
run-loader: build
	$(MAKE) -C $(LOADER_DIR) build
	$(LOADER_DIR)/ufa-loader ./$(BINARY_NAME) watch --agent-type agent-worker $(ARGS)

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
