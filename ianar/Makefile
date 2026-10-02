.PHONY: all build build-frontend build-go run run-loader dev dev-frontend deps clean test deploy-dev-binary

BINARY_NAME=ianar
DEV_BIN_DIR=/AI-evo1-dev/bin
LOADER_DIR=../ufa-loader

VERSION := $(shell bash ../scripts/compute-version.sh)

all: build

build: build-frontend build-go

build-frontend:
	cd frontend && npm install && npm run build

build-go:
	go build -ldflags "-X ufa-version.Version=$(VERSION)" -o $(BINARY_NAME) .

run: build
	./$(BINARY_NAME)

run-loader: build
	$(MAKE) -C $(LOADER_DIR) build
	$(LOADER_DIR)/ufa-loader ./$(BINARY_NAME) $(ARGS)

dev:
	go run . -dev

dev-frontend:
	cd frontend && npm run dev

deps:
	go mod tidy

clean:
	go clean; rm -f $(BINARY_NAME); rm -rf frontend/dist; mkdir -p frontend/dist
	echo "<!doctype html><html><body>Run make build to build the frontend.</body></html>" > frontend/dist/index.html

test:
	go test -v ./...

deploy-dev-binary: build
	@mkdir -p $(DEV_BIN_DIR)
	cp $(BINARY_NAME) $(DEV_BIN_DIR)/$(BINARY_NAME)
	@echo "$(BINARY_NAME) deployed to $(DEV_BIN_DIR)"
