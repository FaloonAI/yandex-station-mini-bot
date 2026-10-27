package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/FaloonAI/yandex-station-mini-bot/internal/config"
	"github.com/FaloonAI/yandex-station-mini-bot/internal/netdiag"
	"github.com/FaloonAI/yandex-station-mini-bot/internal/presence"
	"github.com/FaloonAI/yandex-station-mini-bot/internal/speaker"
	"github.com/FaloonAI/yandex-station-mini-bot/internal/telegram"
)

type App struct {
	cfg     *config.Config
	spk     *speaker.Controller
	tg      *telegram.Client
	scanner *presence.Scanner
	diag    *netdiag.Diagnostics
}

func main() {
	configPath := flag.String("c", "/data/telegram-bot/config.json", "path to config.json")
	flag.Parse()

	log.Printf("Starting Yandex Station Mini Bot with config: %s", *configPath)

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	spk := speaker.NewController(cfg.SoundsDir, cfg.ScriptsDir, cfg.QuasarLogs)
	tg, err := telegram.NewClient(cfg.BotToken, cfg.AdminID)
	if err != nil {
		log.Fatalf("Failed to create telegram client: %v", err)
	}

	scanner := presence.NewScanner(presence.WithSubnet(cfg.Subnet))
	if cfg.DevicesFile != "" {
		_ = scanner.LoadKnownDevices(cfg.DevicesFile)
	}
	diag := netdiag.NewDiagnostics()

	app := &App{
		cfg:     cfg,
		spk:     spk,
		tg:      tg,
		scanner: scanner,
		diag:    diag,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
	}()

	// Send Boot Notification to Admin
	go func() {
		time.Sleep(1 * time.Second)
		wifi, _ := app.spk.GetWiFiStatus()
		bootMsg := fmt.Sprintf("🟢 *Яндекс Станция Mini (YNDX-0004) в сети!*\n\n%s\n\nБот готов к работе. Нажмите /start для вызова меню.", wifi)
		_, _ = app.tg.SendMarkdown(ctx, cfg.AdminID, bootMsg, app.getMainMenuMarkup())
	}()

	log.Printf("Listening for Telegram updates for Admin ID: %d", cfg.AdminID)

	handler := func(ctx context.Context, u telegram.Update) error {
		app.handleUpdate(ctx, &u)
		return nil
	}

	if err := tg.StartPolling(ctx, handler); err != nil && ctx.Err() == nil {
		log.Fatalf("Polling error: %v", err)
	}
}

func (a *App) autoDeleteDelay() time.Duration {
	sec := a.cfg.AutoDeleteSec
	if sec <= 0 {
		sec = 5
	}
	return time.Duration(sec) * time.Second
}

func (a *App) sendEphemeral(ctx context.Context, chatID int64, text string, triggerMsgID int64) {
	delay := a.autoDeleteDelay()
	rep, err := a.tg.SendTextMessage(ctx, chatID, text)
	if err == nil && rep != nil {
		a.tg.DeleteAfter(delay, chatID, triggerMsgID, rep.MessageID)
	} else if triggerMsgID > 0 {
		a.tg.DeleteAfter(delay, chatID, triggerMsgID)
	}
}

