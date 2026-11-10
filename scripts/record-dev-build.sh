#!/usr/bin/env bash
set -euo pipefail

# Records, in <bin-dir>/.ufabuild-built, that each named sub-app was built
# from <commit>:
#
#   scripts/record-dev-build.sh <bin-dir> <commit> <sub-app>...
#
# One "<sub-app> <commit>" line per sub-app, sorted; sub-apps not named keep
# whatever line they had. 'make deploy-dev-binaries' and 'make
# update-dev-binaries' run this against their staging dir, so the record is
# swapped into the dev bin dir together with the binaries it describes.
# local-representative's dev-repo watcher diffs each sub-app's recorded
# commit against HEAD to tell which sub-apps a build needs
# (local-representative/ufaplan.go).

if [ $# -lt 2 ]; then
    echo "usage: $0 <bin-dir> <commit> <sub-app>..." >&2
    exit 2
fi
dir=$1
commit=$2
shift 2

rec="$dir/.ufabuild-built"
tmp="$rec.tmp"
{
    if [ -f "$rec" ]; then
        awk -v apps=" $* " 'NF == 2 && index(apps, " " $1 " ") == 0' "$rec"
    fi
    for app in "$@"; do
        printf '%s %s\n' "$app" "$commit"
    done
} | sort > "$tmp"
mv "$tmp" "$rec"
