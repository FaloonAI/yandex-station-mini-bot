package timer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// TimerItem represents an active timer or alarm exposed to callers.
type TimerItem struct {
	ID        string        `json:"id"`
	Label     string        `json:"label"`
	Remaining time.Duration `json:"remaining"`
	Target    time.Time     `json:"target"`
	IsAlarm   bool          `json:"is_alarm"`
}

// entry is an internal tracking unit for a running timer or alarm.
type entry struct {
	id        string
	label     string
	target    time.Time
	isAlarm   bool
	onTrigger func()
	timer     *time.Timer
}

// Manager manages active timers and alarms concurrently.
type Manager struct {
	mu      sync.RWMutex
	entries map[string]*entry
	closed  bool
}

// NewManager initializes a new thread-safe Timer Manager.
func NewManager() *Manager {
	return &Manager{
		entries: make(map[string]*entry),
	}
}

// generateID produces a unique compact identifier for a timer (e.g. "tm_a1b2c3d4").
func generateID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to nanosecond timestamp if crypto rand fails
		return fmt.Sprintf("tm_%x", time.Now().UnixNano()&0xffffffff)
	}
	return "tm_" + hex.EncodeToString(b)
}

// AddTimer creates and starts a countdown timer that executes onTrigger after duration.
// Returns the unique timer ID.
func (m *Manager) AddTimer(duration time.Duration, label string, onTrigger func()) string {
	if duration <= 0 {
		duration = time.Millisecond
	}
	if label == "" {
		label = "Таймер"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ""
	}

	id := generateID()
	for {
		if _, exists := m.entries[id]; !exists {
			break
		}
		id = generateID()
	}

	target := time.Now().Add(duration)
	e := &entry{
		id:        id,
		label:     label,
		target:    target,
		isAlarm:   false,
		onTrigger: onTrigger,
	}

	e.timer = time.AfterFunc(duration, func() {
		m.fireEntry(id)
	})

	m.entries[id] = e
	return id
}

// AddAlarm creates an alarm scheduled for targetTime.
// If targetTime is in the past, it triggers near-immediately.
// Returns the unique timer ID.
func (m *Manager) AddAlarm(targetTime time.Time, label string, onTrigger func()) string {
	if label == "" {
		label = "Будильник"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ""
	}

	id := generateID()
	for {
		if _, exists := m.entries[id]; !exists {
			break
		}
		id = generateID()
	}

	now := time.Now()
	dur := targetTime.Sub(now)
	if dur <= 0 {
		dur = time.Millisecond
	}

	e := &entry{
		id:        id,
		label:     label,
		target:    targetTime,
		isAlarm:   true,
		onTrigger: onTrigger,
	}

	e.timer = time.AfterFunc(dur, func() {
		m.fireEntry(id)
	})

	m.entries[id] = e
	return id
}

// fireEntry handles timer expiration: deletes the item and triggers the callback asynchronously.
func (m *Manager) fireEntry(id string) {
	m.mu.Lock()
	e, exists := m.entries[id]
	if !exists {
		m.mu.Unlock()
		return
	}
	delete(m.entries, id)
	m.mu.Unlock()

	if e.onTrigger != nil {
		go func(fn func()) {
			defer func() {
				_ = recover() // Guard manager against panics in caller callback
			}()
			fn()
		}(e.onTrigger)
	}
}

// ListActive returns a snapshot of all active timers and alarms, sorted by remaining time.
func (m *Manager) ListActive() []TimerItem {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	items := make([]TimerItem, 0, len(m.entries))

	for _, e := range m.entries {
		rem := e.target.Sub(now)
		if rem < 0 {
			rem = 0
		}
		items = append(items, TimerItem{
			ID:        e.id,
			Label:     e.label,
			Remaining: rem,
			Target:    e.target,
			IsAlarm:   e.isAlarm,
		})
	}

	// Sort ascending by target time
	sort.Slice(items, func(i, j int) bool {
		return items[i].Target.Before(items[j].Target)
	})

	return items
}

// Cancel cancels and removes an active timer or alarm by ID.
// Returns true if the timer was found and cancelled, false otherwise.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, exists := m.entries[id]
	if !exists {
		return false
	}

	if e.timer != nil {
		e.timer.Stop()
	}
	delete(m.entries, id)
	return true
}

// Get returns the status of a specific timer if active.
func (m *Manager) Get(id string) (TimerItem, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	e, exists := m.entries[id]
	if !exists {
		return TimerItem{}, false
	}

	rem := e.target.Sub(time.Now())
	if rem < 0 {
		rem = 0
	}

	return TimerItem{
		ID:        e.id,
		Label:     e.label,
		Remaining: rem,
		Target:    e.target,
		IsAlarm:   e.isAlarm,
	}, true
}

// Count returns the number of active timers and alarms.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}

// Close stops all active timers and prevents new timers from being added.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return
	}
	m.closed = true

	for id, e := range m.entries {
		if e.timer != nil {
			e.timer.Stop()
		}
		delete(m.entries, id)
	}
}