func (a *App) handleUpdate(ctx context.Context, u *telegram.Update) {
	// 1. Handle Callback Queries (Inline buttons)
	if u.IsCallback() {
		cb := u.CallbackQuery
		if cb != nil {
			log.Printf("[callback] Data: %s, User: %d", cb.Data, u.SenderID())
		}
		a.handleCallback(ctx, u)
		return
	}

	msg := u.Message
	if msg == nil || msg.Chat == nil {
		return
	}

	// 2. Handle Voice Messages (Intercom: Play voice -> Record reply -> Send back)
	if u.IsVoice() || u.IsAudio() {
		log.Printf("[voice] Incoming audio from %d", msg.Chat.ID)
		a.handleVoiceIntercom(ctx, u)
		return
	}

	// 3. Handle Text Commands & Messages
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	log.Printf("[message] Chat %d: %s", msg.Chat.ID, text)

	if u.IsCommand() {
		cmd, args := u.Command()
		log.Printf("[command] /%s %s", cmd, args)
		switch cmd {
		case "start", "menu":
			a.sendMainMenu(ctx, msg.Chat.ID)
			a.tg.DeleteAfter(3*time.Second, msg.Chat.ID, msg.MessageID)
		case "status", "fastfetch":
			a.sendFastfetchStatus(ctx, msg.Chat.ID, 0)
			a.tg.DeleteAfter(3*time.Second, msg.Chat.ID, msg.MessageID)
		case "presence", "whoishome":
			a.showPresenceMenu(ctx, msg.Chat.ID, 0)
			a.tg.DeleteAfter(3*time.Second, msg.Chat.ID, msg.MessageID)
		case "net", "ping":
			a.showNetDiagMenu(ctx, msg.Chat.ID, 0)
			a.tg.DeleteAfter(3*time.Second, msg.Chat.ID, msg.MessageID)
		case "speedtest":
			a.runSpeedtest(ctx, msg.Chat.ID, 0)
			a.tg.DeleteAfter(3*time.Second, msg.Chat.ID, msg.MessageID)
		case "logs", "log":
			a.showLogsView(ctx, msg.Chat.ID, 0)
			a.tg.DeleteAfter(3*time.Second, msg.Chat.ID, msg.MessageID)
		case "devname":
			a.handleDevNameCommand(ctx, msg.Chat.ID, args, msg.MessageID)
		case "say":
			if args != "" {
				log.Printf("[tts] Speaking: %s", args)
				if err := a.spk.PlayTTS(args); err != nil {
					log.Printf("[tts] Error: %v", err)
					a.sendEphemeral(ctx, msg.Chat.ID, fmt.Sprintf("❌ Ошибка озвучки: %v", err), msg.MessageID)
				} else {
					a.sendEphemeral(ctx, msg.Chat.ID, fmt.Sprintf("🗣 Озвучено: %s", args), msg.MessageID)
				}
			} else {
				a.sendEphemeral(ctx, msg.Chat.ID, "ℹ️ Использование: `/say <текст>`", msg.MessageID)
			}
		case "play":
			if args != "" {
				log.Printf("[sound] Playing: %s", args)
				if err := a.spk.PlaySound(args); err == nil {
					a.sendEphemeral(ctx, msg.Chat.ID, fmt.Sprintf("📢 Воспроизводится: %s", args), msg.MessageID)
				} else {
					log.Printf("[sound] Error: %v", err)
					a.sendEphemeral(ctx, msg.Chat.ID, fmt.Sprintf("❌ Ошибка: %v", err), msg.MessageID)
				}
			}
		case "listen":
			sec := 5
			if args != "" {
				if s, err := strconv.Atoi(args); err == nil && s > 0 && s <= 30 {
					sec = s
				}
			}
			a.tg.DeleteAfter(3*time.Second, msg.Chat.ID, msg.MessageID)
			a.runListenCommand(ctx, msg.Chat.ID, sec)
		case "vol":
			if args != "" {
				if v, err := strconv.Atoi(args); err == nil {
					_ = a.spk.SetVolume(v)
					a.sendEphemeral(ctx, msg.Chat.ID, fmt.Sprintf("🔊 Громкость установлена: %d%%", v), msg.MessageID)
				}
			}
		case "reboot":
			a.sendEphemeral(ctx, msg.Chat.ID, "🔄 Перезагрузка колонки...", msg.MessageID)
			_ = a.spk.Reboot()
		default:
			a.sendMainMenu(ctx, msg.Chat.ID)
			a.tg.DeleteAfter(3*time.Second, msg.Chat.ID, msg.MessageID)
		}
		return
	}

	// Any plain text sent to bot: treat as TTS
	log.Printf("[tts] Plain text to speech: %s", text)
	if err := a.spk.PlayTTS(text); err != nil {
		log.Printf("[tts] Error: %v", err)
		a.sendEphemeral(ctx, msg.Chat.ID, fmt.Sprintf("❌ Ошибка озвучки: %v", err), msg.MessageID)
	} else {
		a.sendEphemeral(ctx, msg.Chat.ID, fmt.Sprintf("🗣 Озвучено: \"%s\"", text), msg.MessageID)
	}
}

