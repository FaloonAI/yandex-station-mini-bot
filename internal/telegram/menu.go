package telegram

import (
	"fmt"
	"math"
)

// Standard callback data constants for Yandex Station Mini bot navigation and actions.
const (
	CallbackMenuMain   = "menu:main"
	CallbackMenuSounds = "menu:sounds"
	CallbackMenuVolume = "menu:volume"
	CallbackMenuRecord = "menu:record"
	CallbackMenuSystem = "menu:system"

	CallbackSoundPlayPrefix      = "snd:play:"
	CallbackVolumeSetPrefix      = "vol:set:"
	CallbackVolumeStepPrefix     = "vol:step:"
	CallbackRecordDurationPrefix = "rec:sec:"
	CallbackSystemActionPrefix   = "sys:act:"

	ActionStopPlayback  = "stop_sound"
	ActionStatus        = "status"
	ActionQuasarLogs    = "quasar_logs"
	ActionBotLogs       = "bot_logs"
	ActionRestartQuasar = "restart_quasar"
)

// NewButton creates an InlineKeyboardButton that sends a callback query.
func NewButton(text string, callbackData string) InlineKeyboardButton {
	return InlineKeyboardButton{
		Text:         text,
		CallbackData: callbackData,
	}
}

// NewURLButton creates an InlineKeyboardButton that opens a URL.
func NewURLButton(text string, url string) InlineKeyboardButton {
	return InlineKeyboardButton{
		Text: text,
		URL:  url,
	}
}

// NewRow returns a slice of buttons representing a single keyboard row.
func NewRow(buttons ...InlineKeyboardButton) []InlineKeyboardButton {
	return buttons
}

// NewKeyboard constructs an InlineKeyboardMarkup from rows of buttons.
func NewKeyboard(rows ...[]InlineKeyboardButton) *InlineKeyboardMarkup {
	var cleanRows [][]InlineKeyboardButton
	for _, row := range rows {
		if len(row) > 0 {
			cleanRows = append(cleanRows, row)
		}
	}
	return &InlineKeyboardMarkup{
		InlineKeyboard: cleanRows,
	}
}

