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
	"github.com/FaloonAI/yandex-station-mini-bot/internal/speaker"
	"github.com/FaloonAI/yandex-station-mini-bot/internal/telegram"
)

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
		wifi, _ := spk.GetWiFiStatus()
		bootMsg := fmt.Sprintf("🟢 *Яндекс Станция Mini (YNDX-0004) в сети!*\n\n%s\n\nБот готов к работе. Нажмите /start для вызова меню.", wifi)
		_, _ = tg.SendMarkdown(ctx, cfg.AdminID, bootMsg, getMainMenuMarkup())
	}()

	log.Printf("Listening for Telegram updates for Admin ID: %d", cfg.AdminID)

	handler := func(ctx context.Context, u telegram.Update) error {
		handleUpdate(ctx, &u, tg, spk, cfg.AdminID)
		return nil
	}

	if err := tg.StartPolling(ctx, handler); err != nil && ctx.Err() == nil {
		log.Fatalf("Polling error: %v", err)
	}
}

func handleUpdate(ctx context.Context, u *telegram.Update, tg *telegram.Client, spk *speaker.Controller, adminID int64) {
	// 1. Handle Callback Queries (Inline buttons)
	if u.IsCallback() {
		handleCallback(ctx, u, tg, spk, adminID)
		return
	}

	msg := u.Message
	if msg == nil || msg.Chat == nil {
		return
	}

	// 2. Handle Voice Messages (Intercom: Play voice -> Record reply -> Send back)
	if u.IsVoice() || u.IsAudio() {
		handleVoiceIntercom(ctx, u, tg, spk)
		return
	}

	// 3. Handle Text Commands & Messages
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	if u.IsCommand() {
		cmd, args := u.Command()
		switch cmd {
		case "start", "menu":
			sendMainMenu(ctx, tg, msg.Chat.ID)
		case "status", "fastfetch":
			sendFastfetchStatus(ctx, tg, spk, msg.Chat.ID, 0)
		case "say":
			if args != "" {
				if err := spk.PlayTTS(args); err != nil {
					_, _ = tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("❌ Ошибка озвучки: %v", err))
				} else {
					_, _ = tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("🗣 Озвучено: %s", args))
				}
			}
		case "play":
			if args != "" {
				if err := spk.PlaySound(args); err == nil {
					_, _ = tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("📢 Воспроизводится: %s", args))
				} else {
					_, _ = tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("❌ Ошибка: %v", err))
				}
			}
		case "listen":
			sec := 5
			if args != "" {
				if s, err := strconv.Atoi(args); err == nil && s > 0 && s <= 30 {
					sec = s
				}
			}
			runListenCommand(ctx, tg, spk, msg.Chat.ID, sec)
		case "vol":
			if args != "" {
				if v, err := strconv.Atoi(args); err == nil {
					_ = spk.SetVolume(v)
					_, _ = tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("🔊 Громкость установлена: %d%%", v))
				}
			}
		case "reboot":
			_, _ = tg.SendTextMessage(ctx, msg.Chat.ID, "🔄 Перезагрузка колонки...")
			_ = spk.Reboot()
		default:
			sendMainMenu(ctx, tg, msg.Chat.ID)
		}
		return
	}

	// Any plain text sent to bot: treat as TTS
	if err := spk.PlayTTS(text); err != nil {
		_, _ = tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("❌ Ошибка озвучки: %v", err))
	} else {
		_, _ = tg.SendTextMessage(ctx, msg.Chat.ID, fmt.Sprintf("🗣 Озвучено: \"%s\"", text))
	}
}

