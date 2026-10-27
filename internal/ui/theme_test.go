package ui

import (
	"errors"
	"sync"
	"testing"
)

func TestPredefinedThemes(t *testing.T) {
	tests := []struct {
		theme    Theme
		expected map[ButtonID]string
	}{
		{
			theme: ThemeDefault,
			expected: map[ButtonID]string{
				BtnStatus:     "📊",
				BtnIntercom:   "🎙️",
				BtnSoundboard: "📢",
				BtnTTS:        "🔊",
				BtnVolume:     "🎚️",
				BtnSiren:      "🚨",
				BtnDialogs:    "💬",
				BtnSystem:     "⚙️",
				BtnTimer:      "⏰",
				BtnPresence:   "👥",
				BtnNetDiag:    "📶",
				BtnTheme:      "🎨",
				BtnBack:       "⬅️",
			},
		},
		{
			theme: ThemePremium,
			expected: map[ButtonID]string{
				BtnStatus:     "💎",
				BtnIntercom:   "🔮",
				BtnSoundboard: "🎶",
				BtnTTS:        "🗣️",
				BtnVolume:     "🎛️",
				BtnSiren:      "🛡️",
				BtnDialogs:    "📜",
				BtnSystem:     "💻",
				BtnTimer:      "⏳",
				BtnPresence:   "👑",
				BtnNetDiag:    "🚀",
				BtnTheme:      "🌟",
				BtnBack:       "↩️",
			},
		},
		{
			theme: ThemeCyberpunk,
			expected: map[ButtonID]string{
				BtnStatus:     "⚡",
				BtnIntercom:   "📡",
				BtnSoundboard: "💥",
				BtnTTS:        "🤖",
				BtnVolume:     "🎚️",
				BtnSiren:      "☣️",
				BtnDialogs:    "📟",
				BtnSystem:     "🔧",
				BtnTimer:      "⏱️",
				BtnPresence:   "👁️",
				BtnNetDiag:    "🛰️",
				BtnTheme:      "🧬",
				BtnBack:       "🔙",
			},
		},
		{
			theme: ThemeMinimal,
			expected: map[ButtonID]string{
				BtnStatus:     "▪️",
				BtnIntercom:   "🎙",
				BtnSoundboard: "🔊",
				BtnTTS:        "💬",
				BtnVolume:     "🔉",
				BtnSiren:      "⚠️",
				BtnDialogs:    "📄",
				BtnSystem:     "⚙",
				BtnTimer:      "⏱",
				BtnPresence:   "👤",
				BtnNetDiag:    "📶",
				BtnTheme:      "◻️",
				BtnBack:       "⬅",
			},
		},
	}

	for _, tc := range tests {
		for btn, expectedIcon := range tc.expected {
			got := tc.theme.Icon(btn)
			if got != expectedIcon {
				t.Errorf("[%s] button %s expected %s, got %s", tc.theme.Name, btn, expectedIcon, got)
			}
		}
	}
}

func TestThemeManagerSwitching(t *testing.T) {
	tm := NewThemeManager("Default")

	if tm.CurrentThemeName() != "Default" {
		t.Errorf("expected initial theme 'Default', got %s", tm.CurrentThemeName())
	}

	if err := tm.SetTheme("Cyberpunk"); err != nil {
		t.Fatalf("SetTheme failed: %v", err)
	}

	if tm.CurrentThemeName() != "Cyberpunk" {
		t.Errorf("expected theme 'Cyberpunk', got %s", tm.CurrentThemeName())
	}

	if tm.Icon(BtnStatus) != "⚡" {
		t.Errorf("expected status icon '⚡', got %s", tm.Icon(BtnStatus))
	}

	// Case-insensitivity check
	if err := tm.SetTheme("premium"); err != nil {
		t.Fatalf("SetTheme(premium) failed: %v", err)
	}
	if tm.CurrentThemeName() != "Premium" {
		t.Errorf("expected 'Premium', got %s", tm.CurrentThemeName())
	}

	// Unknown theme error check
	err := tm.SetTheme("NonExistent")
	if err == nil || !errors.Is(err, ErrThemeNotFound) {
		t.Errorf("expected ErrThemeNotFound, got %v", err)
	}
}

func TestFormatButton(t *testing.T) {
	tm := NewThemeManager("Minimal")
	formatted := tm.StatusButton()
	expected := "▪️ Статус & Fastfetch"
	if formatted != expected {
		t.Errorf("expected %q, got %q", expected, formatted)
	}

	back := tm.BackButton()
	expectedBack := "⬅ Назад"
	if back != expectedBack {
		t.Errorf("expected %q, got %q", expectedBack, back)
	}
}

func TestConcurrentThemeManager(t *testing.T) {
	tm := NewThemeManager("Default")
	themes := tm.ListThemes()

	const goroutines = 20
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				themeName := themes[j%len(themes)]
				_ = tm.SetTheme(themeName)
				_ = tm.Icon(BtnStatus)
				_ = tm.StatusButton()
				_ = tm.CurrentThemeName()
			}
		}(i)
	}

	wg.Wait()
}

func TestSetIcon(t *testing.T) {
	tm := NewThemeManager("Default")
	tm.SetIcon(BtnStatus, "👑")
	if tm.Icon(BtnStatus) != "👑" {
		t.Errorf("expected 👑, got %s", tm.Icon(BtnStatus))
	}
	if tm.Format(BtnStatus, "Статус") != "👑 Статус" {
		t.Errorf("expected '👑 Статус', got %s", tm.Format(BtnStatus, "Статус"))
	}
}
