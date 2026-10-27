#!/bin/sh
set -eu
BASE=/data/alice-homebrew
PID=$BASE/service.pid
valid_pid() {
    [ -f "$PID" ] || return 1
    value=$(cat "$PID")
    case "$value" in ''|*[!0-9]*) return 1;; esac
    [ -r "/proc/$value/cmdline" ] || return 1
    tr '\000' ' ' < "/proc/$value/cmdline" | grep -F "$BASE/alice-homebrew station" >/dev/null
}
case "${1:-start}" in
 stop)
    [ -x /data/telegram-bot/start.sh ] && /data/telegram-bot/start.sh stop >/dev/null 2>&1 || true
    if valid_pid; then
        kill -TERM "$value"
        tries=0
        while valid_pid && [ "$tries" -lt 10 ]; do sleep 1; tries=$((tries + 1)); done
        if valid_pid; then echo 'Service did not stop; refusing to replace its binary.' >&2; exit 1; fi
    fi
    exit 0;;
 start)
    if valid_pid; then exit 0; fi
    if [ ! -x "$BASE/alice-homebrew" ] && [ -f "$BASE/state/manager-update.json" ] && [ -x "$BASE/alice-homebrew.previous" ]; then
        mv "$BASE/alice-homebrew.previous" "$BASE/alice-homebrew"
    fi
    test -x "$BASE/alice-homebrew"
    umask 077
    mkdir -p "$BASE/state"
    if [ -f "$BASE/state/manager-update.json" ]; then
        "$BASE/alice-homebrew" recover-update "$BASE/state/manager-update.json"
    fi
    if [ -f "$BASE/service.log" ]; then mv "$BASE/service.log" "$BASE/service.previous.log"; fi
    setsid "$BASE/alice-homebrew" station --listen :8088 --data "$BASE/state" --hardware-led --managed \
      </dev/null >"$BASE/service.log" 2>&1 &
    echo "$!" > "$PID"
    sleep 1
    [ -x /data/telegram-bot/start.sh ] && /data/telegram-bot/start.sh start >/dev/null 2>&1 || true
    if ! valid_pid; then cat "$BASE/service.log" >&2; exit 1; fi;;
 *) echo 'Usage: start.sh start|stop' >&2; exit 2;;
esac
