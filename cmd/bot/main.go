package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/FaloonAI/yandex-station-mini-bot/internal/config"
	"github.com/FaloonAI/yandex-station-mini-bot/internal/netdiag"
	"github.com/FaloonAI/yandex-station-mini-bot/internal/presence"
	"github.com/FaloonAI/yandex-station-mini-bot/internal/soundboard"
	"github.com/FaloonAI/yandex-station-mini-bot/internal/speaker"
	"github.com/FaloonAI/yandex-station-mini-bot/internal/telegram"
)

type App struct {
	configPath       string
	cfg              *config.Config
	spk              *speaker.Controller
	tg               *telegram.Client
	scanner          *presence.Scanner
	diag             *netdiag.Diagnostics
	sb               *soundboard.Manager
	awaitingAddSound sync.Map // chatID int64 -> bool
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

	sbManager, err := soundboard.NewManager(cfg.SoundsDir, nil)
	if err != nil {
		log.Fatalf("Soundboard manager error: %v", err)
	}

	app := &App{
		configPath: *configPath,
		cfg:        cfg,
		spk:        spk,
		tg:         tg,
		scanner:    scanner,
		diag:       diag,
		sb:         sbManager,
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

	// 2. Handle Audio/Voice Messages (Soundboard upload OR Intercom)
	if u.IsVoice() || u.IsAudio() {
		caption := strings.TrimSpace(msg.Caption)
		_, awaiting := a.awaitingAddSound.Load(msg.Chat.ID)

		if strings.HasPrefix(caption, "/addsound") || awaiting {
			log.Printf("[soundboard] Processing custom sound upload from %d", msg.Chat.ID)
			a.handleSoundUpload(ctx, u)
			return
		}

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
		case "setsubnet":
			a.handleSetSubnetCommand(ctx, msg.Chat.ID, args, msg.MessageID)
		case "addsound":
			a.handleAddSoundCommand(ctx, msg.Chat.ID, args, msg.MessageID)
		case "delsound":
			a.handleDelSoundCommand(ctx, msg.Chat.ID, args, msg.MessageID)
		case "rensound":
			a.handleRenSoundCommand(ctx, msg.Chat.ID, args, msg.MessageID)
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
				filePath, err := a.sb.SoundFilePath(args)
				if err != nil {
					a.sendEphemeral(ctx, msg.Chat.ID, fmt.Sprintf("❌ Звук не найден: %v", err), msg.MessageID)
				} else if err := a.spk.PlayAudio(filePath); err == nil {
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
		a.showSoundboardMenu(ctx, chatID, msgID, 0)

	case strings.HasPrefix(data, "sb:page:"):
		p, _ := strconv.Atoi(strings.TrimPrefix(data, "sb:page:"))
		a.showSoundboardMenu(ctx, chatID, msgID, p)

	case data == "sb:add":
		a.awaitingAddSound.Store(chatID, true)
		text := "➕ *Добавление своего звука в саундборд*\n\n" +
			"Отправьте в чат аудиофайл (`.mp3`, `.wav`, `.ogg`, `.m4a`, `.flac`) или запишите голосовое сообщение.\n\n" +
			"💡 В подписи к файлу или командой укажите название:\n" +
			"`/addsound <Название>`\n_Пример:_ `/addsound Победа`\n\n" +
			"⚡ *Автоматическая оптимизация звука:*\n" +
			"• Обрезка тишины (Silence Gate)\n" +
			"• Ограничение длительности до 15 сек с мягким fade-out\n" +
			"• Нормализация громкости (Peak Normalization)\n" +
			"• Сжатие в моно 22 кГц WAV (экономия до 80% RAM и памяти)"
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("⬅️ К саундборду", "nav:soundboard")).
			Build()
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "sb:del_menu":
		a.showSoundboardDeleteMenu(ctx, chatID, msgID)

	case strings.HasPrefix(data, "sb:del:"):
		id := strings.TrimPrefix(data, "sb:del:")
		if err := a.sb.Delete(id); err != nil {
			_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, fmt.Sprintf("Ошибка: %v", err))
		} else {
			_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, "Звук удален!")
			a.showSoundboardDeleteMenu(ctx, chatID, msgID)
		}

	case data == "sb:ren_menu":
		text := "✏️ *Переименование звука*\n\n" +
			"Отправьте команду:\n" +
			"`/rensound <ID> <Новое название>`\n\n" +
			"_Пример:_ `/rensound fart Мега Пук`"
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("⬅️ К саундборду", "nav:soundboard")).
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

	case data == "sys:subnet":
		a.showSubnetMenu(ctx, chatID, msgID)

	case strings.HasPrefix(data, "subnet:"):
		arg := strings.TrimPrefix(data, "subnet:")
		targetCIDR := arg
		if arg == "auto" {
			detected, err := presence.DetectLocalSubnet("wlan0")
			if err != nil || detected == "" {
				_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, "Не удалось определить подсеть wlan0")
				return
			}
			targetCIDR = detected
		}

		if err := a.scanner.SetSubnet(targetCIDR); err != nil {
			_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, fmt.Sprintf("Ошибка: %v", err))
		} else {
			a.cfg.Subnet = a.scanner.GetSubnet()
			if a.configPath != "" {
				_ = a.cfg.Save(a.configPath)
			}
			_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, fmt.Sprintf("Подсеть обновлена: %s", a.cfg.Subnet))
			a.showSubnetMenu(ctx, chatID, msgID)
		}

	// --- Actions ---
	case strings.HasPrefix(data, "snd:"):
		sound := strings.TrimPrefix(data, "snd:")
		if sound == "stop" {
			a.spk.StopPlayback()
			_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, "Звук остановлен")
		} else {
			filePath, err := a.sb.SoundFilePath(sound)
			if err != nil {
				_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, fmt.Sprintf("❌ Звук не найден: %v", err))
			} else {
				if err := a.spk.PlayAudio(filePath); err != nil {
					_ = a.tg.AnswerCallbackAlert(ctx, cb.ID, fmt.Sprintf("❌ Ошибка: %v", err))
				} else {
					_ = a.tg.AnswerCallback(ctx, cb.ID)
				}
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
// Soundboard UI (List, Paginate, Add, Delete, Rename)
// -------------------------------------------------------------

func (a *App) showSoundboardMenu(ctx context.Context, chatID int64, msgID int64, page int) {
	sounds := a.sb.List()
	total := len(sounds)
	pageSize := 8
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}

	start := page * pageSize
	end := start + pageSize
	if end > total {
		end = total
	}

	text := fmt.Sprintf("📢 *Саундборд* (Звуков: *%d*)\n\nВыберите звук для воспроизведения на колонке:", total)

	builder := telegram.NewKeyboardBuilder()
	if total > 0 {
		pageSounds := sounds[start:end]
		for i := 0; i < len(pageSounds); i += 2 {
			btn1 := telegram.NewButton(pageSounds[i].Name, "snd:"+pageSounds[i].ID)
			if i+1 < len(pageSounds) {
				btn2 := telegram.NewButton(pageSounds[i+1].Name, "snd:"+pageSounds[i+1].ID)
				builder.Row(btn1, btn2)
			} else {
				builder.Row(btn1)
			}
		}
	}

	if totalPages > 1 {
		prevPage := page - 1
		if prevPage < 0 {
			prevPage = totalPages - 1
		}
		nextPage := page + 1
		if nextPage >= totalPages {
			nextPage = 0
		}
		builder.Row(
			telegram.NewButton("◀️ Назад", fmt.Sprintf("sb:page:%d", prevPage)),
			telegram.NewButton(fmt.Sprintf("%d/%d", page+1, totalPages), "nav:soundboard"),
			telegram.NewButton("Вперед ▶️", fmt.Sprintf("sb:page:%d", nextPage)),
		)
	}

	builder.Row(
		telegram.NewButton("➕ Добавить звук", "sb:add"),
		telegram.NewButton("🗑️ Удалить", "sb:del_menu"),
	)
	builder.Row(
		telegram.NewButton("✏️ Переименовать", "sb:ren_menu"),
		telegram.NewButton("⏹ Остановить", "snd:stop"),
	)
	builder.Row(telegram.NewButton("⬅️ Назад", "nav:main"))

	markup := builder.Build()
	if msgID > 0 {
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = a.tg.SendMarkdown(ctx, chatID, text, markup)
	}
}

