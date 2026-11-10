#!/usr/bin/env bash
set -euo pipefail

# Writes a .ufabuild build request for the commit you're on: the same
# gitignored, never-committed request a dev-mode LR's dev-repo watcher writes
# itself when it pulls in a [condocv2-xxxx]-tagged commit (see
# docs/DevMode.md). It's local to this checkout -- it asks the LR watching
# this checkout to build, and no other. That LR's rebuild button lights up;
# with auto-rebuild on, it builds after the usual 90-second delay.
#
# Usage: scripts/ufabuild.sh
#
# Refuses to run unless the working tree is clean (no staged or unstaged
# changes; the watcher won't build a dirty tree anyway) and .ufabuild is
# ignored here, as it is on any branch from condocs v2's switch to ufabuild
# onward.

usage() {
    echo "usage: $0" >&2
    exit 2
}

for arg in "$@"; do
    case "$arg" in
        -h|--help) usage ;;
        *) echo "unknown argument: $arg" >&2; usage ;;
    esac
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

if ! git rev-parse --git-dir >/dev/null 2>&1; then
    echo "error: $REPO_ROOT isn't a git checkout" >&2
    exit 1
fi

if git ls-files --error-unmatch -- .ufabuild >/dev/null 2>&1; then
    echo "error: .ufabuild is tracked on this branch (the old committed paradigm); switch to a branch that gitignores it" >&2
    exit 1
fi
if ! git check-ignore -q .ufabuild; then
    echo "error: .ufabuild isn't gitignored here; add /.ufabuild to .gitignore first" >&2
    exit 1
fi

if [ -n "$(git diff HEAD)" ]; then
    echo "error: working tree isn't clean; commit or revert these first:" >&2
    git status --short >&2
    exit 1
fi

commit=$(git rev-parse HEAD)

cat > .ufabuild <<EOF
# A build request for local-representative's dev-repo watcher: gitignored,
# and removed once 'make deploy-dev-binaries' has built it.
commit: $commit
tag: manual
tagged: $commit
requested_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF

echo "wrote a .ufabuild request for ${commit:0:7}"
