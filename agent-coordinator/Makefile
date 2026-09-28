.PHONY: build build-frontend build-go run run-loader dev dev-frontend clean test deploy-dev-binary deps

BINARY_NAME=agent-coordinator
DEV_BIN_DIR=/AI-evo1-dev/bin
LOADER_DIR=../ufa-loader

# Repo-level version, embedded into the binary below -- see docs/DevMode.md
# "Versioning" and scripts/compute-version.sh.
VERSION := $(shell bash ../scripts/compute-version.sh)

all: build

# Full production build: frontend then Go binary (embeds the built frontend).
build: build-frontend build-go

build-frontend:
	cd frontend && npm install && npm run build

build-go:
	go build -ldflags "-X ufa-version.Version=$(VERSION)" -o $(BINARY_NAME) .

# Run the built binary.
run: build
	./$(BINARY_NAME)

# Run under ufa-loader: it relaunches $(BINARY_NAME) whenever a SIGHUP sent to
# its pid makes it announce a restart on stdout before exiting (see
# ufa-loader/README.md / docs/DevMode.md). Extra flags: `make run-loader
# ARGS="--dev-mode"`.
run-loader: build
	$(MAKE) -C $(LOADER_DIR) build
	$(LOADER_DIR)/ufa-loader ./$(BINARY_NAME) $(ARGS)

# Dev mode: run Go server without frontend (use dev-frontend in a separate terminal).
dev:
	go run . -dev

# Run the Vite dev server (proxies /ws to the Go server on :8083).
dev-frontend:
	cd frontend && npm run dev

# Fetch Go dependencies (run once after clone or when go.mod changes).
deps:
	go mod tidy

# Clean build artifacts; restore the frontend/dist placeholder.
clean:
	go clean
	rm -f $(BINARY_NAME)
	rm -rf frontend/dist
	mkdir -p frontend/dist
	echo "<!doctype html><html><body>Run make build to build the frontend.</body></html>" > frontend/dist/index.html

test:
	go test -v ./...

deploy-dev-binary: build
	@mkdir -p $(DEV_BIN_DIR)
	cp $(BINARY_NAME) $(DEV_BIN_DIR)/$(BINARY_NAME)
	@echo "$(BINARY_NAME) deployed to $(DEV_BIN_DIR)"
