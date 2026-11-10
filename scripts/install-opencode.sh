#!/usr/bin/env bash
set -euo pipefail

# Install or check the opencode CLI (https://opencode.ai), the "opencode"
# agent ambiguous-agent runs. Replicates forjor/hello-open-code's devcontainer
# install (.devcontainer/devcontainer.json postCreateCommand + start.sh) on
# this host:
#
#   - curl and tar, which opencode's installer needs
#   - curl -fsSL https://opencode.ai/install | bash -s -- --no-modify-path,
#     which puts the binary in ~/.opencode/bin
#   - ~/.config/opencode and ~/.local/share/opencode, its config and data
#
# hello-open-code puts ~/.opencode/bin on PATH by appending to ~/.zshrc and
# ~/.bashrc; local-representative and the agents it runs never read those,
# so instead opencode is symlinked into /usr/local/bin (as root), or
# ~/.local/bin when root isn't available.
#
# Run by hand or from agent-coordinator's debug -> install tab (through
# local-representative, agentinstall.go), which runs it as LR's user with no
# terminal -- so anything needing root goes through 'sudo -n' and fails
# clearly rather than waiting for a password.
#
# Usage:
#   ./scripts/install-opencode.sh           # install opencode if missing
#   ./scripts/install-opencode.sh --check   # report status only; exit 1 if opencode is missing

PROG=$(basename "$0")

INSTALLER_URL="https://opencode.ai/install"
OPENCODE_BIN="${HOME}/.opencode/bin/opencode"
SYSTEM_LINK_DIR=/usr/local/bin
USER_LINK_DIR="${HOME}/.local/bin"

# ── Argument parsing ──────────────────────────────────────────────────────────

CHECK_MODE=false
for arg in "$@"; do
    case "$arg" in
        --check|-c)
            CHECK_MODE=true
            ;;
        --help|-h)
            printf 'Usage: %s [--check]\n\n' "$PROG"
            printf '  (no flag)  Install the opencode CLI (via %s) if it is missing.\n' "$INSTALLER_URL"
            printf '  --check    Report status without making changes. Exits 1 if opencode is missing.\n'
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

# ── Check ─────────────────────────────────────────────────────────────────────

check_all() {
    printf '=== opencode check ===\n'
    hdr "opencode:"
    if command -v opencode &>/dev/null; then
        ok "opencode $(opencode --version 2>/dev/null | head -n1)  ($(command -v opencode))"
        printf '\n'
        return 0
    fi
    if [ -x "$OPENCODE_BIN" ]; then
        miss "opencode is installed at ${OPENCODE_BIN} but is not on PATH"
    else
        miss "opencode not found in PATH"
    fi
    printf '\n'
    return 1
}

# ── Install ───────────────────────────────────────────────────────────────────

install_prereqs() {
    local need=()
    command -v curl &>/dev/null || need+=(curl)
    command -v tar &>/dev/null || need+=(tar)
    [ "${#need[@]}" -eq 0 ] && return 0
    hdr "Installing ${need[*]}..."
    can_root || die "opencode's installer needs ${need[*]}, and installing them needs root: re-run as root (sudo $0), or install them yourself."
    as_root apt-get update -qq
    as_root apt-get install -y "${need[@]}"
}

install_opencode() {
    hdr "Running opencode's installer (${INSTALLER_URL})..."
    curl -fsSL "$INSTALLER_URL" | bash -s -- --no-modify-path
    [ -x "$OPENCODE_BIN" ] || die "the installer finished but ${OPENCODE_BIN} is missing"
}

# link_opencode puts opencode on PATH for every process, not just login shells.
link_opencode() {
    hdr "Putting opencode on PATH..."
    if can_root; then
        as_root ln -sf "$OPENCODE_BIN" "${SYSTEM_LINK_DIR}/opencode"
        info "Symlinked ${SYSTEM_LINK_DIR}/opencode -> ${OPENCODE_BIN}"
        return
    fi
    mkdir -p "$USER_LINK_DIR"
    ln -sf "$OPENCODE_BIN" "${USER_LINK_DIR}/opencode"
    info "Root isn't available -- symlinked ${USER_LINK_DIR}/opencode -> ${OPENCODE_BIN}"
}

# ── Main ──────────────────────────────────────────────────────────────────────

if check_all; then
    if "$CHECK_MODE"; then
        printf '=== opencode is installed. ===\n'
    else
        printf '=== opencode is already installed. Nothing to do. ===\n'
    fi
    exit 0
fi

if "$CHECK_MODE"; then
    printf '=== opencode is not installed. Run "./%s" (without --check) to install it. ===\n' "$PROG"
    exit 1
fi

printf '=== Installing opencode... ===\n'

install_prereqs
[ -x "$OPENCODE_BIN" ] || install_opencode
link_opencode

# hello-open-code's start.sh creates these (bind-mounted into its container)
# so opencode config and 'opencode auth login' credentials have a home.
mkdir -p "${HOME}/.config/opencode" "${HOME}/.local/share/opencode"
info "~/.config/opencode and ~/.local/share/opencode exist"

printf '\n=== Verifying installation... ===\n'
if check_all; then
    printf '=== opencode installed. Export ANTHROPIC_API_KEY, GEMINI_API_KEY or XAI_API_KEY (or run opencode auth login) to use it. ===\n'
    exit 0
fi
printf '=== opencode installed at %s, but %s is not on PATH. Add it to PATH (for local-representative too) so agents can find opencode. ===\n' "$OPENCODE_BIN" "$USER_LINK_DIR"
exit 1