func (a *App) handleCallback(ctx context.Context, u *telegram.Update) {
	cb := u.CallbackQuery
	if cb == nil || cb.Message == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	data := cb.Data

	_ = a.tg.AnswerCallback(ctx, cb.ID)

	switch {
	case data == "nav:main":
		a.editToMainMenu(ctx, chatID, msgID)

	case data == "nav:status":
		a.sendFastfetchStatus(ctx, chatID, msgID)

	case data == "nav:presence":
		a.showPresenceMenu(ctx, chatID, msgID)

	case data == "nav:netdiag":
		a.showNetDiagMenu(ctx, chatID, msgID)

	case data == "net:speedtest":
		a.runSpeedtest(ctx, chatID, msgID)

	case data == "nav:intercom":
		text := "🎙️ *Рация & Прослушка комнаты*\n\n" +
			"• *Рация:* отправьте голосовое сообщение в этот чат — колонка озвучит его в комнате и сразу запишет 5 секунд ответа.\n\n" +
			"• *Прослушка:* выберите длительность записи ниже:"
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("👂 Послушать 5 сек", "rec:5"), telegram.NewButton("👂 Послушать 10 сек", "rec:10")).
			Row(telegram.NewButton("👂 Послушать 15 сек", "rec:15"), telegram.NewButton("👂 Послушать 30 сек", "rec:30")).
			Row(telegram.NewButton("⬅️ Назад", "nav:main")).
			Build()
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:soundboard":
		text := "📢 *Саундборд*\n\nВыберите звук для мгновенного воспроизведения на колонке:"
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("📢 Airhorn", "snd:airhorn"), telegram.NewButton("🎺 Bruh", "snd:bruh")).
			Row(telegram.NewButton("🔔 Discord", "snd:discord"), telegram.NewButton("💨 Fart", "snd:fart")).
			Row(telegram.NewButton("🪈 Metal Pipe", "snd:metal_pipe"), telegram.NewButton("🎵 Rickroll", "snd:rickroll")).
			Row(telegram.NewButton("🔊 Win XP", "snd:winxp"), telegram.NewButton("⏹ Остановить", "snd:stop")).
			Row(telegram.NewButton("⬅️ Назад", "nav:main")).
			Build()
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:volume":
		a.showVolumeMenu(ctx, chatID, msgID)

	case data == "nav:tts":
		text := "🔊 *Синтез речи (TTS)*\n\n" +
			"Просто напишите любой текст в чат или используйте команду:\n`/say Привет, мир!`\n\nКолонка мгновенно произнесет его приятным голосом."
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("⬅️ Назад", "nav:main")).
			Build()
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:dialogs":
		dialogs, err := a.spk.GetLatestDialogs(6)
		text := "💬 *История диалогов с Алисой:*\n\n"
		if err != nil || len(dialogs) == 0 {
			text += "_(Диалогов пока нет или логи пусты)_"
		} else {
			for _, d := range dialogs {
				text += "• `" + d + "`\n"
			}
		}
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("🔄 Обновить", "nav:dialogs")).
			Row(telegram.NewButton("⬅️ Назад", "nav:main")).
			Build()
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:system":
		a.showSystemMenu(ctx, chatID, msgID)

	// --- Actions ---
	case strings.HasPrefix(data, "snd:"):
		sound := strings.TrimPrefix(data, "snd:")
		if sound == "stop" {
			a.spk.StopPlayback()
			_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, "Звук остановлен")
		} else {
			if err := a.spk.PlaySound(sound); err != nil {
				_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, fmt.Sprintf("❌ Ошибка: %v", err))
			} else {
				_ = a.tg.AnswerCallback(ctx, cb.ID)
			}
		}

	case strings.HasPrefix(data, "rec:"):
		sec, _ := strconv.Atoi(strings.TrimPrefix(data, "rec:"))
		if sec <= 0 {
			sec = 5
		}
		_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, fmt.Sprintf("Запись %d секунд...", sec))
		a.runListenCommand(ctx, chatID, sec)

	case strings.HasPrefix(data, "vol:"):
		v, _ := strconv.Atoi(strings.TrimPrefix(data, "vol:"))
		_ = a.spk.SetVolume(v)
		_ = a.tg.AnswerCallback(ctx, cb.ID)
		a.showVolumeMenu(ctx, chatID, msgID)

	case strings.HasPrefix(data, "volstep:"):
		diff, _ := strconv.Atoi(strings.TrimPrefix(data, "volstep:"))
		cur, _, _ := a.spk.GetVolume()
		_ = a.spk.SetVolume(cur + diff)
		a.showVolumeMenu(ctx, chatID, msgID)

	case data == "mute:toggle":
		_, muted, _ := a.spk.GetVolume()
		_ = a.spk.SetMute(!muted)
		a.showVolumeMenu(ctx, chatID, msgID)

	case data == "gesture:off":
		_ = a.spk.SetGesturesEnabled(false)
		_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, "Жесты заблокированы")
		a.showSystemMenu(ctx, chatID, msgID)

	case data == "gesture:on":
		_ = a.spk.SetGesturesEnabled(true)
		_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, "Жесты включены")
		a.showSystemMenu(ctx, chatID, msgID)

	case data == "sys:drop_caches":
		_ = a.spk.DropCaches()
		_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, "Кэш RAM успешно очищен!")

	case data == "sys:logs":
		a.showLogsView(ctx, chatID, msgID)

	case data == "sys:reboot_confirm":
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("⚠️ Подтвердить перезагрузку", "sys:reboot_do")).
			Row(telegram.NewButton("Отмена", "nav:system")).
			Build()
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, "❓ Вы уверены, что хотите перезагрузить колонку?", telegram.ParseModeMarkdown, markup)

	case data == "sys:reboot_do":
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, "🔄 *Колонка перезагружается...*\nБот пришлет уведомление после старта.", telegram.ParseModeMarkdown, nil)
		_ = a.spk.Reboot()
	}
}

