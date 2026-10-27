#!/bin/sh
# start.sh: Service controller for Yandex Station Mini Bot
BASE=/data/telegram-bot
PIDFILE=$BASE/bot.pid
BIN=$BASE/yandex-mini-bot
CONFIG=$BASE/config.json
LOGFILE=$BASE/bot.log

case "${1:-start}" in
  start)
    if [ -f "$PIDFILE" ] && kill -0 $(cat "$PIDFILE") 2>/dev/null; then
      echo "Yandex Station Mini Bot is already running (PID $(cat $PIDFILE))"
      exit 0
    fi
    if [ ! -x "$BIN" ]; then
      echo "Error: $BIN is not executable" >&2
      exit 1
    fi
    echo "Starting Yandex Station Mini Bot..."
    # Rotate log if large
    if [ -f "$LOGFILE" ] && [ $(wc -c < "$LOGFILE") -gt 500000 ]; then
      mv -f "$LOGFILE" "$LOGFILE.old"
    fi
    LD_LIBRARY_PATH=/system/vendor/quasar/libs setsid "$BIN" -c "$CONFIG" </dev/null >>"$LOGFILE" 2>&1 &
    echo $! > "$PIDFILE"
    sleep 1
    if kill -0 $(cat "$PIDFILE") 2>/dev/null; then
      echo "Bot started successfully (PID $(cat $PIDFILE))"
    else
      echo "Bot failed to start. Last log lines:" >&2
      tail -n 15 "$LOGFILE" >&2
      exit 1
    fi
    ;;

  stop)
    if [ -f "$PIDFILE" ]; then
      PID=$(cat "$PIDFILE")
      echo "Stopping Yandex Station Mini Bot (PID $PID)..."
      kill "$PID" 2>/dev/null
      tries=0
      while kill -0 "$PID" 2>/dev/null && [ "$tries" -lt 10 ]; do
        sleep 1
        tries=$((tries + 1))
      done
      kill -9 "$PID" 2>/dev/null
      rm -f "$PIDFILE"
    fi
    killall -9 yandex-mini-bot 2>/dev/null || true
    echo "Bot stopped."
    ;;

  restart)
    $0 stop
    sleep 1
    $0 start
    ;;

  status)
    if [ -f "$PIDFILE" ] && kill -0 $(cat "$PIDFILE") 2>/dev/null; then
      echo "Yandex Station Mini Bot is RUNNING (PID $(cat $PIDFILE))"
    else
      echo "Yandex Station Mini Bot is STOPPED"
      exit 1
    fi
    ;;

  logs)
    tail -n 30 "$LOGFILE"
    ;;

  *)
    echo "Usage: $0 start|stop|restart|status|logs"
    exit 2
    ;;
esac
exit 0