func (a *App) showSoundboardDeleteMenu(ctx context.Context, chatID int64, msgID int64) {
	sounds := a.sb.List()
	var custom []soundboard.SoundItem
	for _, s := range sounds {
		if !s.IsBuiltin {
			custom = append(custom, s)
		}
	}

	builder := telegram.NewKeyboardBuilder()
	var text string

	if len(custom) == 0 {
		text = "🗑️ *Удаление звуков*\n\n_(Пользовательских звуков пока нет. Встроенные системные звуки защищены от удаления)_"
	} else {
		text = "🗑️ *Выберите звук для удаления:*"
		for _, s := range custom {
			btnText := fmt.Sprintf("❌ %s (%.1f с)", s.Name, s.DurationSec)
			builder.Row(telegram.NewButton(btnText, "sb:del:"+s.ID))
		}
	}

	builder.Row(telegram.NewButton("⬅️ К саундборду", "nav:soundboard"))

	markup := builder.Build()
	if msgID > 0 {
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = a.tg.SendMarkdown(ctx, chatID, text, markup)
	}
}

func (a *App) handleAddSoundCommand(ctx context.Context, chatID int64, args string, userMsgID int64) {
	name := strings.TrimSpace(args)
	if name == "" {
		a.awaitingAddSound.Store(chatID, true)
		help := "➕ *Добавление своего звука:*\n\n" +
			"Отправьте аудиофайл или голосовое сообщение в этот чат с подписью:\n" +
			"`/addsound <Название>`\n_Пример:_ `/addsound Победа`"
		a.sendEphemeral(ctx, chatID, help, userMsgID)
		return
	}

	a.awaitingAddSound.Store(chatID, true)
	a.sendEphemeral(ctx, chatID, fmt.Sprintf("🎵 Ожидаю аудиофайл или голосовое сообщение для звука *\"%s\"*...", name), userMsgID)
}

