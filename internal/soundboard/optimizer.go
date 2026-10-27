package soundboard

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	TargetSampleRate  = 22050 // Mono 22.05 kHz: ideal frequency bandwidth for single Station Mini speaker
	TargetChannels    = 1     // Mono channel: saves 50% storage and RAM
	TargetBitsPerSamp = 16    // 16-bit linear PCM
	MaxDurationSec    = 15.0  // Max duration cap in seconds
	MaxSamples        = int(MaxDurationSec * TargetSampleRate) // 330,750 samples max
	SilenceThreshold  = 250   // Amplitude threshold for silence gate out of 32767 (~ -42 dBFS)
	TargetPeakAmp     = 28500 // Target peak amplitude (~ -1.2 dBFS headroom against clipping)
	MaxGainMultiplier = 4.0   // Max +12 dB automatic digital gain to avoid amplifying background noise
)

// OptimizationResult contains statistics of the audio optimization process.
type OptimizationResult struct {
	OriginalSize   int64   `json:"original_size"`
	OptimizedSize  int64   `json:"optimized_size"`
	DurationSec    float64 `json:"duration_sec"`
	SavedBytes     int64   `json:"saved_bytes"`
	SavedPercent   float64 `json:"saved_percent"`
	GainMultiplier float64 `json:"gain_multiplier"`
}

// AudioDecoder defines the interface for converting any input audio into raw mono S16LE PCM.
type AudioDecoder func(ctx context.Context, inputPath, pcmOutputPath string) error

// DefaultAudioDecoder runs the native GStreamer decodebin pipeline on Linux A113X,
// or reads WAV PCM if GStreamer is not available.
func DefaultAudioDecoder(ctx context.Context, inputPath, pcmOutputPath string) error {
	maindPath := "/system/vendor/quasar/maind"
	if _, err := os.Stat(maindPath); err == nil {
		// Native Quasar GStreamer decodebin pipeline: converts MP3/OGG/OPUS/AAC/FLAC/WAV to raw S16LE mono 22050Hz
		pipeline := fmt.Sprintf("filesrc location=%s ! decodebin ! audioconvert ! audioresample ! audio/x-raw,rate=%d,channels=%d,format=S16LE ! filesink location=%s",
			inputPath, TargetSampleRate, TargetChannels, pcmOutputPath)

		cmd := exec.CommandContext(ctx, maindPath, "--gst-launch", pipeline)
		cmd.Env = append(os.Environ(),
			"LD_LIBRARY_PATH=/system/vendor/quasar/libs",
			"CA_CERTIFICATES=/system/vendor/quasar/ca-certificates.crt",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("gstreamer decode error: %w (output: %s)", err, string(out))
		}
		return nil
	}

	// Fallback for development/testing when input is already a WAV file or simple PCM
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}

	// If it's a WAV file with header >= 44 bytes, strip header
	if len(data) > 44 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
		data = data[44:]
	}

	return os.WriteFile(pcmOutputPath, data, 0644)
}

// Optimizer processes, filters, normalizes, and packages audio into optimized WAV files.
type Optimizer struct {
	decoder AudioDecoder
}

// NewOptimizer creates a new sound optimizer.
func NewOptimizer(decoder AudioDecoder) *Optimizer {
	if decoder == nil {
		decoder = DefaultAudioDecoder
	}
	return &Optimizer{decoder: decoder}
}

