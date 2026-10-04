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
#
# The `go mod edit -droprequire=golang.org/x/sys` + `go mod tidy` stopgap that
# used to run here is gone: go.mod/go.sum now carry the resolved, go1.25-
# compatible pin it was there to re-derive (x/sys v0.30.0, the real MVS maximum
# across robotgo et al -- v0.48.0 was never derived from the graph, it came from
# 54a4c26's tidy, and its own go.mod demands go >= 1.26, which this host's
# 1.25.0 toolchain hard-errors on rather than switching for a *dependency's*
# requirement). Dropping it is what that comment said to do once the resolved
# state was committed, but it also fixes a loop: because the recipe rewrote
# tracked go.mod/go.sum on every single build, it kept undoing exactly this
# pin -- Revision K committed x/sys v0.30.0 and by Revision L's commit the tree
# was back to v0.48.0/go 1.26.0 with no one having asked for it. A pin that the
# build step re-derives is not a pin, so the fix could not stick until this
# came out.
build-go:
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
