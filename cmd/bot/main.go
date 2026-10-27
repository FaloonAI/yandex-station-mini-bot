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
	"github.com/FaloonAI/yandex-station-mini-bot/internal/timer"
	"github.com/FaloonAI/yandex-station-mini-bot/internal/ui"
)

type App struct {
	cfg     *config.Config
	spk     *speaker.Controller
	tg      *telegram.Client
	tm      *ui.ThemeManager
	timer   *timer.Manager
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

	themeMgr := ui.NewThemeManager(cfg.Theme)
	timerMgr := timer.NewManager()
	scanner := presence.NewScanner(presence.WithSubnet(cfg.Subnet))
	if cfg.DevicesFile != "" {
		_ = scanner.LoadKnownDevices(cfg.DevicesFile)
	}
	diag := netdiag.NewDiagnostics()

	app := &App{
		cfg:     cfg,
		spk:     spk,
		tg:      tg,
		tm:      themeMgr,
		timer:   timerMgr,
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
		app.timer.Close()
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
		case "status", "fastfetch":
			a.sendFastfetchStatus(ctx, msg.Chat.ID, 0)
		case "timer":
			a.handleTimerCommand(ctx, msg.Chat.ID, args)
		case "alarm":
			a.handleAlarmCommand(ctx, msg.Chat.ID, args)
		case "presence", "whoishome":
			a.showPresenceMenu(ctx, msg.Chat.ID, 0)
		case "net", "ping":
			a.showNetDiagMenu(ctx, msg.Chat.ID, 0)
		case "speedtest":
			a.runSpeedtest(ctx, msg.Chat.ID, 0)
		case "theme":
			if args != "" {
				if err := a.tm.SetTheme(args); err == nil {
					_, _ = a.tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("🎨 Тема оформления изменена на: *%s*", a.tm.CurrentThemeName()))
				} else {
					_, _ = a.tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("❌ Ошибка: доступные темы: %s", strings.Join(a.tm.ListThemes(), ", ")))
				}
			} else {
				a.showThemeMenu(ctx, msg.Chat.ID, 0)
			}
		case "setemoji", "emoji":
			a.handleSetEmojiCommand(ctx, msg.Chat.ID, args)
		case "devname":
			a.handleDevNameCommand(ctx, msg.Chat.ID, args)
		case "say":
			if args != "" {
				log.Printf("[tts] Speaking: %s", args)
				if err := a.spk.PlayTTS(args); err != nil {
					log.Printf("[tts] Error: %v", err)
					_, _ = a.tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("❌ Ошибка озвучки: %v", err))
				} else {
					_, _ = a.tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("🗣 Озвучено: %s", args))
				}
			}
		case "play":
			if args != "" {
				log.Printf("[sound] Playing: %s", args)
				if err := a.spk.PlaySound(args); err == nil {
					_, _ = a.tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("📢 Воспроизводится: %s", args))
				} else {
					log.Printf("[sound] Error: %v", err)
					_, _ = a.tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("❌ Ошибка: %v", err))
				}
			}
		case "listen":
			sec := 5
			if args != "" {
				if s, err := strconv.Atoi(args); err == nil && s > 0 && s <= 30 {
					sec = s
				}
			}
			a.runListenCommand(ctx, msg.Chat.ID, sec)
		case "vol":
			if args != "" {
				if v, err := strconv.Atoi(args); err == nil {
					_ = a.spk.SetVolume(v)
					_, _ = a.tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("🔊 Громкость установлена: %d%%", v))
				}
			}
		case "reboot":
			_, _ = a.tg.SendTextMessage(ctx, msg.Chat.ID, "🔄 Перезагрузка колонки...")
			_ = a.spk.Reboot()
		default:
			a.sendMainMenu(ctx, msg.Chat.ID)
		}
		return
	}

	// Any plain text sent to bot: treat as TTS
	log.Printf("[tts] Plain text to speech: %s", text)
	if err := a.spk.PlayTTS(text); err != nil {
		log.Printf("[tts] Error: %v", err)
		_, _ = a.tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("❌ Ошибка озвучки: %v", err))
	} else {
		_, _ = a.tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("🗣 Озвучено: \"%s\"", text))
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

	case data == "nav:timer":
		a.showTimerMenu(ctx, chatID, msgID)

	case data == "nav:presence":
		a.showPresenceMenu(ctx, chatID, msgID)

	case data == "nav:netdiag":
		a.showNetDiagMenu(ctx, chatID, msgID)

	case data == "net:speedtest":
		a.runSpeedtest(ctx, chatID, msgID)

	case data == "nav:theme":
		a.showThemeMenu(ctx, chatID, msgID)

	case strings.HasPrefix(data, "theme:set:"):
		themeName := strings.TrimPrefix(data, "theme:set:")
		if err := a.tm.SetTheme(themeName); err == nil {
			_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, fmt.Sprintf("Установлена тема: %s", a.tm.CurrentThemeName()))
		}
		a.showThemeMenu(ctx, chatID, msgID)

	case strings.HasPrefix(data, "timer:add:"):
		durStr := strings.TrimPrefix(data, "timer:add:")
		dur, err := time.ParseDuration(durStr)
		if err == nil {
			label := fmt.Sprintf("Таймер на %s", durStr)
			a.scheduleTimer(chatID, dur, label)
			_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, fmt.Sprintf("Таймер на %s запущен!", durStr))
		}
		a.showTimerMenu(ctx, chatID, msgID)

	case strings.HasPrefix(data, "timer:cancel:"):
		id := strings.TrimPrefix(data, "timer:cancel:")
		if a.timer.Cancel(id) {
			_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, "Таймер отменен")
		}
		a.showTimerMenu(ctx, chatID, msgID)

	case data == "timer:cancel_all":
		active := a.timer.ListActive()
		for _, item := range active {
			a.timer.Cancel(item.ID)
		}
		_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, "Все таймеры отменены")
		a.showTimerMenu(ctx, chatID, msgID)

	case data == "nav:intercom":
		text := fmt.Sprintf("%s *Рация & Прослушка комнаты*\n\n"+
			"• *Рация:* отправьте голосовое сообщение в этот чат — колонка озвучит его в комнате и сразу запишет 5 секунд ответа.\n\n"+
			"• *Прослушка:* выберите длительность записи ниже:", a.tm.Icon(ui.BtnIntercom))
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("👂 Послушать 5 сек", "rec:5"), telegram.NewButton("👂 Послушать 10 сек", "rec:10")).
			Row(telegram.NewButton("👂 Послушать 15 сек", "rec:15"), telegram.NewButton("👂 Послушать 30 сек", "rec:30")).
			Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).
			Build()
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:soundboard":
		text := fmt.Sprintf("%s *Саундборд*\n\nВыберите звук для мгновенного воспроизведения на колонке:", a.tm.Icon(ui.BtnSoundboard))
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("📢 Airhorn", "snd:airhorn"), telegram.NewButton("🚨 Alarm", "snd:alarm_loud")).
			Row(telegram.NewButton("🎺 Bruh", "snd:bruh"), telegram.NewButton("🔔 Discord", "snd:discord")).
			Row(telegram.NewButton("💨 Fart", "snd:fart"), telegram.NewButton("🪈 Metal Pipe", "snd:metal_pipe")).
			Row(telegram.NewButton("🎵 Rickroll", "snd:rickroll"), telegram.NewButton("🔊 Win XP", "snd:winxp")).
			Row(telegram.NewButton("⏹ Остановить звук", "snd:stop")).
			Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).
			Build()
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:volume":
		a.showVolumeMenu(ctx, chatID, msgID)

	case data == "nav:tts":
		text := fmt.Sprintf("%s *Синтез речи (TTS)*\n\n"+
			"Просто напишите любой текст в чат или используйте команду:\n`/say Привет, мир!`\n\nКолонка мгновенно произнесет его приятным голосом.", a.tm.Icon(ui.BtnTTS))
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).
			Build()
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:alarm":
		statusText := "выключена ⚪"
		if a.spk.IsSirenActive() {
			statusText = "АКТИВНА 🔴"
		}
		text := fmt.Sprintf("%s *Охранная сирена*\n\nТекущее состояние: *%s*\n\nПри включении колонка выкрутит громкость на 100%% и будет непрерывно выть сиреной.", a.tm.Icon(ui.BtnSiren), statusText)
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("🚨 Включить сирену", "siren:on"), telegram.NewButton("🛑 Выключить", "siren:off")).
			Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).
			Build()
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:dialogs":
		dialogs, err := a.spk.GetLatestDialogs(6)
		text := fmt.Sprintf("%s *История диалогов с Алисой:*\n\n", a.tm.Icon(ui.BtnDialogs))
		if err != nil || len(dialogs) == 0 {
			text += "_(Диалогов пока нет или логи пусты)_"
		} else {
			for _, d := range dialogs {
				text += "• `" + d + "`\n"
			}
		}
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("🔄 Обновить", "nav:dialogs")).
			Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).
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

	case data == "siren:on":
		a.spk.StartSiren()
		_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, "Сирена включена!")
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, "🚨 *Охранная сирена АКТИВНА 🔴*", telegram.ParseModeMarkdown,
			telegram.NewKeyboardBuilder().Row(telegram.NewButton("🛑 Выключить сирену", "siren:off"), telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).Build())

	case data == "siren:off":
		a.spk.StopSiren()
		_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, "Сирена выключена")
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, "⚪ *Охранная сирена выключена*", telegram.ParseModeMarkdown,
			telegram.NewKeyboardBuilder().Row(telegram.NewButton("🚨 Включить сирену", "siren:on"), telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).Build())

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
		Row(telegram.NewButton(a.tm.Format(ui.BtnStatus, "Статус & Fastfetch"), "nav:status")).
		Row(telegram.NewButton(a.tm.Format(ui.BtnIntercom, "Рация & Прослушка"), "nav:intercom"), telegram.NewButton(a.tm.Format(ui.BtnSoundboard, "Саундборд"), "nav:soundboard")).
		Row(telegram.NewButton(a.tm.Format(ui.BtnTTS, "Озвучить текст"), "nav:tts"), telegram.NewButton(a.tm.Format(ui.BtnVolume, "Громкость"), "nav:volume")).
		Row(telegram.NewButton(a.tm.Format(ui.BtnTimer, "Таймеры & Будильники"), "nav:timer"), telegram.NewButton(a.tm.Format(ui.BtnPresence, "Кто дома?"), "nav:presence")).
		Row(telegram.NewButton(a.tm.Format(ui.BtnNetDiag, "Сеть & Пинг"), "nav:netdiag"), telegram.NewButton(a.tm.Format(ui.BtnSiren, "Охранная сирена"), "nav:alarm")).
		Row(telegram.NewButton(a.tm.Format(ui.BtnTheme, "Оформление (Тема)"), "nav:theme"), telegram.NewButton(a.tm.Format(ui.BtnDialogs, "Диалоги"), "nav:dialogs")).
		Row(telegram.NewButton(a.tm.Format(ui.BtnSystem, "Управление системой"), "nav:system")).
		Build()
}

