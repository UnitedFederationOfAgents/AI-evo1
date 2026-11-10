.PHONY: build test clean deploy-dev-binary

# ufa-configurable is a shared library package (no binary). These targets exist so
# it can take part in the repo-wide `make test-all` / build matrix.

build:
	go build ./...

test:
	go test -v ./...

clean:
	go clean

# No binary to deploy; present so `make deploy-dev-binaries` can iterate uniformly.
deploy-dev-binary:
	@echo "ufa-configurable is a library; nothing to deploy"
