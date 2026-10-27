package speaker

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type EQBands struct {
	Band0 float64 // Bass (100 Hz, gain in dB: -24.0 to +12.0)
	Band1 float64 // Mid (1100 Hz, gain in dB: -24.0 to +12.0)
	Band2 float64 // Treble (11000 Hz, gain in dB: -24.0 to +12.0)
}

// EqualizerPresets holds pre-tuned EQ profiles for the Yandex Station Mini.
var EqualizerPresets = map[string]EQBands{
	"balanced":   {Band0: 0.0, Band1: 0.0, Band2: 0.0},
	"bass_boost": {Band0: 7.0, Band1: -1.0, Band2: 1.0},
	"vocal":      {Band0: -4.0, Band1: 6.0, Band2: 2.0},
	"treble":     {Band0: 2.0, Band1: -1.0, Band2: 6.0},
}

// EqualizerPresetNames maps preset IDs to user-friendly display labels.
var EqualizerPresetNames = map[string]string{
	"balanced":   "⚖️ Сбалансированный",
	"bass_boost": "🔊 Усиление басов (Bass Boost)",
	"vocal":      "🗣️ Голос и подкасты (Vocal)",
	"treble":     "🎵 Яркий / Музыка (Treble)",
}

type Controller struct {
	mu         sync.Mutex
	playCmd    *exec.Cmd
	soundsDir  string
	scriptsDir string
	quasarLogs string
	gesturesOn bool
	currentEQ  string

	btMu      sync.Mutex
	btCmd     *exec.Cmd
	btStdin   io.WriteCloser
	btRunning bool
}

func NewController(soundsDir, scriptsDir, quasarLogs string) *Controller {
	return &Controller{
		soundsDir:  soundsDir,
		scriptsDir: scriptsDir,
		quasarLogs: quasarLogs,
		gesturesOn: true,
		currentEQ:  "balanced",
	}
}

// SetEqualizerPreset updates the active equalizer profile.
func (c *Controller) SetEqualizerPreset(presetID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	clean := strings.ToLower(strings.TrimSpace(presetID))
	if _, ok := EqualizerPresets[clean]; !ok {
		return fmt.Errorf("unknown equalizer preset %q (available: balanced, bass_boost, vocal, treble)", presetID)
	}
	c.currentEQ = clean
	return nil
}

// GetEqualizerPreset returns the currently active EQ preset ID.
func (c *Controller) GetEqualizerPreset() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentEQ == "" {
		return "balanced"
	}
	return c.currentEQ
}

func (c *Controller) buildPlaybackPipelineLocked(filePath string) string {
	bands, ok := EqualizerPresets[c.currentEQ]
	if !ok {
		bands = EqualizerPresets["balanced"]
	}
	return fmt.Sprintf("filesrc location=%s ! decodebin ! audioconvert ! equalizer-3bands band0=%.1f band1=%.1f band2=%.1f ! audioresample ! audio/x-raw,rate=48000,channels=2,format=S16LE ! alsasink device=default",
		filePath, bands.Band0, bands.Band1, bands.Band2)
}

// -------------------------------------------------------------
// Audio Playback & TTS via Native Hardware GStreamer Engine
// -------------------------------------------------------------

// PlayAudio plays any audio file (WAV, MP3, OPUS, OGG) using Quasar's native 64-bit GStreamer pipeline with active EQ.
func (c *Controller) PlayAudio(filePath string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stopCurrentPlaybackLocked()

	pipeline := c.buildPlaybackPipelineLocked(filePath)
	cmd := exec.Command("/system/vendor/quasar/maind", "--gst-launch", pipeline)
	cmd.Env = append(os.Environ(),
		"LD_LIBRARY_PATH=/system/vendor/quasar/libs",
		"CA_CERTIFICATES=/system/vendor/quasar/ca-certificates.crt",
	)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start audio player: %w", err)
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

// PlayAudioSync plays an audio file synchronously, waiting for playback to finish before returning.
func (c *Controller) PlayAudioSync(filePath string) error {
	c.mu.Lock()
	c.stopCurrentPlaybackLocked()

	pipeline := c.buildPlaybackPipelineLocked(filePath)
	cmd := exec.Command("/system/vendor/quasar/maind", "--gst-launch", pipeline)
	cmd.Env = append(os.Environ(),
		"LD_LIBRARY_PATH=/system/vendor/quasar/libs",
		"CA_CERTIFICATES=/system/vendor/quasar/ca-certificates.crt",
	)

	c.playCmd = cmd
	c.mu.Unlock()

	err := cmd.Run()

	c.mu.Lock()
	if c.playCmd == cmd {
		c.playCmd = nil
	}
	c.mu.Unlock()

	return err
}

func (c *Controller) PlayWav(path string) error {
	return c.PlayAudio(path)
}

func (c *Controller) PlaySound(soundName string) error {
	path := fmt.Sprintf("%s/%s.wav", c.soundsDir, soundName)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("sound %s not found: %w", soundName, err)
	}
	return c.PlayAudio(path)
}

func (c *Controller) StopPlayback() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopCurrentPlaybackLocked()
}

func (c *Controller) stopCurrentPlaybackLocked() {
	if c.playCmd != nil && c.playCmd.Process != nil {
		_ = c.playCmd.Process.Kill()
		c.playCmd = nil
	}
	_ = exec.Command("pkill", "-9", "-f", "maind --gst-launch").Run()
}