func (a *App) sendMainMenu(ctx context.Context, chatID int64) {
	text := fmt.Sprintf("🤖 *Яндекс Станция Mini (YNDX-0004)*\nСтатус: *В сети* 🟢\nТема: *%s*\n\nВыберите раздел в меню:", a.tm.CurrentThemeName())
	_, _ = a.tg.SendMarkdown(ctx, chatID, text, a.getMainMenuMarkup())
}

func (a *App) editToMainMenu(ctx context.Context, chatID int64, msgID int64) {
	text := fmt.Sprintf("🤖 *Яндекс Станция Mini (YNDX-0004)*\nСтатус: *В сети* 🟢\nТема: *%s*\n\nВыберите раздел в меню:", a.tm.CurrentThemeName())
	_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, a.getMainMenuMarkup())
}

func (a *App) sendFastfetchStatus(ctx context.Context, chatID int64, editMsgID int64) {
	ff, _ := a.spk.GetFastfetch()
	wifi, _ := a.spk.GetWiFiStatus()

	text := fmt.Sprintf("```\n%s\n```\n%s", ff, wifi)
	markup := telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("🔄 Обновить", "nav:status")).
		Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).
		Build()

	if editMsgID > 0 {
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, editMsgID, text, telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = a.tg.SendMarkdown(ctx, chatID, text, markup)
	}
}

