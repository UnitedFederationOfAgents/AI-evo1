.PHONY: test test-all build-all clean-all deploy-dev-binaries

# Sub-projects in this directory
SUBPROJECTS = clauditable clod ambiguous-agent federation-command dungeon-keeper condoccer local-representative

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

# Clean dev bin dir and deploy all binaries there
deploy-dev-binaries:
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
