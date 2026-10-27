package telegram

import (
	"strings"
)

// ParseMode specifies the message formatting parse mode.
type ParseMode string

const (
	ParseModeNone       ParseMode = ""
	ParseModeMarkdown   ParseMode = "Markdown"
	ParseModeMarkdownV2 ParseMode = "MarkdownV2"
	ParseModeHTML       ParseMode = "HTML"
)

// APIResponse represents the raw JSON response from the Telegram Bot API.
type APIResponse[T any] struct {
	Ok          bool                `json:"ok"`
	Result      T                   `json:"result,omitempty"`
	Description string              `json:"description,omitempty"`
	ErrorCode   int                 `json:"error_code,omitempty"`
	Parameters  *ResponseParameters `json:"parameters,omitempty"`
}

// ResponseParameters contains information about why a request was unsuccessful.
type ResponseParameters struct {
	MigrateToChatID int64 `json:"migrate_to_chat_id,omitempty"`
	RetryAfter      int   `json:"retry_after,omitempty"`
}

// User represents a Telegram user or bot.
type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

// FullName returns the user's first and last name.
func (u *User) FullName() string {
	if u == nil {
		return ""
	}
	if u.LastName != "" {
		return u.FirstName + " " + u.LastName
	}
	return u.FirstName
}

// Chat represents a Telegram chat.
type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"` // "private", "group", "supergroup", "channel"
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

// Voice represents a voice note.
type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"` // in seconds
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Audio represents an audio file to be treated as music by Telegram.
type Audio struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"` // in seconds
	Performer    string `json:"performer,omitempty"`
	Title        string `json:"title,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Document represents a general file sent in Telegram.
type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// InlineKeyboardButton represents one button of an inline keyboard.
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// InlineKeyboardMarkup represents an inline keyboard that appears inside the message.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// Message represents a Telegram message.
type Message struct {
	MessageID   int64                 `json:"message_id"`
	From        *User                 `json:"from,omitempty"`
	Date        int64                 `json:"date"`
	Chat        *Chat                 `json:"chat"`
	Text        string                `json:"text,omitempty"`
	Caption     string                `json:"caption,omitempty"`
	Voice       *Voice                `json:"voice,omitempty"`
	Audio       *Audio                `json:"audio,omitempty"`
	Document    *Document             `json:"document,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// IsVoice returns true if the message has a voice note.
func (m *Message) IsVoice() bool {
	return m != nil && m.Voice != nil
}

// IsAudio returns true if the message contains an audio file.
func (m *Message) IsAudio() bool {
	return m != nil && (m.Audio != nil || (m.Document != nil && (strings.HasPrefix(m.Document.MimeType, "audio/") || strings.HasSuffix(strings.ToLower(m.Document.FileName), ".wav") || strings.HasSuffix(strings.ToLower(m.Document.FileName), ".mp3") || strings.HasSuffix(strings.ToLower(m.Document.FileName), ".ogg"))))
}

// AudioFileID returns the Telegram file ID and suggested file name.
func (m *Message) AudioFileID() (string, string) {
	if m == nil {
		return "", ""
	}
	if m.Voice != nil {
		return m.Voice.FileID, "voice.oga"
	}
	if m.Audio != nil {
		name := m.Audio.FileName
		if name == "" {
			name = "audio.mp3"
		}
		return m.Audio.FileID, name
	}
	if m.Document != nil {
		name := m.Document.FileName
		if name == "" {
			name = "document.audio"
		}
		return m.Document.FileID, name
	}
	return "", ""
}

// IsCommand checks if the message starts with a bot slash command.
func (m *Message) IsCommand() bool {
	if m == nil {
		return false
	}
	text := strings.TrimSpace(m.Text)
	return strings.HasPrefix(text, "/")
}

// Command extracts the command name (without leading slash and bot username) and argument string.
func (m *Message) Command() (string, string) {
	if m == nil {
		return "", ""
	}
	text := strings.TrimSpace(m.Text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}

	parts := strings.SplitN(text[1:], " ", 2)
	cmd := parts[0]
	if atIdx := strings.Index(cmd, "@"); atIdx != -1 {
		cmd = cmd[:atIdx]
	}

	var args string
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return cmd, args
}

// CallbackQuery represents an incoming callback query from a button on an inline keyboard.
type CallbackQuery struct {
	ID              string   `json:"id"`
	From            *User    `json:"from"`
	Message         *Message `json:"message,omitempty"`
	InlineMessageID string   `json:"inline_message_id,omitempty"`
	Data            string   `json:"data,omitempty"`
}

// Update represents an incoming update from Telegram long polling.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	EditedMessage *Message       `json:"edited_message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Sender returns the User who generated this update.
func (u *Update) Sender() *User {
	if u.Message != nil && u.Message.From != nil {
		return u.Message.From
	}
	if u.CallbackQuery != nil && u.CallbackQuery.From != nil {
		return u.CallbackQuery.From
	}
	if u.EditedMessage != nil && u.EditedMessage.From != nil {
		return u.EditedMessage.From
	}
	return nil
}

// SenderID returns the Telegram User ID of the sender or 0 if absent.
func (u *Update) SenderID() int64 {
	if s := u.Sender(); s != nil {
		return s.ID
	}
	return 0
}