// -------------------------------------------------------------
// Timer & Alarm UI
// -------------------------------------------------------------

func (a *App) showTimerMenu(ctx context.Context, chatID int64, msgID int64) {
	active := a.timer.ListActive()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s *Таймеры & Будильники*\n\n", a.tm.Icon(ui.BtnTimer)))

	if len(active) == 0 {
		sb.WriteString("_(Активных таймеров и будильников нет)_\n\n")
	} else {
		sb.WriteString("*Активные таймеры:*\n")
		for _, item := range active {
			typeIcon := "⏱"
			if item.IsAlarm {
				typeIcon = "⏰"
			}
			rem := item.Remaining.Round(time.Second)
			sb.WriteString(fmt.Sprintf("• %s *%s*: осталось `%s` (до %s)\n", typeIcon, item.Label, rem, item.Target.Format("15:04:05")))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("💡 *Быстрый запуск таймера:* выберите кнопки ниже\n")
	sb.WriteString("✍️ *Команды:* `/timer 15m Пицца`, `/alarm 08:30 Подъем`")

	builder := telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("⏱ 1 мин", "timer:add:1m"), telegram.NewButton("⏱ 5 мин", "timer:add:5m"), telegram.NewButton("⏱ 15 мин", "timer:add:15m")).
		Row(telegram.NewButton("⏱ 30 мин", "timer:add:30m"), telegram.NewButton("⏱ 45 мин", "timer:add:45m"), telegram.NewButton("⏱ 1 час", "timer:add:1h"))

	if len(active) > 0 {
		builder.Row(telegram.NewButton("❌ Сбросить все таймеры", "timer:cancel_all"))
	}

	builder.Row(telegram.NewButton("🔄 Обновить", "nav:timer"), telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main"))

	markup := builder.Build()
	if msgID > 0 {
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, sb.String(), telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = a.tg.SendMarkdown(ctx, chatID, sb.String(), markup)
	}
}

func (a *App) scheduleTimer(chatID int64, dur time.Duration, label string) {
	target := time.Now().Add(dur)
	a.timer.AddTimer(dur, label, func() {
		log.Printf("[timer] Timer triggered: %s", label)
		// 1. Play alert sound
		_ = a.spk.PlaySound("telegram")
		time.Sleep(500 * time.Millisecond)

		// 2. Speak with TTS
		_ = a.spk.PlayTTS(fmt.Sprintf("Внимание! Таймер завершен: %s", label))

		// 3. Send Telegram notification
		notif := fmt.Sprintf("⏰ *Таймер завершен!*\n\n📝 *%s*\n🕒 Время: %s", label, target.Format("15:04:05"))
		_, _ = a.tg.SendTextMessage(context.Background(), chatID, notif)
	})
}

func (a *App) handleTimerCommand(ctx context.Context, chatID int64, args string) {
	if args == "" {
		a.showTimerMenu(ctx, chatID, 0)
		return
	}

	parts := strings.SplitN(args, " ", 2)
	dur, err := time.ParseDuration(parts[0])
	if err != nil {
		_, _ = a.tg.SendTextMessage(ctx, chatID, "❌ Неверный формат времени. Пример: `/timer 15m Пицца` (доступно: 30s, 5m, 1h)")
		return
	}

	label := "Таймер"
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		label = strings.TrimSpace(parts[1])
	}

	a.scheduleTimer(chatID, dur, label)
	_, _ = a.tg.SendTextMessage(ctx, chatID, fmt.Sprintf("⏱ Таймер *\"%s\"* установлен на *%s* (сработает в %s)", label, dur, time.Now().Add(dur).Format("15:04:05")))
}

func (a *App) handleAlarmCommand(ctx context.Context, chatID int64, args string) {
	if args == "" {
		_, _ = a.tg.SendTextMessage(ctx, chatID, "ℹ️ Использование: `/alarm 08:30 Подъем!`")
		return
	}

	parts := strings.SplitN(args, " ", 2)
	timeStr := parts[0]
	label := "Будильник"
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		label = strings.TrimSpace(parts[1])
	}

	now := time.Now()
	parsedTime, err := time.ParseInLocation("15:04", timeStr, now.Location())
	if err != nil {
		_, _ = a.tg.SendTextMessage(ctx, chatID, "❌ Неверный формат времени. Используйте `ЧЧ:ММ`, например: `/alarm 07:30`")
		return
	}

	target := time.Date(now.Year(), now.Month(), now.Day(), parsedTime.Hour(), parsedTime.Minute(), 0, 0, now.Location())
	if target.Before(now) {
		target = target.Add(24 * time.Hour) // Next day
	}

	dur := target.Sub(now)
	a.timer.AddAlarm(target, label, func() {
		log.Printf("[alarm] Alarm triggered: %s", label)
		_ = a.spk.PlaySound("alarm_loud")
		time.Sleep(1 * time.Second)
		_ = a.spk.PlayTTS(fmt.Sprintf("Доброе утро! Сработал будильник: %s", label))

		notif := fmt.Sprintf("⏰ *Сработал будильник!*\n\n📝 *%s*\n🕒 Время: %s", label, target.Format("15:04"))
		_, _ = a.tg.SendTextMessage(context.Background(), chatID, notif)
	})

	_, _ = a.tg.SendTextMessage(ctx, chatID, fmt.Sprintf("⏰ Будильник *\"%s\"* установлен на *%s* (через %s)", label, target.Format("15:04"), dur.Round(time.Minute)))
}

