.PHONY: test test-all build-all clean-all deploy-dev-binaries check-dev-deps

# Sub-projects in this directory
SUBPROJECTS = ufa-configurable ufa-version ufa-loader clauditable clod ambiguous-agent federation-command dungeon-keeper condoccer local-representative agent-coordinator

DEV_BIN_DIR=/AI-evo1-dev/bin

# Run tests in all sub-projects
test: test-all

test-all:
	@echo "Running tests for all AI-evo1 sub-projects..."
	@for proj in $(SUBPROJECTS); do \
		echo ""; \
		echo "=== Testing $$proj ==="; \
		$(MAKE) -C $$proj test || exit 1; \
	done
	@echo ""
	@echo "=== All tests passed ==="

# Build all sub-projects
build-all:
	@echo "Building all AI-evo1 sub-projects..."
	@for proj in $(SUBPROJECTS); do \
		echo ""; \
		echo "=== Building $$proj ==="; \
		$(MAKE) -C $$proj build || exit 1; \
	done
	@echo ""
	@echo "=== All builds completed ==="

# Clean all sub-projects
clean-all:
	@echo "Cleaning all AI-evo1 sub-projects..."
	@for proj in $(SUBPROJECTS); do \
		echo ""; \
		echo "=== Cleaning $$proj ==="; \
		$(MAKE) -C $$proj clean || exit 1; \
	done
	@echo ""
	@echo "=== All sub-projects cleaned ==="

# Verify OS-level dependencies are present without making changes
check-dev-deps:
	@./scripts/install-dev-deps.sh --check

# Clean dev bin dir and deploy all binaries there
#
# '.building' (repo root, gitignored) is a lock file for this target itself
# -- created the moment the target starts, removed only once it's fully
# done. See condocs/initialDistributedDevelopmentImpls/Step5Prompt.md
# Revision I: local-representative's dev-repo watcher now detects rebuild
# completion off this file's presence rather than solely off this recipe's
# own exit, so it also correctly reflects a build already running (e.g.
# started just before an LR restart) or one kicked off by hand outside LR.
deploy-dev-binaries: check-dev-deps
	@touch .building
	@echo "Cleaning $(DEV_BIN_DIR)..."
	rm -rf $(DEV_BIN_DIR)
	mkdir -p $(DEV_BIN_DIR)
	@echo "Deploying all binaries to $(DEV_BIN_DIR)..."
	@for proj in $(SUBPROJECTS); do \
		echo ""; \
		echo "=== Deploying $$proj ==="; \
		$(MAKE) -C $$proj deploy-dev-binary || exit 1; \
	done
	@echo ""
	@echo "=== All binaries deployed to $(DEV_BIN_DIR) ==="
	@ls -la $(DEV_BIN_DIR)
	@rm -f .building