func (a *App) getMainMenuMarkup() *telegram.InlineKeyboardMarkup {
	return telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("📊 Статус & Fastfetch", "nav:status")).
		Row(telegram.NewButton("🎙️ Рация & Прослушка", "nav:intercom"), telegram.NewButton("📢 Саундборд", "nav:soundboard")).
		Row(telegram.NewButton("🔊 Озвучить текст", "nav:tts"), telegram.NewButton("🎚️ Громкость", "nav:volume")).
		Row(telegram.NewButton("👥 Кто дома?", "nav:presence"), telegram.NewButton("📶 Сеть & Пинг", "nav:netdiag")).
		Row(telegram.NewButton("💬 Диалоги с Алисой", "nav:dialogs")).
		Row(telegram.NewButton("⚙️ Управление системой", "nav:system")).
		Build()
}

func (a *App) sendMainMenu(ctx context.Context, chatID int64) {
	text := "🤖 *Яндекс Станция Mini (YNDX-0004)*\nСтатус: *В сети* 🟢\n\nВыберите раздел в меню:"
	_, _ = a.tg.SendMarkdown(ctx, chatID, text, a.getMainMenuMarkup())
}

func (a *App) editToMainMenu(ctx context.Context, chatID int64, msgID int64) {
	text := "🤖 *Яндекс Станция Mini (YNDX-0004)*\nСтатус: *В сети* 🟢\n\nВыберите раздел в меню:"
	_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, a.getMainMenuMarkup())
}