func (a *App) handleDelSoundCommand(ctx context.Context, chatID int64, args string, userMsgID int64) {
	id := strings.TrimSpace(args)
	if id == "" {
		a.sendEphemeral(ctx, chatID, "ℹ️ Использование: `/delsound <ID_звука>`", userMsgID)
		return
	}

	if err := a.sb.Delete(id); err != nil {
		a.sendEphemeral(ctx, chatID, fmt.Sprintf("❌ Не удалось удалить звук: %v", err), userMsgID)
		return
	}

	a.sendEphemeral(ctx, chatID, fmt.Sprintf("✅ Звук `%s` успешно удален из каталога!", id), userMsgID)
}

func (a *App) handleRenSoundCommand(ctx context.Context, chatID int64, args string, userMsgID int64) {
	parts := strings.SplitN(strings.TrimSpace(args), " ", 2)
	if len(parts) < 2 {
		a.sendEphemeral(ctx, chatID, "ℹ️ Использование: `/rensound <ID_звука> <Новое название>`\nПример: `/rensound fart Мега Звук`", userMsgID)
		return
	}

	id := strings.TrimSpace(parts[0])
	newName := strings.TrimSpace(parts[1])

	if err := a.sb.Rename(id, newName); err != nil {
		a.sendEphemeral(ctx, chatID, fmt.Sprintf("❌ Не удалось переименовать звук: %v", err), userMsgID)
		return
	}

	a.sendEphemeral(ctx, chatID, fmt.Sprintf("✅ Звуку `%s` присвоено новое имя: *%s*", id, newName), userMsgID)
}

func (a *App) handleSoundUpload(ctx context.Context, u *telegram.Update) {
	msg := u.Message
	if msg == nil {
		return
	}
	chatID := msg.Chat.ID
	fileID, defaultName := msg.AudioFileID()
	if fileID == "" {
		return
	}

	caption := strings.TrimSpace(msg.Caption)
	var soundName string
	if strings.HasPrefix(caption, "/addsound") {
		soundName = strings.TrimSpace(strings.TrimPrefix(caption, "/addsound"))
	} else if caption != "" {
		soundName = caption
	}

	if soundName == "" {
		soundName = strings.TrimSuffix(defaultName, filepath.Ext(defaultName))
	}
	if soundName == "" || soundName == "voice" || soundName == "audio" || soundName == "document" {
		soundName = fmt.Sprintf("Звук %d", time.Now().Unix()%1000)
	}

	a.awaitingAddSound.Delete(chatID)

	mProc, _ := a.tg.SendTextMessage(ctx, chatID, "⏳ Скачиваю и оптимизирую аудиофайл...")

	tmpInput := filepath.Join(os.TempDir(), fmt.Sprintf("upload_%d%s", time.Now().UnixNano(), filepath.Ext(defaultName)))
	defer os.Remove(tmpInput)

	if _, err := a.tg.DownloadFile(ctx, fileID, tmpInput); err != nil {
		if mProc != nil {
			_ = a.tg.DeleteMessage(ctx, chatID, mProc.MessageID)
		}
		a.sendEphemeral(ctx, chatID, fmt.Sprintf("❌ Ошибка скачивания файла: %v", err), msg.MessageID)
		return
	}

	item, res, err := a.sb.Add(ctx, soundName, tmpInput)
	if mProc != nil {
		_ = a.tg.DeleteMessage(ctx, chatID, mProc.MessageID)
	}

	if err != nil {
		a.sendEphemeral(ctx, chatID, fmt.Sprintf("❌ Не удалось добавить звук: %v", err), msg.MessageID)
		return
	}

	origKB := float64(res.OriginalSize) / 1024.0
	optKB := float64(res.OptimizedSize) / 1024.0

	repText := fmt.Sprintf("✅ *Звук успешно добавлен в саундборд!*\n\n"+
		"📝 Название: *%s*\n"+
		"🔑 ID: `%s`\n"+
		"⏱ Длительность: *%.1f сек*\n"+
		"📦 Размер: *%.1f КБ* _(было %.1f КБ, экономия %.0f%%)_\n"+
		"🔊 Нормализация: *x%.2f*\n\n"+
		"Звук готов к воспроизведению!",
		item.Name, item.ID, res.DurationSec, optKB, origKB, res.SavedPercent, res.GainMultiplier)

	markup := telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("▶️ Воспроизвести", "snd:"+item.ID)).
		Row(telegram.NewButton("📢 Перейти в саундборд", "nav:soundboard")).
		Row(telegram.NewButton("⬅️ Главное меню", "nav:main")).
		Build()

	_, _ = a.tg.SendMarkdown(ctx, chatID, repText, markup)
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

		sb.WriteString(fmt.Sprintf("📊 Всего активных узлов: *%d*\n", onlineCount))
		sb.WriteString(fmt.Sprintf("🌐 Подсеть сканирования: `%s`\n\n", a.scanner.GetSubnet()))
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
// Subnet Configuration in System Menu
// -------------------------------------------------------------

