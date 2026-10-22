#!/usr/bin/env bash
set -euo pipefail

# Commits a .ufabuild build request: a commit whose sole change is the
# repo-root .ufabuild naming the commit you're on as its parent. Every dev-mode
# LR following this branch with "ufabuild watch" on builds once it pulls the
# commit (see docs/DevMode.md). The commit is pushed to the branch's upstream
# unless told otherwise.
#
# Usage: scripts/ufabuild.sh [nopush]
#
#   nopush  commit the request locally but don't push it.
#
# Refuses to run unless the working tree is clean (no staged, unstaged or
# untracked changes) and HEAD is on a branch, so the request names exactly
# the commit that's checked out and nothing else rides along with it.

usage() {
    echo "usage: $0 [nopush]" >&2
    exit 2
}

push=true
for arg in "$@"; do
    case "$arg" in
        nopush|--nopush|--no-push) push=false ;;
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

branch=$(git symbolic-ref --quiet --short HEAD) || {
    echo "error: HEAD is detached; check out the dev branch first" >&2
    exit 1
}

if [ -n "$(git status --porcelain --untracked-files=normal)" ]; then
    echo "error: working tree isn't clean; commit or stash these first:" >&2
    git status --short >&2
    exit 1
fi

parent=$(git rev-parse HEAD)

printf 'parent: %s\n' "$parent" > .ufabuild
git add .ufabuild
git commit -q -m "ufabuild: build ${parent:0:7}" -- .ufabuild

echo "committed .ufabuild request $(git rev-parse --short HEAD) on $branch (parent ${parent:0:7})"

if $push; then
    git push
else
    echo "not pushed (nopush); run 'git push' when you want the watcher to see it"
fi
