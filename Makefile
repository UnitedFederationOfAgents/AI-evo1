.PHONY: test test-all build-all clean-all

# Sub-projects in this directory
SUBPROJECTS = clauditable clod ambiguous-agent federation-command

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
