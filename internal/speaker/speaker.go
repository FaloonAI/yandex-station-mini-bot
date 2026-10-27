package speaker

import (
	"bytes"
	"crypto/tls"
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

	"github.com/gen2brain/alsa"
	"github.com/hajimehoshi/go-mp3"
	"github.com/kazzmir/opus-go/ogg"
	"github.com/kazzmir/opus-go/opus"
	"github.com/kazzmir/opus-go/wav"
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

	cmd := exec.Command("/system/workdir/bin/aplay", "-D", "default", path)
	cmd.Env = append(os.Environ(),
		"ALSA_CONFIG_PATH=/tmp/alsa/alsa.conf",
		"ALSA_CONFIG_DIR=/tmp/alsa",
		"LD_LIBRARY_PATH=/system/workdir/lib:/system/vendor/quasar/libs",
	)

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
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

	// Embedded Buildroot system has outdated root CA certificates; use InsecureSkipVerify
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

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

// DecodeOggOpusToWav decodes an Ogg Opus audio file into a 48kHz WAV file using pure Go
func DecodeOggOpusToWav(inOggPath, outWavPath string) error {
	in, err := os.Open(inOggPath)
	if err != nil {
		return fmt.Errorf("failed to open opus file: %w", err)
	}
	defer in.Close()

	r, err := ogg.NewOpusReader(in)
	if err != nil {
		return fmt.Errorf("failed to create opus reader: %w", err)
	}

	dec, err := opus.NewDecoderFromHead(r.Head)
	if err != nil {
		return fmt.Errorf("failed to create opus decoder: %w", err)
	}
	defer dec.Close()

	outf, err := os.Create(outWavPath)
	if err != nil {
		return fmt.Errorf("failed to create wav output: %w", err)
	}
	defer outf.Close()

	channels := int(r.Head.Channels)
	if channels <= 0 {
		channels = 1
	}

	ww, err := wav.NewWriter(outf, 48000, channels)
	if err != nil {
		return fmt.Errorf("failed to create wav writer: %w", err)
	}
	defer ww.Close()

	maxFrame := 5760
	pcm := make([]int16, maxFrame*channels)
	preSkipRemaining := int(r.Head.PreSkip)

	for {
		pkt, err := r.ReadAudioPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read audio packet: %w", err)
		}

		n, err := dec.Decode(pkt.Data, pcm, maxFrame, false)
		if err != nil {
			return fmt.Errorf("decode opus packet: %w", err)
		}

		frames := pcm[:n*channels]
		if preSkipRemaining > 0 {
			skip := preSkipRemaining
			if skip > n {
				skip = n
			}
			preSkipRemaining -= skip
			drop := skip * channels
			if drop >= len(frames) {
				continue
			}
			frames = frames[drop:]
		}

		if len(frames) > 0 {
			if err := ww.WriteInt16PCM(frames); err != nil {
				return fmt.Errorf("write pcm: %w", err)
			}
		}
	}

	return nil
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
				cmd := exec.Command("/system/workdir/bin/aplay", "-D", "default", sirenPath)
				cmd.Env = append(os.Environ(),
					"ALSA_CONFIG_PATH=/tmp/alsa/alsa.conf",
					"ALSA_CONFIG_DIR=/tmp/alsa",
					"LD_LIBRARY_PATH=/system/workdir/lib:/system/vendor/quasar/libs",
				)
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
		arg = "off"
	}
	cmd := exec.Command("/system/workdir/bin/amixer", "sset", "SPK mute", arg)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/system/vendor/quasar/libs")
	return cmd.Run()
}

// -------------------------------------------------------------
// Pure Go Hardware Microphone Recording (hw:0,2 PDM Array)
// -------------------------------------------------------------

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

	// 3. Downmix captured 8-channel S32 samples to 1-channel 16-bit WAV with volume normalization
	actualFrames := uint32(readFrames)
	if actualFrames > totalFrames {
		actualFrames = totalFrames
	}

	monoSamps := make([]int16, actualFrames)
	var maxAbs float64 = 1.0

	for i := uint32(0); i < actualFrames; i++ {
		offset := i * 8
		ch0 := float64(rawSamples[offset+0])
		ch1 := float64(rawSamples[offset+1])
		avg := (ch0 + ch1) / 2.0
		// Convert from 32-bit signed to 16-bit range
		val16 := avg / 65536.0
		if math.Abs(val16) > maxAbs {
			maxAbs = math.Abs(val16)
		}
		monoSamps[i] = int16(val16)
	}

	// Normalize peak gain for loud, clear room audio
	if maxAbs > 10.0 {
		gain := 31000.0 / maxAbs
		for i := range monoSamps {
			norm := float64(monoSamps[i]) * gain
			if norm > 32767 {
				norm = 32767
			} else if norm < -32768 {
				norm = -32768
			}
			monoSamps[i] = int16(norm)
		}
	}

	outBuf := new(bytes.Buffer)
	for _, s := range monoSamps {
		_ = binary.Write(outBuf, binary.LittleEndian, s)
	}

	if err := writeWAVFile(cleanFile, outBuf.Bytes(), 48000, 1, 16); err != nil {
		return "", fmt.Errorf("failed to save wav: %w", err)
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
	baseOutput := cleanFastfetchOutput(string(out))
	if err != nil || strings.TrimSpace(baseOutput) == "" {
		baseOutput = c.getFallbackSysInfo()
	}

	// Enrich with detailed hardware telemetry
	extra := c.getHardwareTelemetry()
	if extra != "" {
		return baseOutput + "\n" + extra, nil
	}
	return baseOutput, nil
}

func (c *Controller) getHardwareTelemetry() string {
	var sb strings.Builder

	// 1. CPU Temperature
	tempC := "N/A"
	if data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp"); err == nil {
		raw := strings.TrimSpace(string(data))
		if val, err := strconv.Atoi(raw); err == nil && val > 0 {
			tempC = fmt.Sprintf("%.1f°C", float64(val)/1000.0)
		}
	}

	// 2. Load average
	loadAvg := "N/A"
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			loadAvg = fmt.Sprintf("%s, %s, %s", fields[0], fields[1], fields[2])
		}
	}

	// 3. Hardware model & SoC details
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("📟 Модель: Яндекс Станция Mini (YNDX-0004)\n")
	sb.WriteString("⚡ SoC: Amlogic Meson A113X (4x Cortex-A53 @ 1.20GHz)\n")
	sb.WriteString(fmt.Sprintf("🌡️ Температура CPU: %s\n", tempC))
	sb.WriteString(fmt.Sprintf("📈 Load Average: %s", loadAvg))

	return sb.String()
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
		return "📶 Wi-Fi: нет данных", nil
	}

	ssid := ""
	ip := ""
	freq := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "ssid=") {
			ssid = strings.TrimPrefix(line, "ssid=")
		}
		if strings.HasPrefix(line, "ip_address=") {
			ip = strings.TrimPrefix(line, "ip_address=")
		}
		if strings.HasPrefix(line, "freq=") {
			freq = strings.TrimPrefix(line, "freq=") + " MHz"
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

	freqStr := ""
	if freq != "" {
		freqStr = fmt.Sprintf(" (%s)", freq)
	}

	return fmt.Sprintf("📶 Сеть: %s%s\n📡 Сигнал: %s\n🌐 IP: %s", ssid, freqStr, rssi, ip), nil
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
