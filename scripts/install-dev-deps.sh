#!/usr/bin/env bash
set -euo pipefail

# Install or check OS-level dependencies for 'make deploy-dev-binaries'.
#
# Usage:
#   ./scripts/install-dev-deps.sh           # install missing deps and create dirs
#   ./scripts/install-dev-deps.sh --check   # report status only; exit 1 if anything missing

PROG=$(basename "$0")

# Minimum required versions
GO_MIN_MAJOR=1
GO_MIN_MINOR=25
NODE_MIN_MAJOR=18

# Go version to download when missing. Override via env: GO_INSTALL_VERSION=1.25.1 ./install-dev-deps.sh
GO_INSTALL_VERSION="${GO_INSTALL_VERSION:-1.25.0}"
GO_INSTALL_DIR=/usr/local

# Node.js major version to install when missing (must be >= NODE_MIN_MAJOR)
NODE_INSTALL_MAJOR=20

# Directories required at build and runtime (deepest paths; parents are created automatically)
REQUIRED_DIRS=(
    "/AI-evo1-dev/bin"
    "/host-agent-files/agent-records"
)

# ── Argument parsing ──────────────────────────────────────────────────────────

CHECK_MODE=false
for arg in "$@"; do
    case "$arg" in
        --check|-c)
            CHECK_MODE=true
            ;;
        --help|-h)
            printf 'Usage: %s [--check]\n\n' "$PROG"
            printf '  (no flag)  Install missing OS-level dependencies and create required directories.\n'
            printf '  --check    Report dependency status without making changes. Exits 1 if anything is missing.\n\n'
            printf 'Environment:\n'
            printf '  GO_INSTALL_VERSION  Go version to download when missing (default: %s)\n' "$GO_INSTALL_VERSION"
            exit 0
            ;;
        *)
            printf 'Unknown argument: %s\n' "$arg" >&2
            exit 1
            ;;
    esac
done

# ── Helpers ───────────────────────────────────────────────────────────────────

issues=0

ok()   { printf '  [  OK  ] %s\n' "$*"; }
fail() { printf '  [MISSING] %s\n' "$*"; issues=$((issues + 1)); }
info() { printf '  %s\n' "$*"; }
hdr()  { printf '\n%s\n' "$*"; }

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        printf 'ERROR: %s must be run as root to install dependencies (e.g. sudo %s).\n' "$PROG" "$PROG" >&2
        exit 1
    fi
}

# Returns 0 if the installed Go major.minor satisfies the minimum.
go_version_satisfies() {
    local major="$1" minor="$2"
    if [ "$major" -gt "$GO_MIN_MAJOR" ]; then return 0; fi
    if [ "$major" -eq "$GO_MIN_MAJOR" ] && [ "$minor" -ge "$GO_MIN_MINOR" ]; then return 0; fi
    return 1
}

# Emit major and minor of the installed Go version to stdout (space-separated).
installed_go_ver() {
    local raw
    raw=$(go version 2>/dev/null | sed 's/.*go\([0-9]*\.[0-9]*\).*/\1/')
    printf '%s %s' "$(printf '%s' "$raw" | cut -d. -f1)" "$(printf '%s' "$raw" | cut -d. -f2)"
}

# Emit major of the installed Node.js version to stdout.
installed_node_major() {
    node --version 2>/dev/null | tr -d 'v' | cut -d. -f1
}

# ── Check functions ───────────────────────────────────────────────────────────

check_go() {
    hdr "Go (>= ${GO_MIN_MAJOR}.${GO_MIN_MINOR}):"
    if ! command -v go &>/dev/null; then
        fail "go not found in PATH"
        return
    fi
    local raw major minor
    raw=$(go version | sed 's/.*go\([0-9]*\.[0-9]*\).*/\1/')
    major=$(printf '%s' "$raw" | cut -d. -f1)
    minor=$(printf '%s' "$raw" | cut -d. -f2)
    if go_version_satisfies "$major" "$minor"; then
        ok "go ${raw}  ($(command -v go))"
    else
        fail "go ${raw} found — ${GO_MIN_MAJOR}.${GO_MIN_MINOR}+ required"
    fi
}

check_node() {
    hdr "Node.js (>= ${NODE_MIN_MAJOR}):"
    if ! command -v node &>/dev/null; then
        fail "node not found in PATH"
        return
    fi
    local ver major
    ver=$(node --version | tr -d 'v')
    major=$(printf '%s' "$ver" | cut -d. -f1)
    if [ "$major" -ge "$NODE_MIN_MAJOR" ]; then
        ok "node v${ver}  ($(command -v node))"
    else
        fail "node v${major} found — ${NODE_MIN_MAJOR}+ required"
    fi
}

check_npm() {
    hdr "npm:"
    if command -v npm &>/dev/null; then
        ok "npm $(npm --version)  ($(command -v npm))"
    else
        fail "npm not found in PATH"
    fi
}