// -------------------------------------------------------------
// Presence Detection UI ("Кто дома?")
// -------------------------------------------------------------

func (a *App) showPresenceMenu(ctx context.Context, chatID int64, msgID int64) {
	devices, err := a.scanner.Scan(ctx)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s *Детектор присутствия (Кто дома?)*\n\n", a.tm.Icon(ui.BtnPresence)))

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
		Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).
		Build()

	if msgID > 0 {
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, sb.String(), telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = a.tg.SendMarkdown(ctx, chatID, sb.String(), markup)
	}
}

func (a *App) handleDevNameCommand(ctx context.Context, chatID int64, args string) {
	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		_, _ = a.tg.SendTextMessage(ctx, chatID, "ℹ️ Использование: `/devname <MAC-адрес> <Название устройства>`\nПример: `/devname 24:0f:5e:02:02:9a Роутер`")
		return
	}

	mac := parts[0]
	name := strings.TrimSpace(parts[1])
	a.scanner.SetDeviceName(mac, name)
	if a.cfg.DevicesFile != "" {
		_ = a.scanner.SaveKnownDevices(a.cfg.DevicesFile)
	}

	_, _ = a.tg.SendTextMessage(ctx, chatID, fmt.Sprintf("✅ Устройству `%s` присвоено имя: *%s*", presence.NormalizeMAC(mac), name))
}

