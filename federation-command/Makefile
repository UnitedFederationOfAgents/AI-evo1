.PHONY: build docker-build run run-loader clean test all deploy-dev-binary

BINARY_NAME=federation-command
LOADER_DIR=../ufa-loader

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

# Run the interactive shell
run: build
	@echo "Starting federation-command interactive shell..."
	./$(BINARY_NAME)

# Run under ufa-loader: it relaunches $(BINARY_NAME) whenever a SIGHUP sent to
# its pid makes it announce a restart on stdout before exiting (see
# ufa-loader/README.md / docs/DevMode.md).
run-loader: build
	$(MAKE) -C $(LOADER_DIR) build
	$(LOADER_DIR)/ufa-loader ./$(BINARY_NAME)

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
