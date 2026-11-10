#!/usr/bin/env bash
set -euo pipefail

# Computes the repo-level version string every sub-application Makefile
# embeds into its binary at build time via:
#
#   go build -ldflags "-X ufa-version.Version=$$(../scripts/compute-version.sh)" -o <bin> .
#
# Prints just the version string to stdout. Versions are computed from the
# repo's git state, not per sub-application: every binary built from the
# same commit gets the same version, so a `make deploy-dev-binaries` after
# touching any single sub-application gives *all* of them a new version.
#
# Rules:
#   - If HEAD is exactly a "vX.Y.Z" tag, print that tag verbatim.
#   - Otherwise print:
#       <next-patch-of-most-recent-semver-tag>-<branch-heuristic>-<shortsha>
#     e.g. most recent tag v0.4.2 on branch "main" -> v0.4.3-main-8b1e2d4
#     or on branch "condoc/InitialDistributedDevelopment-1789821651/main"
#     -> v0.4.3-inidisdev-8b1e2d4
#   - Outside a git checkout (e.g. a Docker build that doesn't COPY .git),
#     prints "dev".

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

if ! git rev-parse --git-dir >/dev/null 2>&1; then
    echo "dev"
    exit 0
fi

# HEAD is exactly a semver tag -> use it as-is.
if exact_tag=$(git describe --tags --exact-match --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null); then
    echo "$exact_tag"
    exit 0
fi

# Most recent semver tag reachable from HEAD, sorted by version number (not
# creation order, so an out-of-order tag doesn't produce a stale bump).
last_tag=$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -n1)
if [ -z "$last_tag" ]; then
    last_tag="v0.0.0"
fi

ver="${last_tag#v}"
major="${ver%%.*}"
rest="${ver#*.}"
minor="${rest%%.*}"
patch="${rest#*.}"
next_patch="v${major}.${minor}.$((patch + 1))"

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
short_sha=$(git rev-parse --short HEAD 2>/dev/null || echo "0000000")

# branch_heuristic condenses a branch name into a short, stable label:
#   - "main"/"master" pass through unchanged.
#   - condoc branches (condoc/<CamelCaseStepName>-<timestamp>/<parent>) use
#     the initials of the CamelCase step name, e.g.
#     "InitialDistributedDevelopment" -> "inidisdev".
#   - anything else falls back to the initials of every "/"/"-"-separated
#     segment of the full branch name.
branch_heuristic() {
    local branch="$1"
    if [ "$branch" = "main" ] || [ "$branch" = "master" ]; then
        printf '%s' "$branch"
        return
    fi

    local seg base camel_seg=""
    IFS='/' read -ra segs <<< "$branch"
    for seg in "${segs[@]}"; do
        # Strip a trailing "-<digits>" suffix (condoc branches carry a
        # start-time timestamp there) before checking for a CamelCase word.
        base=$(printf '%s' "$seg" | sed -E 's/-[0-9]+$//')
        if printf '%s' "$base" | grep -Eq '[a-z][A-Z]'; then
            camel_seg="$base"
            break
        fi
    done
    local target="${camel_seg:-$branch}"

    local abbr
    abbr=$(printf '%s' "$target" \
        | sed -E 's/([a-z0-9])([A-Z])/\1 \2/g' \
        | tr '/_-' '   ' \
        | tr ' ' '\n' \
        | grep -v '^$' \
        | cut -c1-3 \
        | tr '[:upper:]' '[:lower:]' \
        | tr -d '\n')

    if [ -z "$abbr" ]; then
        abbr=$(printf '%s' "$branch" | tr -c '[:alnum:]' '-' | cut -c1-12 | tr '[:upper:]' '[:lower:]')
    fi
    printf '%.20s' "$abbr"
}

heuristic=$(branch_heuristic "$branch")

echo "${next_patch}-${heuristic}-${short_sha}"
