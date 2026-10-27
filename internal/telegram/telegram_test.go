package telegram

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUpdateParsingAndAuth(t *testing.T) {
	const adminID = int64(12345678)
	const strangerID = int64(99999999)

	client, err := NewClient("test-token", adminID)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// 1. Message from admin with command
	adminMsgJSON := `{
		"update_id": 1,
		"message": {
			"message_id": 100,
			"date": 1600000000,
			"chat": {"id": 12345678, "type": "private"},
			"from": {"id": 12345678, "is_bot": false, "first_name": "Admin", "username": "admin_user"},
			"text": "/play airhorn"
		}
	}`

	u, err := client.ParseUpdate([]byte(adminMsgJSON))
	if err != nil {
		t.Fatalf("failed to parse update: %v", err)
	}

	if !client.IsAuthorized(u) {
		t.Errorf("expected update to be authorized for admin")
	}
	if u.SenderID() != adminID {
		t.Errorf("expected sender ID %d, got %d", adminID, u.SenderID())
	}
	if u.ChatID() != adminID {
		t.Errorf("expected chat ID %d, got %d", adminID, u.ChatID())
	}
	if !u.IsCommand() {
		t.Errorf("expected message to be recognized as command")
	}

	cmd, args := u.Command()
	if cmd != "play" || args != "airhorn" {
		t.Errorf("expected cmd 'play' and args 'airhorn', got '%s' '%s'", cmd, args)
	}

	// 2. Callback query from stranger
	strangerCallbackJSON := `{
		"update_id": 2,
		"callback_query": {
			"id": "cb_123",
			"from": {"id": 99999999, "is_bot": false, "first_name": "Stranger", "username": "stranger_user"},
			"message": {
				"message_id": 200,
				"date": 1600000001,
				"chat": {"id": 99999999, "type": "private"}
			},
			"data": "snd:play:airhorn"
		}
	}`

	u2, err := client.ParseUpdate([]byte(strangerCallbackJSON))
	if err != nil {
		t.Fatalf("failed to parse update: %v", err)
	}

	if client.IsAuthorized(u2) {
		t.Errorf("expected stranger update to be rejected")
	}
	if err := client.CheckAuthorized(u2); err == nil {
		t.Errorf("expected CheckAuthorized to return ErrUnauthorized")
	}
	if !u2.IsCallback() {
		t.Errorf("expected update to be callback query")
	}
	if u2.CallbackData() != "snd:play:airhorn" {
		t.Errorf("expected callback data 'snd:play:airhorn', got '%s'", u2.CallbackData())
	}

	// 3. Voice message
	voiceMsgJSON := `{
		"update_id": 3,
		"message": {
			"message_id": 101,
			"date": 1600000002,
			"chat": {"id": 12345678, "type": "private"},
			"from": {"id": 12345678, "is_bot": false, "first_name": "Admin"},
			"voice": {
				"file_id": "voice_file_123",
				"file_unique_id": "uniq_1",
				"duration": 5,
				"mime_type": "audio/ogg",
				"file_size": 12345
			}
		}
	}`

	u3, err := client.ParseUpdate([]byte(voiceMsgJSON))
	if err != nil {
		t.Fatalf("failed to parse voice update: %v", err)
	}
	if !u3.IsVoice() {
		t.Errorf("expected message to be recognized as voice")
	}
	if u3.Voice().Duration != 5 {
		t.Errorf("expected duration 5, got %d", u3.Voice().Duration)
	}
}

