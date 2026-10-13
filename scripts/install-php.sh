#!/usr/bin/env bash
#
# install-php.sh — Install the PHP packages required by bserver.
#
# bserver uses PHP in two ways:
#   - php-cgi: serves .php files via CGI (e.g. index.php, WordPress)
#   - php (CLI): runs inline PHP in YAML script blocks (script: php)
#
# Usage:
#   sudo ./scripts/install-php.sh          # install php-cli and php-cgi
#   ./scripts/install-php.sh check         # check if PHP is already available
#

set -euo pipefail

die()  { echo "Error: $*" >&2; exit 1; }
info() { echo "==> $*"; }

# ── Check mode ───────────────────────────────────────────────────────────

check_php() {
    local ok=true

    if command -v php >/dev/null 2>&1; then
        info "php (CLI) found: $(php -v | head -1)"
    else
        echo "    php (CLI) — NOT FOUND"
        ok=false
    fi

    if command -v php-cgi >/dev/null 2>&1; then
        info "php-cgi found: $(php-cgi -v | head -1)"
    else
        # Check common locations not in PATH
        for p in /usr/local/bin/php-cgi /opt/homebrew/bin/php-cgi /usr/bin/php-cgi; do
            if [ -x "$p" ]; then
                info "php-cgi found at $p: $($p -v | head -1)"
                ok=true  # don't override a missing php CLI
                continue 2
            fi
        done
        echo "    php-cgi — NOT FOUND"
        ok=false
    fi

    if [ "$ok" = true ]; then
        info "All PHP components are available."
        return 0
    else
        echo ""
        echo "Run 'sudo ./scripts/install-php.sh' to install missing components."
        return 1
    fi
}

# ── Install: Linux ───────────────────────────────────────────────────────

install_linux() {
    if command -v apt-get >/dev/null 2>&1; then
        info "Detected Debian/Ubuntu (apt)…"
        apt-get update -qq
        apt-get install -y php-cli php-cgi
    elif command -v dnf >/dev/null 2>&1; then
        info "Detected Fedora/RHEL (dnf)…"
        dnf install -y php-cli php-cgi
    elif command -v yum >/dev/null 2>&1; then
        info "Detected CentOS/RHEL (yum)…"
        yum install -y php-cli php-cgi
    elif command -v pacman >/dev/null 2>&1; then
        info "Detected Arch Linux (pacman)…"
        pacman -Sy --noconfirm php php-cgi
    elif command -v apk >/dev/null 2>&1; then
        info "Detected Alpine Linux (apk)…"
        apk add --no-cache php php-cgi php-json
    elif command -v zypper >/dev/null 2>&1; then
        info "Detected openSUSE (zypper)…"
        zypper install -y php php-cgi
    else
        die "Could not detect a supported package manager (apt, dnf, yum, pacman, apk, zypper)."
    fi
}

# ── Install: macOS ───────────────────────────────────────────────────────

install_macos() {
    if command -v brew >/dev/null 2>&1; then
        info "Installing PHP via Homebrew…"
        brew install php
        # Homebrew's php formula includes both php and php-cgi.
    else
        die "Homebrew not found. Install it from https://brew.sh then re-run this script."
    fi
}

# ── Main ─────────────────────────────────────────────────────────────────

ACTION="${1:-install}"

case "$ACTION" in
    check)
        check_php
        ;;
    install)
        if [ "$(id -u)" -ne 0 ]; then
            die "This script must be run as root (use sudo)."
        fi

        case "$(uname -s)" in
            Linux)  install_linux  ;;
            Darwin) install_macos  ;;
            *)      die "Unsupported OS: $(uname -s)" ;;
        esac

        echo ""
        info "Verifying installation…"
        check_php || true

        # bserver resolves php-cgi ONCE, at startup. A running instance will
        # keep failing .php requests with "exec: no command" until it is
        # restarted to pick up the newly-installed php-cgi.
        echo ""
        if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet bserver 2>/dev/null; then
            info "IMPORTANT: bserver is already running and resolved php-cgi at startup."
            info "           Restart it now so PHP takes effect:"
            echo   "               sudo systemctl restart bserver"
        elif command -v launchctl >/dev/null 2>&1 && launchctl list 2>/dev/null | grep -q "com.local.bserver"; then
            info "IMPORTANT: bserver is already running and resolved php-cgi at startup."
            info "           Restart it now so PHP takes effect:"
            echo   "               sudo ./scripts/install-service.sh restart"
        else
            info "PHP is ready. Start bserver and it will detect php-cgi:"
            echo   "               sudo ./scripts/install-service.sh"
        fi
        ;;
    *)
        die "Unknown action: $ACTION (use install or check)"
        ;;
esac
