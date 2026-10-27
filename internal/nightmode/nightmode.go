package nightmode

import (
	"fmt"
	"sync"
	"time"
)

// Config represents night mode operational parameters.
type Config struct {
	Enabled         bool `json:"enabled"`
	StartHour       int  `json:"start_hour"`
	EndHour         int  `json:"end_hour"`
	MaxVolume       int  `json:"max_volume"`
	BlockSoundboard bool `json:"block_soundboard"`
}

// Manager manages quiet hours / night mode state.
type Manager struct {
	mu  sync.RWMutex
	cfg Config
}

// NewManager creates a new Night Mode manager with the provided configuration.
func NewManager(cfg Config) *Manager {
	if cfg.StartHour < 0 || cfg.StartHour > 23 {
		cfg.StartHour = 23
	}
	if cfg.EndHour < 0 || cfg.EndHour > 23 {
		cfg.EndHour = 8
	}
	if cfg.MaxVolume <= 0 || cfg.MaxVolume > 100 {
		cfg.MaxVolume = 25
	}
	return &Manager{cfg: cfg}
}

// GetConfig returns a copy of current night mode configuration.
func (m *Manager) GetConfig() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// SetEnabled toggles night mode active/inactive.
func (m *Manager) SetEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Enabled = enabled
}

// SetHours sets the start and end hours for night mode (0-23).
func (m *Manager) SetHours(start, end int) error {
	if start < 0 || start > 23 || end < 0 || end > 23 {
		return fmt.Errorf("hours must be between 0 and 23 (got start=%d, end=%d)", start, end)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.StartHour = start
	m.cfg.EndHour = end
	return nil
}

// SetMaxVolume updates the maximum allowed volume percentage during night hours.
func (m *Manager) SetMaxVolume(vol int) error {
	if vol < 5 || vol > 100 {
		return fmt.Errorf("max volume must be between 5%% and 100%% (got %d)", vol)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.MaxVolume = vol
	return nil
}

// SetBlockSoundboard toggles blocking of soundboard sounds during night mode.
func (m *Manager) SetBlockSoundboard(block bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.BlockSoundboard = block
}

// IsNightHour checks whether a specific hour falls within night hours.
func IsNightHour(hour, start, end int) bool {
	if start == end {
		return true
	}
	if start < end {
		return hour >= start && hour < end
	}
	// Overnight wrap-around (e.g. 23:00 to 08:00)
	return hour >= start || hour < end
}

// IsCurrentlyNight checks if night mode is enabled and the current time is within quiet hours.
func (m *Manager) IsCurrentlyNight() bool {
	return m.IsNightAt(time.Now())
}

// IsNightAt checks whether night mode is active at a given time point.
func (m *Manager) IsNightAt(t time.Time) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.cfg.Enabled {
		return false
	}
	return IsNightHour(t.Hour(), m.cfg.StartHour, m.cfg.EndHour)
}

// ShouldBlockSoundboard checks whether soundboard playback should be blocked right now.
func (m *Manager) ShouldBlockSoundboard() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.cfg.Enabled || !m.cfg.BlockSoundboard {
		return false
	}
	return IsNightHour(time.Now().Hour(), m.cfg.StartHour, m.cfg.EndHour)
}

// EnforceVolume limits volume to MaxVolume if night mode is currently active.
// Returns the adjusted volume and whether a restriction was applied.
func (m *Manager) EnforceVolume(currentVol int) (int, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.cfg.Enabled {
		return currentVol, false
	}

	if !IsNightHour(time.Now().Hour(), m.cfg.StartHour, m.cfg.EndHour) {
		return currentVol, false
	}

	if currentVol > m.cfg.MaxVolume {
		return m.cfg.MaxVolume, true
	}

	return currentVol, false
}
