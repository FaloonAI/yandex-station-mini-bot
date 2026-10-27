package ui

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ButtonID represents standard UI actions and navigation button types.
type ButtonID string

const (
	BtnStatus     ButtonID = "status"
	BtnIntercom   ButtonID = "intercom"
	BtnSoundboard ButtonID = "soundboard"
	BtnTTS        ButtonID = "tts"
	BtnVolume     ButtonID = "volume"
	BtnSiren      ButtonID = "siren"
	BtnDialogs    ButtonID = "dialogs"
	BtnSystem     ButtonID = "system"
	BtnTimer      ButtonID = "timer"
	BtnPresence   ButtonID = "presence"
	BtnNetDiag    ButtonID = "netdiag"
	BtnTheme      ButtonID = "theme"
	BtnBack       ButtonID = "back"
)

// Theme defines the visual emoji styling for all bot menu buttons.
type Theme struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Intercom   string `json:"intercom"`
	Soundboard string `json:"soundboard"`
	TTS        string `json:"tts"`
	Volume     string `json:"volume"`
	Siren      string `json:"siren"`
	Dialogs    string `json:"dialogs"`
	System     string `json:"system"`
	Timer      string `json:"timer"`
	Presence   string `json:"presence"`
	NetDiag    string `json:"netdiag"`
	Theme      string `json:"theme"`
	Back       string `json:"back"`
}

// Icon retrieves the icon associated with a specific ButtonID.
func (t Theme) Icon(id ButtonID) string {
	switch id {
	case BtnStatus:
		return t.Status
	case BtnIntercom:
		return t.Intercom
	case BtnSoundboard:
		return t.Soundboard
	case BtnTTS:
		return t.TTS
	case BtnVolume:
		return t.Volume
	case BtnSiren:
		return t.Siren
	case BtnDialogs:
		return t.Dialogs
	case BtnSystem:
		return t.System
	case BtnTimer:
		return t.Timer
	case BtnPresence:
		return t.Presence
	case BtnNetDiag:
		return t.NetDiag
	case BtnTheme:
		return t.Theme
	case BtnBack:
		return t.Back
	default:
		return ""
	}
}

// Format prepends the theme icon to the provided button text.
func (t Theme) Format(id ButtonID, label string) string {
	icon := t.Icon(id)
	if icon == "" {
		return label
	}
	return fmt.Sprintf("%s %s", icon, label)
}

var (
	// ThemeDefault is the standard expressive UI theme.
	ThemeDefault = Theme{
		Name:       "Default",
		Status:     "📊",
		Intercom:   "🎙️",
		Soundboard: "📢",
		TTS:        "🔊",
		Volume:     "🎚️",
		Siren:      "🚨",
		Dialogs:    "💬",
		System:     "⚙️",
		Timer:      "⏰",
		Presence:   "👥",
		NetDiag:    "📶",
		Theme:      "🎨",
		Back:       "⬅️",
	}

	// ThemePremium is a sleek, neon-purple modern theme.
	ThemePremium = Theme{
		Name:       "Premium",
		Status:     "💎",
		Intercom:   "🔮",
		Soundboard: "🎶",
		TTS:        "🗣️",
		Volume:     "🎛️",
		Siren:      "🛡️",
		Dialogs:    "📜",
		System:     "💻",
		Timer:      "⏳",
		Presence:   "👑",
		NetDiag:    "🚀",
		Theme:      "🌟",
		Back:       "↩️",
	}

	// ThemeCyberpunk is a futuristic high-tech theme.
	ThemeCyberpunk = Theme{
		Name:       "Cyberpunk",
		Status:     "⚡",
		Intercom:   "📡",
		Soundboard: "💥",
		TTS:        "🤖",
		Volume:     "🎚️",
		Siren:      "☣️",
		Dialogs:    "📟",
		System:     "🔧",
		Timer:      "⏱️",
		Presence:   "👁️",
		NetDiag:    "🛰️",
		Theme:      "🧬",
		Back:       "🔙",
	}

	// ThemeMinimal is a monochrome, clean minimalist theme.
	ThemeMinimal = Theme{
		Name:       "Minimal",
		Status:     "▪️",
		Intercom:   "🎙",
		Soundboard: "🔊",
		TTS:        "💬",
		Volume:     "🔉",
		Siren:      "⚠️",
		Dialogs:    "📄",
		System:     "⚙",
		Timer:      "⏱",
		Presence:   "👤",
		NetDiag:    "📶",
		Theme:      "◻️",
		Back:       "⬅",
	}
)

var (
	// ErrThemeNotFound indicates the requested theme name is not registered.
	ErrThemeNotFound = errors.New("theme not found")
)

// ThemeManager maintains the active theme and allows dynamic, thread-safe switching.
type ThemeManager struct {
	mu           sync.RWMutex
	currentTheme Theme
	themes       map[string]Theme
}

