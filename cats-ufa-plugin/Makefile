.PHONY: all build run test clean deploy-dev-binary

BINARY_NAME=cats-ufa-plugin
DEV_BIN_DIR=/AI-evo1-dev/bin

all: build

build:
	go build -o $(BINARY_NAME) .

run: build
	./$(BINARY_NAME) $(ARGS)

test:
	go test -v ./...

clean:
	go clean; rm -f $(BINARY_NAME)

# Deployed beside the other binaries (the root Makefile's deploy-dev-binaries
# builds every SUBPROJECTS entry, this one included): local-representative's
# plugins tab -> registration -> "register example" launches it from there
# (condocs/privateRepoOpsImpls/Step3Prompt.md Revision E). It can still be
# run by hand.
deploy-dev-binary: build
	@mkdir -p $(DEV_BIN_DIR)
	cp $(BINARY_NAME) $(DEV_BIN_DIR)/$(BINARY_NAME)
	@echo "$(BINARY_NAME) deployed to $(DEV_BIN_DIR)"
