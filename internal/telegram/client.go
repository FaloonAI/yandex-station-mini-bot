package telegram

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL       = "https://api.telegram.org/bot"
	defaultPollTimeout   = 25 // seconds for getUpdates long polling
	defaultReqTimeout    = 30 * time.Second
	defaultUploadTimeout = 120 * time.Second
)

var (
	// ErrUnauthorized is returned when an action or update is attempted by a non-admin.
	ErrUnauthorized = errors.New("unauthorized: sender is not authorized admin")
	// ErrTelegramAPI is the base error for failed Telegram API responses.
	ErrTelegramAPI = errors.New("telegram api error")
	// ErrEmptyToken is returned when bot token is not supplied.
	ErrEmptyToken = errors.New("telegram bot token is required")
)

// APIError represents an error returned by Telegram Bot API.
type APIError struct {
	ErrorCode   int
	Description string
	Parameters  *ResponseParameters
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram api error [%d]: %s", e.ErrorCode, e.Description)
}

func (e *APIError) Is(target error) bool {
	return target == ErrTelegramAPI
}

// Client is a zero-dependency Telegram Bot API client.
type Client struct {
	token       string
	adminID     int64
	apiBaseURL  string
	httpClient  *http.Client
	pollTimeout int
}

// ClientOption defines a functional option for configuring Client.
type ClientOption func(*Client)

// WithHTTPClient configures a custom *http.Client.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// WithBaseURL overrides the default Telegram API base URL (useful for testing or reverse proxies).
func WithBaseURL(url string) ClientOption {
	return func(c *Client) {
		trimmed := strings.TrimRight(url, "/")
		if !strings.HasSuffix(trimmed, "/bot") {
			trimmed += "/bot"
		}
		c.apiBaseURL = trimmed
	}
}

// WithPollTimeout sets the long polling timeout in seconds (default is 25).
func WithPollTimeout(seconds int) ClientOption {
	return func(c *Client) {
		if seconds > 0 {
			c.pollTimeout = seconds
		}
	}
}

// NewClient initializes a new Telegram Bot API client.
func NewClient(token string, adminID int64, opts ...ClientOption) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrEmptyToken
	}

	c := &Client{
		token:      strings.TrimSpace(token),
		adminID:    adminID,
		apiBaseURL: defaultBaseURL,
		httpClient: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
		pollTimeout: defaultPollTimeout,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c, nil
}

// AdminID returns the configured AdminID.
func (c *Client) AdminID() int64 {
	return c.adminID
}

// IsAdmin checks if the given user ID matches the authorized AdminID.
func (c *Client) IsAdmin(userID int64) bool {
	return c.adminID != 0 && userID == c.adminID
}

// IsAuthorized verifies if the incoming update originates from the authorized AdminID.
func (c *Client) IsAuthorized(u *Update) bool {
	if u == nil {
		return false
	}
	return c.IsAdmin(u.SenderID())
}

// CheckAuthorized returns nil if the update is from AdminID, or ErrUnauthorized.
func (c *Client) CheckAuthorized(u *Update) error {
	if !c.IsAuthorized(u) {
		senderID := int64(0)
		if u != nil {
			senderID = u.SenderID()
		}
		return fmt.Errorf("%w: user %d is not admin %d", ErrUnauthorized, senderID, c.adminID)
	}
	return nil
}

// ParseUpdate decodes a raw JSON update payload.
func (c *Client) ParseUpdate(data []byte) (*Update, error) {
	var update Update
	if err := json.Unmarshal(data, &update); err != nil {
		return nil, fmt.Errorf("failed to unmarshal update: %w", err)
	}
	return &update, nil
}

// endpoint builds the full URL for a Telegram API method.
func (c *Client) endpoint(method string) string {
	return fmt.Sprintf("%s%s/%s", c.apiBaseURL, c.token, method)
}