func handleCallback(ctx context.Context, u *telegram.Update, tg *telegram.Client, spk *speaker.Controller, adminID int64) {
	cb := u.CallbackQuery
	if cb == nil || cb.Message == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	data := cb.Data

	_ = tg.AnswerCallback(ctx, cb.ID)

	switch {
	case data == "nav:main":
		editToMainMenu(ctx, tg, chatID, msgID)

	case data == "nav:status":
		sendFastfetchStatus(ctx, tg, spk, chatID, msgID)

	case data == "nav:intercom":
		text := "🎙️ *Рация & Прослушка комнаты*\n\n" +
			"• *Рация:* отправьте голосовое сообщение в этот чат — колонка озвучит его в комнате и сразу запишет 5 секунд ответа.\n\n" +
			"• *Прослушка:* выберите длительность записи ниже:"
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("👂 Послушать 5 сек", "rec:5"), telegram.NewButton("👂 Послушать 10 сек", "rec:10")).
			Row(telegram.NewButton("👂 Послушать 15 сек", "rec:15"), telegram.NewButton("👂 Послушать 30 сек", "rec:30")).
			Row(telegram.NewButton("⬅️ Назад", "nav:main")).
			Build()
		_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:soundboard":
		text := "📢 *Саундборд*\n\nВыберите звук для мгновенного воспроизведения на колонке:"
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("📢 Airhorn", "snd:airhorn"), telegram.NewButton("🚨 Alarm", "snd:alarm_loud")).
			Row(telegram.NewButton("🎺 Bruh", "snd:bruh"), telegram.NewButton("🔔 Discord", "snd:discord")).
			Row(telegram.NewButton("💨 Fart", "snd:fart"), telegram.NewButton("🪈 Metal Pipe", "snd:metal_pipe")).
			Row(telegram.NewButton("🎵 Rickroll", "snd:rickroll"), telegram.NewButton("🔊 Win XP", "snd:winxp")).
			Row(telegram.NewButton("⏹ Остановить звук", "snd:stop")).
			Row(telegram.NewButton("⬅️ Назад", "nav:main")).
			Build()
		_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:volume":
		showVolumeMenu(ctx, tg, spk, chatID, msgID)

	case data == "nav:tts":
		text := "🔊 *Синтез речи (TTS)*\n\n" +
			"Просто напишите любой текст в чат или используйте команду:\n`/say Привет, мир!`\n\nКолонка мгновенно произнесет его приятным голосом."
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("⬅️ Назад", "nav:main")).
			Build()
		_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:alarm":
		statusText := "выключена ⚪"
		if spk.IsSirenActive() {
			statusText = "АКТИВНА 🔴"
		}
		text := fmt.Sprintf("🚨 *Охранная сирена*\n\nТекущее состояние: *%s*\n\nПри включении колонка выкрутит громкость на 100%% и будет непрерывно выть сиреной.", statusText)
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("🚨 Включить сирену", "siren:on"), telegram.NewButton("🛑 Выключить", "siren:off")).
			Row(telegram.NewButton("⬅️ Назад", "nav:main")).
			Build()
		_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:dialogs":
		dialogs, err := spk.GetLatestDialogs(6)
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
		_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	case data == "nav:system":
		gestureStatus := "активны ✅"
		if !spk.AreGesturesEnabled() {
			gestureStatus = "заблокированы 🛑"
		}
		text := fmt.Sprintf("⚙️ *Управление системой*\n\n• Датчик жестов ToF: *%s*\n• Питание: онлайн\n\nВыберите действие:", gestureStatus)
		gestureBtn := telegram.NewButton("✋ Заблокировать жесты", "gesture:off")
		if !spk.AreGesturesEnabled() {
			gestureBtn = telegram.NewButton("✋ Включить жесты", "gesture:on")
		}
		markup := telegram.NewKeyboardBuilder().
			Row(gestureBtn).
			Row(telegram.NewButton("🧹 Очистить кэш RAM", "sys:drop_caches")).
			Row(telegram.NewButton("🔄 Перезагрузить колонку", "sys:reboot_confirm")).
			Row(telegram.NewButton("⬅️ Назад", "nav:main")).
			Build()
		_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)

	// --- Actions ---
	case strings.HasPrefix(data, "snd:"):
		sound := strings.TrimPrefix(data, "snd:")
		if sound == "stop" {
			spk.StopPlayback()
			_ = tg.AnswerCallbackAlert(ctx, cb.ID, "Звук остановлен")
		} else {
			if err := spk.PlaySound(sound); err != nil {
				_ = tg.AnswerCallbackAlert(ctx, cb.ID, fmt.Sprintf("❌ Ошибка: %v", err))
			} else {
				_ = tg.AnswerCallback(ctx, cb.ID)
			}
		}

	case strings.HasPrefix(data, "rec:"):
		sec, _ := strconv.Atoi(strings.TrimPrefix(data, "rec:"))
		if sec <= 0 {
			sec = 5
		}
		_ = tg.AnswerCallbackAlert(ctx, cb.ID, fmt.Sprintf("Запись %d секунд...", sec))
		runListenCommand(ctx, tg, spk, chatID, sec)

	case strings.HasPrefix(data, "vol:"):
		v, _ := strconv.Atoi(strings.TrimPrefix(data, "vol:"))
		_ = spk.SetVolume(v)
		_ = tg.AnswerCallback(ctx, cb.ID)
		showVolumeMenu(ctx, tg, spk, chatID, msgID)

	case strings.HasPrefix(data, "volstep:"):
		diff, _ := strconv.Atoi(strings.TrimPrefix(data, "volstep:"))
		cur, _, _ := spk.GetVolume()
		_ = spk.SetVolume(cur + diff)
		showVolumeMenu(ctx, tg, spk, chatID, msgID)

	case data == "mute:toggle":
		_, muted, _ := spk.GetVolume()
		_ = spk.SetMute(!muted)
		showVolumeMenu(ctx, tg, spk, chatID, msgID)

	case data == "siren:on":
		spk.StartSiren()
		_ = tg.AnswerCallbackAlert(ctx, cb.ID, "Сирена включена!")
		_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, "🚨 *Охранная сирена АКТИВНА 🔴*", telegram.ParseModeMarkdown,
			telegram.NewKeyboardBuilder().Row(telegram.NewButton("🛑 Выключить сирену", "siren:off"), telegram.NewButton("⬅️ Назад", "nav:main")).Build())

	case data == "siren:off":
		spk.StopSiren()
		_ = tg.AnswerCallbackAlert(ctx, cb.ID, "Сирена выключена")
		_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, "⚪ *Охранная сирена выключена*", telegram.ParseModeMarkdown,
			telegram.NewKeyboardBuilder().Row(telegram.NewButton("🚨 Включить сирену", "siren:on"), telegram.NewButton("⬅️ Назад", "nav:main")).Build())

	case data == "gesture:off":
		_ = spk.SetGesturesEnabled(false)
		_ = tg.AnswerCallbackAlert(ctx, cb.ID, "Жесты заблокированы")
		editToSystemMenu(ctx, tg, spk, chatID, msgID)

	case data == "gesture:on":
		_ = spk.SetGesturesEnabled(true)
		_ = tg.AnswerCallbackAlert(ctx, cb.ID, "Жесты включены")
		editToSystemMenu(ctx, tg, spk, chatID, msgID)

	case data == "sys:drop_caches":
		_ = spk.DropCaches()
		_ = tg.AnswerCallbackAlert(ctx, cb.ID, "Кэш RAM успешно очищен!")

	case data == "sys:reboot_confirm":
		markup := telegram.NewKeyboardBuilder().
			Row(telegram.NewButton("⚠️ Подтвердить перезагрузку", "sys:reboot_do")).
			Row(telegram.NewButton("Отмена", "nav:system")).
			Build()
		_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, "❓ Вы уверены, что хотите перезагрузить колонку?", telegram.ParseModeMarkdown, markup)

	case data == "sys:reboot_do":
		_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, "🔄 *Колонка перезагружается...*\nБот пришлет уведомление после старта.", telegram.ParseModeMarkdown, nil)
		_ = spk.Reboot()
	}
}