// NewThemeManager constructs a ThemeManager with all standard predefined themes.
// Defaults to "Default" if initialTheme is omitted or not found.
func NewThemeManager(initialTheme ...string) *ThemeManager {
	tm := &ThemeManager{
		themes: make(map[string]Theme),
	}

	tm.RegisterTheme(ThemeDefault)
	tm.RegisterTheme(ThemePremium)
	tm.RegisterTheme(ThemeCyberpunk)
	tm.RegisterTheme(ThemeMinimal)

	selected := "Default"
	if len(initialTheme) > 0 && initialTheme[0] != "" {
		selected = initialTheme[0]
	}

	if t, ok := tm.themes[strings.ToLower(selected)]; ok {
		tm.currentTheme = t
	} else {
		tm.currentTheme = ThemeDefault
	}

	return tm
}

// RegisterTheme adds or updates a theme in the manager.
func (tm *ThemeManager) RegisterTheme(t Theme) {
	if t.Name == "" {
		return
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.themes[strings.ToLower(t.Name)] = t
}

// SetTheme switches the active theme by name (case-insensitive).
func (tm *ThemeManager) SetTheme(name string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	t, exists := tm.themes[strings.ToLower(strings.TrimSpace(name))]
	if !exists {
		return fmt.Errorf("%w: %s", ErrThemeNotFound, name)
	}

	tm.currentTheme = t
	return nil
}

// GetTheme returns the currently active Theme.
func (tm *ThemeManager) GetTheme() Theme {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.currentTheme
}

// CurrentThemeName returns the name of the active theme.
func (tm *ThemeManager) CurrentThemeName() string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.currentTheme.Name
}

// ListThemes returns the list of all available theme names, sorted alphabetically.
func (tm *ThemeManager) ListThemes() []string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	names := make([]string, 0, len(tm.themes))
	for _, t := range tm.themes {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}

// SetIcon updates or overrides the icon for a specific ButtonID in the currently active theme.
func (tm *ThemeManager) SetIcon(id ButtonID, icon string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	switch id {
	case BtnStatus:
		tm.currentTheme.Status = icon
	case BtnIntercom:
		tm.currentTheme.Intercom = icon
	case BtnSoundboard:
		tm.currentTheme.Soundboard = icon
	case BtnTTS:
		tm.currentTheme.TTS = icon
	case BtnVolume:
		tm.currentTheme.Volume = icon
	case BtnSiren:
		tm.currentTheme.Siren = icon
	case BtnDialogs:
		tm.currentTheme.Dialogs = icon
	case BtnSystem:
		tm.currentTheme.System = icon
	case BtnTimer:
		tm.currentTheme.Timer = icon
	case BtnPresence:
		tm.currentTheme.Presence = icon
	case BtnNetDiag:
		tm.currentTheme.NetDiag = icon
	case BtnTheme:
		tm.currentTheme.Theme = icon
	case BtnBack:
		tm.currentTheme.Back = icon
	}
}

// Icon returns the icon for the specified ButtonID under the currently active theme.
func (tm *ThemeManager) Icon(id ButtonID) string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.currentTheme.Icon(id)
}

// FormatButton formats a button label with the active theme's icon.
func (tm *ThemeManager) FormatButton(id ButtonID, label string) string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.currentTheme.Format(id, label)
}

// Format formats a button label with the active theme's icon (convenience alias to FormatButton).
func (tm *ThemeManager) Format(id ButtonID, label string) string {
	return tm.FormatButton(id, label)
}

// Standard button formatters for high-frequency menus
func (tm *ThemeManager) StatusButton() string     { return tm.FormatButton(BtnStatus, "Статус & Fastfetch") }
func (tm *ThemeManager) IntercomButton() string   { return tm.FormatButton(BtnIntercom, "Рация & Прослушка") }
func (tm *ThemeManager) SoundboardButton() string { return tm.FormatButton(BtnSoundboard, "Саундборд") }
func (tm *ThemeManager) TTSButton() string        { return tm.FormatButton(BtnTTS, "Озвучить текст (TTS)") }
func (tm *ThemeManager) VolumeButton() string     { return tm.FormatButton(BtnVolume, "Громкость") }
func (tm *ThemeManager) SirenButton() string      { return tm.FormatButton(BtnSiren, "Сирена (Охрана)") }
func (tm *ThemeManager) DialogsButton() string    { return tm.FormatButton(BtnDialogs, "Диалоги Алисы") }
func (tm *ThemeManager) SystemButton() string     { return tm.FormatButton(BtnSystem, "Управление системой") }
func (tm *ThemeManager) TimerButton() string      { return tm.FormatButton(BtnTimer, "Таймеры & Будильники") }
func (tm *ThemeManager) PresenceButton() string   { return tm.FormatButton(BtnPresence, "Кто дома?") }
func (tm *ThemeManager) NetDiagButton() string    { return tm.FormatButton(BtnNetDiag, "Диагностика сети") }
func (tm *ThemeManager) ThemeButton() string      { return tm.FormatButton(BtnTheme, "Тема оформления") }
func (tm *ThemeManager) BackButton() string       { return tm.FormatButton(BtnBack, "Назад") }