func TestMockTelegramAPICalls(t *testing.T) {
	const adminID = int64(12345678)
	const testToken = "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(path, "/sendMessage"):
			var body SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			resp := APIResponse[*Message]{
				Ok: true,
				Result: &Message{
					MessageID: 555,
					Date:      time.Now().Unix(),
					Chat:      &Chat{ID: body.ChatID, Type: "private"},
					Text:      body.Text,
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case strings.HasSuffix(path, "/editMessageText"):
			var body EditMessageTextRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			resp := APIResponse[*Message]{
				Ok: true,
				Result: &Message{
					MessageID: body.MessageID,
					Chat:      &Chat{ID: body.ChatID, Type: "private"},
					Text:      body.Text,
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case strings.HasSuffix(path, "/answerCallbackQuery"):
			var body AnswerCallbackQueryRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			resp := APIResponse[bool]{
				Ok:     true,
				Result: true,
			}
			_ = json.NewEncoder(w).Encode(resp)

		case strings.HasSuffix(path, "/sendVoice"):
			mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
				http.Error(w, "invalid content type", http.StatusBadRequest)
				return
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			var gotChatID string
			var gotFileName string
			for {
				p, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if p.FormName() == "chat_id" {
					b, _ := io.ReadAll(p)
					gotChatID = string(b)
				}
				if p.FormName() == "voice" {
					gotFileName = p.FileName()
					_, _ = io.ReadAll(p)
				}
			}

			if gotChatID != "12345678" || gotFileName != "test.ogg" {
				http.Error(w, "missing or invalid multipart fields", http.StatusBadRequest)
				return
			}

			resp := APIResponse[*Message]{
				Ok: true,
				Result: &Message{
					MessageID: 777,
					Voice: &Voice{
						FileID:   "v_777",
						Duration: 10,
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case strings.HasSuffix(path, "/getUpdates"):
			resp := APIResponse[[]Update]{
				Ok: true,
				Result: []Update{
					{
						UpdateID: 10,
						Message: &Message{
							MessageID: 10,
							From:      &User{ID: adminID, Username: "admin"},
							Chat:      &Chat{ID: adminID},
							Text:      "ping",
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(testToken, adminID, WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// 1. Send text message
	msg, err := client.SendTextMessage(ctx, adminID, "Hello Admin")
	if err != nil {
		t.Fatalf("SendTextMessage failed: %v", err)
	}
	if msg.MessageID != 555 || msg.Text != "Hello Admin" {
		t.Errorf("unexpected message result: %+v", msg)
	}

	// 2. Edit message text with Inline Keyboard
	kb := MainMenuKeyboard()
	edited, err := client.EditMessageTextQuick(ctx, adminID, 555, "Updated Text", ParseModeHTML, kb)
	if err != nil {
		t.Fatalf("EditMessageTextQuick failed: %v", err)
	}
	if edited.Text != "Updated Text" {
		t.Errorf("unexpected edit result: %+v", edited)
	}

	// 3. Answer callback
	if err := client.AnswerCallbackAlert(ctx, "cb_1", "Sound started"); err != nil {
		t.Fatalf("AnswerCallbackAlert failed: %v", err)
	}

	// 4. Send voice via multipart
	dummyVoiceData := []byte("OGG_DUMMY_AUDIO_DATA")
	voiceMsg, err := client.SendVoiceBytes(ctx, adminID, "test.ogg", dummyVoiceData, "Sample Voice", kb)
	if err != nil {
		t.Fatalf("SendVoiceBytes failed: %v", err)
	}
	if voiceMsg.MessageID != 777 || voiceMsg.Voice == nil {
		t.Errorf("unexpected voice message response: %+v", voiceMsg)
	}

	// 5. Get updates (long polling call)
	updates, err := client.GetUpdates(ctx, GetUpdatesRequest{Timeout: 1})
	if err != nil {
		t.Fatalf("GetUpdates failed: %v", err)
	}
	if len(updates) != 1 || updates[0].UpdateID != 10 {
		t.Errorf("unexpected updates: %+v", updates)
	}
}

func TestMenuBuilders(t *testing.T) {
	// 1. Main menu
	mainMenu := MainMenuKeyboard()
	if len(mainMenu.InlineKeyboard) < 3 {
		t.Errorf("expected at least 3 rows in main menu, got %d", len(mainMenu.InlineKeyboard))
	}

	// 2. Soundboard pagination
	sounds := []string{"airhorn", "alarm", "bruh", "siren", "winxp", "what", "fart"}
	sb := SoundboardKeyboard(sounds, 0, 4)
	if len(sb.InlineKeyboard) == 0 {
		t.Fatalf("expected non-empty soundboard")
	}

	// Marshal to JSON to ensure it serializes properly
	data, err := json.Marshal(sb)
	if err != nil {
		t.Fatalf("failed to marshal soundboard: %v", err)
	}
	if !strings.Contains(string(data), "snd:play:airhorn") {
		t.Errorf("expected soundboard to include callback snd:play:airhorn")
	}

	// 3. Grid builder
	grid := NewGridKeyboard(3,
		NewButton("1", "d1"),
		NewButton("2", "d2"),
		NewButton("3", "d3"),
		NewButton("4", "d4"),
	)
	if len(grid.InlineKeyboard) != 2 {
		t.Errorf("expected 2 rows for 4 buttons with col=3, got %d", len(grid.InlineKeyboard))
	}
	if len(grid.InlineKeyboard[0]) != 3 {
		t.Errorf("expected 3 buttons in first row, got %d", len(grid.InlineKeyboard[0]))
	}
	if len(grid.InlineKeyboard[1]) != 1 {
		t.Errorf("expected 1 button in second row, got %d", len(grid.InlineKeyboard[1]))
	}
}
