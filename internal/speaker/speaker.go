package speaker

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/go-mp3"
)

type Controller struct {
	mu           sync.Mutex
	playCmd      *exec.Cmd
	soundsDir    string
	scriptsDir   string
	quasarLogs   string
	gesturesOn   bool
	alarmRunning bool
	stopAlarmCh  chan struct{}
}

func NewController(soundsDir, scriptsDir, quasarLogs string) *Controller {
	return &Controller{
		soundsDir:   soundsDir,
		scriptsDir:  scriptsDir,
		quasarLogs:  quasarLogs,
		gesturesOn:  true,
		stopAlarmCh: make(chan struct{}),
	}
}

// -------------------------------------------------------------
// Audio Playback & TTS
// -------------------------------------------------------------

func (c *Controller) PlayWav(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stopCurrentPlaybackLocked()

	cmd := exec.Command("/system/workdir/bin/aplay", "-D", "plughw:0,0", path)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/system/vendor/quasar/libs")

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start aplay: %w", err)
	}
	c.playCmd = cmd

	go func() {
		_ = cmd.Wait()
		c.mu.Lock()
		if c.playCmd == cmd {
			c.playCmd = nil
		}
		c.mu.Unlock()
	}()

	return nil
}

func (c *Controller) PlaySound(soundName string) error {
	path := fmt.Sprintf("%s/%s.wav", c.soundsDir, soundName)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("sound %s not found: %w", soundName, err)
	}
	return c.PlayWav(path)
}

func (c *Controller) StopPlayback() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopCurrentPlaybackLocked()
}

func (c *Controller) stopCurrentPlaybackLocked() {
	if c.alarmRunning {
		close(c.stopAlarmCh)
		c.stopAlarmCh = make(chan struct{})
		c.alarmRunning = false
	}
	if c.playCmd != nil && c.playCmd.Process != nil {
		_ = c.playCmd.Process.Kill()
		c.playCmd = nil
	}
	_ = exec.Command("killall", "-9", "aplay").Run()
}

// PlayTTS downloads Google Translate TTS mp3, decodes in memory to 16-bit WAV and plays
func (c *Controller) PlayTTS(text string) error {
	encText := url.QueryEscape(text)
	ttsURL := fmt.Sprintf("https://translate.google.com/translate_tts?ie=UTF-8&q=%s&tl=ru&client=tw-ob", encText)

	req, err := http.NewRequest(http.MethodGet, ttsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("tts request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tts server returned %d", resp.StatusCode)
	}

	mp3Data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read tts mp3: %w", err)
	}

	decoder, err := mp3.NewDecoder(bytes.NewReader(mp3Data))
	if err != nil {
		return fmt.Errorf("failed to create mp3 decoder: %w", err)
	}

	rawPCM, err := io.ReadAll(decoder)
	if err != nil {
		return fmt.Errorf("failed to decode mp3 to pcm: %w", err)
	}

	tempWav := "/tmp/tts_voice.wav"
	if err := writeWAVFile(tempWav, rawPCM, decoder.SampleRate(), 2, 16); err != nil {
		return fmt.Errorf("failed to write wav: %w", err)
	}

	return c.PlayWav(tempWav)
}

// StartSiren plays siren in a continuous loop until stopped
func (c *Controller) StartSiren() {
	c.mu.Lock()
	if c.alarmRunning {
		c.mu.Unlock()
		return
	}
	c.alarmRunning = true
	stopCh := c.stopAlarmCh
	c.mu.Unlock()

	sirenPath := fmt.Sprintf("%s/siren.wav", c.soundsDir)

	go func() {
		// Set volume to 100% for alarm
		_ = c.SetVolume(100)
		for {
			select {
			case <-stopCh:
				return
			default:
				cmd := exec.Command("/system/workdir/bin/aplay", "-D", "plughw:0,0", sirenPath)
				cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/system/vendor/quasar/libs")
				_ = cmd.Run()
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()
}

func (c *Controller) StopSiren() {
	c.StopPlayback()
}

func (c *Controller) IsSirenActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.alarmRunning
}

// -------------------------------------------------------------
// Volume & Mute Controls
// -------------------------------------------------------------

var volRegex = regexp.MustCompile(`\[(\d+)%\]`)

func (c *Controller) GetVolume() (int, bool, error) {
	cmd := exec.Command("/system/workdir/bin/amixer", "sget", "Master")
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/system/vendor/quasar/libs")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, false, fmt.Errorf("amixer error: %w", err)
	}

	matches := volRegex.FindStringSubmatch(string(out))
	if len(matches) < 2 {
		return 0, false, fmt.Errorf("could not parse volume from: %s", string(out))
	}
	vol, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, false, err
	}

	isMuted := false
	cmdMute := exec.Command("/system/workdir/bin/amixer", "sget", "SPK mute")
	cmdMute.Env = append(os.Environ(), "LD_LIBRARY_PATH=/system/vendor/quasar/libs")
	if outMute, err := cmdMute.CombinedOutput(); err == nil {
		if strings.Contains(string(outMute), "[off]") {
			isMuted = true
		}
	}

	return vol, isMuted, nil
}

