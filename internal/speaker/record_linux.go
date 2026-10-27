//go:build linux

package speaker

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gen2brain/alsa"
)

// RecordRoom captures audio from the Station Mini 1's 8-channel PDM hardware microphone array (hw:0,2).
// Applies transient startup frame drop, DC-mean removal, smoothed high-pass filtering, and audio normalization.
func (c *Controller) RecordRoom(durationSec int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cleanFile := "/tmp/rec_room_mono.wav"
	_ = os.Remove(cleanFile)

	if durationSec <= 0 {
		durationSec = 5
	}
	if durationSec > 30 {
		durationSec = 30
	}

	// 1. Temporarily pause launcher and kill maind so hw:0,2 is released
	launcherPIDBytes, _ := exec.Command("pidof", "quasar_launcher2").Output()
	launcherPID := strings.TrimSpace(string(launcherPIDBytes))
	if launcherPID != "" {
		_ = exec.Command("kill", "-STOP", launcherPID).Run()
		defer func() {
			_ = exec.Command("kill", "-CONT", launcherPID).Run()
		}()
	}

	_ = exec.Command("killall", "-9", "maind").Run()
	time.Sleep(300 * time.Millisecond)

	// 2. Open hardware device hw:0,2 with MMAP in pure Go
	cfg := alsa.Config{
		Channels:    8,
		Rate:        48000,
		PeriodSize:  256,
		PeriodCount: 4,
		Format:      alsa.SNDRV_PCM_FORMAT_S32_LE,
	}

	pcm, err := alsa.PcmOpenByName("hw:0,2", alsa.PCM_IN|alsa.PCM_MMAP, &cfg)
	if err != nil {
		return "", fmt.Errorf("failed to open microphone array: %w", err)
	}
	defer pcm.Close()

	totalFrames := uint32(durationSec * 48000)
	rawSamples := make([]int32, totalFrames*8)

	readFrames, err := pcm.MmapRead(rawSamples)
	if err != nil && readFrames == 0 {
		return "", fmt.Errorf("microphone capture failed: %w", err)
	}

	actualFrames := uint32(readFrames)
	// Skip 500ms (24000 frames) to completely discard PDM hardware turn-on transient
	const skipFrames = uint32(24000)
	if actualFrames <= skipFrames+4800 {
		return "", fmt.Errorf("insufficient frames recorded (%d)", actualFrames)
	}

	validFrames := actualFrames - skipFrames

	// 3. Compute global DC bias mean across microphones 0..3
	var sum float64 = 0.0
	for i := uint32(0); i < validFrames; i++ {
		offset := (i + skipFrames) * 8
		avg := (float64(rawSamples[offset]) + float64(rawSamples[offset+1]) + float64(rawSamples[offset+2]) + float64(rawSamples[offset+3])) / 4.0
		sum += avg
	}
	meanDC := sum / float64(validFrames)

	// 4. Extract mono signal, subtract DC offset, apply 1-pole high-pass filter (cutoff ~150Hz), find peak
	filtered := make([]float64, validFrames)
	var maxAbs float64 = 1000000.0 // Noise floor floor to prevent over-amplification of silence
	alpha := 0.980                 // Cutoff ~150Hz at 48kHz
	prevX := 0.0
	prevY := 0.0

	for i := uint32(0); i < validFrames; i++ {
		offset := (i + skipFrames) * 8
		rawMono := (float64(rawSamples[offset]) + float64(rawSamples[offset+1]) + float64(rawSamples[offset+2]) + float64(rawSamples[offset+3])) / 4.0
		x := rawMono - meanDC

		y := alpha * (prevY + x - prevX)
		prevX = x
		prevY = y

		filtered[i] = y
		absVal := math.Abs(y)
		if absVal > maxAbs {
			maxAbs = absVal
		}
	}

	// 5. Normalize dynamic range and encode to 16-bit PCM WAV
	pcm16 := make([]byte, validFrames*2)
	scale := 28000.0 / maxAbs // Target ~85% FS

	for i := uint32(0); i < validFrames; i++ {
		s := filtered[i] * scale
		if s > 32767 {
			s = 32767
		} else if s < -32768 {
			s = -32768
		}
		binary.LittleEndian.PutUint16(pcm16[i*2:], uint16(int16(s)))
	}

	if err := writeWAVFile(cleanFile, pcm16, 48000, 1, 16); err != nil {
		return "", fmt.Errorf("failed to save wav: %w", err)
	}

	return cleanFile, nil
}
