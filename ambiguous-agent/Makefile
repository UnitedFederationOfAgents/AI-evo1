.PHONY: build docker-build run clean test all

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
	./$(BINARY_NAME) -r -p "What files are in this directory?"

# Clean up build artifacts
clean:
	go clean
	rm -f $(BINARY_NAME)

# Run Go tests
test:
	go test -v ./...
