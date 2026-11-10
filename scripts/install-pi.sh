#!/usr/bin/env bash
set -euo pipefail

# Install or check the Pi coding agent (https://pi.dev), the "pi" agent
# ambiguous-agent runs. Replicates forjor/hello-pi's devcontainer install
# (.devcontainer/devcontainer.json postCreateCommand + start.sh) on this host:
#
#   - Node.js LTS -- Pi needs >= 22.19 (hello-pi README) -- with npm
#   - npm install -g --ignore-scripts @earendil-works/pi-coding-agent
#   - ~/.pi, where pi keeps its credentials and config
#
# What hello-pi's container got for free is done here explicitly: a too-old
# or missing Node.js is replaced from NodeSource, and when the global npm
# prefix isn't writable the package goes to ~/.local instead.
#
# Run by hand or from agent-coordinator's debug -> install tab (through
# local-representative, agentinstall.go), which runs it as LR's user with no
# terminal -- so anything needing root goes through 'sudo -n' and fails
# clearly rather than waiting for a password.
#
# Usage:
#   ./scripts/install-pi.sh           # install pi if missing
#   ./scripts/install-pi.sh --check   # report status only; exit 1 if pi is missing

PROG=$(basename "$0")

PI_PACKAGE="@earendil-works/pi-coding-agent"

# Minimum Node.js version Pi runs on, and the NodeSource major installed when
# the host's is missing or older.
NODE_MIN_MAJOR=22
NODE_MIN_MINOR=19
NODE_INSTALL_MAJOR="${NODE_INSTALL_MAJOR:-22}"

# Where pi goes when the global npm prefix isn't writable and root isn't
# available: its bin lands in $USER_PREFIX/bin.
USER_PREFIX="${HOME}/.local"

# ── Argument parsing ──────────────────────────────────────────────────────────

CHECK_MODE=false
for arg in "$@"; do
    case "$arg" in
        --check|-c)
            CHECK_MODE=true
            ;;
        --help|-h)
            printf 'Usage: %s [--check]\n\n' "$PROG"
            printf '  (no flag)  Install the Pi coding agent (%s) if it is missing.\n' "$PI_PACKAGE"
            printf '  --check    Report status without making changes. Exits 1 if pi is missing.\n\n'
            printf 'Environment:\n'
            printf '  NODE_INSTALL_MAJOR  Node.js major to install when missing or too old (default: %s)\n' "$NODE_INSTALL_MAJOR"
            exit 0
            ;;
        *)
            printf 'Unknown argument: %s\n' "$arg" >&2
            exit 1
            ;;
    esac
done

# ── Helpers ───────────────────────────────────────────────────────────────────

ok()   { printf '  [  OK  ] %s\n' "$*"; }
miss() { printf '  [MISSING] %s\n' "$*"; }
info() { printf '  %s\n' "$*"; }
hdr()  { printf '\n%s\n' "$*"; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# as_root runs a command as root: directly when already root, else through
# passwordless sudo. Returns 1 without running it when neither is possible.
as_root() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    elif command -v sudo &>/dev/null && sudo -n true 2>/dev/null; then
        sudo -n "$@"
    else
        return 1
    fi
}

can_root() {
    [ "$(id -u)" -eq 0 ] || { command -v sudo &>/dev/null && sudo -n true 2>/dev/null; }
}

# node_satisfies reports whether the installed Node.js is >= NODE_MIN_MAJOR.NODE_MIN_MINOR.
node_satisfies() {
    command -v node &>/dev/null || return 1
    local ver major minor
    ver=$(node --version 2>/dev/null | tr -d 'v')
    major=$(printf '%s' "$ver" | cut -d. -f1)
    minor=$(printf '%s' "$ver" | cut -d. -f2)
    [ -n "$major" ] && [ -n "$minor" ] || return 1
    if [ "$major" -gt "$NODE_MIN_MAJOR" ]; then return 0; fi
    [ "$major" -eq "$NODE_MIN_MAJOR" ] && [ "$minor" -ge "$NODE_MIN_MINOR" ]
}

# ── Check ─────────────────────────────────────────────────────────────────────

