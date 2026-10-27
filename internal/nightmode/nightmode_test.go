package nightmode

import (
	"testing"
	"time"
)

func TestIsNightHour(t *testing.T) {
	tests := []struct {
		hour     int
		start    int
		end      int
		expected bool
	}{
		// Overnight: 23:00 to 08:00
		{23, 23, 8, true},
		{0, 23, 8, true},
		{3, 23, 8, true},
		{7, 23, 8, true},
		{8, 23, 8, false},
		{12, 23, 8, false},
		{22, 23, 8, false},

		// Daytime / Same day: 01:00 to 06:00
		{0, 1, 6, false},
		{1, 1, 6, true},
		{5, 1, 6, true},
		{6, 1, 6, false},
		{10, 1, 6, false},

		// All day
		{15, 10, 10, true},
	}

	for _, tc := range tests {
		got := IsNightHour(tc.hour, tc.start, tc.end)
		if got != tc.expected {
			t.Errorf("IsNightHour(hour=%d, start=%d, end=%d) = %v, expected %v", tc.hour, tc.start, tc.end, got, tc.expected)
		}
	}
}

func TestNightManager(t *testing.T) {
	cfg := Config{
		Enabled:         true,
		StartHour:       23,
		EndHour:         8,
		MaxVolume:       25,
		BlockSoundboard: true,
	}

	mgr := NewManager(cfg)

	// Check 02:00 (night)
	nightTime := time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)
	if !mgr.IsNightAt(nightTime) {
		t.Errorf("expected night mode to be active at 02:00")
	}

	// Check 14:00 (day)
	dayTime := time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)
	if mgr.IsNightAt(dayTime) {
		t.Errorf("expected night mode to be inactive at 14:00")
	}

	// Disable and verify
	mgr.SetEnabled(false)
	if mgr.IsNightAt(nightTime) {
		t.Errorf("expected night mode to be inactive when disabled")
	}

	// Re-enable and test setters
	mgr.SetEnabled(true)
	if err := mgr.SetHours(22, 7); err != nil {
		t.Fatalf("SetHours failed: %v", err)
	}
	if mgr.GetConfig().StartHour != 22 || mgr.GetConfig().EndHour != 7 {
		t.Errorf("unexpected hours: %+v", mgr.GetConfig())
	}

	if err := mgr.SetMaxVolume(20); err != nil {
		t.Fatalf("SetMaxVolume failed: %v", err)
	}
	if mgr.GetConfig().MaxVolume != 20 {
		t.Errorf("unexpected max volume: %d", mgr.GetConfig().MaxVolume)
	}

	// Test invalid hours
	if err := mgr.SetHours(25, 0); err == nil {
		t.Errorf("expected error for invalid start hour 25, got nil")
	}
}
