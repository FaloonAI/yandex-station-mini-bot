package soundboard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	validIDRegex = regexp.MustCompile(`[^a-zA-Z0-9_\-]+`)
)

// SoundItem represents a sound in the soundboard catalog.
type SoundItem struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Filename    string    `json:"filename"`
	DurationSec float64   `json:"duration_sec"`
	SizeBytes   int64     `json:"size_bytes"`
	IsBuiltin   bool      `json:"is_builtin"`
	CreatedAt   time.Time `json:"created_at"`
}

// BuiltinDefaultSounds lists the standard built-in sounds on the station.
var BuiltinDefaultSounds = map[string]string{
	"airhorn":    "📢 Airhorn",
	"bruh":       "🎺 Bruh",
	"discord":    "🔔 Discord",
	"fart":       "💨 Fart",
	"metal_pipe": "🪈 Metal Pipe",
	"rickroll":   "🎵 Rickroll",
	"winxp":      "🔊 Win XP",
}

// Manager handles soundboard catalog, storage, optimization, and CRUD operations.
type Manager struct {
	mu        sync.RWMutex
	soundsDir string
	metaFile  string
	sounds    map[string]*SoundItem
	optimizer *Optimizer
}

// NewManager creates a soundboard manager pointing to soundsDir.
func NewManager(soundsDir string, opt *Optimizer) (*Manager, error) {
	if err := os.MkdirAll(soundsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create sounds dir: %w", err)
	}

	if opt == nil {
		opt = NewOptimizer(nil)
	}

	m := &Manager{
		soundsDir: soundsDir,
		metaFile:  filepath.Join(soundsDir, "sounds.json"),
		sounds:    make(map[string]*SoundItem),
		optimizer: opt,
	}

	if err := m.loadOrDiscover(); err != nil {
		return nil, err
	}

	return m, nil
}

// SanitizeID converts any sound title into a safe lowercase alphanumeric identifier.
func SanitizeID(name string) string {
	clean := strings.TrimSpace(strings.ToLower(name))
	clean = strings.ReplaceAll(clean, " ", "_")
	clean = validIDRegex.ReplaceAllString(clean, "")
	if len(clean) > 24 {
		clean = clean[:24]
	}
	clean = strings.Trim(clean, "_-")
	if clean == "" {
		clean = fmt.Sprintf("sound_%d", time.Now().Unix()%10000)
	}
	return clean
}

func (m *Manager) loadOrDiscover() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Try loading existing metadata
	if data, err := os.ReadFile(m.metaFile); err == nil {
		var list []*SoundItem
		if err := json.Unmarshal(data, &list); err == nil {
			for _, item := range list {
				// Verify file actually exists
				path := filepath.Join(m.soundsDir, item.Filename)
				if fi, err := os.Stat(path); err == nil {
					item.SizeBytes = fi.Size()
					m.sounds[item.ID] = item
				}
			}
		}
	}

	// 2. Discover WAV files in soundsDir that might not be in metadata
	entries, err := os.ReadDir(m.soundsDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".wav") {
				continue
			}
			id := strings.TrimSuffix(strings.ToLower(e.Name()), ".wav")
			if _, exists := m.sounds[id]; !exists {
				fi, _ := e.Info()
				name := id
				isBuiltin := false
				if builtinName, ok := BuiltinDefaultSounds[id]; ok {
					name = builtinName
					isBuiltin = true
				} else {
					name = strings.Title(strings.ReplaceAll(id, "_", " "))
				}

				item := &SoundItem{
					ID:        id,
					Name:      name,
					Filename:  e.Name(),
					SizeBytes: fi.Size(),
					IsBuiltin: isBuiltin,
					CreatedAt: fi.ModTime(),
				}
				m.sounds[id] = item
			}
		}
	}

	return m.saveLocked()
}

func (m *Manager) saveLocked() error {
	list := make([]*SoundItem, 0, len(m.sounds))
	for _, item := range m.sounds {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.metaFile, data, 0644)
}

// List returns a sorted slice of all sounds in the soundboard catalog.
func (m *Manager) List() []SoundItem {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make([]SoundItem, 0, len(m.sounds))
	for _, item := range m.sounds {
		res = append(res, *item)
	}

	// Sort built-in first, then by ID
	sort.Slice(res, func(i, j int) bool {
		if res[i].IsBuiltin != res[j].IsBuiltin {
			return res[i].IsBuiltin && !res[j].IsBuiltin
		}
		return res[i].ID < res[j].ID
	})
	return res
}

// Get returns the SoundItem by its ID.
func (m *Manager) Get(id string) (*SoundItem, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	item, ok := m.sounds[id]
	if !ok {
		return nil, fmt.Errorf("sound %q not found", id)
	}
	return item, nil
}

// SoundFilePath returns the absolute path on disk to the sound's WAV file.
func (m *Manager) SoundFilePath(id string) (string, error) {
	item, err := m.Get(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(m.soundsDir, item.Filename), nil
}

// Add processes and optimizes an incoming audio file and registers it in the catalog.
func (m *Manager) Add(ctx context.Context, displayName, rawInputPath string) (*SoundItem, *OptimizationResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.sounds) >= 50 {
		return nil, nil, fmt.Errorf("достигнут лимит каталога звуков (максимум 50). Удалите ненужные звуки")
	}

	trimmedName := strings.TrimSpace(displayName)
	if trimmedName == "" {
		return nil, nil, fmt.Errorf("название звука не может быть пустым")
	}

	id := SanitizeID(trimmedName)
	if item, exists := m.sounds[id]; exists && item.IsBuiltin {
		return nil, nil, fmt.Errorf("имя %q занято встроенным звуком, выберите другое", id)
	}

	targetFilename := fmt.Sprintf("%s.wav", id)
	targetPath := filepath.Join(m.soundsDir, targetFilename)

	// Run audio optimization algorithm
	optResult, err := m.optimizer.Optimize(ctx, rawInputPath, targetPath)
	if err != nil {
		return nil, nil, fmt.Errorf("ошибка оптимизации аудио: %w", err)
	}

	item := &SoundItem{
		ID:          id,
		Name:        trimmedName,
		Filename:    targetFilename,
		DurationSec: optResult.DurationSec,
		SizeBytes:   optResult.OptimizedSize,
		IsBuiltin:   false,
		CreatedAt:   time.Now(),
	}

	m.sounds[id] = item
	_ = m.saveLocked()

	return item, optResult, nil
}

// Delete removes a sound from the catalog and deletes its file.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.sounds[id]
	if !exists {
		return fmt.Errorf("sound %q not found", id)
	}

	if item.IsBuiltin {
		return fmt.Errorf("встроенный звук %q нельзя удалить", item.Name)
	}

	filePath := filepath.Join(m.soundsDir, item.Filename)
	_ = os.Remove(filePath)

	delete(m.sounds, id)
	return m.saveLocked()
}

// Rename updates the display name of a sound in the catalog.
func (m *Manager) Rename(id, newDisplayName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.sounds[id]
	if !exists {
		return fmt.Errorf("sound %q not found", id)
	}

	cleanName := strings.TrimSpace(newDisplayName)
	if cleanName == "" {
		return fmt.Errorf("новое название не может быть пустым")
	}

	item.Name = cleanName
	return m.saveLocked()
}
