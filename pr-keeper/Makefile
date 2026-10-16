.PHONY: build test clean deploy-dev-binary

# pr-keeper is a shared library package (no binary): GitHub pull request
# create/inspect/merge, used by condoccer's slopspace control scheme and
# meant to be reused by dungeon-keeper and claudomation. These targets exist
# so it can take part in the repo-wide `make test-all` / build matrix.

build:
	go build ./...

test:
	go test -v ./...

clean:
	go clean

# No binary to deploy; present so `make deploy-dev-binaries` can iterate uniformly.
deploy-dev-binary:
	@echo "pr-keeper is a library; nothing to deploy"
