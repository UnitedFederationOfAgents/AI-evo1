.PHONY: build clean test all deploy-dev-binary

BINARY_NAME=dtt

# Repo-level version, embedded into the binary below -- see docs/DevMode.md
# "Versioning" and scripts/compute-version.sh.
VERSION := $(shell bash ../scripts/compute-version.sh)

all: build

# Build the Go binary. The built-in T3s (builtin/) are embedded into it.
build:
	go build -ldflags "-X ufa-version.Version=$(VERSION)" -o $(BINARY_NAME) .

clean:
	go clean
	rm -f $(BINARY_NAME)

# Run Go tests. The materialize-tree run test is skipped when uv isn't
# installed.
test:
	go test -v ./...

DEV_BIN_DIR=/AI-evo1-dev/bin

# Build and deploy this binary to the shared dev bin directory
deploy-dev-binary: build
	@mkdir -p $(DEV_BIN_DIR)
	cp $(BINARY_NAME) $(DEV_BIN_DIR)/$(BINARY_NAME)
	@echo "$(BINARY_NAME) deployed to $(DEV_BIN_DIR)"
