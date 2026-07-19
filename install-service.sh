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
#   sudo ./install-service.sh update   # git pull, rebuild & restart if changed
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
    local fetch
    if command -v curl >/dev/null 2>&1; then
        fetch="curl -fsSL -o"
        curl -fsSL -o "$tmp/$tarball" "$url"
        curl -fsSL -o "$tmp/$tarball.sha256" "${url}.sha256"
    elif command -v wget >/dev/null 2>&1; then
        fetch="wget -q -O"
        wget -q -O "$tmp/$tarball" "$url"
        wget -q -O "$tmp/$tarball.sha256" "${url}.sha256"
    else
        die "Neither curl nor wget found. Install one of them and retry."
    fi
    : "${fetch:=}" # silence shellcheck unused

    info "Verifying SHA-256 checksum…"
    local expected actual
    expected="$(awk '{print $1}' "$tmp/$tarball.sha256" | tr -d '[:space:]')"
    if [ -z "$expected" ]; then
        die "Could not read expected SHA-256 from ${url}.sha256"
    fi
    if command -v sha256sum >/dev/null 2>&1; then
        actual="$(sha256sum "$tmp/$tarball" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then
        actual="$(shasum -a 256 "$tmp/$tarball" | awk '{print $1}')"
    else
        die "Neither sha256sum nor shasum is available; cannot verify Go download."
    fi
    if [ "$expected" != "$actual" ]; then
        rm -rf "$tmp"
        die "SHA-256 mismatch for $tarball: expected $expected, got $actual"
    fi
    info "Checksum OK ($actual)"

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

# ── Update helper ──────────────────────────────────────────────────────

do_update() {
    need_root

    # Verify we're in a git repo.
    if [ ! -d "$SCRIPT_DIR/.git" ]; then
        die "No git repository found in $SCRIPT_DIR — cannot update."
    fi

    info "Checking for updates…"
    local before after
    before="$(git -C "$SCRIPT_DIR" rev-parse HEAD)"
    git -C "$SCRIPT_DIR" pull --ff-only
    after="$(git -C "$SCRIPT_DIR" rev-parse HEAD)"

    if [ "$before" = "$after" ]; then
        info "Already up to date."
        return 0
    fi

    info "Updated $before → $after"

    # Make sure Go is available.
    if ! command -v go >/dev/null 2>&1; then
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

    info "Rebuilding bserver…"
    local version
    version="$(git -C "$SCRIPT_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)"
    (cd "$SCRIPT_DIR" && go build -ldflags "-X main.Version=${version}" -o bserver)

    if [ ! -x "$BINARY" ]; then
        die "Build failed — $BINARY not found."
    fi
    info "Build successful: $BINARY"

    # Restart the service.
    local platform
    platform="$(detect_platform)"
    case "$platform" in
        systemd) restart_systemd ;;
        launchd) restart_launchd ;;
    esac

    info "Update complete."
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

# Confirm the unit actually reached a stable running state. A service with
# Restart=on-failure that dies on startup (bad config, missing sandbox path,
# crash) does not make `systemctl start` fail — systemd just re-queues it and
# it flaps between "activating (auto-restart)" and "failed". We therefore
# sample the state across a window longer than one RestartSec cycle: if we ever
# see it failed or auto-restarting, it is not up; success requires it to be
# genuinely "active (running)" at the end. Returns 0 if up, 1 otherwise.
verify_started() {
    info "Verifying $SERVICE_NAME started cleanly…"
    local i state sub
    for i in 1 2 3 4 5 6 7 8; do
        state="$(systemctl is-active "$SERVICE_NAME" 2>/dev/null || true)"
        sub="$(systemctl show -p SubState --value "$SERVICE_NAME" 2>/dev/null || true)"
        if [ "$state" = "failed" ] || [ "$sub" = "auto-restart" ] || [ "$sub" = "failed" ]; then
            return 1
        fi
        sleep 1
    done
    state="$(systemctl is-active "$SERVICE_NAME" 2>/dev/null || true)"
    sub="$(systemctl show -p SubState --value "$SERVICE_NAME" 2>/dev/null || true)"
    [ "$state" = "active" ] && [ "$sub" = "running" ]
}

