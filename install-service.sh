#!/usr/bin/env bash
#
# install-service.sh — Install bserver as a system service.
#
# Supports:
#   - Linux with systemd
#   - macOS with launchd
#
# Usage:
#   sudo ./install-service.sh          # build (if needed) then install and enable
#   sudo ./install-service.sh restart  # restart the service
#   sudo ./install-service.sh remove   # uninstall the service
#   ./install-service.sh log           # follow the service log (no sudo needed)
#

set -euo pipefail

SERVICE_NAME="bserver"
GO_VERSION="1.24.4"

# Resolve the directory this script lives in (i.e. where the binary is).
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="$SCRIPT_DIR/bserver"

# ── Helpers ──────────────────────────────────────────────────────────────

die()  { echo "Error: $*" >&2; exit 1; }
info() { echo "==> $*"; }

need_root() {
    if [ "$(id -u)" -ne 0 ]; then
        die "This script must be run as root (use sudo)."
    fi
}

# ── Go install & build helpers ───────────────────────────────────────────

install_go() {
    info "Go not found — installing Go ${GO_VERSION}…"
    local arch
    case "$(uname -m)" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) die "Unsupported architecture: $(uname -m)" ;;
    esac

    local os
    case "$(uname -s)" in
        Linux)  os="linux" ;;
        Darwin) os="darwin" ;;
        *) die "Unsupported OS for Go install: $(uname -s)" ;;
    esac

    local tarball="go${GO_VERSION}.${os}-${arch}.tar.gz"
    local url="https://go.dev/dl/${tarball}"
    local tmp
    tmp="$(mktemp -d)"

    info "Downloading ${url}…"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "$tmp/$tarball" "$url"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "$tmp/$tarball" "$url"
    else
        die "Neither curl nor wget found. Install one of them and retry."
    fi

    info "Extracting to /usr/local/go…"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$tmp/$tarball"
    rm -rf "$tmp"

    export PATH="/usr/local/go/bin:$PATH"

    if ! command -v go >/dev/null 2>&1; then
        die "Go installation failed — go not found in PATH after install."
    fi
    info "Go $(go version | awk '{print $3}') installed successfully."
}

ensure_binary() {
    if [ -x "$BINARY" ]; then
        return 0
    fi

    info "Binary $BINARY not found — building from source…"

    # Make sure Go is available.
    if ! command -v go >/dev/null 2>&1; then
        # Check common locations not in current PATH.
        for p in /usr/local/go/bin/go /usr/lib/go/bin/go /snap/bin/go; do
            if [ -x "$p" ]; then
                export PATH="$(dirname "$p"):$PATH"
                break
            fi
        done
    fi

    if ! command -v go >/dev/null 2>&1; then
        install_go
    fi

    info "Compiling bserver…"
    local version
    version="$(git -C "$SCRIPT_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)"
    (cd "$SCRIPT_DIR" && go build -ldflags "-X main.Version=${version}" -o bserver)

    if [ ! -x "$BINARY" ]; then
        die "Build completed but $BINARY not found."
    fi
    info "Build successful: $BINARY"
}

# ── Detect platform ─────────────────────────────────────────────────────

detect_platform() {
    case "$(uname -s)" in
        Linux)
            if command -v systemctl >/dev/null 2>&1; then
                echo "systemd"
            else
                die "Linux detected but systemd not found. Only systemd is supported."
            fi
            ;;
        Darwin)
            echo "launchd"
            ;;
        *)
            die "Unsupported OS: $(uname -s)"
            ;;
    esac
}

# ── systemd (Linux) ─────────────────────────────────────────────────────

UNIT_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

install_systemd() {
    need_root
    info "Installing systemd unit → $UNIT_FILE"

    cat > "$UNIT_FILE" <<EOF
[Unit]
Description=bserver – YAML/Markdown web server with auto-TLS
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$SCRIPT_DIR
ExecStart=$BINARY
Restart=on-failure
RestartSec=5

# bserver drops privileges to nobody internally after binding ports,
# so we start as root to allow binding 80/443.

# Environment overrides (uncomment/edit as needed):
#Environment=LE_EMAIL=you@example.com
#Environment=HTTP_ADDR=:80
#Environment=HTTPS_ADDR=:443
#Environment=CERT_CACHE=./cert-cache

# Logging goes to journalctl
StandardOutput=journal
StandardError=journal
SyslogIdentifier=$SERVICE_NAME

# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=$SCRIPT_DIR/www/cert-cache $SCRIPT_DIR/www
ProtectHome=read-only
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME"
    systemctl start  "$SERVICE_NAME"

    info "Service installed and started."
    info "Useful commands:"
    echo "    sudo systemctl status  $SERVICE_NAME"
    echo "    sudo systemctl restart $SERVICE_NAME"
    echo "    ./install-service.sh log              # follow logs"
}

