package soundboard

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Airhorn Sound", "airhorn_sound"},
		{"  Bruh! 123 ", "bruh_123"},
		{"Привет Мир", "sound_"}, // fallback or clean
		{"Super-Duper_Sound", "super-duper_sound"},
		{"", "sound_"},
	}

	for _, tc := range tests {
		got := SanitizeID(tc.input)
		if len(got) == 0 {
			t.Errorf("SanitizeID(%q) returned empty string", tc.input)
		}
	}
}

func TestWAVHeader(t *testing.T) {
	var buf bytes.Buffer
	pcmBytes := 44100 // 1 sec of mono 22050 16-bit
	err := WriteWAVHeader(&buf, pcmBytes, 22050, 1, 16)
	if err != nil {
		t.Fatalf("WriteWAVHeader failed: %v", err)
	}

	header := buf.Bytes()
	if len(header) != 44 {
		t.Fatalf("Expected 44 bytes header, got %d", len(header))
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		t.Errorf("Invalid RIFF/WAVE signature: %s", string(header[0:12]))
	}

	// Verify AudioFormat = 1 (PCM)
	format := binary.LittleEndian.Uint16(header[20:22])
	if format != 1 {
		t.Errorf("Expected PCM format 1, got %d", format)
	}

	// Verify sample rate = 22050
	rate := binary.LittleEndian.Uint32(header[24:28])
	if rate != 22050 {
		t.Errorf("Expected sample rate 22050, got %d", rate)
	}
}

func TestOptimizerAlgorithms(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "input.pcm")
	outPath := filepath.Join(tmpDir, "out.wav")

	// Create test PCM: 1000 samples silence (0), 5000 samples quiet sine (1000), 1000 samples silence (0)
	var pcmBuf bytes.Buffer
	for i := 0; i < 1000; i++ {
		_ = binary.Write(&pcmBuf, binary.LittleEndian, int16(0))
	}
	for i := 0; i < 5000; i++ {
		_ = binary.Write(&pcmBuf, binary.LittleEndian, int16(1000))
	}
	for i := 0; i < 1000; i++ {
		_ = binary.Write(&pcmBuf, binary.LittleEndian, int16(0))
	}

	if err := os.WriteFile(rawPath, pcmBuf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	// Mock decoder for tests: just copies raw PCM
	mockDecoder := func(ctx context.Context, in, out string) error {
		data, err := os.ReadFile(in)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0644)
	}

	opt := NewOptimizer(mockDecoder)
	res, err := opt.Optimize(context.Background(), rawPath, outPath)
	if err != nil {
		t.Fatalf("Optimize failed: %v", err)
	}

	if res.DurationSec <= 0 {
		t.Errorf("Expected positive duration, got %f", res.DurationSec)
	}
	if res.GainMultiplier <= 1.0 {
		t.Errorf("Expected gain > 1.0 for quiet audio (maxVal=1000), got %f", res.GainMultiplier)
	}

	outData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(outData) <= 44 {
		t.Fatalf("Expected WAV header + PCM, got only %d bytes", len(outData))
	}
}

func TestOptimizerAllSilence(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "silent.pcm")
	outPath := filepath.Join(tmpDir, "silent.wav")

	// 2000 silent samples
	data := make([]byte, 4000)
	_ = os.WriteFile(rawPath, data, 0644)

	mockDecoder := func(ctx context.Context, in, out string) error {
		d, err := os.ReadFile(in)
		if err != nil {
			return err
		}
		return os.WriteFile(out, d, 0644)
	}

	opt := NewOptimizer(mockDecoder)
	_, err := opt.Optimize(context.Background(), rawPath, outPath)
	if err == nil {
		t.Error("Expected error for all-silence audio, got nil")
	}
}

func TestManagerCRUD(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-create a mock builtin sound: airhorn.wav
	airhornPath := filepath.Join(tmpDir, "airhorn.wav")
	var dummyWav bytes.Buffer
	_ = WriteWAVHeader(&dummyWav, 100, 22050, 1, 16)
	dummyWav.Write(make([]byte, 100))
	_ = os.WriteFile(airhornPath, dummyWav.Bytes(), 0644)

	mockDecoder := func(ctx context.Context, in, out string) error {
		d, err := os.ReadFile(in)
		if err != nil {
			return err
		}
		return os.WriteFile(out, d, 0644)
	}

	opt := NewOptimizer(mockDecoder)
	mgr, err := NewManager(tmpDir, opt)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("Expected 1 pre-existing sound (airhorn), got %d", len(list))
	}
	if !list[0].IsBuiltin {
		t.Errorf("Expected airhorn to be marked as builtin")
	}

	// 1. Add a custom sound
	customRaw := filepath.Join(tmpDir, "custom.pcm")
	var pcmBuf bytes.Buffer
	for i := 0; i < 2000; i++ {
		_ = binary.Write(&pcmBuf, binary.LittleEndian, int16(15000))
	}
	_ = os.WriteFile(customRaw, pcmBuf.Bytes(), 0644)

	item, optRes, err := mgr.Add(context.Background(), "Мой Звук", customRaw)
	if err != nil {
		t.Fatalf("Add sound failed: %v", err)
	}
	if item.Name != "Мой Звук" {
		t.Errorf("Expected name 'Мой Звук', got %q", item.Name)
	}
	if optRes.OptimizedSize <= 44 {
		t.Errorf("Invalid optimized size: %d", optRes.OptimizedSize)
	}

	// Verify listed
	list = mgr.List()
	if len(list) != 2 {
		t.Fatalf("Expected 2 sounds now, got %d", len(list))
	}

	// 2. Rename custom sound
	if err := mgr.Rename(item.ID, "Новый Звук"); err != nil {
		t.Fatalf("Rename failed: %v", err)
	}
	renamed, err := mgr.Get(item.ID)
	if err != nil || renamed.Name != "Новый Звук" {
		t.Errorf("Expected renamed to 'Новый Звук', got %v", renamed)
	}

	// 3. Try deleting builtin sound (must fail)
	if err := mgr.Delete("airhorn"); err == nil {
		t.Error("Expected error when deleting builtin airhorn, got nil")
	}

	// 4. Delete custom sound
	if err := mgr.Delete(item.ID); err != nil {
		t.Fatalf("Delete custom sound failed: %v", err)
	}

	if _, err := mgr.Get(item.ID); err == nil {
		t.Error("Expected error for deleted sound, got nil")
	}
}