// -------------------------------------------------------------
// Network Diagnostics & Speedtest UI
// -------------------------------------------------------------

func (a *App) showNetDiagMenu(ctx context.Context, chatID int64, msgID int64) {
	wifi, _ := a.diag.GetWiFiInfo()
	pings, _ := a.diag.PingHosts(ctx)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s *Сетевая диагностика & Пинг*\n\n", a.tm.Icon(ui.BtnNetDiag)))

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
		Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).
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
			Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Главное меню"), "nav:main")).
			Build()

		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
	}()
}

// -------------------------------------------------------------
// Theme & Emoji UI Customization
// -------------------------------------------------------------

func (a *App) showThemeMenu(ctx context.Context, chatID int64, msgID int64) {
	current := a.tm.GetTheme()

	text := fmt.Sprintf("%s *Оформление & Эмодзи кнопок*\n\n"+
		"Текущая активная тема: *%s*\n\n"+
		"Примеры иконок текущей темы:\n"+
		"• Статус: %s | Саундборд: %s\n"+
		"• Таймер: %s | Кто дома: %s\n"+
		"• Сеть: %s | Охрана: %s\n\n"+
		"Выберите готовую тему ниже или замените любой эмодзи вручную (включая Telegram Premium):\n"+
		"`/setemoji <кнопка> <эмодзи>`",
		a.tm.Icon(ui.BtnTheme), current.Name,
		current.Status, current.Soundboard,
		current.Timer, current.Presence,
		current.NetDiag, current.Siren)

	markup := telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("📊 Default", "theme:set:Default"), telegram.NewButton("💎 Premium", "theme:set:Premium")).
		Row(telegram.NewButton("⚡ Cyberpunk", "theme:set:Cyberpunk"), telegram.NewButton("▪️ Minimal", "theme:set:Minimal")).
		Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).
		Build()

	if msgID > 0 {
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = a.tg.SendMarkdown(ctx, chatID, text, markup)
	}
}