// PlayTTS downloads Google Translate TTS mp3 and plays it directly via native GStreamer
func (c *Controller) PlayTTS(text string) error {
	encText := url.QueryEscape(text)
	// Use translate.google.ru to avoid DPI/ISP connection resets in RU region
	ttsURL := fmt.Sprintf("https://translate.google.ru/translate_tts?ie=UTF-8&q=%s&tl=ru&client=tw-ob", encText)

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
		return fmt.Errorf("tts server returned HTTP %d", resp.StatusCode)
	}

	mp3Data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read tts mp3: %w", err)
	}

	tempMP3 := "/tmp/tts_voice.mp3"
	if err := os.WriteFile(tempMP3, mp3Data, 0644); err != nil {
		return fmt.Errorf("failed to write tts mp3: %w", err)
	}

	return c.PlayAudio(tempMP3)
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
// Bluetooth (A2DP Sink / Broadcom BSA Stack) Control
// -------------------------------------------------------------

// IsBluetoothEnabled checks if the Broadcom Bluetooth stack (bsa_server) is currently running.
func (c *Controller) IsBluetoothEnabled() bool {
	out, err := exec.Command("pidof", "bsa_server").Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// IsBluetoothDiscoverable checks if the bt_demo sink client is active.
func (c *Controller) IsBluetoothDiscoverable() bool {
	out, err := exec.Command("pidof", "bt_demo").Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// GetBluetoothPID returns the process ID of bsa_server or empty string.
func (c *Controller) GetBluetoothPID() string {
	out, err := exec.Command("pidof", "bsa_server").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// GetBluetoothDeviceName returns the broadcast name of the station for pairing.
func (c *Controller) GetBluetoothDeviceName() string {
	data, err := os.ReadFile("/vendor/bsa/bt_config.xml")
	if err != nil {
		return "Yandex Station Mini"
	}
	re := regexp.MustCompile(`<local_name>(.*?)</local_name>`)
	m := re.FindSubmatch(data)
	if len(m) > 1 && len(m[1]) > 0 {
		return string(m[1])
	}
	return "Yandex Station Mini"
}

// SetBluetoothEnabled starts or stops the Broadcom Bluetooth BSA stack and A2DP sink.
func (c *Controller) SetBluetoothEnabled(enable bool) error {
	c.btMu.Lock()
	defer c.btMu.Unlock()

	if !enable {
		if c.btStdin != nil {
			_, _ = fmt.Fprintln(c.btStdin, "99")
			_ = c.btStdin.Close()
			c.btStdin = nil
		}
		if c.btCmd != nil && c.btCmd.Process != nil {
			_ = c.btCmd.Process.Kill()
			c.btCmd = nil
		}
		c.btRunning = false

		_ = exec.Command("killall", "-9", "bt_demo").Run()
		_ = exec.Command("/system/workdir/bin/localSendSocket", "/tmp/RequestIOGuard", "GNOTIFY=BTSYNC_PAIRING_END").Run()
		_ = exec.Command("/system/vendor/quasar/bsa_server_run.sh", "0").Run()
		slog.Info("[Bluetooth] Stack disabled")
		return nil
	}

	// 1. Ensure bt_config.xml is configured for discoverable and connectable mode
	_ = exec.Command("sh", "-c", "sed -i 's/<visible>0<\\/visible>/<visible>1<\\/visible>/g' /vendor/bsa/bt_config.xml; sed -i 's/<connectable>0<\\/connectable>/<connectable>1<\\/connectable>/g' /vendor/bsa/bt_config.xml").Run()

	// 2. Start bsa_server if not already running
	if out, err := exec.Command("pidof", "bsa_server").Output(); err != nil || len(strings.TrimSpace(string(out))) == 0 {
		if err := exec.Command("/system/vendor/quasar/bsa_server_run.sh", "1").Run(); err != nil {
			return fmt.Errorf("failed to start bsa_server: %w", err)
		}
		time.Sleep(1500 * time.Millisecond)
	}

	// 3. Notify RequestIOGuard to enter pairing mode and switch UART clocks
	_ = exec.Command("/system/workdir/bin/localSendSocket", "/tmp/RequestIOGuard", "GNOTIFY=BTSYNC_PAIRING_BEGIN").Run()

	// 4. Kill any orphan bt_demo processes
	_ = exec.Command("killall", "-9", "bt_demo").Run()
	time.Sleep(200 * time.Millisecond)

	// 5. Spawn bt_demo with interactive stdin
	cmd := exec.Command("/system/workdir/bin/bt_demo")
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/system/vendor/quasar/libs")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to open bt_demo stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("failed to open bt_demo stdout: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("failed to start bt_demo: %w", err)
	}

	c.btCmd = cmd
	c.btStdin = stdin
	c.btRunning = true

	// Monitor bt_demo output in background
	go func(r io.Reader) {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "sink") || strings.Contains(line, "volume") || strings.Contains(line, "Dev:") || strings.Contains(line, "connected") {
				slog.Info("[Bluetooth]", "event", line)
			}
		}
	}(stdout)

	// Send Sink Service start (1) and Connectable/Discoverable (3 -> 1, 1) commands progressively
	go func(in io.Writer) {
		time.Sleep(600 * time.Millisecond)
		_, _ = fmt.Fprintln(in, "1") // 1 => Start Sink Service
		time.Sleep(800 * time.Millisecond)
		_, _ = fmt.Fprintln(in, "3") // 3 => Set connectable/discoverable
		time.Sleep(400 * time.Millisecond)
		_, _ = fmt.Fprintln(in, "1") // Choose discoverable: 1
		time.Sleep(400 * time.Millisecond)
		_, _ = fmt.Fprintln(in, "1") // Choose connectable: 1
		slog.Info("[Bluetooth] Initialized A2DP Sink and set Discoverable/Connectable")
	}(stdin)

	return nil
}

// RestartBluetooth cleanly cycles the Bluetooth stack.
func (c *Controller) RestartBluetooth() error {
	_ = c.SetBluetoothEnabled(false)
	time.Sleep(1 * time.Second)
	return c.SetBluetoothEnabled(true)
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