func (a *App) sendFastfetchStatus(ctx context.Context, chatID int64, editMsgID int64) {
	ff, _ := a.spk.GetFastfetch()
	wifi, _ := a.spk.GetWiFiStatus()

	text := fmt.Sprintf("```\n%s\n```\n%s", ff, wifi)
	markup := telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("🔄 Обновить", "nav:status")).
		Row(telegram.NewButton("⬅️ Назад", "nav:main")).
		Build()

	if editMsgID > 0 {
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, editMsgID, text, telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = a.tg.SendMarkdown(ctx, chatID, text, markup)
	}
}

// -------------------------------------------------------------
// Presence Detection UI ("Кто дома?")
// -------------------------------------------------------------

func (a *App) showPresenceMenu(ctx context.Context, chatID int64, msgID int64) {
	devices, err := a.scanner.Scan(ctx)

	var sb strings.Builder
	sb.WriteString("👥 *Детектор присутствия (Кто дома?)*\n\n")

	if err != nil {
		sb.WriteString(fmt.Sprintf("⚠️ Ошибка сканирования сети: %v\n\n", err))
	} else if len(devices) == 0 {
		sb.WriteString("_(Устройства в локальной сети не обнаружены)_\n\n")
	} else {
		onlineCount := 0
		var knownList []string
		var otherList []string

		for _, d := range devices {
			if !d.IsOnline {
				continue
			}
			onlineCount++
			if d.IsSelf {
				continue // Skip speaker itself from presence list
			}

			if d.Name != "" {
				knownList = append(knownList, fmt.Sprintf("• 🟢 *%s* (`%s`)", d.Name, d.IP))
			} else {
				otherList = append(otherList, fmt.Sprintf("• ⚪ `%s` (`%s`)", d.IP, d.MAC))
			}
		}

		if len(knownList) > 0 {
			sb.WriteString("*Домочадцы в сети:*\n")
			for _, k := range knownList {
				sb.WriteString(k + "\n")
			}
			sb.WriteString("\n")
		}

		if len(otherList) > 0 {
			sb.WriteString("*Другие сетевые устройства:*\n")
			maxOther := 8
			if len(otherList) < maxOther {
				maxOther = len(otherList)
			}
			for i := 0; i < maxOther; i++ {
				sb.WriteString(otherList[i] + "\n")
			}
			if len(otherList) > maxOther {
				sb.WriteString(fmt.Sprintf("_...и еще %d устройств_\n", len(otherList)-maxOther))
			}
			sb.WriteString("\n")
		}

		sb.WriteString(fmt.Sprintf("📊 Всего активных узлов: *%d*\n\n", onlineCount))
	}

	sb.WriteString("💡 *Чтобы присвоить имя устройству, напишите:*\n`/devname <MAC> <Имя>`\n_Пример: `/devname 24:0f:5e:02:02:9a Роутер Keenetic`_")

	markup := telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("🔄 Сканировать снова", "nav:presence")).
		Row(telegram.NewButton("⬅️ Назад", "nav:main")).
		Build()

	if msgID > 0 {
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, sb.String(), telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = a.tg.SendMarkdown(ctx, chatID, sb.String(), markup)
	}
}

func (a *App) handleDevNameCommand(ctx context.Context, chatID int64, args string, userMsgID int64) {
	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		a.sendEphemeral(ctx, chatID, "ℹ️ Использование: `/devname <MAC-адрес> <Название устройства>`\nПример: `/devname 24:0f:5e:02:02:9a Роутер`", userMsgID)
		return
	}

	mac := parts[0]
	name := strings.TrimSpace(parts[1])
	a.scanner.SetDeviceName(mac, name)
	if a.cfg.DevicesFile != "" {
		_ = a.scanner.SaveKnownDevices(a.cfg.DevicesFile)
	}

	a.sendEphemeral(ctx, chatID, fmt.Sprintf("✅ Устройству `%s` присвоено имя: *%s*", presence.NormalizeMAC(mac), name), userMsgID)
}

