package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	BotToken    string `json:"bot_token"`
	AdminID     int64  `json:"admin_id"`
	SoundsDir   string `json:"sounds_dir"`
	ScriptsDir  string `json:"scripts_dir"`
	LogPath     string `json:"log_path"`
	QuasarLogs  string `json:"quasar_logs"`
	AplayPath   string `json:"aplay_path"`
	ArecordPath string `json:"arecord_path"`
	AmixerPath  string `json:"amixer_path"`
	Theme       string `json:"theme,omitempty"`
	Subnet      string `json:"subnet,omitempty"`
	DevicesFile string `json:"devices_file,omitempty"`
}

func DefaultConfig() *Config {
	return &Config{
		BotToken:    "",
		AdminID:     0,
		SoundsDir:   "/data/telegram-bot/sounds",
		ScriptsDir:  "/data/telegram-bot/scripts",
		LogPath:     "/data/telegram-bot/bot.log",
		QuasarLogs:  "/data/quasar/daemons_logs/maind.log",
		AplayPath:   "/system/workdir/bin/aplay",
		ArecordPath: "/system/workdir/bin/arecord",
		AmixerPath:  "/system/workdir/bin/amixer",
		Theme:       "Default",
		Subnet:      "192.168.20.0/24",
		DevicesFile: "/data/telegram-bot/known_devices.json",
	}
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	if err := json.NewDecoder(file).Decode(cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config json: %w", err)
	}

	if cfg.BotToken == "" {
		return nil, fmt.Errorf("bot_token must not be empty in config")
	}
	if cfg.AdminID == 0 {
		return nil, fmt.Errorf("admin_id must not be empty in config")
	}

	return cfg, nil
}