func (c *Controller) SetVolume(percent int) error {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	cmd := exec.Command("/system/workdir/bin/amixer", "sset", "Master", fmt.Sprintf("%d%%", percent))
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/system/vendor/quasar/libs")
	return cmd.Run()
}

func (c *Controller) SetMute(mute bool) error {
	arg := "on"
	if mute {
		arg = "off" // In ALSA for this driver, 'off' disables playback on SPK
	}
	cmd := exec.Command("/system/workdir/bin/amixer", "sset", "SPK mute", arg)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/system/vendor/quasar/libs")
	return cmd.Run()
}

// -------------------------------------------------------------
// Microphone Recording (8ch S32_LE downmixed to 1ch S16_LE WAV)
// -------------------------------------------------------------

func (c *Controller) RecordRoom(durationSec int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	rawFile := "/tmp/rec_room_raw.wav"
	cleanFile := "/tmp/rec_room_mono.wav"
	_ = os.Remove(rawFile)
	_ = os.Remove(cleanFile)

	scriptPath := fmt.Sprintf("%s/record_helper.sh", c.scriptsDir)
	cmd := exec.Command("sh", scriptPath, strconv.Itoa(durationSec), rawFile)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/system/vendor/quasar/libs")
	
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("record_helper failed: %w", err)
	}

	info, err := os.Stat(rawFile)
	if err != nil || info.Size() < 2000 {
		return "", fmt.Errorf("recorded file missing or too small")
	}

	// Downmix 8-channel S32_LE to clean Mono 16-bit WAV with normalization
	if err := downmix8chToMono16(rawFile, cleanFile); err != nil {
		return "", fmt.Errorf("audio downmix failed: %w", err)
	}

	return cleanFile, nil
}

// -------------------------------------------------------------
// System Controls & Diagnostics
// -------------------------------------------------------------

func (c *Controller) GetFastfetch() (string, error) {
	fastfetchBin := "/data/opt/bin/fastfetch"
	if _, err := os.Stat(fastfetchBin); err != nil {
		fastfetchBin = "/data/opt/lib/fastfetch.bin"
	}
	cmd := exec.Command(fastfetchBin, "--pipe", "false")
	cmd.Env = append(os.Environ(), "PATH=/data/opt/bin:/usr/bin:/bin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback simple system info
		return c.getFallbackSysInfo(), nil
	}
	return cleanFastfetchOutput(string(out)), nil
}

func (c *Controller) getFallbackSysInfo() string {
	cmd := exec.Command("sh", "-c", "uname -a; uptime; free -m; df -h /bkupgrade")
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func (c *Controller) GetWiFiStatus() (string, error) {
	cmd := exec.Command("wpa_cli", "-iwlan0", "status")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "Wi-Fi: нет данных", nil
	}

	ssid := ""
	ip := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "ssid=") {
			ssid = strings.TrimPrefix(line, "ssid=")
		}
		if strings.HasPrefix(line, "ip_address=") {
			ip = strings.TrimPrefix(line, "ip_address=")
		}
	}

	rssi := "N/A"
	if data, err := os.ReadFile("/proc/net/wireless"); err == nil {
		lines := strings.Split(string(data), "\n")
		if len(lines) >= 3 {
			fields := strings.Fields(lines[2])
			if len(fields) >= 4 {
				rssi = fields[3] + " dBm"
			}
		}
	}

	return fmt.Sprintf("📶 Сеть: %s\n📡 Сигнал: %s\n🌐 IP: %s", ssid, rssi, ip), nil
}

func (c *Controller) SetGesturesEnabled(enable bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gesturesOn = enable

	sig := "-STOP"
	if enable {
		sig = "-CONT"
	}
	return exec.Command("killall", sig, "input_eventd").Run()
}