restart_systemd() {
    need_root
    if [ ! -f "$UNIT_FILE" ]; then
        die "Service not installed (no unit file at $UNIT_FILE)."
    fi
    info "Restarting systemd service…"
    systemctl restart "$SERVICE_NAME"
    info "Service restarted."
    systemctl --no-pager status "$SERVICE_NAME"
}

remove_systemd() {
    need_root
    if [ ! -f "$UNIT_FILE" ]; then
        info "Unit file not found — nothing to remove."
        return
    fi
    info "Stopping and removing systemd service…"
    systemctl stop    "$SERVICE_NAME" 2>/dev/null || true
    systemctl disable "$SERVICE_NAME" 2>/dev/null || true
    rm -f "$UNIT_FILE"
    systemctl daemon-reload
    info "Service removed."
}

log_systemd() {
    info "Following $SERVICE_NAME logs (Ctrl-C to stop)…"
    exec journalctl -u "$SERVICE_NAME" -f
}

# ── launchd (macOS) ─────────────────────────────────────────────────────

PLIST_LABEL="com.local.${SERVICE_NAME}"
PLIST_FILE="/Library/LaunchDaemons/${PLIST_LABEL}.plist"

install_launchd() {
    need_root
    info "Installing launchd plist → $PLIST_FILE"

    cat > "$PLIST_FILE" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${PLIST_LABEL}</string>

    <key>ProgramArguments</key>
    <array>
        <string>${BINARY}</string>
    </array>

    <key>WorkingDirectory</key>
    <string>${SCRIPT_DIR}</string>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>

    <key>StandardOutPath</key>
    <string>/var/log/${SERVICE_NAME}.log</string>
    <key>StandardErrorPath</key>
    <string>/var/log/${SERVICE_NAME}.log</string>

    <!-- Environment overrides (uncomment/edit as needed):
    <key>EnvironmentVariables</key>
    <dict>
        <key>LE_EMAIL</key>
        <string>you@example.com</string>
    </dict>
    -->
</dict>
</plist>
EOF

    chmod 644 "$PLIST_FILE"
    launchctl load -w "$PLIST_FILE"

    info "Service installed and loaded."
    info "Useful commands:"
    echo "    sudo launchctl list | grep $SERVICE_NAME"
    echo "    sudo launchctl unload $PLIST_FILE"
    echo "    ./install-service.sh log              # follow logs"
}

restart_launchd() {
    need_root
    if [ ! -f "$PLIST_FILE" ]; then
        die "Service not installed (no plist at $PLIST_FILE)."
    fi
    info "Restarting launchd service…"
    launchctl unload "$PLIST_FILE" 2>/dev/null || true
    launchctl load -w "$PLIST_FILE"
    info "Service restarted."
}

remove_launchd() {
    need_root
    if [ ! -f "$PLIST_FILE" ]; then
        info "Plist not found — nothing to remove."
        return
    fi
    info "Unloading and removing launchd service…"
    launchctl unload "$PLIST_FILE" 2>/dev/null || true
    rm -f "$PLIST_FILE"
    info "Service removed."
}

log_launchd() {
    local logfile="/var/log/${SERVICE_NAME}.log"
    if [ ! -f "$logfile" ]; then
        die "Log file $logfile not found. Is the service installed?"
    fi
    info "Following $SERVICE_NAME logs (Ctrl-C to stop)…"
    exec tail -f "$logfile"
}

# ── Main ────────────────────────────────────────────────────────────────

ACTION="${1:-install}"
PLATFORM="$(detect_platform)"

# For install, ensure the binary exists (build from source if necessary).
if [ "$ACTION" = "install" ]; then
    ensure_binary
fi

case "$PLATFORM" in
    systemd)
        case "$ACTION" in
            install) install_systemd ;;
            restart) restart_systemd ;;
            remove)  remove_systemd  ;;
            log)     log_systemd     ;;
            *)       die "Unknown action: $ACTION (use install, restart, remove, or log)" ;;
        esac
        ;;
    launchd)
        case "$ACTION" in
            install) install_launchd ;;
            restart) restart_launchd ;;
            remove)  remove_launchd  ;;
            log)     log_launchd     ;;
            *)       die "Unknown action: $ACTION (use install, restart, remove, or log)" ;;
        esac
        ;;
esac
