.PHONY: build docker-build run clean test all deploy-dev-binary

BINARY_NAME=clod
TEST_FILE=cat_story.txt

# Repo-level version, embedded into the binary below -- see docs/DevMode.md
# "Versioning" and scripts/compute-version.sh.
VERSION := $(shell bash ../scripts/compute-version.sh)

all: build

# Build the Go binary
build:
	go build -ldflags "-X ufa-version.Version=$(VERSION)" -o $(BINARY_NAME) .

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

DEV_BIN_DIR=/AI-evo1-dev/bin

# Build and deploy this binary to the shared dev bin directory
deploy-dev-binary: build
	@mkdir -p $(DEV_BIN_DIR)
	cp $(BINARY_NAME) $(DEV_BIN_DIR)/$(BINARY_NAME)
	@echo "$(BINARY_NAME) deployed to $(DEV_BIN_DIR)"