// -------------------------------------------------------------
// Network Diagnostics & Speedtest UI
// -------------------------------------------------------------

func (a *App) showNetDiagMenu(ctx context.Context, chatID int64, msgID int64) {
	wifi, _ := a.diag.GetWiFiInfo()
	pings, _ := a.diag.PingHosts(ctx)

	var sb strings.Builder
	sb.WriteString("📶 *Сетевая диагностика & Пинг*\n\n")

	if wifi.Connected {
		signalPercent := 100
		if wifi.Signal < -90 {
			signalPercent = 0
		} else if wifi.Signal < -30 {
			signalPercent = int(float64(wifi.Signal+90) / 60.0 * 100.0)
		}
		sb.WriteString(fmt.Sprintf("*Wi-Fi соединение:*\n• Сеть (SSID): *%s*\n• Частота: *%d МГц* | Канал: *%s*\n• Сигнал: *%d dBm* (%d%%)\n• Битрейт: *%.1f Мбит/с*\n\n",
			wifi.SSID, wifi.Frequency, getWiFiChannel(wifi.Frequency), wifi.Signal, signalPercent, wifi.TxBitrate))
	}

	sb.WriteString("*Пинг до ключевых серверов:*\n")
	for _, p := range pings {
		icon := "🟢"
		latStr := fmt.Sprintf("%.1f ms", float64(p.Latency.Microseconds())/1000.0)
		if !p.Success {
			icon = "🔴"
			latStr = "ошибка"
		} else if p.Latency > 100*time.Millisecond {
			icon = "🟡"
		}
		sb.WriteString(fmt.Sprintf("• %s *%s*: `%s`\n", icon, p.Name, latStr))
	}

	markup := telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("🚀 Запустить тест скорости", "net:speedtest"), telegram.NewButton("🔄 Обновить пинг", "nav:netdiag")).
		Row(telegram.NewButton("⬅️ Назад", "nav:main")).
		Build()

	if msgID > 0 {
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, sb.String(), telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = a.tg.SendMarkdown(ctx, chatID, sb.String(), markup)
	}
}

func getWiFiChannel(freq int) string {
	switch freq {
	case 2412:
		return "1 (2.4G)"
	case 2417:
		return "2 (2.4G)"
	case 2422:
		return "3 (2.4G)"
	case 2427:
		return "4 (2.4G)"
	case 2432:
		return "5 (2.4G)"
	case 2437:
		return "6 (2.4G)"
	case 2442:
		return "7 (2.4G)"
	case 2447:
		return "8 (2.4G)"
	case 2452:
		return "9 (2.4G)"
	case 2457:
		return "10 (2.4G)"
	case 2462:
		return "11 (2.4G)"
	case 2467:
		return "12 (2.4G)"
	case 2472:
		return "13 (2.4G)"
	default:
		if freq >= 5000 {
			return fmt.Sprintf("5G (%d)", freq)
		}
		return fmt.Sprintf("%d", freq)
	}
}

func (a *App) runSpeedtest(ctx context.Context, chatID int64, msgID int64) {
	_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, "🚀 *Выполняется тест скорости...*\n\nЗагрузка тестового блока 5 МБ через CDN...", telegram.ParseModeMarkdown, nil)

	go func() {
		res, err := a.diag.RunSpeedtest(ctx)
		var text string
		if err != nil {
			text = fmt.Sprintf("❌ Ошибка измерения скорости: %v", err)
		} else {
			mb := float64(res.DownloadedBytes) / (1024 * 1024)
			text = fmt.Sprintf("🚀 *Результаты теста скорости:*\n\n"+
				"• Скорость скачивания: *%.2f Мбит/с*\n"+
				"• Загружено: *%.1f МБ* за *%.2f сек*\n\n"+
				"✅ Тест завершен успешно!",
				res.SpeedMbps, mb, res.Duration.Seconds())
		}

		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("🔄 Повторить тест", "net:speedtest"), telegram.NewButton("⬅️ К сети", "nav:netdiag")).
			Row(telegram.NewButton("⬅️ Главное меню", "nav:main")).
			Build()

		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
	}()
}

