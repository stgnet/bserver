#!/usr/bin/env bash
#
# bserver-memcheck.sh — scan /var/log/bserver.log for memory-monitor alerts
# (MEMMON_WARN, MEMMON_DUMP) since the last run and email any hits to the
# configured recipient via local sendmail.
#
# Designed to be idempotent: the state file records the timestamp of the
# newest alert already reported, so re-running never spams. If the state
# file is missing, the first run establishes a baseline without emailing.
#
# Usage:
#   bserver-memcheck.sh             # normal check
#   bserver-memcheck.sh --force     # email all historical alerts (debug)
#   bserver-memcheck.sh --install-cron  # write /etc/cron.d/bserver-memcheck
#   bserver-memcheck.sh --test      # send a dry-run alert to verify mail path

set -euo pipefail

LOG_FILE="${BSERVER_LOG:-/var/log/bserver.log}"
STATE_FILE="${BSERVER_MEMCHECK_STATE:-/var/lib/bserver-diag/memcheck.state}"
RECIPIENT="${BSERVER_MEMCHECK_TO:-scott@stg.net}"
FROM="${BSERVER_MEMCHECK_FROM:-bserver@$(hostname -f 2>/dev/null || hostname)}"
HOST="$(hostname -f 2>/dev/null || hostname)"
SENDMAIL="${SENDMAIL:-/usr/sbin/sendmail}"

# Prevent overlap between cron runs.
LOCK="/var/lock/bserver-memcheck.lock"
exec 9>"$LOCK"
if ! flock -n 9; then
    exit 0  # another run in progress; silent exit
fi

send_mail() {
    local subject="$1"
    local body="$2"
    {
        printf 'From: %s\n' "$FROM"
        printf 'To: %s\n' "$RECIPIENT"
        printf 'Subject: %s\n' "$subject"
        printf 'Content-Type: text/plain; charset=utf-8\n'
        printf '\n'
        printf '%s\n' "$body"
    } | "$SENDMAIL" -t -oi
}

case "${1:-}" in
    --install-cron)
        cat > /etc/cron.d/bserver-memcheck <<'EOF'
# Check bserver memory-monitor alerts every 5 minutes. Any MEMMON_WARN or
# MEMMON_DUMP lines since the last run are emailed to the configured
# recipient via local sendmail. Silent when nothing to report.
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
MAILTO=""
*/5 * * * * root /root/bserver/bserver-memcheck.sh
EOF
        chmod 0644 /etc/cron.d/bserver-memcheck
        echo "Installed /etc/cron.d/bserver-memcheck (runs every 5 minutes)"
        exit 0
        ;;
    --test)
        send_mail "bserver memcheck test ($HOST)" \
            "This is a test message from bserver-memcheck.sh on $HOST at $(date). If you received this, the mail path is working."
        echo "Test message sent to $RECIPIENT"
        exit 0
        ;;
    --force)
        FORCE=1
        ;;
    "")
        FORCE=0
        ;;
    *)
        echo "Usage: $0 [--install-cron|--test|--force]" >&2
        exit 2
        ;;
esac

[ -r "$LOG_FILE" ] || exit 0

mkdir -p "$(dirname "$STATE_FILE")"

# First run: seed state with the newest alert currently present so we don't
# email historical alerts from before monitoring started. No state → baseline.
if [ ! -f "$STATE_FILE" ] && [ "$FORCE" = "0" ]; then
    latest="$(grep -a -E 'MEMMON_(WARN|DUMP)' "$LOG_FILE" | tail -n1 \
              | awk '{print $1" "$2}' || true)"
    if [ -z "$latest" ]; then
        latest="0000/00/00 00:00:00"
    fi
    printf '%s\n' "$latest" > "$STATE_FILE"
    exit 0
fi

last_seen="$(cat "$STATE_FILE" 2>/dev/null || echo '0000/00/00 00:00:00')"
[ "$FORCE" = "1" ] && last_seen="0000/00/00 00:00:00"

# Extract alerts newer than last_seen. Log timestamps are fixed-width
# "YYYY/MM/DD HH:MM:SS ..." so lexical comparison is chronological.
new_alerts="$(grep -a -E 'MEMMON_(WARN|DUMP)' "$LOG_FILE" \
    | awk -v cutoff="$last_seen" '
        {
            ts = $1 " " $2
            if (ts > cutoff) print $0
        }
    ')"

if [ -z "$new_alerts" ]; then
    exit 0
fi

# Most recent snapshot for context
latest_snapshot="$(grep -a '^[0-9].* MEMMON ' "$LOG_FILE" | tail -n1 || true)"

# Recent dump files (if any)
dump_dir="/var/lib/bserver-diag"
recent_dumps=""
if [ -d "$dump_dir" ]; then
    recent_dumps="$(ls -lh "$dump_dir" 2>/dev/null | tail -n+2 || true)"
fi

alert_count="$(printf '%s\n' "$new_alerts" | wc -l)"
has_dump="$(printf '%s\n' "$new_alerts" | grep -c MEMMON_DUMP || true)"

if [ "$has_dump" -gt 0 ]; then
    subject="[bserver-$HOST] MEMMON_DUMP: heap/goroutine threshold crossed"
else
    subject="[bserver-$HOST] MEMMON_WARN: $alert_count new memory warning(s)"
fi

body="bserver memory-monitor alerts since ${last_seen}:

${new_alerts}

Most recent snapshot:
${latest_snapshot:-(none)}

Files in ${dump_dir}:
${recent_dumps:-(empty)}

Host:      ${HOST}
Log:       ${LOG_FILE}
State:     ${STATE_FILE}

To analyze a heap dump:
  scp ${HOST}:${dump_dir}/<heap-file> .
  go tool pprof -top -inuse_space <heap-file>

To live-inspect via pprof (if pprof-addr is enabled):
  ssh -L 6060:127.0.0.1:6060 ${HOST}
  go tool pprof -http :8080 http://localhost:6060/debug/pprof/heap
"

send_mail "$subject" "$body"

# Record the newest alert timestamp so we don't re-send it next run.
newest="$(printf '%s\n' "$new_alerts" | tail -n1 | awk '{print $1" "$2}')"
printf '%s\n' "$newest" > "$STATE_FILE"
