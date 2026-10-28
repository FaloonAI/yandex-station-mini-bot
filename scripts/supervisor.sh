#!/bin/sh
# supervisor.sh: Persistent supervisor loop for Yandex Station Mini Bot
BASE=/data/telegram-bot
BIN=$BASE/yandex-mini-bot
CONFIG=$BASE/config.json
LOGFILE=$BASE/bot.log
PIDFILE=$BASE/bot.pid

# Ignore HUP and INT in supervisor
trap '' HUP

while true; do
  echo "$(date '+%Y-%m-%d %H:%M:%S') [supervisor] Launching yandex-mini-bot..." >> "$LOGFILE"
  LD_LIBRARY_PATH=/system/vendor/quasar/libs "$BIN" -c "$CONFIG" </dev/null >>"$LOGFILE" 2>&1 &
  BOT_PID=$!
  echo "$BOT_PID" > "$PIDFILE"
  wait "$BOT_PID"
  CODE=$?
  echo "$(date '+%Y-%m-%d %H:%M:%S') [supervisor] Process exited ($CODE). Respawning in 2s..." >> "$LOGFILE"
  sleep 2
done