// Optimize processes an audio file through silence stripping, peak normalization, duration capping,
// and writes an optimized standard WAV file.
func (opt *Optimizer) Optimize(ctx context.Context, inputPath, outputPath string) (*OptimizationResult, error) {
	origInfo, err := os.Stat(inputPath)
	if err != nil {
		return nil, fmt.Errorf("input audio file not found: %w", err)
	}
	originalSize := origInfo.Size()

	tmpPCM := filepath.Join(os.TempDir(), fmt.Sprintf("opt_temp_%d.pcm", origInfo.ModTime().UnixNano()))
	defer os.Remove(tmpPCM)

	// 1. Decode input audio into raw mono 22050Hz 16-bit PCM
	if err := opt.decoder(ctx, inputPath, tmpPCM); err != nil {
		return nil, fmt.Errorf("audio decoding failed: %w", err)
	}

	rawBytes, err := os.ReadFile(tmpPCM)
	if err != nil {
		return nil, fmt.Errorf("failed to read decoded PCM: %w", err)
	}

	if len(rawBytes) < 2 {
		return nil, fmt.Errorf("decoded audio is empty")
	}

	// 2. Parse 16-bit little endian samples
	sampleCount := len(rawBytes) / 2
	samples := make([]int16, sampleCount)
	for i := 0; i < sampleCount; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(rawBytes[i*2 : i*2+2]))
	}

	// 3. Silence Gate: strip leading and trailing silence
	trimmed := stripSilence(samples, SilenceThreshold)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("audio file contains only silence")
	}

	// 4. Duration Cap: enforce max duration (15s) with soft fade-out
	if len(trimmed) > MaxSamples {
		trimmed = trimmed[:MaxSamples]
		applyFadeOut(trimmed, TargetSampleRate/10) // 100ms fade out to avoid clicks
	}

	// 5. Peak Normalization & Dynamic Range Gain
	gain := calculateGain(trimmed, TargetPeakAmp, MaxGainMultiplier)
	applyGain(trimmed, gain)

	// 6. Write final WAV with 44-byte standard header
	var wavBuf bytes.Buffer
	pcmByteCount := len(trimmed) * 2
	if err := WriteWAVHeader(&wavBuf, pcmByteCount, TargetSampleRate, TargetChannels, TargetBitsPerSamp); err != nil {
		return nil, err
	}

	for _, s := range trimmed {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(s))
		wavBuf.Write(b[:])
	}

	if err := os.WriteFile(outputPath, wavBuf.Bytes(), 0644); err != nil {
		return nil, fmt.Errorf("failed to save optimized wav: %w", err)
	}

	optSize := int64(wavBuf.Len())
	savedBytes := originalSize - optSize
	var savedPercent float64
	if originalSize > 0 {
		savedPercent = math.Max(0, float64(savedBytes)/float64(originalSize)*100.0)
	}

	durationSec := float64(len(trimmed)) / float64(TargetSampleRate)

	return &OptimizationResult{
		OriginalSize:   originalSize,
		OptimizedSize:  optSize,
		DurationSec:    durationSec,
		SavedBytes:     savedBytes,
		SavedPercent:   savedPercent,
		GainMultiplier: gain,
	}, nil
}

// stripSilence finds the first and last non-silent samples and trims with 50ms padding.
func stripSilence(samples []int16, threshold int16) []int16 {
	first := -1
	last := -1

	for i, s := range samples {
		val := s
		if val < 0 {
			val = -val
		}
		if val >= threshold {
			if first == -1 {
				first = i
			}
			last = i
		}
	}

	if first == -1 || last == -1 {
		return nil
	}

	// 50ms padding (1102 samples at 22050Hz)
	pad := TargetSampleRate * 5 / 100
	first = int(math.Max(0, float64(first-pad)))
	last = int(math.Min(float64(len(samples)-1), float64(last+pad)))

	return samples[first : last+1]
}

// calculateGain determines the scaling factor to bring peak amplitude to target.
func calculateGain(samples []int16, targetPeak int, maxGain float64) float64 {
	var maxVal int16
	for _, s := range samples {
		val := s
		if val < 0 {
			val = -val
		}
		if val > maxVal {
			maxVal = val
		}
	}

	if maxVal == 0 {
		return 1.0
	}

	gain := float64(targetPeak) / float64(maxVal)
	if gain > maxGain {
		gain = maxGain
	}
	if gain < 0.1 {
		gain = 0.1
	}
	return gain
}

// applyGain scales all samples by the gain factor with soft clipping.
func applyGain(samples []int16, gain float64) {
	if math.Abs(gain-1.0) < 0.01 {
		return
	}
	for i, s := range samples {
		v := float64(s) * gain
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		samples[i] = int16(v)
	}
}

// applyFadeOut applies a linear fade-out to the last fadeSamples of the audio.
func applyFadeOut(samples []int16, fadeSamples int) {
	n := len(samples)
	if fadeSamples > n {
		fadeSamples = n
	}
	start := n - fadeSamples
	for i := 0; i < fadeSamples; i++ {
		factor := float64(fadeSamples-i) / float64(fadeSamples)
		samples[start+i] = int16(float64(samples[start+i]) * factor)
	}
}
