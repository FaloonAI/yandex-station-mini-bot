package speaker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEqualizerPresets(t *testing.T) {
	c := NewController("/tmp/sounds", "/tmp/scripts", "/tmp/logs")

	if c.GetEqualizerPreset() != "balanced" {
		t.Fatalf("expected initial EQ to be 'balanced', got %s", c.GetEqualizerPreset())
	}

	for id, name := range EqualizerPresetNames {
		err := c.SetEqualizerPreset(id)
		if err != nil {
			t.Errorf("failed to set preset %s (%s): %v", id, name, err)
		}
		if c.GetEqualizerPreset() != id {
			t.Errorf("expected EQ preset %s, got %s", id, c.GetEqualizerPreset())
		}
	}

	// Test invalid preset
	err := c.SetEqualizerPreset("super_mega_bass_unknown")
	if err == nil {
		t.Error("expected error setting invalid preset, got nil")
	}
}

func TestWriteWAVFile(t *testing.T) {
	tmpDir := t.TempDir()
	wavPath := filepath.Join(tmpDir, "test.wav")

	dummyPCM := make([]byte, 1000)
	err := writeWAVFile(wavPath, dummyPCM, 16000, 1, 16)
	if err != nil {
		t.Fatalf("writeWAVFile failed: %v", err)
	}

	info, err := os.Stat(wavPath)
	if err != nil {
		t.Fatalf("stat on created wav failed: %v", err)
	}
	if info.Size() != int64(44+len(dummyPCM)) {
		t.Errorf("expected file size %d, got %d", 44+len(dummyPCM), info.Size())
	}
}

func TestAnsiFilter(t *testing.T) {
	colored := "\x1b[31mError\x1b[0m: \x1b[32mSuccess\x1b[0m"
	cleaned := cleanFastfetchOutput(colored)
	if cleaned != "Error: Success" {
		t.Errorf("expected 'Error: Success', got %q", cleaned)
	}
}