check_make() {
    hdr "make:"
    if command -v make &>/dev/null; then
        ok "make found  ($(command -v make))"
    else
        fail "make not found in PATH"
    fi
}

check_dirs() {
    hdr "Required directories:"
    for dir in "${REQUIRED_DIRS[@]}"; do
        if [ -d "$dir" ]; then
            ok "$dir"
        else
            fail "$dir  (not found)"
        fi
    done
}

run_all_checks() {
    printf '=== Dependency check for: make deploy-dev-binaries ===\n'
    check_go
    check_node
    check_npm
    check_make
    check_dirs
    printf '\n'
}

# ── Install functions ─────────────────────────────────────────────────────────

require_curl() {
    if ! command -v curl &>/dev/null; then
        printf 'ERROR: curl is required for downloads. Install it first:\n' >&2
        printf '       apt-get install -y curl\n' >&2
        exit 1
    fi
}

install_go() {
    hdr "Installing Go ${GO_INSTALL_VERSION}..."
    require_curl
    local tarball="go${GO_INSTALL_VERSION}.linux-amd64.tar.gz"
    local url="https://go.dev/dl/${tarball}"
    local tmp
    tmp=$(mktemp -d)
    info "Downloading ${url}..."
    curl -fSL --progress-bar "$url" -o "${tmp}/${tarball}"
    info "Extracting to ${GO_INSTALL_DIR}..."
    rm -rf "${GO_INSTALL_DIR}/go"
    tar -C "${GO_INSTALL_DIR}" -xzf "${tmp}/${tarball}"
    rm -rf "$tmp"
    # Make the freshly installed go available for the rest of this script
    export PATH="${GO_INSTALL_DIR}/go/bin:${PATH}"
    info "Installed to ${GO_INSTALL_DIR}/go"
    info "Add '${GO_INSTALL_DIR}/go/bin' to your PATH if not already configured."
}

install_node_npm() {
    hdr "Installing Node.js ${NODE_INSTALL_MAJOR}.x (includes npm)..."
    require_curl
    local setup
    setup=$(mktemp /tmp/nodesource_setup.XXXXXX.sh)
    info "Fetching NodeSource setup script for Node.js ${NODE_INSTALL_MAJOR}..."
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_INSTALL_MAJOR}.x" -o "$setup"
    bash "$setup"
    rm -f "$setup"
    apt-get install -y nodejs
}

install_make_pkg() {
    hdr "Installing make..."
    apt-get update -qq
    apt-get install -y make
}

create_missing_dirs() {
    hdr "Creating missing directories..."
    local created=0
    for dir in "${REQUIRED_DIRS[@]}"; do
        if [ ! -d "$dir" ]; then
            mkdir -p "$dir"
            # Hand ownership to the real user when invoked via sudo.
            chmod 777 -R $dir
            info "Created: $dir"
            created=$((created + 1))
        else
            info "Already exists: $dir"
        fi
    done
    if [ "$created" -eq 0 ]; then
        info "All directories already exist."
    fi
}

# ── Main ──────────────────────────────────────────────────────────────────────

run_all_checks

if "$CHECK_MODE"; then
    if [ "$issues" -gt 0 ]; then
        printf '=== %d issue(s) found. Run "./%s" (without --check) to install. ===\n' "$issues" "$PROG"
        exit 1
    fi
    printf '=== All dependencies satisfied. ===\n'
    exit 0
fi

if [ "$issues" -eq 0 ]; then
    printf '=== All dependencies already satisfied. Nothing to install. ===\n'
    exit 0
fi

printf '=== Installing %d missing item(s)... ===\n' "$issues"

require_root

# Determine what needs to be installed
need_go=false
need_node=false
need_make=false

if ! command -v go &>/dev/null; then
    need_go=true
else
    read -r go_major go_minor <<< "$(installed_go_ver)"
    if ! go_version_satisfies "$go_major" "$go_minor"; then
        need_go=true
    fi
fi

if ! command -v node &>/dev/null; then
    need_node=true
elif [ "$(installed_node_major)" -lt "$NODE_MIN_MAJOR" ]; then
    need_node=true
fi
# npm ships with nodejs via NodeSource; if npm is missing but node is ok, also reinstall
if ! command -v npm &>/dev/null; then
    need_node=true
fi

if ! command -v make &>/dev/null; then
    need_make=true
fi

# Install in dependency order
if "$need_go";   then install_go;       fi
if "$need_node"; then install_node_npm; fi
if "$need_make"; then install_make_pkg; fi
create_missing_dirs

# Verify everything is now in order
printf '\n=== Verifying installation... ===\n'
issues=0
run_all_checks

if [ "$issues" -gt 0 ]; then
    printf '=== %d issue(s) remain after installation. Please investigate manually. ===\n' "$issues"
    exit 1
fi
printf '=== All dependencies satisfied. ===\n'
