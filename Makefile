.PHONY: test test-all build-all clean-all deploy-dev-binaries check-dev-deps

# Sub-projects in this directory
SUBPROJECTS = ufa-configurable ufa-version ufa-loader clauditable clod ambiguous-agent federation-command dungeon-keeper condoccer session-manager the-conversationalist local-representative agent-coordinator

DEV_BIN_DIR=/AI-evo1-dev/bin
# Staging dir binaries are actually built into -- a sibling of DEV_BIN_DIR so
# the final swap below is a same-filesystem rename, not a cross-device copy.
# See Step5Prompt.md Revision L.
DEV_BIN_STAGING_DIR=$(DEV_BIN_DIR).new

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
#
# Every sub-project actually builds into DEV_BIN_STAGING_DIR, a scratch
# staging directory, rather than DEV_BIN_DIR itself -- DEV_BIN_DIR is only
# ever touched by the final swap once every sub-project has deployed
# successfully. This is Revision L's fix for auto-rebuild losing the dev
# binaries it's supposed to be replacing: previously this recipe wiped
# DEV_BIN_DIR *before* rebuilding into it, so any failure partway through
# the sub-project loop below (or anything killing this recipe outright, e.g.
# an auto-update restart racing a rebuild) left DEV_BIN_DIR missing whichever
# binaries hadn't been re-deployed yet -- ufa-loader would then find nothing
# to (re)launch. Staging first means a failed or interrupted build simply
# leaves the previous, still-working DEV_BIN_DIR untouched.
deploy-dev-binaries: check-dev-deps
	@touch .building
	@echo "Building into staging dir $(DEV_BIN_STAGING_DIR)..."
	rm -rf $(DEV_BIN_STAGING_DIR)
	mkdir -p $(DEV_BIN_STAGING_DIR)
	@for proj in $(SUBPROJECTS); do \
		echo ""; \
		echo "=== Building $$proj ==="; \
		$(MAKE) -C $$proj deploy-dev-binary DEV_BIN_DIR=$(DEV_BIN_STAGING_DIR) || { \
			echo ""; \
			echo "=== Build failed -- leaving $(DEV_BIN_DIR) untouched ==="; \
			rm -rf $(DEV_BIN_STAGING_DIR); \
			rm -f .building; \
			exit 1; \
		}; \
	done
	@echo ""
	@echo "Swapping staged binaries into $(DEV_BIN_DIR)..."
	rm -rf $(DEV_BIN_DIR)
	mv $(DEV_BIN_STAGING_DIR) $(DEV_BIN_DIR)
	@echo ""
	@echo "=== All binaries deployed to $(DEV_BIN_DIR) ==="
	@ls -la $(DEV_BIN_DIR)
	@rm -f .building