func (c *Controller) AreGesturesEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gesturesOn
}

func (c *Controller) DropCaches() error {
	return exec.Command("sh", "-c", "echo 3 > /proc/sys/vm/drop_caches").Run()
}

func (c *Controller) Reboot() error {
	go func() {
		time.Sleep(1 * time.Second)
		_ = exec.Command("reboot").Run()
	}()
	return nil
}

// -------------------------------------------------------------
// Alice Dialogue History
// -------------------------------------------------------------

var dialogRegex = regexp.MustCompile(`(Recognized:.*?|User:.*?|Directive:.*?)`)

func (c *Controller) GetLatestDialogs(limit int) ([]string, error) {
	data, err := os.ReadFile(c.quasarLogs)
	if err != nil {
		return nil, fmt.Errorf("cannot read quasar logs: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	var results []string

	for i := len(lines) - 1; i >= 0 && len(results) < limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.Contains(line, "hypothesis") || strings.Contains(line, "recognized") || strings.Contains(line, "onVoiceStarted") {
			results = append(results, line)
		}
	}

	if len(results) == 0 {
		return []string{"Диалоги не найдены или лог очищен."}, nil
	}

	return results, nil
}

// -------------------------------------------------------------
// Audio Downmixer Helper (8ch 32-bit LE -> 1ch 16-bit LE)
// -------------------------------------------------------------

func downmix8chToMono16(inFile, outFile string) error {
	f, err := os.Open(inFile)
	if err != nil {
		return err
	}
	defer f.Close()

	// Read and skip WAV header (44 bytes)
	header := make([]byte, 44)
	if _, err := io.ReadFull(f, header); err != nil {
		return err
	}

	rawBytes, err := io.ReadAll(f)
	if err != nil {
		return err
	}

	// 8 channels, 4 bytes per sample = 32 bytes per audio frame
	numFrames := len(rawBytes) / 32
	if numFrames == 0 {
		return fmt.Errorf("no audio frames found")
	}

	monoSamples := make([]int16, numFrames)
	var maxAbs float64 = 1.0

	// Extract channel 0 and 1, convert from S32 to S16
	for i := 0; i < numFrames; i++ {
		offset := i * 32
		ch0 := int32(binary.LittleEndian.Uint32(rawBytes[offset : offset+4]))
		ch1 := int32(binary.LittleEndian.Uint32(rawBytes[offset+4 : offset+8]))

		avg := (float64(ch0) + float64(ch1)) / 2.0
		// Convert 32-bit signed float to 16-bit range
		val16 := avg / 65536.0
		if math.Abs(val16) > maxAbs {
			maxAbs = math.Abs(val16)
		}
		monoSamples[i] = int16(val16)
	}

	// Normalize volume to 95% peak
	if maxAbs > 10.0 {
		gain := 31000.0 / maxAbs
		for i := 0; i < numFrames; i++ {
			norm := float64(monoSamples[i]) * gain
			if norm > 32767 {
				norm = 32767
			} else if norm < -32768 {
				norm = -32768
			}
			monoSamples[i] = int16(norm)
		}
	}

	outBuf := new(bytes.Buffer)
	for _, s := range monoSamples {
		_ = binary.Write(outBuf, binary.LittleEndian, s)
	}

	return writeWAVFile(outFile, outBuf.Bytes(), 48000, 1, 16)
}

func writeWAVFile(filePath string, pcmData []byte, sampleRate, numChannels, bitsPerSample int) error {
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	byteRate := sampleRate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8
	dataSize := len(pcmData)
	chunkSize := 36 + dataSize

	// Write RIFF header
	_, _ = f.WriteString("RIFF")
	_ = binary.Write(f, binary.LittleEndian, uint32(chunkSize))
	_, _ = f.WriteString("WAVEfmt ")
	_ = binary.Write(f, binary.LittleEndian, uint32(16)) // subchunk1 size
	_ = binary.Write(f, binary.LittleEndian, uint16(1))  // PCM
	_ = binary.Write(f, binary.LittleEndian, uint16(numChannels))
	_ = binary.Write(f, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(f, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(f, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(f, binary.LittleEndian, uint16(bitsPerSample))
	_, _ = f.WriteString("data")
	_ = binary.Write(f, binary.LittleEndian, uint32(dataSize))

	_, err = f.Write(pcmData)
	return err
}

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func cleanFastfetchOutput(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}
