.PHONY: all build build-frontend build-go run run-loader dev dev-frontend deps clean test deploy-dev-binary

BINARY_NAME=ianar
DEV_BIN_DIR=/AI-evo1-dev/bin
LOADER_DIR=../ufa-loader

VERSION := $(shell bash ../scripts/compute-version.sh)

all: build

build: build-frontend build-go

build-frontend:
	cd frontend && npm install && npm run build

# robotgo (native capture/mouse control, see Step1Prompt.md Revision A) links
# against libX11/libXtst/etc. via cgo. Other subprojects build fine with this
# host's CGO_ENABLED=0 default since they're pure Go, but that default leaves
# robotgo's cgo-only files out of the build, producing "undefined: Bitmap"/
# "undefined: Move"/etc. compile errors from its remaining non-cgo files.
# Forcing it on here (and only here) fixes that without touching the other
# subprojects' preference for static, cgo-free binaries. Needs a C toolchain
# and X11 dev headers (build-essential, libx11-dev, libxtst-dev, libpng-dev,
# libxcomposite-dev -- the last for xcomposite_linux.go's overlay-window
# capture fallback, Step1SubstepDPrompt.md Revision L) on the build host --
# see scripts/install-dev-deps.sh, which checks for/installs these as part
# of `make check-dev-deps`/`make deploy-dev-binaries`.
build-go:
	# go.mod's indirect golang.org/x/sys requirement got bumped to v0.48.0 (whose
	# own go.mod demands go >= 1.26) by the `go mod tidy` run in 54a4c26, which also
	# bumped this module's `go` directive to 1.26.0 to match. Revision A's reply
	# hand-reverted just that directive back to `go 1.25` (to match this host's
	# installed toolchain -- see Step1SubstepDPrompt.md's dependency check) without
	# re-resolving the now-inconsistent x/sys pin, so `go build` refuses to load the
	# module graph (Build Debug 3). Drop the stale pin and re-tidy here as a
	# stopgap so MVS re-derives a go1.25-compatible version from the real
	# transitive requirers (robotgo et al) using this host's network access; once
	# someone does that and commits the resulting go.mod/go.sum, these two lines
	# are fast no-ops and can be dropped to match the other subprojects' build-go.
	go mod edit -droprequire=golang.org/x/sys
	go mod tidy
	CGO_ENABLED=1 go build -ldflags "-X ufa-version.Version=$(VERSION)" -o $(BINARY_NAME) .

run: build
	./$(BINARY_NAME)

run-loader: build
	$(MAKE) -C $(LOADER_DIR) build
	$(LOADER_DIR)/ufa-loader ./$(BINARY_NAME) $(ARGS)

dev:
	CGO_ENABLED=1 go run . -dev

dev-frontend:
	cd frontend && npm run dev

deps:
	go mod tidy

clean:
	go clean; rm -f $(BINARY_NAME); rm -rf frontend/dist; mkdir -p frontend/dist
	echo "<!doctype html><html><body>Run make build to build the frontend.</body></html>" > frontend/dist/index.html

test:
	CGO_ENABLED=1 go test -v ./...

deploy-dev-binary: build
	@mkdir -p $(DEV_BIN_DIR)
	cp $(BINARY_NAME) $(DEV_BIN_DIR)/$(BINARY_NAME)
	@echo "$(BINARY_NAME) deployed to $(DEV_BIN_DIR)"