check_all() {
    local missing=0
    printf '=== Pi coding agent check ===\n'
    hdr "pi:"
    if command -v pi &>/dev/null; then
        ok "pi $(pi --version 2>/dev/null | head -n1)  ($(command -v pi))"
    elif [ -x "${USER_PREFIX}/bin/pi" ]; then
        miss "pi is installed at ${USER_PREFIX}/bin/pi but ${USER_PREFIX}/bin is not on PATH"
        missing=1
    else
        miss "pi not found in PATH"
        missing=1
    fi
    hdr "Node.js (>= ${NODE_MIN_MAJOR}.${NODE_MIN_MINOR}):"
    if node_satisfies; then
        ok "node $(node --version)  ($(command -v node))"
    elif command -v node &>/dev/null; then
        miss "node $(node --version) found -- ${NODE_MIN_MAJOR}.${NODE_MIN_MINOR}+ required"
    else
        miss "node not found in PATH"
    fi
    hdr "npm:"
    if command -v npm &>/dev/null; then
        ok "npm $(npm --version)  ($(command -v npm))"
    else
        miss "npm not found in PATH"
    fi
    printf '\n'
    return "$missing"
}

# ── Install ───────────────────────────────────────────────────────────────────

install_node() {
    hdr "Installing Node.js ${NODE_INSTALL_MAJOR}.x (includes npm) from NodeSource..."
    can_root || die "Node.js >= ${NODE_MIN_MAJOR}.${NODE_MIN_MINOR} is needed and installing it needs root: re-run as root (sudo $0), or install Node.js ${NODE_INSTALL_MAJOR} yourself."
    command -v curl &>/dev/null || as_root apt-get install -y curl
    local setup
    setup=$(mktemp /tmp/nodesource_setup.XXXXXX.sh)
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_INSTALL_MAJOR}.x" -o "$setup"
    as_root bash "$setup"
    rm -f "$setup"
    as_root apt-get install -y nodejs
    node_satisfies || die "Node.js is still older than ${NODE_MIN_MAJOR}.${NODE_MIN_MINOR} after installing: $(node --version 2>/dev/null || echo none)"
    info "Installed node $(node --version)"
}

install_pi() {
    hdr "Installing ${PI_PACKAGE}..."
    local prefix
    prefix=$(npm prefix -g)
    if [ -w "${prefix}/lib" ] || { [ ! -e "${prefix}/lib" ] && [ -w "$prefix" ]; }; then
        npm install -g --ignore-scripts "$PI_PACKAGE"
    elif can_root; then
        info "Global npm prefix ${prefix} is not writable -- installing as root."
        as_root npm install -g --ignore-scripts "$PI_PACKAGE"
    else
        info "Global npm prefix ${prefix} is not writable and root isn't available -- installing into ${USER_PREFIX}."
        mkdir -p "$USER_PREFIX"
        npm install -g --ignore-scripts --prefix "$USER_PREFIX" "$PI_PACKAGE"
    fi
}

# ── Main ──────────────────────────────────────────────────────────────────────

if check_all; then
    if "$CHECK_MODE"; then
        printf '=== pi is installed. ===\n'
    else
        printf '=== pi is already installed. Nothing to do. ===\n'
    fi
    exit 0
fi

if "$CHECK_MODE"; then
    printf '=== pi is not installed. Run "./%s" (without --check) to install it. ===\n' "$PROG"
    exit 1
fi

printf '=== Installing pi... ===\n'

node_satisfies && command -v npm &>/dev/null || install_node
install_pi

# hello-pi's start.sh creates ~/.pi (bind-mounted into its container) so
# saved pi credentials have somewhere to live.
mkdir -p "${HOME}/.pi"
info "~/.pi exists"

printf '\n=== Verifying installation... ===\n'
if check_all; then
    printf '=== pi installed. Export ANTHROPIC_API_KEY, GEMINI_API_KEY or XAI_API_KEY (or log in with pi) to use it. ===\n'
    exit 0
fi
if [ -x "${USER_PREFIX}/bin/pi" ]; then
    printf '=== pi installed at %s/bin/pi, but that directory is not on PATH. Add it to PATH (for local-representative too) so agents can find pi. ===\n' "$USER_PREFIX"
    exit 1
fi
printf '=== pi still not found after installing. Please investigate manually. ===\n'
exit 1