// SenderUsername returns the username of the sender or empty string.
func (u *Update) SenderUsername() string {
	if s := u.Sender(); s != nil {
		return s.Username
	}
	return ""
}

// ChatID returns the Telegram Chat ID associated with this update.
func (u *Update) ChatID() int64 {
	if u.Message != nil && u.Message.Chat != nil {
		return u.Message.Chat.ID
	}
	if u.CallbackQuery != nil && u.CallbackQuery.Message != nil && u.CallbackQuery.Message.Chat != nil {
		return u.CallbackQuery.Message.Chat.ID
	}
	if u.EditedMessage != nil && u.EditedMessage.Chat != nil {
		return u.EditedMessage.Chat.ID
	}
	return 0
}

// Text returns the message text, caption, or callback query data depending on update type.
func (u *Update) Text() string {
	if u.Message != nil {
		if u.Message.Text != "" {
			return u.Message.Text
		}
		return u.Message.Caption
	}
	if u.CallbackQuery != nil {
		return u.CallbackQuery.Data
	}
	if u.EditedMessage != nil {
		if u.EditedMessage.Text != "" {
			return u.EditedMessage.Text
		}
		return u.EditedMessage.Caption
	}
	return ""
}

// IsMessage returns true if the update is an incoming message.
func (u *Update) IsMessage() bool {
	return u.Message != nil
}

// IsCallback returns true if the update is an inline keyboard callback query.
func (u *Update) IsCallback() bool {
	return u.CallbackQuery != nil
}

// CallbackID returns the callback query ID if present.
func (u *Update) CallbackID() string {
	if u.CallbackQuery != nil {
		return u.CallbackQuery.ID
	}
	return ""
}

// CallbackData returns the callback query data string if present.
func (u *Update) CallbackData() string {
	if u.CallbackQuery != nil {
		return u.CallbackQuery.Data
	}
	return ""
}

// IsVoice returns true if the update contains a voice note.
func (u *Update) IsVoice() bool {
	return (u.Message != nil && u.Message.Voice != nil) ||
		(u.EditedMessage != nil && u.EditedMessage.Voice != nil)
}

// Voice returns the Voice object if present in the message.
func (u *Update) Voice() *Voice {
	if u.Message != nil && u.Message.Voice != nil {
		return u.Message.Voice
	}
	if u.EditedMessage != nil && u.EditedMessage.Voice != nil {
		return u.EditedMessage.Voice
	}
	return nil
}

// IsAudio returns true if the update contains an audio track.
func (u *Update) IsAudio() bool {
	return (u.Message != nil && u.Message.Audio != nil) ||
		(u.EditedMessage != nil && u.EditedMessage.Audio != nil)
}

// Audio returns the Audio object if present in the message.
func (u *Update) Audio() *Audio {
	if u.Message != nil && u.Message.Audio != nil {
		return u.Message.Audio
	}
	if u.EditedMessage != nil && u.EditedMessage.Audio != nil {
		return u.EditedMessage.Audio
	}
	return nil
}

// IsCommand returns true if the update is a message starting with a slash command.
func (u *Update) IsCommand() bool {
	if u.Message != nil {
		return u.Message.IsCommand()
	}
	return false
}

// Command extracts the command name and arguments from the message.
func (u *Update) Command() (string, string) {
	if u.Message != nil {
		return u.Message.Command()
	}
	return "", ""
}

// SendMessageRequest contains options for sending text messages.
type SendMessageRequest struct {
	ChatID                int64                 `json:"chat_id"`
	Text                  string                `json:"text"`
	ParseMode             ParseMode             `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool                  `json:"disable_web_page_preview,omitempty"`
	ReplyMarkup           *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// EditMessageTextRequest contains options for editing message text.
type EditMessageTextRequest struct {
	ChatID                int64                 `json:"chat_id,omitempty"`
	MessageID             int64                 `json:"message_id,omitempty"`
	InlineMessageID       string                `json:"inline_message_id,omitempty"`
	Text                  string                `json:"text"`
	ParseMode             ParseMode             `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool                  `json:"disable_web_page_preview,omitempty"`
	ReplyMarkup           *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// AnswerCallbackQueryRequest contains options for answering callback queries.
type AnswerCallbackQueryRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
	URL             string `json:"url,omitempty"`
	CacheTime       int    `json:"cache_time,omitempty"`
}

// SendVoiceRequest contains options for sending voice notes.
type SendVoiceRequest struct {
	ChatID      int64                 `json:"chat_id"`
	Caption     string                `json:"caption,omitempty"`
	ParseMode   ParseMode             `json:"parse_mode,omitempty"`
	Duration    int                   `json:"duration,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// SendAudioRequest contains options for sending audio files.
type SendAudioRequest struct {
	ChatID      int64                 `json:"chat_id"`
	Caption     string                `json:"caption,omitempty"`
	ParseMode   ParseMode             `json:"parse_mode,omitempty"`
	Duration    int                   `json:"duration,omitempty"`
	Performer   string                `json:"performer,omitempty"`
	Title       string                `json:"title,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// GetUpdatesRequest contains parameters for getUpdates.
type GetUpdatesRequest struct {
	Offset         int64    `json:"offset,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

// DeleteMessageRequest contains parameters for deleteMessage.
type DeleteMessageRequest struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
}