func (a *App) showSubnetMenu(ctx context.Context, chatID int64, msgID int64) {
	detected, _ := presence.DetectLocalSubnet("wlan0")
	if detected == "" {
		detected = "(не определена)"
	}

	text := fmt.Sprintf("🌐 *Настройка подсети сканирования («Кто дома?»)*\n\n"+
		"• Текущая подсеть: *%s*\n"+
		"• Wi-Fi интерфейс wlan0: *%s*\n\n"+
		"Выберите быструю предустановку или отправьте команду:\n"+
		"`/setsubnet <CIDR>`\n_Пример:_ `/setsubnet 192.168.1.0/24`",
		a.scanner.GetSubnet(), detected)

	builder := telegram.NewKeyboardBuilder()
	if detected != "" && detected != "(не определена)" {
		builder.Row(telegram.NewButton("🔄 Авто (текущая Wi-Fi сеть)", "subnet:auto"))
	}
	builder.Row(telegram.NewButton("192.168.20.0/24", "subnet:192.168.20.0/24"), telegram.NewButton("192.168.1.0/24", "subnet:192.168.1.0/24"))
	builder.Row(telegram.NewButton("192.168.0.0/24", "subnet:192.168.0.0/24"), telegram.NewButton("192.168.31.0/24", "subnet:192.168.31.0/24"))
	builder.Row(telegram.NewButton("⬅️ Назад в систему", "nav:system"))

	markup := builder.Build()
	if msgID > 0 {
		_, _ = a.tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = a.tg.SendMarkdown(ctx, chatID, text, markup)
	}
}

func (a *App) handleSetSubnetCommand(ctx context.Context, chatID int64, args string, userMsgID int64) {
	cidr := strings.TrimSpace(args)
	if cidr == "" {
		a.sendEphemeral(ctx, chatID, "ℹ️ Использование: `/setsubnet <CIDR>`\nПример: `/setsubnet 192.168.1.0/24`", userMsgID)
		return
	}

	if _, _, err := net.ParseCIDR(cidr); err != nil {
		a.sendEphemeral(ctx, chatID, fmt.Sprintf("❌ Неверный формат CIDR: %v", err), userMsgID)
		return
	}

	if err := a.scanner.SetSubnet(cidr); err != nil {
		a.sendEphemeral(ctx, chatID, fmt.Sprintf("❌ Ошибка: %v", err), userMsgID)
		return
	}

	a.cfg.Subnet = a.scanner.GetSubnet()
	if a.configPath != "" {
		_ = a.cfg.Save(a.configPath)
	}

	a.sendEphemeral(ctx, chatID, fmt.Sprintf("✅ Подсеть сканирования успешно обновлена: *%s*", a.cfg.Subnet), userMsgID)
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
	text := fmt.Sprintf("⚙️ *Управление системой*\n\n• Датчик жестов ToF: *%s*\n• Подсеть «Кто дома»: `%s`\n• Питание: онлайн\n\nВыберите действие:",
		gestureStatus, a.scanner.GetSubnet())
	gestureBtn := telegram.NewButton("✋ Заблокировать жесты", "gesture:off")
	if !a.spk.AreGesturesEnabled() {
		gestureBtn = telegram.NewButton("✋ Включить жесты", "gesture:on")
	}
	markup := telegram.NewKeyboardBuilder().
		Row(gestureBtn).
		Row(telegram.NewButton(fmt.Sprintf("🌐 Сменить подсеть (%s)", a.scanner.GetSubnet()), "sys:subnet")).
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