func sendMainMenu(ctx context.Context, tg *telegram.Client, chatID int64) {
	text := "🤖 *Яндекс Станция Mini (YNDX-0004)*\n" +
		"Статус: *В сети* 🟢\n\n" +
		"Выберите нужный раздел в меню ниже:"
	_, _ = tg.SendMarkdown(ctx, chatID, text, getMainMenuMarkup())
}

func editToMainMenu(ctx context.Context, tg *telegram.Client, chatID int64, msgID int64) {
	text := "🤖 *Яндекс Станция Mini (YNDX-0004)*\n" +
		"Статус: *В сети* 🟢\n\n" +
		"Выберите нужный раздел в меню ниже:"
	_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, getMainMenuMarkup())
}

func getMainMenuMarkup() *telegram.InlineKeyboardMarkup {
	return telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("📊 Статус & Fastfetch", "nav:status")).
		Row(telegram.NewButton("🎙️ Рация & Прослушка", "nav:intercom"), telegram.NewButton("📢 Саундборд", "nav:soundboard")).
		Row(telegram.NewButton("🔊 Озвучить текст (TTS)", "nav:tts"), telegram.NewButton("🎚️ Громкость", "nav:volume")).
		Row(telegram.NewButton("🚨 Сирена (Охрана)", "nav:alarm"), telegram.NewButton("💬 Диалоги Алисы", "nav:dialogs")).
		Row(telegram.NewButton("⚙️ Управление системой", "nav:system")).
		Build()
}

func sendFastfetchStatus(ctx context.Context, tg *telegram.Client, spk *speaker.Controller, chatID int64, editMsgID int64) {
	ff, _ := spk.GetFastfetch()
	wifi, _ := spk.GetWiFiStatus()

	text := fmt.Sprintf("```\n%s\n```\n%s", ff, wifi)
	markup := telegram.NewKeyboardBuilder().
		Row(telegram.NewButton("🔄 Обновить", "nav:status")).
		Row(telegram.NewButton("⬅️ Назад", "nav:main")).
		Build()

	if editMsgID > 0 {
		_, _ = tg.EditMessageTextQuick(ctx, chatID, editMsgID, text, telegram.ParseModeMarkdown, markup)
	} else {
		_, _ = tg.SendMarkdown(ctx, chatID, text, markup)
	}
}