# Explain why the service is not running, pulling the reason out of the logs.
# systemd's own startup failures (e.g. mount-namespace/sandbox errors) land in
# the journal rather than the app log file, so we surface both.
report_start_failure() {
    local logfile="/var/log/${SERVICE_NAME}.log"
    echo >&2
    echo "Error: $SERVICE_NAME was installed and enabled but is NOT running." >&2
    echo "       It failed to start; the reason follows." >&2
    echo >&2
    echo "── systemctl status ──────────────────────────────────────────" >&2
    systemctl --no-pager --full status "$SERVICE_NAME" 2>&1 | sed 's/^/    /' >&2 || true
    echo >&2
    echo "── recent journal (journalctl -u $SERVICE_NAME) ──────────────" >&2
    journalctl -u "$SERVICE_NAME" --no-pager -n 20 2>&1 | sed 's/^/    /' >&2 || true
    if [ -s "$logfile" ]; then
        echo >&2
        echo "── recent app log ($logfile) ──" >&2
        tail -n 20 "$logfile" 2>&1 | sed 's/^/    /' >&2 || true
    fi
    echo >&2
    echo "Fix the cause shown above, then re-run:" >&2
    echo "    sudo ./install-service.sh restart" >&2
    echo >&2
}

install_systemd() {
    need_root

    # Cgroup memory limits (Linux only — backstop for runaway scripts).
    #
    # Computed from /proc/meminfo at install time so absolute byte values
    # are baked into the unit file. We use absolute values rather than the
    # `MemoryMax=75%` percentage syntax because percentages require systemd
    # 240+ on cgroup v2; absolute byte values work on both cgroup hierarchies
    # and on older systemd releases.
    #
    # If MemoryMax is exceeded the kernel OOM-kills bserver; systemd then
    # restarts it (Restart=on-failure below). Combined with the in-process
    # JS heap watchdog (js-heap-mb), this is the belt-and-braces safety net.
    local mem_total_kb mem_directives=""
    if [ -r /proc/meminfo ]; then
        mem_total_kb="$(awk '/^MemTotal:/ {print $2; exit}' /proc/meminfo)"
        if [ -n "${mem_total_kb:-}" ] && [ "$mem_total_kb" -gt 0 ]; then
            local mem_max_kb mem_high_kb
            mem_max_kb=$(( mem_total_kb * 75 / 100 ))
            mem_high_kb=$(( mem_total_kb * 60 / 100 ))
            mem_directives="# Memory backstop (computed from $((mem_total_kb / 1024)) MB total RAM)
MemoryHigh=${mem_high_kb}K
MemoryMax=${mem_max_kb}K"
            info "Memory limits: high=$((mem_high_kb / 1024)) MB, max=$((mem_max_kb / 1024)) MB"
        fi
    fi

    # PHP session storage. bserver runs php-cgi as "nobody" after dropping
    # privileges, and PrivateTmp=yes wipes /tmp on every restart — so we
    # use a persistent per-service directory owned by nobody. Sessions
    # (including the Google OAuth access/refresh token for crm.stg.net)
    # survive restarts as long as the session cookie is still valid.
    local session_dir="/var/lib/${SERVICE_NAME}-sessions"
    if [ ! -d "$session_dir" ]; then
        info "Creating session directory $session_dir"
        mkdir -p "$session_dir"
    fi
    chown nobody:nogroup "$session_dir"
    chmod 0700 "$session_dir"

    # Memory-monitor diagnostic dumps. Owned by nobody because bserver drops
    # privileges before writing heap/goroutine pprof dumps here.
    local diag_dir="/var/lib/${SERVICE_NAME}-diag"
    if [ ! -d "$diag_dir" ]; then
        info "Creating diagnostic dump directory $diag_dir"
        mkdir -p "$diag_dir"
    fi
    chown nobody:nogroup "$diag_dir"
    chmod 0750 "$diag_dir"

    # TLS certificate cache. bserver writes Let's Encrypt certs here as nobody
    # (after dropping privileges), so it must exist and be owned by nobody
    # BEFORE the unit starts: it is a ReadWritePaths= entry, and systemd aborts
    # namespace setup (status=226/NAMESPACE) if any such path is missing, which
    # would happen on every fresh install since bserver only creates it lazily
    # at runtime.
    local cert_dir="$SCRIPT_DIR/cert-cache"
    if [ ! -d "$cert_dir" ]; then
        info "Creating TLS certificate cache directory $cert_dir"
        mkdir -p "$cert_dir"
    fi
    chown nobody:nogroup "$cert_dir"
    chmod 0700 "$cert_dir"

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

# Logging
StandardOutput=append:/var/log/$SERVICE_NAME.log
StandardError=append:/var/log/$SERVICE_NAME.log
SyslogIdentifier=$SERVICE_NAME

# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=$SCRIPT_DIR/cert-cache $SCRIPT_DIR/www /var/log/$SERVICE_NAME.log /tmp $session_dir $diag_dir -/var/spool/postfix/maildrop -/var/spool/postfix/public
ProtectHome=read-only
PrivateTmp=yes

${mem_directives}

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME"

    # Clear any prior failed/flapping state so status output and the restart
    # counter reflect only this install attempt.
    systemctl reset-failed "$SERVICE_NAME" 2>/dev/null || true

    local verb="started"
    if systemctl is-active --quiet "$SERVICE_NAME"; then
        verb="restarted"
        # `|| true`: a crash-looping unit can make start/restart return non-zero;
        # we diagnose the real state via verify_started below, not the exit code.
        systemctl restart "$SERVICE_NAME" || true
    else
        systemctl start "$SERVICE_NAME" || true
    fi

    if ! verify_started; then
        report_start_failure
        exit 1
    fi

    info "Service installed and $verb successfully."
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
    local logfile="/var/log/${SERVICE_NAME}.log"
    if [ -f "$logfile" ]; then
        info "Following $SERVICE_NAME logs (Ctrl-C to stop)…"
        exec tail -f "$logfile"
    else
        info "Log file not found, falling back to journalctl…"
        exec journalctl -u "$SERVICE_NAME" -f
    fi
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
    launchctl unload "$PLIST_FILE" 2>/dev/null || true
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

# ── Preflight checks (install only) ─────────────────────────────────────
#
# bserver binds :80/:443 as root, then drops privileges to "nobody". So every
# file it serves at runtime — the vhost dirs under www/ and the TLS cert cache
# — must be reachable by "nobody". The classic footgun is installing under
# root's home (/root, mode 0700), which "nobody" cannot traverse: the service
# starts, but every request fails (421/404) and cert writes fail. These checks
# catch that BEFORE installing, and recommend (never apply) the fix.

# Run a command as the unprivileged "nobody" user (ground-truth access test).
# Returns 2 if we have no way to switch users (check is then skipped upstream).
run_as_nobody() {
    if command -v runuser >/dev/null 2>&1; then
        runuser -u nobody -- "$@" 2>/dev/null
    elif command -v sudo >/dev/null 2>&1; then
        sudo -n -u nobody -- "$@" 2>/dev/null
    else
        return 2
    fi
}

# Absolute path of root's home directory (portable across Linux/macOS).
root_home_dir() {
    local h=""
    if command -v getent >/dev/null 2>&1; then
        h="$(getent passwd root 2>/dev/null | cut -d: -f6)"
    fi
    [ -n "$h" ] || h="$(eval echo ~root 2>/dev/null || true)"
    case "$h" in /*) echo "$h" ;; *) echo "/root" ;; esac
}

# True if CHILD ($1) is at or below PARENT ($2) in the directory tree.
path_is_under() {
    local child="${1%/}/" parent="${2%/}/"
    case "$child" in "$parent"*) return 0 ;; *) return 1 ;; esac
}

# Echo the shallowest ancestor of $1 that "nobody" cannot traverse (and return
# 0); return 1 if the whole chain is traversable. Array-free for bash 3.2.
first_untraversable() {
    local target="$1" p="$1" acc="" dir
    while [ -n "$p" ] && [ "$p" != "/" ]; do
        acc="$p
$acc"
        p="$(dirname "$p")"
    done
    while IFS= read -r dir; do
        [ -n "$dir" ] || continue
        if ! run_as_nobody test -x "$dir"; then
            printf '%s\n' "$dir"
            return 0
        fi
    done <<EOF
$acc
EOF
    return 1
}

preflight_install() {
    need_root

    # bserver only drops privileges when the "nobody" user exists; otherwise it
    # keeps running as root and these access constraints do not apply.
    if ! id nobody >/dev/null 2>&1; then
        return 0
    fi

    # ── Check 1: installed under root's home directory ──────────────────
    local root_home
    root_home="$(root_home_dir)"
    if path_is_under "$SCRIPT_DIR" "$root_home"; then
        cat >&2 <<EOF

Error: bserver is installed under root's home directory:
    $SCRIPT_DIR   (inside $root_home)

bserver drops privileges to "nobody" at runtime, and "nobody" cannot enter
$root_home (mode 0700). The service would start, but every request would fail
(421 Misdirected / 404) and TLS certificate writes would fail.

Relocate the install somewhere world-traversable, then re-run — for example:
    sudo systemctl stop bserver 2>/dev/null || true
    sudo mv "$SCRIPT_DIR" /bserver
    cd /bserver && sudo ./install-service.sh

Aborting: no changes made.
EOF
        exit 1
    fi

    # ── Check 2: "nobody" can actually reach the served files ───────────
    # Sanity-check that we can run as nobody at all; if not, skip rather than
    # block on a false negative.
    if ! run_as_nobody test -x /; then
        info "Note: could not verify filesystem access as 'nobody'; skipping permission preflight."
        return 0
    fi
    local served="$SCRIPT_DIR/www"
    [ -d "$served" ] || served="$SCRIPT_DIR"
    local blocked
    if blocked="$(first_untraversable "$served")"; then
        cat >&2 <<EOF

Error: the "nobody" user cannot reach bserver's files.

bserver drops privileges to "nobody" at runtime, but this directory is not
traversable by it:
    $blocked

bserver could not read its content or TLS certificates, so requests would
fail (421 Misdirected / 404).

Recommended fix (grants traverse-only access — not read or listing):
    sudo chmod o+x "$blocked"
then re-run:
    sudo ./install-service.sh

Aborting: no changes made.
EOF
        exit 1
    fi
}

# ── Optional component checks (post-install, warning-only) ───────────────
#
# bserver's core (YAML/Markdown pages, embedded JS) has no external runtime
# dependencies. But some features shell out to interpreters that may or may not
# be installed — and a given instance may legitimately not need them. So after
# a successful install we only *warn* about what's missing and print a
# recommended install command; we never fail or install anything.
#
# Not checked on purpose: JavaScript uses an embedded engine (no node needed),
# and sh/bash are effectively always present.

have_php() {
    command -v php-cgi >/dev/null 2>&1 && return 0
    local p
    for p in /usr/local/bin/php-cgi /opt/homebrew/bin/php-cgi /usr/bin/php-cgi; do
        [ -x "$p" ] && return 0
    done
    return 1
}

have_python() {
    command -v python3 >/dev/null 2>&1 || command -v python >/dev/null 2>&1
}

# Best-effort "install python3" command for the detected package manager.
python_install_cmd() {
    if   command -v apt-get >/dev/null 2>&1; then echo "sudo apt-get install -y python3"
    elif command -v dnf     >/dev/null 2>&1; then echo "sudo dnf install -y python3"
    elif command -v yum     >/dev/null 2>&1; then echo "sudo yum install -y python3"
    elif command -v zypper  >/dev/null 2>&1; then echo "sudo zypper install -y python3"
    elif command -v pacman  >/dev/null 2>&1; then echo "sudo pacman -S --noconfirm python"
    elif command -v apk     >/dev/null 2>&1; then echo "sudo apk add python3"
    elif command -v brew    >/dev/null 2>&1; then echo "brew install python3"
    else echo "install python3 via your package manager"
    fi
}

check_optional_components() {
    local missing=0

    echo
    info "Optional components:"

    if have_php; then
        echo "    php     — present"
    else
        missing=1
        echo "    php     — NOT installed; .php pages and 'script: php' will not run"
        if [ -x "$SCRIPT_DIR/install-php.sh" ]; then
            echo "              install: sudo $SCRIPT_DIR/install-php.sh"
        else
            echo "              install php-cli and php-cgi via your package manager"
        fi
    fi

    if have_python; then
        echo "    python  — present"
    else
        missing=1
        echo "    python  — NOT installed; 'script: python' data sources will not run"
        echo "              install: $(python_install_cmd)"
    fi

    if [ "$missing" -ne 0 ]; then
        echo
        info "The above are optional — install only the features this instance needs."
    fi
    return 0
}

# ── Main ────────────────────────────────────────────────────────────────

ACTION="${1:-install}"

# The update action handles its own platform detection and restart.
if [ "$ACTION" = "update" ]; then
    do_update
    exit 0
fi

PLATFORM="$(detect_platform)"

# For install: verify the location is usable (before spending time building),
# then ensure the binary exists (build from source if necessary).
if [ "$ACTION" = "install" ]; then
    preflight_install
    ensure_binary
fi

case "$PLATFORM" in
    systemd)
        case "$ACTION" in
            install) install_systemd ;;
            restart) restart_systemd ;;
            remove)  remove_systemd  ;;
            log)     log_systemd     ;;
            *)       die "Unknown action: $ACTION (use install, restart, remove, update, or log)" ;;
        esac
        ;;
    launchd)
        case "$ACTION" in
            install) install_launchd ;;
            restart) restart_launchd ;;
            remove)  remove_launchd  ;;
            log)     log_launchd     ;;
            *)       die "Unknown action: $ACTION (use install, restart, remove, update, or log)" ;;
        esac
        ;;
esac

# After a successful install, warn (only) about optional features not present.
if [ "$ACTION" = "install" ]; then
    check_optional_components
fi
