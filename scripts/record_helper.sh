#!/bin/sh
# record_helper.sh: High-quality microphone recording for Yandex Station Mini 1
DURATION=${1:-5}
OUTFILE=${2:-/tmp/rec_room_raw.wav}

# 1. Boost PDM microphone gain if amixer control exists
LD_LIBRARY_PATH=/system/vendor/quasar/libs /system/workdir/bin/amixer cset numid=12 40 >/dev/null 2>&1

# 2. Pause launcher so it doesn't trigger rapid restart loop
LPID=$(pidof quasar_launcher2)
if [ -n "$LPID" ]; then
    kill -STOP $LPID 2>/dev/null
fi

# 3. Stop maind temporarily so ALSA hardware capture device hw:0,2 is released
killall -9 maind 2>/dev/null
usleep 150000

# 4. Record 8-channel 48kHz audio from PDM microphone array
LD_LIBRARY_PATH=/system/vendor/quasar/libs /system/workdir/bin/arecord -D hw:0,2 -d "$DURATION" -c 8 -r 48000 -f S32_LE "$OUTFILE" 2>/dev/null

# 5. Instantly resume launcher so Alice restarts cleanly
if [ -n "$LPID" ]; then
    kill -CONT $LPID 2>/dev/null
fi