// doJSON executes a POST request with JSON body and unmarshals the generic APIResponse.
func doJSON[T any](ctx context.Context, c *Client, method string, payload any, fallbackTimeout time.Duration) (T, error) {
	var zero T

	reqCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && fallbackTimeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, fallbackTimeout)
		defer cancel()
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return zero, fmt.Errorf("failed to marshal payload for %s: %w", method, err)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint(method), bytes.NewReader(jsonBytes))
	if err != nil {
		return zero, fmt.Errorf("failed to create request for %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return zero, fmt.Errorf("http request %s failed: %w", method, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, fmt.Errorf("failed to read response body for %s: %w", method, err)
	}

	var apiResp APIResponse[T]
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		return zero, fmt.Errorf("failed to parse json response for %s (status %d): %w", method, resp.StatusCode, err)
	}

	if !apiResp.Ok {
		return zero, &APIError{
			ErrorCode:   apiResp.ErrorCode,
			Description: apiResp.Description,
			Parameters:  apiResp.Parameters,
		}
	}

	return apiResp.Result, nil
}

// GetUpdates fetches updates via long polling.
func (c *Client) GetUpdates(ctx context.Context, req GetUpdatesRequest) ([]Update, error) {
	if req.Timeout <= 0 {
		req.Timeout = c.pollTimeout
	}
	pollHTTPTimeout := time.Duration(req.Timeout+15) * time.Second
	return doJSON[[]Update](ctx, c, "getUpdates", req, pollHTTPTimeout)
}

// StartPolling runs a resilient long polling loop that dispatches updates to handler.
// Non-admin updates are strictly ignored and dropped to protect the bot.
func (c *Client) StartPolling(ctx context.Context, handler func(ctx context.Context, update Update) error) error {
	var offset int64 = 0
	backoff := 1 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req := GetUpdatesRequest{
			Offset:         offset,
			Timeout:        c.pollTimeout,
			AllowedUpdates: []string{"message", "edited_message", "callback_query"},
		}

		updates, err := c.GetUpdates(ctx, req)
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ctx.Err()
			}

			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.Parameters != nil && apiErr.Parameters.RetryAfter > 0 {
				sleepDuration := time.Duration(apiErr.Parameters.RetryAfter) * time.Second
				log.Printf("[telegram] rate limited, backing off for %v", sleepDuration)
				time.Sleep(sleepDuration)
				continue
			}

			log.Printf("[telegram] getUpdates failed: %v, retrying in %v...", err, backoff)
			select {
			case <-time.After(backoff):
				backoff *= 2
				if backoff > 20*time.Second {
					backoff = 20 * time.Second
				}
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// Reset backoff upon successful request
		backoff = 1 * time.Second

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}

			// Strict Admin Security Check
			if !c.IsAuthorized(&update) {
				log.Printf("[telegram] dropped unauthorized update %d from user %d (@%s)",
					update.UpdateID, update.SenderID(), update.SenderUsername())
				continue
			}

			if handler != nil {
				if err := handler(ctx, update); err != nil {
					log.Printf("[telegram] update %d handler error: %v", update.UpdateID, err)
				}
			}
		}
	}
}

// SendMessage sends a text message with full options.
func (c *Client) SendMessage(ctx context.Context, req SendMessageRequest) (*Message, error) {
	return doJSON[*Message](ctx, c, "sendMessage", req, defaultReqTimeout)
}

// SendTextMessage sends a plain text message to a chat.
func (c *Client) SendTextMessage(ctx context.Context, chatID int64, text string) (*Message, error) {
	return c.SendMessage(ctx, SendMessageRequest{
		ChatID: chatID,
		Text:   text,
	})
}

// SendMarkdown sends a markdown formatted message.
func (c *Client) SendMarkdown(ctx context.Context, chatID int64, text string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	return c.SendMessage(ctx, SendMessageRequest{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   ParseModeMarkdown,
		ReplyMarkup: replyMarkup,
	})
}

// SendHTML sends an HTML formatted message.
func (c *Client) SendHTML(ctx context.Context, chatID int64, text string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	return c.SendMessage(ctx, SendMessageRequest{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   ParseModeHTML,
		ReplyMarkup: replyMarkup,
	})
}

// EditMessageText edits the text of a message previously sent by the bot.
func (c *Client) EditMessageText(ctx context.Context, req EditMessageTextRequest) (*Message, error) {
	return doJSON[*Message](ctx, c, "editMessageText", req, defaultReqTimeout)
}