func (a *App) showVolumeMenu(ctx context.Context, chatID int64, msgID int64) {
	vol, muted, _ := a.spk.GetVolume()
	muteStatus := "выключен 🔊"
	if muted {
		muteStatus = "ВКЛЮЧЕН (звук заглушен) 🔇"
	}
	text := fmt.Sprintf("🎚️ *Управление громкостью*\n\nТекущая громкость: *%d%%*\nРежим Mute: *%s*\n\nВыберите уровень или измените ступенями:", vol, muteStatus)

	muteBtn := telegram.NewButton("🔇 Заглушить (Mute)", "mute:toggle")
	if muted {
		muteBtn = telegram.NewButton("🔊 Включить звук (Unmute)", "mute:toggle")
	}

	markup := telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("🔉 -10%", "volstep:-10"), telegram.NewButton("🔊 +10%", "volstep:10")).
		Row(telegram.NewButton("20%", "vol:20"), telegram.NewButton("40%", "vol:40"), telegram.NewButton("60%", "vol:60"), telegram.NewButton("80%", "vol:80"), telegram.NewButton("100%", "vol:100")).
		Row(muteBtn).
		Row(telegram.NewButton("⬅️ Назад", "nav:main")).
		Build()

	_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
}

func (a *App) showSystemMenu(ctx context.Context, chatID int64, msgID int64) {
	gestureStatus := "активны ✅"
	if !a.spk.AreGesturesEnabled() {
		gestureStatus = "заблокированы 🛑"
	}
	text := fmt.Sprintf("⚙️ *Управление системой*\n\n• Датчик жестов ToF: *%s*\n• Питание: онлайн\n\nВыберите действие:", gestureStatus)
	gestureBtn := telegram.NewButton("✋ Заблокировать жесты", "gesture:off")
	if !a.spk.AreGesturesEnabled() {
		gestureBtn = telegram.NewButton("✋ Включить жесты", "gesture:on")
	}
	markup := telegram.NewKeyboardBuilder().
		Row(gestureBtn).
		Row(telegram.NewButton("📋 Просмотр логов (Live Logs)", "sys:logs")).
		Row(telegram.NewButton("🧹 Очистить кэш RAM", "sys:drop_caches")).
		Row(telegram.NewButton("🔄 Перезагрузить колонку", "sys:reboot_confirm")).
		Row(telegram.NewButton("⬅️ Назад", "nav:main")).
		Build()
	_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
}

func (a *App) showLogsView(ctx context.Context, chatID int64, msgID int64) {
	logContent := a.getRecentLogs(20)
	nowStr := time.Now().Format("15:04:05")
	text := fmt.Sprintf("📋 *Live Logs бота* _(обновлено в %s)_\n\n```\n%s\n```", nowStr, logContent)

	markup := telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("🔄 Обновить логи", "sys:logs")).
		Row(telegram.NewButton("⬅️ Назад в систему", "nav:system")).
		Row(telegram.NewButton("⬅️ Главное меню", "nav:main")).
		Build()

	if msgID > 0 {
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = a.tg.SendMarkdown(ctx, chatID, text, markup)
	}
}

func (a *App) getRecentLogs(linesCount int) string {
	logPath := a.cfg.LogPath
	if logPath == "" {
		logPath = "/data/telegram-bot/bot.log"
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Sprintf("Не удалось прочитать файл логов: %v", err)
	}

	rawLines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(rawLines) == 0 || (len(rawLines) == 1 && rawLines[0] == "") {
		return "(Файл логов пуст)"
	}

	start := 0
	if len(rawLines) > linesCount {
		start = len(rawLines) - linesCount
	}
	selected := rawLines[start:]

	for i, l := range selected {
		if len(l) > 120 {
			selected[i] = l[:117] + "..."
		}
	}

	return strings.Join(selected, "\n")
}

