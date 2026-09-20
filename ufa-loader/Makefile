.PHONY: build run clean test deploy-dev-binary

BINARY_NAME=ufa-loader
DEV_BIN_DIR=/AI-evo1-dev/bin

all: build

build:
	go build -o $(BINARY_NAME) .

# Example: make run BIN=../local-representative/local-representative ARGS="--dev-mode"
run: build
	./$(BINARY_NAME) $(BIN) $(ARGS)

clean:
	go clean
	rm -f $(BINARY_NAME)

test:
	go test -v ./...

deploy-dev-binary: build
	@mkdir -p $(DEV_BIN_DIR)
	cp $(BINARY_NAME) $(DEV_BIN_DIR)/$(BINARY_NAME)
	@echo "$(BINARY_NAME) deployed to $(DEV_BIN_DIR)"
