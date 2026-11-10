.PHONY: test test-all build-all clean-all deploy-dev-binaries update-dev-binaries list-subprojects check-dev-deps

# Sub-projects in this directory. cats-ufa-plugin must stay listed: it is
# what local-representative's plugins tab "register example" launches from
# DEV_BIN_DIR (condocs/privateRepoOpsImpls/Step3Prompt.md Revision E).
SUBPROJECTS = ufa-configurable ufa-version ufa-hostid ufa-loader clauditable clod ambiguous-agent dtt federation-command dungeon-keeper pr-keeper condocv2 session-manager the-conversationalist ianar composer diagrammographer cats-ufa-plugin local-representative agent-coordinator

DEV_BIN_DIR=/AI-evo1-dev/bin
# Staging dir binaries are actually built into -- a sibling of DEV_BIN_DIR so
# the final swap below is a same-filesystem rename, not a cross-device copy.
# See Step5Prompt.md Revision L.
DEV_BIN_STAGING_DIR=$(DEV_BIN_DIR).new

# The sub-apps 'make update-dev-binaries' builds, e.g.
#   make update-dev-binaries APPS="condocv2 the-conversationalist"
# Built in SUBPROJECTS order whatever order they're given in.
APPS ?=
UPDATE_APPS = $(filter $(APPS),$(SUBPROJECTS))
UNKNOWN_APPS = $(filter-out $(SUBPROJECTS),$(APPS))

# Agent install scripts, copied into DEV_BIN_DIR/scripts by both deploy
# targets: where local-representative finds them (agentinstall.go) for the
# debug -> install tab when it isn't watching a --dev-repo checkout.
AGENT_INSTALL_SCRIPTS = scripts/install-pi.sh scripts/install-opencode.sh

# Run tests in all sub-projects
test: test-all

#
# Every sub-project is tested even if an earlier one fails, so a single
# failure doesn't hide the state of the rest; the failures are listed (and
# the target fails) at the end.
test-all:
	@echo "Running tests for all AI-evo1 sub-projects..."
	@failed=""; \
	for proj in $(SUBPROJECTS); do \
		echo ""; \
		echo "=== Testing $$proj ==="; \
		$(MAKE) -C $$proj test || failed="$$failed $$proj"; \
	done; \
	echo ""; \
	if [ -n "$$failed" ]; then \
		echo "=== Tests FAILED in:$$failed ==="; \
		exit 1; \
	fi; \
	echo "=== All tests passed ==="

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
	@bash scripts/record-dev-build.sh $(DEV_BIN_STAGING_DIR) "$$(git rev-parse HEAD)" $(SUBPROJECTS) || { \
		rm -rf $(DEV_BIN_STAGING_DIR); rm -f .building; exit 1; }
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
	@mkdir -p $(DEV_BIN_STAGING_DIR)/scripts && cp $(AGENT_INSTALL_SCRIPTS) $(DEV_BIN_STAGING_DIR)/scripts/ || { \
		rm -rf $(DEV_BIN_STAGING_DIR); rm -f .building; exit 1; }
	@echo ""
	@echo "Swapping staged binaries into $(DEV_BIN_DIR)..."
	rm -rf $(DEV_BIN_DIR)
	mv $(DEV_BIN_STAGING_DIR) $(DEV_BIN_DIR)
	@echo ""
	@echo "=== All binaries deployed to $(DEV_BIN_DIR) ==="
	@ls -la $(DEV_BIN_DIR)
	@rm -f .building

# Rebuild only the sub-apps named in APPS, leaving every other binary in
# DEV_BIN_DIR as it is -- what local-representative's dev-repo watcher runs
# for the core repo, naming the sub-apps a .ufabuild request's commits
# changed (local-representative/ufaplan.go, docs/DevMode.md "Per-sub-app
# builds"). Plug-in dev repos still use deploy-dev-binaries.
#
# Same '.building' lock and staging-then-swap as deploy-dev-binaries, except
# the staging dir starts as a copy of the current DEV_BIN_DIR, so a failed or
# interrupted build still leaves DEV_BIN_DIR untouched. Both targets record,
# in DEV_BIN_DIR/.ufabuild-built, the commit each sub-app was last built
# from (scripts/record-dev-build.sh) -- what the watcher diffs against to
# tell which sub-apps changed.
update-dev-binaries: check-dev-deps
	@if [ -z "$(strip $(APPS))" ]; then \
		echo "usage: make update-dev-binaries APPS=\"<sub-app> ...\""; \
		echo "sub-apps: $(SUBPROJECTS)"; \
		exit 1; \
	fi
	@if [ -n "$(UNKNOWN_APPS)" ]; then \
		echo "unknown sub-app(s): $(UNKNOWN_APPS)"; \
		echo "sub-apps: $(SUBPROJECTS)"; \
		exit 1; \
	fi
	@touch .building
	@fail() { \
		echo ""; \
		echo "=== $$1 -- leaving $(DEV_BIN_DIR) untouched ==="; \
		rm -rf $(DEV_BIN_STAGING_DIR); \
		rm -f .building; \
		exit 1; \
	}; \
	commit=$$(git rev-parse HEAD) || fail "git rev-parse HEAD failed"; \
	echo "Updating $(UPDATE_APPS) from $$commit in staging dir $(DEV_BIN_STAGING_DIR)..."; \
	rm -rf $(DEV_BIN_STAGING_DIR); \
	if [ -d $(DEV_BIN_DIR) ]; then \
		cp -a $(DEV_BIN_DIR) $(DEV_BIN_STAGING_DIR) || fail "Copying $(DEV_BIN_DIR) failed"; \
	else \
		mkdir -p $(DEV_BIN_STAGING_DIR) || fail "Creating $(DEV_BIN_STAGING_DIR) failed"; \
	fi; \
	bash scripts/record-dev-build.sh $(DEV_BIN_STAGING_DIR) $$commit $(UPDATE_APPS) || fail "Recording the build failed"; \
	for proj in $(UPDATE_APPS); do \
		echo ""; \
		echo "=== Building $$proj ==="; \
		$(MAKE) -C $$proj deploy-dev-binary DEV_BIN_DIR=$(DEV_BIN_STAGING_DIR) || fail "Build of $$proj failed"; \
	done; \
	mkdir -p $(DEV_BIN_STAGING_DIR)/scripts && cp $(AGENT_INSTALL_SCRIPTS) $(DEV_BIN_STAGING_DIR)/scripts/ || fail "Copying the agent install scripts failed"; \
	echo ""; \
	echo "Swapping staged binaries into $(DEV_BIN_DIR)..."; \
	rm -rf $(DEV_BIN_DIR) && mv $(DEV_BIN_STAGING_DIR) $(DEV_BIN_DIR) || { rm -f .building; exit 1; }; \
	echo ""; \
	echo "=== Updated $(UPDATE_APPS) in $(DEV_BIN_DIR) ==="
	@rm -f .building

# The sub-projects, space-separated -- how local-representative's dev-repo
# watcher learns which sub-apps there are (ufaplan.go).
list-subprojects:
	@echo $(SUBPROJECTS)