func (a *App) runListenCommand(ctx context.Context, chatID int64, sec int) {
	go func() {
		log.Printf("[listen] Recording %d seconds...", sec)
		m1, _ := a.tg.SendTextMessage(ctx, chatID, fmt.Sprintf("👂 Запись аудио из комнаты (%d сек)...", sec))
		filePath, err := a.spk.RecordRoom(sec)
		if m1 != nil {
			_ = a.tg.DeleteMessage(ctx, chatID, m1.MessageID)
		}
		if err != nil {
			log.Printf("[listen] RecordRoom error: %v", err)
			a.sendEphemeral(ctx, chatID, fmt.Sprintf("❌ Ошибка записи: %v", err), 0)
			return
		}
		log.Printf("[listen] Sending audio file: %s", filePath)
		_, err = a.tg.SendAudioFile(ctx, chatID, filePath, fmt.Sprintf("🎙️ Аудиозапись из комнаты (%d сек)", sec), fmt.Sprintf("Запись %d сек", sec), "Яндекс Станция Mini", nil)
		if err != nil {
			log.Printf("[listen] SendAudioFile error: %v", err)
			a.sendEphemeral(ctx, chatID, fmt.Sprintf("❌ Ошибка отправки аудио: %v", err), 0)
		}
	}()
}

func (a *App) handleVoiceIntercom(ctx context.Context, u *telegram.Update) {
	chatID := u.ChatID()
	go func() {
		log.Printf("[intercom] Processing incoming voice message from chat %d", chatID)
		m1, _ := a.tg.SendTextMessage(ctx, chatID, "🎙️ Голосовое сообщение принято! Озвучиваю в комнате...")

		// 1. Play chime alert
		_ = a.spk.PlaySound("telegram")
		time.Sleep(500 * time.Millisecond)

		// 2. Download incoming voice file from Telegram and play via native GStreamer
		var fileID string
		if u.IsVoice() {
			fileID = u.Voice().FileID
		} else if u.IsAudio() {
			fileID = u.Audio().FileID
		}

		if fileID != "" {
			ogaPath := "/tmp/incoming_voice.oga"
			_ = os.Remove(ogaPath)
			if downloadedPath, err := a.tg.DownloadFile(ctx, fileID, ogaPath); err == nil {
				log.Printf("[intercom] Playing voice note: %s", downloadedPath)
				_ = a.spk.PlayAudioSync(downloadedPath)
			} else {
				log.Printf("[intercom] Download failed: %v", err)
			}
		}

		time.Sleep(500 * time.Millisecond)

		// 3. Record response from room
		m2, _ := a.tg.SendTextMessage(ctx, chatID, "👂 Записываю ответ из комнаты (5 сек)...")
		log.Printf("[intercom] Recording 5s room response...")
		recFile, err := a.spk.RecordRoom(5)

		// Delete intermediate notifications to keep chat clean
		if m1 != nil {
			_ = a.tg.DeleteMessage(ctx, chatID, m1.MessageID)
		}
		if m2 != nil {
			_ = a.tg.DeleteMessage(ctx, chatID, m2.MessageID)
		}

		if err == nil {
			log.Printf("[intercom] Sending recorded reply: %s", recFile)
			_, err = a.tg.SendAudioFile(ctx, chatID, recFile, "🎙️ Ответ из комнаты", "Ответ из комнаты", "Яндекс Станция Mini", nil)
			if err != nil {
				log.Printf("[intercom] SendAudioFile error: %v", err)
				a.sendEphemeral(ctx, chatID, fmt.Sprintf("❌ Ошибка отправки ответа: %v", err), 0)
			}
		} else {
			log.Printf("[intercom] RecordRoom error: %v", err)
			a.sendEphemeral(ctx, chatID, fmt.Sprintf("❌ Ошибка записи ответа: %v", err), 0)
		}
	}()
}