func showVolumeMenu(ctx context.Context, tg *telegram.Client, spk *speaker.Controller, chatID int64, msgID int64) {
	vol, muted, _ := spk.GetVolume()
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

	_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
}

func editToSystemMenu(ctx context.Context, tg *telegram.Client, spk *speaker.Controller, chatID int64, msgID int64) {
	gestureStatus := "активны ✅"
	if !spk.AreGesturesEnabled() {
		gestureStatus = "заблокированы 🛑"
	}
	text := fmt.Sprintf("⚙️ *Управление системой*\n\n• Датчик жестов ToF: *%s*\n• Питание: онлайн\n\nВыберите действие:", gestureStatus)
	gestureBtn := telegram.NewButton("✋ Заблокировать жесты", "gesture:off")
	if !spk.AreGesturesEnabled() {
		gestureBtn = telegram.NewButton("✋ Включить жесты", "gesture:on")
	}
	markup := telegram.NewKeyboardBuilder().
		Row(gestureBtn).
		Row(telegram.NewButton("🧹 Очистить кэш RAM", "sys:drop_caches")).
		Row(telegram.NewButton("🔄 Перезагрузить колонку", "sys:reboot_confirm")).
		Row(telegram.NewButton("⬅️ Назад", "nav:main")).
		Build()
	_, _ = tg.EditMessageTextQuick(ctx, chatID, msgID, text, telegram.ParseModeMarkdown, markup)
}

func runListenCommand(ctx context.Context, tg *telegram.Client, spk *speaker.Controller, chatID int64, sec int) {
	go func() {
		_, _ = tg.SendTextMessage(ctx, chatID, fmt.Sprintf("👂 Запись аудио из комнаты (%d сек)...", sec))
		filePath, err := spk.RecordRoom(sec)
		if err != nil {
			_, _ = tg.SendTextMessage(ctx, chatID, fmt.Sprintf("❌ Ошибка записи: %v", err))
			return
		}
		_, err = tg.SendAudioFile(ctx, chatID, filePath, fmt.Sprintf("🎙️ Аудиозапись из комнаты (%d сек)", sec), fmt.Sprintf("Запись %d сек", sec), "Яндекс Станция Mini", nil)
		if err != nil {
			_, _ = tg.SendTextMessage(ctx, chatID, fmt.Sprintf("❌ Ошибка отправки аудио: %v", err))
		}
	}()
}

func handleVoiceIntercom(ctx context.Context, u *telegram.Update, tg *telegram.Client, spk *speaker.Controller) {
	chatID := u.ChatID()
	go func() {
		_, _ = tg.SendTextMessage(ctx, chatID, "🎙️ Голосовое сообщение принято! Озвучиваю в комнате...")

		// 1. Play chime alert
		_ = spk.PlaySound("telegram")
		time.Sleep(500 * time.Millisecond)

		// 2. Download incoming voice file from Telegram and play
		var fileID string
		if u.IsVoice() {
			fileID = u.Voice().FileID
		} else if u.IsAudio() {
			fileID = u.Audio().FileID
		}

		if fileID != "" {
			ogaPath := "/tmp/incoming_voice.oga"
			wavPath := "/tmp/incoming_voice.wav"
			_ = os.Remove(ogaPath)
			_ = os.Remove(wavPath)
			if downloadedPath, err := tg.DownloadFile(ctx, fileID, ogaPath); err == nil {
				if decErr := speaker.DecodeOggOpusToWav(downloadedPath, wavPath); decErr == nil {
					_ = spk.PlayWav(wavPath)
				} else {
					_ = spk.PlayWav(downloadedPath)
				}
			}
		}

		time.Sleep(1 * time.Second)

		// 3. Record response from room
		_, _ = tg.SendTextMessage(ctx, chatID, "👂 Записываю ответ из комнаты (5 сек)...")
		recFile, err := spk.RecordRoom(5)
		if err == nil {
			_, err = tg.SendAudioFile(ctx, chatID, recFile, "🎙️ Ответ из комнаты", "Ответ из комнаты", "Яндекс Станция Mini", nil)
			if err != nil {
				_, _ = tg.SendTextMessage(ctx, chatID, fmt.Sprintf("❌ Ошибка отправки ответа: %v", err))
			}
		}
	}()
}