// EditMessageTextQuick is a convenience wrapper around EditMessageText.
func (c *Client) EditMessageTextQuick(ctx context.Context, chatID int64, messageID int64, text string, parseMode ParseMode, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	return c.EditMessageText(ctx, EditMessageTextRequest{
		ChatID:      chatID,
		MessageID:   messageID,
		Text:        text,
		ParseMode:   parseMode,
		ReplyMarkup: replyMarkup,
	})
}

// AnswerCallbackQuery answers an inline keyboard callback query.
func (c *Client) AnswerCallbackQuery(ctx context.Context, req AnswerCallbackQueryRequest) error {
	_, err := doJSON[bool](ctx, c, "answerCallbackQuery", req, defaultReqTimeout)
	return err
}

// AnswerCallback acknowledges a callback query silently.
func (c *Client) AnswerCallback(ctx context.Context, callbackQueryID string) error {
	return c.AnswerCallbackQuery(ctx, AnswerCallbackQueryRequest{
		CallbackQueryID: callbackQueryID,
	})
}

// AnswerCallbackAlert acknowledges a callback query displaying an alert dialog to user.
func (c *Client) AnswerCallbackAlert(ctx context.Context, callbackQueryID string, alertText string) error {
	return c.AnswerCallbackQuery(ctx, AnswerCallbackQueryRequest{
		CallbackQueryID: callbackQueryID,
		Text:            alertText,
		ShowAlert:       true,
	})
}

// DeleteMessage deletes a message in a chat.
func (c *Client) DeleteMessage(ctx context.Context, chatID int64, messageID int64) error {
	_, err := doJSON[bool](ctx, c, "deleteMessage", DeleteMessageRequest{
		ChatID:    chatID,
		MessageID: messageID,
	}, defaultReqTimeout)
	return err
}

// DeleteAfter schedules automatic deletion of one or more messages after duration d.
func (c *Client) DeleteAfter(d time.Duration, chatID int64, messageIDs ...int64) {
	if len(messageIDs) == 0 || d <= 0 {
		return
	}
	go func() {
		time.Sleep(d)
		ctx, cancel := context.WithTimeout(context.Background(), defaultReqTimeout)
		defer cancel()
		for _, msgID := range messageIDs {
			if msgID > 0 {
				_ = c.DeleteMessage(ctx, chatID, msgID)
			}
		}
	}()
}

// postMultipart uploads a file along with text fields using multipart/form-data.
func (c *Client) postMultipart(ctx context.Context, method string, fields map[string]string, fileFieldName string, fileName string, fileReader io.Reader) (*Message, error) {
	reqCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		reqCtx, cancel = context.WithTimeout(ctx, defaultUploadTimeout)
		defer cancel()
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	for key, val := range fields {
		if val != "" {
			if err := writer.WriteField(key, val); err != nil {
				return nil, fmt.Errorf("failed to write multipart field %s: %w", key, err)
			}
		}
	}

	part, err := writer.CreateFormFile(fileFieldName, fileName)
	if err != nil {
		return nil, fmt.Errorf("failed to create multipart form file %s: %w", fileFieldName, err)
	}

	if _, err := io.Copy(part, fileReader); err != nil {
		return nil, fmt.Errorf("failed to copy file contents for %s: %w", fileName, err)
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint(method), body)
	if err != nil {
		return nil, fmt.Errorf("failed to create multipart request for %s: %w", method, err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("multipart request %s failed: %w", method, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body for %s: %w", method, err)
	}

	var apiResp APIResponse[*Message]
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse json response for %s: %w", method, err)
	}

	if !apiResp.Ok {
		return nil, &APIError{
			ErrorCode:   apiResp.ErrorCode,
			Description: apiResp.Description,
			Parameters:  apiResp.Parameters,
		}
	}

	return apiResp.Result, nil
}

// SendVoice uploads and sends a voice audio message via multipart/form-data.
func (c *Client) SendVoice(ctx context.Context, req SendVoiceRequest, fileName string, reader io.Reader) (*Message, error) {
	if fileName == "" {
		fileName = "voice.ogg"
	}

	fields := map[string]string{
		"chat_id": strconv.FormatInt(req.ChatID, 10),
		"caption": req.Caption,
	}
	if req.ParseMode != "" {
		fields["parse_mode"] = string(req.ParseMode)
	}
	if req.Duration > 0 {
		fields["duration"] = strconv.Itoa(req.Duration)
	}
	if req.ReplyMarkup != nil {
		markupBytes, err := json.Marshal(req.ReplyMarkup)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal reply_markup: %w", err)
		}
		fields["reply_markup"] = string(markupBytes)
	}

	return c.postMultipart(ctx, "sendVoice", fields, "voice", fileName, reader)
}

