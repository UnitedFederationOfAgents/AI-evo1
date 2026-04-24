.PHONY: build docker-build run clean test all

BINARY_NAME=clauditable

all: build

# Build the Go binary
build:
	go build -o $(BINARY_NAME) .

# Build Docker image
docker-build:
	docker build -t $(BINARY_NAME) .

# Run with sample args - wraps echo command
run: build
	@echo "Running clauditable to wrap a simple command..."
	./$(BINARY_NAME) echo "Hello from clauditable"

# Clean up build artifacts and test files
clean:
	go clean
	rm -f $(BINARY_NAME)

# Run Go tests
test:
	go test -v ./...