func (a *App) handleSetEmojiCommand(ctx context.Context, chatID int64, args string) {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		help := "🎨 *Настройка эмодзи кнопок:*\n\n" +
			"Использование: `/setemoji <кнопка> <эмодзи>`\n" +
			"Пример: `/setemoji timer ⏳` или `/setemoji status 💎`\n\n" +
			"*Доступные кнопки:* `status`, `intercom`, `soundboard`, `tts`, `volume`, `siren`, `timer`, `presence`, `netdiag`, `dialogs`, `system`, `theme`, `back`"
		_, _ = a.tg.SendTextMessage(ctx, chatID, help)
		return
	}

	btnKey := strings.ToLower(parts[0])
	emoji := parts[1]
	a.tm.SetIcon(ui.ButtonID(btnKey), emoji)

	_, _ = a.tg.SendTextMessage(ctx, chatID, fmt.Sprintf("✅ Для кнопки `%s` установлен эмодзи: %s\nГлавное меню обновлено!", btnKey, emoji))
}

func (a *App) showVolumeMenu(ctx context.Context, chatID int64, msgID int64) {
	vol, muted, _ := a.spk.GetVolume()
	muteStatus := "выключен 🔊"
	if muted {
		muteStatus = "ВКЛЮЧЕН (звук заглушен) 🔇"
	}
	text := fmt.Sprintf("%s *Управление громкостью*\n\nТекущая громкость: *%d%%*\nРежим Mute: *%s*\n\nВыберите уровень или измените ступенями:", a.tm.Icon(ui.BtnVolume), vol, muteStatus)

	muteBtn := telegram.NewButton("🔇 Заглушить (Mute)", "mute:toggle")
	if muted {
		muteBtn = telegram.NewButton("🔊 Включить звук (Unmute)", "mute:toggle")
	}

	markup := telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("🔉 -10%", "volstep:-10"), telegram.NewButton("🔊 +10%", "volstep:10")).
		Row(telegram.NewButton("20%", "vol:20"), telegram.NewButton("40%", "vol:40"), telegram.NewButton("60%", "vol:60"), telegram.NewButton("80%", "vol:80"), telegram.NewButton("100%", "vol:100")).
		Row(muteBtn).
		Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).
		Build()

	_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
}