// SendVoiceFile sends a voice note from a local file path.
func (c *Client) SendVoiceFile(ctx context.Context, chatID int64, filePath string, caption string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open voice file: %w", err)
	}
	defer file.Close()

	fileName := filepath.Base(filePath)
	req := SendVoiceRequest{
		ChatID:      chatID,
		Caption:     caption,
		ReplyMarkup: replyMarkup,
	}
	return c.SendVoice(ctx, req, fileName, file)
}

// SendVoiceBytes sends a voice note from in-memory bytes.
func (c *Client) SendVoiceBytes(ctx context.Context, chatID int64, fileName string, data []byte, caption string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	req := SendVoiceRequest{
		ChatID:      chatID,
		Caption:     caption,
		ReplyMarkup: replyMarkup,
	}
	return c.SendVoice(ctx, req, fileName, bytes.NewReader(data))
}

// SendAudio uploads and sends an audio track via multipart/form-data.
func (c *Client) SendAudio(ctx context.Context, req SendAudioRequest, fileName string, reader io.Reader) (*Message, error) {
	if fileName == "" {
		fileName = "audio.mp3"
	}

	fields := map[string]string{
		"chat_id":   strconv.FormatInt(req.ChatID, 10),
		"caption":   req.Caption,
		"title":     req.Title,
		"performer": req.Performer,
	}
	if req.ParseMode != "" {
		fields["parse_mode"] = string(req.ParseMode)
	}
	if req.Duration > 0 {
		fields["duration"] = strconv.Itoa(req.Duration)
	}
	if req.ReplyMarkup != nil {
		markupBytes, err := json.Marshal(req.ReplyMarkup)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal reply_markup: %w", err)
		}
		fields["reply_markup"] = string(markupBytes)
	}

	return c.postMultipart(ctx, "sendAudio", fields, "audio", fileName, reader)
}

// SendAudioFile sends an audio file from a local file path.
func (c *Client) SendAudioFile(ctx context.Context, chatID int64, filePath string, caption string, title string, performer string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open audio file: %w", err)
	}
	defer file.Close()

	fileName := filepath.Base(filePath)
	req := SendAudioRequest{
		ChatID:      chatID,
		Caption:     caption,
		Title:       title,
		Performer:   performer,
		ReplyMarkup: replyMarkup,
	}
	return c.SendAudio(ctx, req, fileName, file)
}

// SendAudioBytes sends an audio file from in-memory bytes.
func (c *Client) SendAudioBytes(ctx context.Context, chatID int64, fileName string, data []byte, caption string, title string, performer string, replyMarkup *InlineKeyboardMarkup) (*Message, error) {
	req := SendAudioRequest{
		ChatID:      chatID,
		Caption:     caption,
		Title:       title,
		Performer:   performer,
		ReplyMarkup: replyMarkup,
	}
	return c.SendAudio(ctx, req, fileName, bytes.NewReader(data))
}

// File represents a Telegram file metadata object.
type File struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

// GetFile retrieves basic file information from Telegram Bot API.
func (c *Client) GetFile(ctx context.Context, fileID string) (*File, error) {
	return doJSON[*File](ctx, c, "getFile", map[string]string{"file_id": fileID}, defaultReqTimeout)
}

// DownloadFile downloads a file from Telegram by file_id to a destination on local disk.
func (c *Client) DownloadFile(ctx context.Context, fileID, destPath string) (string, error) {
	fileInfo, err := c.GetFile(ctx, fileID)
	if err != nil {
		return "", fmt.Errorf("getFile failed: %w", err)
	}

	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", c.token, fileInfo.FilePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create download request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("file download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create dest file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("failed to save file stream: %w", err)
	}

	return destPath, nil
}
