package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigLoadSave(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")

	initial := DefaultConfig()
	initial.BotToken = "123456:ABC-DEF"
	initial.AdminID = 1184490028
	initial.Subnet = "192.168.1.0/24"

	if err := initial.Save(cfgPath); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if loaded.BotToken != initial.BotToken {
		t.Errorf("BotToken mismatch: got %s, want %s", loaded.BotToken, initial.BotToken)
	}
	if loaded.AdminID != initial.AdminID {
		t.Errorf("AdminID mismatch: got %d, want %d", loaded.AdminID, initial.AdminID)
	}
	if loaded.Subnet != "192.168.1.0/24" {
		t.Errorf("Subnet mismatch: got %s, want 192.168.1.0/24", loaded.Subnet)
	}

	// Test modifying and saving subnet, night mode and eq
	loaded.Subnet = "10.0.0.0/24"
	loaded.NightModeEnabled = true
	loaded.NightStartHour = 22
	loaded.NightEndHour = 7
	loaded.NightMaxVol = 20
	loaded.EQPreset = "bass_boost"
	if err := loaded.Save(cfgPath); err != nil {
		t.Fatalf("Failed to re-save config: %v", err)
	}

	reloaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("Failed to reload config: %v", err)
	}
	if reloaded.Subnet != "10.0.0.0/24" {
		t.Errorf("Updated subnet mismatch: got %s, want 10.0.0.0/24", reloaded.Subnet)
	}
	if reloaded.EQPreset != "bass_boost" {
		t.Errorf("Updated EQPreset mismatch: got %s, want bass_boost", reloaded.EQPreset)
	}
	if reloaded.NightStartHour != 22 || reloaded.NightEndHour != 7 || reloaded.NightMaxVol != 20 {
		t.Errorf("Updated night mode settings mismatch: %+v", reloaded)
	}
}

func TestConfigValidation(t *testing.T) {
	tmpDir := t.TempDir()
	badPath := filepath.Join(tmpDir, "bad.json")

	_ = os.WriteFile(badPath, []byte(`{"bot_token": ""}`), 0644)
	if _, err := LoadConfig(badPath); err == nil {
		t.Error("Expected error for empty bot_token, got nil")
	}

	_ = os.WriteFile(badPath, []byte(`{"bot_token": "token", "admin_id": 0}`), 0644)
	if _, err := LoadConfig(badPath); err == nil {
		t.Error("Expected error for zero admin_id, got nil")
	}
}