// NewVerticalKeyboard creates a keyboard with 1 button per row.
func NewVerticalKeyboard(buttons ...InlineKeyboardButton) *InlineKeyboardMarkup {
	rows := make([][]InlineKeyboardButton, 0, len(buttons))
	for _, btn := range buttons {
		rows = append(rows, []InlineKeyboardButton{btn})
	}
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

// NewGridKeyboard arranges buttons into rows with a maximum number of columns per row.
func NewGridKeyboard(columns int, buttons ...InlineKeyboardButton) *InlineKeyboardMarkup {
	if columns <= 0 {
		columns = 1
	}

	var rows [][]InlineKeyboardButton
	var currentRow []InlineKeyboardButton

	for _, btn := range buttons {
		currentRow = append(currentRow, btn)
		if len(currentRow) == columns {
			rows = append(rows, currentRow)
			currentRow = nil
		}
	}

	if len(currentRow) > 0 {
		rows = append(rows, currentRow)
	}

	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

// KeyboardBuilder offers a fluent API to build InlineKeyboardMarkup.
type KeyboardBuilder struct {
	rows       [][]InlineKeyboardButton
	currentRow []InlineKeyboardButton
}

// NewKeyboardBuilder initializes an empty KeyboardBuilder.
func NewKeyboardBuilder() *KeyboardBuilder {
	return &KeyboardBuilder{
		rows: make([][]InlineKeyboardButton, 0),
	}
}

// Row adds an entire row of buttons to the builder.
func (b *KeyboardBuilder) Row(buttons ...InlineKeyboardButton) *KeyboardBuilder {
	if len(b.currentRow) > 0 {
		b.rows = append(b.rows, b.currentRow)
		b.currentRow = nil
	}
	if len(buttons) > 0 {
		b.rows = append(b.rows, buttons)
	}
	return b
}

// Button appends a callback button to the current row.
func (b *KeyboardBuilder) Button(text string, callbackData string) *KeyboardBuilder {
	b.currentRow = append(b.currentRow, NewButton(text, callbackData))
	return b
}

// URLButton appends a URL link button to the current row.
func (b *KeyboardBuilder) URLButton(text string, url string) *KeyboardBuilder {
	b.currentRow = append(b.currentRow, NewURLButton(text, url))
	return b
}

// NewRow closes the current row and starts a new one.
func (b *KeyboardBuilder) NewRow() *KeyboardBuilder {
	if len(b.currentRow) > 0 {
		b.rows = append(b.rows, b.currentRow)
		b.currentRow = nil
	}
	return b
}

// Build compiles and returns the InlineKeyboardMarkup.
func (b *KeyboardBuilder) Build() *InlineKeyboardMarkup {
	if len(b.currentRow) > 0 {
		b.rows = append(b.rows, b.currentRow)
		b.currentRow = nil
	}
	return &InlineKeyboardMarkup{
		InlineKeyboard: b.rows,
	}
}

// MainMenuKeyboard creates the top-level control menu for Yandex Station Mini.
func MainMenuKeyboard() *InlineKeyboardMarkup {
	return NewKeyboard(
		NewRow(
			NewButton("🔊 Звуки", CallbackMenuSounds),
			NewButton("🎚️ Громкость", CallbackMenuVolume),
		),
		NewRow(
			NewButton("🎙️ Микрофон", CallbackMenuRecord),
			NewButton("⚙️ Система", CallbackMenuSystem),
		),
		NewRow(
			NewButton("⏹️ Стоп звук", CallbackSystemActionPrefix+ActionStopPlayback),
		),
	)
}

// BackToMainMenuButton returns a single row containing a "Back to Main Menu" button.
func BackToMainMenuButton() []InlineKeyboardButton {
	return NewRow(NewButton("🏠 Главное меню", CallbackMenuMain))
}

// SoundboardKeyboard creates a paginated grid keyboard for sound files.
func SoundboardKeyboard(sounds []string, page int, pageSize int) *InlineKeyboardMarkup {
	if pageSize <= 0 {
		pageSize = 6
	}

	total := len(sounds)
	totalPages := int(math.Ceil(float64(total) / float64(pageSize)))
	if totalPages == 0 {
		totalPages = 1
	}

	if page < 0 {
		page = 0
	} else if page >= totalPages {
		page = totalPages - 1
	}

	start := page * pageSize
	end := start + pageSize
	if end > total {
		end = total
	}

	builder := NewKeyboardBuilder()

	// 2 buttons per row for sound buttons
	var rowButtons []InlineKeyboardButton
	for i := start; i < end; i++ {
		name := sounds[i]
		displayName := "▶ " + name
		btn := NewButton(displayName, CallbackSoundPlayPrefix+name)
		rowButtons = append(rowButtons, btn)
		if len(rowButtons) == 2 {
			builder.Row(rowButtons...)
			rowButtons = nil
		}
	}
	if len(rowButtons) > 0 {
		builder.Row(rowButtons...)
	}

	// Pagination row if multiple pages exist
	if totalPages > 1 {
		var navRow []InlineKeyboardButton
		if page > 0 {
			navRow = append(navRow, NewButton("⬅️ Пред", fmt.Sprintf("%s:%d", CallbackMenuSounds, page-1)))
		} else {
			navRow = append(navRow, NewButton("·", "noop"))
		}

		navRow = append(navRow, NewButton(fmt.Sprintf("%d/%d", page+1, totalPages), "noop"))

		if page < totalPages-1 {
			navRow = append(navRow, NewButton("➡️ След", fmt.Sprintf("%s:%d", CallbackMenuSounds, page+1)))
		} else {
			navRow = append(navRow, NewButton("·", "noop"))
		}
		builder.Row(navRow...)
	}

	builder.Row(
		NewButton("⏹️ Стоп", CallbackSystemActionPrefix+ActionStopPlayback),
		NewButton("🏠 Меню", CallbackMenuMain),
	)

	return builder.Build()
}

// VolumeKeyboard creates volume control buttons with step and preset options.
func VolumeKeyboard(currentVol int) *InlineKeyboardMarkup {
	titleText := "Громкость"
	if currentVol >= 0 {
		titleText = fmt.Sprintf("Текущая: %d%%", currentVol)
	}

	return NewKeyboard(
		NewRow(
			NewButton("🔉 -10%", CallbackVolumeStepPrefix+"-10"),
			NewButton(titleText, "noop"),
			NewButton("🔊 +10%", CallbackVolumeStepPrefix+"+10"),
		),
		NewRow(
			NewButton("🔇 0%", CallbackVolumeSetPrefix+"0"),
			NewButton("25%", CallbackVolumeSetPrefix+"25"),
			NewButton("50%", CallbackVolumeSetPrefix+"50"),
			NewButton("75%", CallbackVolumeSetPrefix+"75"),
			NewButton("100%", CallbackVolumeSetPrefix+"100"),
		),
		BackToMainMenuButton(),
	)
}

// RecordKeyboard creates options for microphone recording duration.
func RecordKeyboard() *InlineKeyboardMarkup {
	return NewKeyboard(
		NewRow(
			NewButton("🎙️ 5 сек", CallbackRecordDurationPrefix+"5"),
			NewButton("🎙️ 10 сек", CallbackRecordDurationPrefix+"10"),
		),
		NewRow(
			NewButton("🎙️ 30 сек", CallbackRecordDurationPrefix+"30"),
			NewButton("🎙️ 60 сек", CallbackRecordDurationPrefix+"60"),
		),
		BackToMainMenuButton(),
	)
}

// SystemKeyboard creates station diagnostic and control options.
func SystemKeyboard() *InlineKeyboardMarkup {
	return NewKeyboard(
		NewRow(
			NewButton("📊 Статус", CallbackSystemActionPrefix+ActionStatus),
			NewButton("⏹️ Стоп звук", CallbackSystemActionPrefix+ActionStopPlayback),
		),
		NewRow(
			NewButton("📋 Логи бота", CallbackSystemActionPrefix+ActionBotLogs),
			NewButton("📋 Логи Quasar", CallbackSystemActionPrefix+ActionQuasarLogs),
		),
		NewRow(
			NewButton("🔄 Перезапуск Quasar", CallbackSystemActionPrefix+ActionRestartQuasar),
		),
		BackToMainMenuButton(),
	)
}
