# 🤖 Yandex Station Mini (YNDX-0004) Telegram Bot Control

[![Go Report Card](https://goreportcard.com/badge/github.com/FaloonAI/yandex-station-mini-bot)](https://goreportcard.com/report/github.com/FaloonAI/yandex-station-mini-bot)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/platform-Linux%20ARM64%20(Amlogic%20A113X)-green.svg)]()

Автономный, легковесный сервис на **Go** для полного удаленного управления рутированной **Яндекс Станцией Mini 1** (`YNDX-0004`, Amlogic Meson A113X, 4x Cortex-A53 `aarch64`, Buildroot 2017.02 Linux 4.9) через Telegram.

Работает автономно непосредственно на процессоре колонки без внешних серверов, без компьютера и без облачных посредников.

---

## 🌟 Возможности

### 📱 Управление и интерфейс
* **Интерактивное Inline-меню (`/start`):** двухуровневая навигация с кнопками и возвратом **«⬅️ Назад»** на каждом шаге.
* **🧹 Автоочистка чата (Ephemeral Cleanup):** сообщения пользователя для запуска действий (озвучка текста, `/say`, `/vol`, `/devname`, `/setemoji`) и временные подтверждения бота автоматически удаляются через 5 секунд (`auto_delete_sec`), сохраняя историю чата безупречно чистой.
* **🎨 Кастомизация эмодзи кнопок:** возможность переопределить любой значок меню командой `/setemoji <кнопка> <эмодзи>` (поддерживаются как стандартные Unicode, так и Telegram Premium эмодзи).

### 🔊 Аудиосистема и микрофоны
* **🎙️ Двусторонняя рация (Интерком):** отправка голосового сообщения из Telegram — колонка воспроизводит звук в комнате через аппаратный GStreamer, активирует микрофоны, записывает 5 секунд ответа и отправляет аудиозапись обратно в чат.
* **👂 Акустическая прослушка (`/listen [сек]`):** дистанционная запись звука с 8-канального PDM-массива (`hw:0,2`) на 5, 10, 15 или 30 секунд.
  * *Акустическая обработка:* подавление стартового переходного броска PDM, вычитание глобального DC-смещения, сглаживающий ФВЧ (~150 Гц) и нормализация динамического диапазона.
* **📢 Мемный саундборд (`/play <звук>`):** мгновенный запуск звуков (*Airhorn, Bruh, Discord, Fart, Metal Pipe, Rickroll, Siren, WinXP*) через нативный 64-битный конвейер GStreamer (`maind --gst-launch`).
* **🗣️ Синтез речи (TTS) (`/say <текст>`):** любой отправленный текст озвучивается через динамик на русском языке.
* **🎚️ Громкость и Mute:** регулировка уровня громкости (±10%, пресеты 20%..100%, переключатель `Mute/Unmute`) через `amixer`.
* **🚨 Охранная сирена:** дистанционный запуск непрерывной сирены тревоги на 100% громкости.

### ⏰ Таймеры и будильники
* **Интеллектуальные таймеры (`/timer 15m Пицца`):** быстрый выбор кнопками (1m, 5m, 15m, 30m, 45m, 1h) или произвольная длительность.
* **Будильник (`/alarm 08:30 Подъем!`):** расчет целевого времени с учетом текущего часового пояса.
* **Оповещение:** при срабатывании проигрывается звуковой джингл, TTS голосом произносит напоминание в комнате, а в Telegram отправляется уведомление.

### 👥 Сетевой мониторинг и диагностика
* **Детектор присутствия («Кто дома?», `/presence`):** параллельное сканирование локальной подсети (`/24`) через ядро Linux (ARP cache) за 1.5 секунды.
* **Метки устройств (`/devname <MAC> <Имя>`):** сохранение понятных имен домочадцев и гаджетов в `known_devices.json`.
* **Сетевая диагностика (`/net`, `/ping`):**
  * Параметры Wi-Fi из `iw dev wlan0 link` (SSID, частота, номер канала, уровень сигнала dBm/%, скорость Tx Bitrate).
  * Пинг до шлюза, Яндекс DNS, Google DNS и Telegram API.
  * **L2 ARPING Fallback:** если роутер блокирует ICMP Echo в LAN, диагностика автоматически замеряет L2 ARP-отклик, гарантируя точный статус шлюза.
* **Тест скорости интернета (`/speedtest`):** реальный замер пропускной способности скачивания через CDN в Мбит/с.

### ⚙️ Системный контроль
* **📊 Fastfetch & Телеметрия (`/status`):** вывод характеристик системы, ASCII-логотипа, температуры CPU Amlogic A113X, Load Average и статуса памяти.
* **📋 Live Logs (`/logs`):** просмотр последних 20 строк журнала `/data/telegram-bot/bot.log` с кнопкой мгновенного обновления в реальном времени.
* **🧹 Очистка RAM:** сброс буферов оперативной памяти (`echo 3 > /proc/sys/vm/drop_caches`).
* **✋ Датчик жестов ToF (VL53L0):** включение/блокировка оптического датчика верхнего сенсора.
* **🔄 Перезагрузка:** безопасная перезагрузка колонки с подтверждением.
* **🔔 Boot Notification:** автоматическое оповещение администратора в Telegram при подаче питания и старте колонки.

---

## 📁 Структура проекта

```text
yandex-station-mini-bot/
├── cmd/
│   └── bot/
│       └── main.go          # Точка входа сервиса, обработка команд и маршрутизация
├── internal/
│   ├── config/              # Загрузка и валидация конфигурации (JSON)
│   ├── netdiag/             # Wi-Fi метрики, ICMP/ARPING пинг, Speedtest
│   ├── presence/            # Сканер подсети ARP и менеджер устройств
│   ├── speaker/             # Интеграция с GStreamer, ALSA, PDM-микрофонами, ToF
│   │   ├── speaker.go       # Управление звуком, TTS, громкостью, fastfetch
│   │   ├── record_linux.go  # Аппаратный захват PDM-микрофонов (Linux ALSA MMAP)
│   │   └── record_other.go  # Заглушка для сборки и тестов на Windows/macOS
│   ├── telegram/            # Чистый клиент Telegram Bot API без внешних SDK
│   ├── timer/               # Потокобезопасный менеджер таймеров и будильников
│   └── ui/                  # Менеджер динамических эмодзи меню
├── scripts/
│   └── start.sh             # Демон-скрипт (start/stop/restart/status/logs)
├── sounds/                  # Набор звуков для саундборда (*.wav)
├── config.example.json      # Пример конфигурационного файла
├── go.mod
└── README.md
```

---

## ⚙️ Конфигурация (`config.json`)

Файл конфигурации размещается по пути `/data/telegram-bot/config.json`:

```json
{
  "bot_token": "8989354711:AAEGHljhkRSEK7aJRgqsgwHwK4-LSqRzqBk",
  "admin_id": 1184490028,
  "sounds_dir": "/data/telegram-bot/sounds",
  "scripts_dir": "/data/telegram-bot/scripts",
  "log_path": "/data/telegram-bot/bot.log",
  "quasar_logs": "/data/quasar/daemons_logs/maind.log",
  "aplay_path": "/system/workdir/bin/aplay",
  "arecord_path": "/system/workdir/bin/arecord",
  "amixer_path": "/system/workdir/bin/amixer",
  "subnet": "192.168.20.0/24",
  "devices_file": "/data/telegram-bot/known_devices.json",
  "auto_delete_sec": 5
}
```

* `bot_token`: токен вашего бота от `@BotFather`.
* `admin_id`: ваш числовой Telegram ID. Бот реагирует **исключительно** на команды этого пользователя.
* `subnet`: локальная подсеть для детектора присутствия домочадцев.
* `auto_delete_sec`: время в секундах до автоудаления сообщений действий и подтверждений (по умолчанию 5 сек).

---

## 🚀 Сборка и установка

### Кросс-компиляция для Amlogic A113X (`linux/arm64`):

```powershell
$env:GOOS = "linux"
$env:GOARCH = "arm64"
go build -ldflags="-s -w" -o bin/yandex-mini-bot ./cmd/bot
```

### Загрузка на колонку (SSH/SCP на порт 2222):

```powershell
cmd /c "ssh -p 2222 -i id_ed25519 root@192.168.20.209 ""cat > /data/telegram-bot/yandex-mini-bot.new && chmod +x /data/telegram-bot/yandex-mini-bot.new"" < bin/yandex-mini-bot"
```

### Управление службой на колонке:

```bash
/data/telegram-bot/start.sh status   # Проверить статус службы
/data/telegram-bot/start.sh restart  # Перезапустить бота
/data/telegram-bot/start.sh logs     # Просмотреть журнал работы
```

---

## 🛡️ Безопасность

* Бот полностью игнорирует сообщения от любых пользователей, кроме `admin_id`.
* Чувствительные файлы (`config.json`, `known_devices.json`, SSH-ключи) изолированы и внесены в `.gitignore`.
* Поддерживается прозрачная маршрутизация Telegram-трафика через Zeroblock/VPN на роутере.

---

## 📜 Лицензия

MIT License