func (a *App) showSystemMenu(ctx context.Context, chatID int64, msgID int64) {
	gestureStatus := "активны ✅"
	if !a.spk.AreGesturesEnabled() {
		gestureStatus = "заблокированы 🛑"
	}
	text := fmt.Sprintf("%s *Управление системой*\n\n• Датчик жестов ToF: *%s*\n• Питание: онлайн\n\nВыберите действие:", a.tm.Icon(ui.BtnSystem), gestureStatus)
	gestureBtn := telegram.NewButton("✋ Заблокировать жесты", "gesture:off")
	if !a.spk.AreGesturesEnabled() {
		gestureBtn = telegram.NewButton("✋ Включить жесты", "gesture:on")
	}
	markup := telegram.NewKeyboardBuilder().
		Row(gestureBtn).
		Row(telegram.NewButton("🧹 Очистить кэш RAM", "sys:drop_caches")).
		Row(telegram.NewButton("🔄 Перезагрузить колонку", "sys:reboot_confirm")).
		Row(telegram.NewButton(a.tm.Format(ui.BtnBack, "Назад"), "nav:main")).
		Build()
	_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
}

func (a *App) runListenCommand(ctx context.Context, chatID int64, sec int) {
	go func() {
		log.Printf("[listen] Recording %d seconds...", sec)
		_, _ = a.tg.SendTextMessage(ctx, chatID, fmt.Sprintf("👂 Запись аудио из комнаты (%d сек)...", sec))
		filePath, err := a.spk.RecordRoom(sec)
		if err != nil {
			log.Printf("[listen] RecordRoom error: %v", err)
			_, _ = a.tg.SendTextMessage(ctx, chatID, fmt.Sprintf("❌ Ошибка записи: %v", err))
			return
		}
		log.Printf("[listen] Sending audio file: %s", filePath)
		_, err = a.tg.SendAudioFile(ctx, chatID, filePath, fmt.Sprintf("🎙️ Аудиозапись из комнаты (%d сек)", sec), fmt.Sprintf("Запись %d сек", sec), "Яндекс Станция Mini", nil)
		if err != nil {
			log.Printf("[listen] SendAudioFile error: %v", err)
			_, _ = a.tg.SendTextMessage(ctx, chatID, fmt.Sprintf("❌ Ошибка отправки аудио: %v", err))
		}
	}()
}

func (a *App) handleVoiceIntercom(ctx context.Context, u *telegram.Update) {
	chatID := u.ChatID()
	go func() {
		log.Printf("[intercom] Processing incoming voice message from chat %d", chatID)
		_, _ = a.tg.SendTextMessage(ctx, chatID, "🎙️ Голосовое сообщение принято! Озвучиваю в комнате...")

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
		_, _ = a.tg.SendTextMessage(ctx, chatID, "👂 Записываю ответ из комнаты (5 сек)...")
		log.Printf("[intercom] Recording 5s room response...")
		recFile, err := a.spk.RecordRoom(5)
		if err == nil {
			log.Printf("[intercom] Sending recorded reply: %s", recFile)
			_, err = a.tg.SendAudioFile(ctx, chatID, recFile, "🎙️ Ответ из комнаты", "Ответ из комнаты", "Яндекс Станция Mini", nil)
			if err != nil {
				log.Printf("[intercom] SendAudioFile error: %v", err)
				_, _ = a.tg.SendTextMessage(ctx, chatID, fmt.Sprintf("❌ Ошибка отправки ответа: %v", err))
			}
		} else {
			log.Printf("[intercom] RecordRoom error: %v", err)
			_, _ = a.tg.SendTextMessage(ctx, chatID, fmt.Sprintf("❌ Ошибка записи ответа: %v", err))
		}
	}()
}
