#!/bin/sh
# start.sh: Service controller for Yandex Station Mini Bot
BASE=/data/telegram-bot
SUPERVISOR=$BASE/scripts/supervisor.sh
SUPERVISOR_FALLBACK=$BASE/supervisor.sh
SUPERVISOR_PIDFILE=$BASE/supervisor.pid
PIDFILE=$BASE/bot.pid
LOGFILE=$BASE/bot.log

is_running() {
  pidof yandex-mini-bot >/dev/null 2>&1
}

case "${1:-start}" in
  start)
    if is_running; then
      echo "Yandex Station Mini Bot is already running (PID $(pidof yandex-mini-bot))"
      exit 0
    fi

    # Rotate log if large (> 500KB)
    if [ -f "$LOGFILE" ] && [ $(wc -c < "$LOGFILE") -gt 500000 ]; then
      mv -f "$LOGFILE" "$LOGFILE.old"
    fi

    SUP="$SUPERVISOR"
    if [ ! -x "$SUP" ]; then
      SUP="$SUPERVISOR_FALLBACK"
    fi

    echo "Starting Yandex Station Mini Bot..."
    setsid "$SUP" </dev/null >/dev/null 2>&1 &
    echo $! > "$SUPERVISOR_PIDFILE"

    sleep 1
    if is_running; then
      echo "Bot started successfully (PID $(pidof yandex-mini-bot))"
    else
      echo "Bot failed to start. Last log lines:" >&2
      tail -n 15 "$LOGFILE" >&2
      exit 1
    fi
    ;;

  stop)
    echo "Stopping Yandex Station Mini Bot..."
    if [ -f "$SUPERVISOR_PIDFILE" ]; then
      kill -9 $(cat "$SUPERVISOR_PIDFILE") 2>/dev/null || true
      rm -f "$SUPERVISOR_PIDFILE"
    fi
    killall -9 supervisor.sh 2>/dev/null || true
    killall -9 yandex-mini-bot 2>/dev/null || true
    rm -f "$PIDFILE"
    echo "Bot stopped."
    ;;

  restart)
    $0 stop
    sleep 1
    $0 start
    ;;

  status)
    if is_running; then
      echo "Yandex Station Mini Bot is RUNNING (PID $(pidof yandex-mini-bot))"
      exit 0
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
