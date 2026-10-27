package netdiag

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PingTarget specifies a host to be pinged.
type PingTarget struct {
	Name string `json:"name"`
	Host string `json:"host"`
}

// PingResult describes the outcome of a ping test against a host.
type PingResult struct {
	Name       string        `json:"name"`
	Host       string        `json:"host"`
	Success    bool          `json:"success"`
	Latency    time.Duration `json:"latency"`
	PacketLoss float64       `json:"packet_loss"`
	Error      string        `json:"error,omitempty"`
}

// WiFiInfo captures wireless connection metrics parsed from `iw dev wlan0 link`.
type WiFiInfo struct {
	Connected bool    `json:"connected"`
	Interface string  `json:"interface"`
	SSID      string  `json:"ssid"`
	BSSID     string  `json:"bssid"`
	Frequency int     `json:"frequency_mhz"`
	Signal    int     `json:"signal_dbm"`
	TxBitrate float64 `json:"tx_bitrate_mbps"`
	RxBytes   int64   `json:"rx_bytes"`
	TxBytes   int64   `json:"tx_bytes"`
	Raw       string  `json:"raw"`
}

// SpeedtestResult summarizes download speed measurement.
type SpeedtestResult struct {
	URL             string        `json:"url"`
	DownloadedBytes int64         `json:"downloaded_bytes"`
	Duration        time.Duration `json:"duration"`
	SpeedMbps       float64       `json:"speed_mbps"`
}

// Diagnostics manages network probing, Wi-Fi link analysis, and internet speed tests.
type Diagnostics struct {
	pingTargets        []PingTarget
	wifiIface          string
	speedtestURL       string
	speedtestChunkSize int64
	httpClient         *http.Client
	execCmd            func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Option configures Diagnostics parameters.
type Option func(*Diagnostics)

// WithPingTargets overrides default ping targets.
func WithPingTargets(targets []PingTarget) Option {
	return func(d *Diagnostics) {
		d.pingTargets = targets
	}
}

// WithWiFiInterface sets the wireless interface name (default "wlan0").
func WithWiFiInterface(iface string) Option {
	return func(d *Diagnostics) {
		d.wifiIface = iface
	}
}

// WithSpeedtestURL configures the download URL used for speed measurement.
func WithSpeedtestURL(url string) Option {
	return func(d *Diagnostics) {
		d.speedtestURL = url
	}
}

// WithSpeedtestChunkSize sets the maximum download bytes for speedtest (e.g. 5MB).
func WithSpeedtestChunkSize(size int64) Option {
	return func(d *Diagnostics) {
		d.speedtestChunkSize = size
	}
}

// WithCommandExecutor configures a custom command runner (for mocking/testing).
func WithCommandExecutor(fn func(ctx context.Context, name string, args ...string) ([]byte, error)) Option {
	return func(d *Diagnostics) {
		d.execCmd = fn
	}
}

// WithHTTPClient overrides the default HTTP client used for speedtest.
func WithHTTPClient(client *http.Client) Option {
	return func(d *Diagnostics) {
		d.httpClient = client
	}
}

// GetDefaultGateway discovers the IPv4 default gateway from /proc/net/route or defaults to 192.168.20.1.
func GetDefaultGateway() string {
	data, err := os.ReadFile("/proc/net/route")
	if err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[1] == "00000000" { // Destination 0.0.0.0 (default gw)
				gwHex := fields[2]
				if len(gwHex) == 8 {
					b, err := hex.DecodeString(gwHex)
					if err == nil && len(b) == 4 {
						return fmt.Sprintf("%d.%d.%d.%d", b[3], b[2], b[1], b[0])
					}
				}
			}
		}
	}
	return "192.168.20.1"
}

// DefaultPingTargets defines typical diagnostic destinations for station mini.
var DefaultPingTargets = []PingTarget{
	{Name: "Шлюз (Роутер)", Host: "192.168.20.1"},
	{Name: "Яндекс DNS", Host: "77.88.8.8"},
	{Name: "Google DNS", Host: "8.8.8.8"},
	{Name: "Telegram API", Host: "149.154.166.110"},
}

// NewDiagnostics creates an instance of Diagnostics.
func NewDiagnostics(opts ...Option) *Diagnostics {
	targets := make([]PingTarget, len(DefaultPingTargets))
	copy(targets, DefaultPingTargets)
	if gw := GetDefaultGateway(); gw != "" {
		for i := range targets {
			if strings.Contains(targets[i].Name, "Шлюз") {
				targets[i].Host = gw
			}
		}
	}

	d := &Diagnostics{
		pingTargets:        targets,
		wifiIface:          "wlan0",
		speedtestURL:       "https://mirror.yandex.ru/debian/ls-lR.gz",
		speedtestChunkSize: 5 * 1024 * 1024, // 5 MB
		execCmd: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			return cmd.CombinedOutput()
		},
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, // Handle minimal embedded Linux CA bundle
				},
				DisableKeepAlives: true,
			},
		},
	}

	for _, opt := range opts {
		opt(d)
	}

	return d
}

// PingHosts executes concurrent pings to all configured targets and returns their latencies.
func (d *Diagnostics) PingHosts(ctx context.Context) ([]PingResult, error) {
	results := make([]PingResult, len(d.pingTargets))
	var wg sync.WaitGroup

	for i, target := range d.pingTargets {
		wg.Add(1)
		go func(idx int, t PingTarget) {
			defer wg.Done()
			results[idx] = d.pingSingle(ctx, t)
		}(i, target)
	}

	wg.Wait()
	return results, nil
}

var (
	packetLossRe    = regexp.MustCompile(`([0-9.]+)%\s*packet loss`)
	rttAvgRe        = regexp.MustCompile(`(?:rtt|round-trip)\s+min/avg/max[^=]*=\s*[0-9.]+/([0-9.]+)/`)
	singleTimeRe    = regexp.MustCompile(`time=([0-9.]+)\s*ms`)
	arpingLatencyRe = regexp.MustCompile(`(?:reply|Reply).*?([0-9.]+)\s*ms`)
	arpingRecvRe    = regexp.MustCompile(`Received\s+([1-9][0-9]*)\s+reply`)
)

func (d *Diagnostics) pingSingle(ctx context.Context, target PingTarget) PingResult {
	res := PingResult{
		Name: target.Name,
		Host: target.Host,
	}

	// Busybox ping: ping -c 2 -W 1 <host>
	out, err := d.execCmd(ctx, "ping", "-c", "2", "-W", "1", target.Host)
	raw := string(out)

	if match := packetLossRe.FindStringSubmatch(raw); len(match) > 1 {
		if loss, parseErr := strconv.ParseFloat(match[1], 64); parseErr == nil {
			res.PacketLoss = loss
		}
	} else if err != nil {
		res.PacketLoss = 100
	}

	// Extract RTT
	if match := rttAvgRe.FindStringSubmatch(raw); len(match) > 1 {
		if latencyMs, parseErr := strconv.ParseFloat(match[1], 64); parseErr == nil {
			res.Latency = time.Duration(latencyMs * float64(time.Millisecond))
			res.Success = true
		}
	} else if match := singleTimeRe.FindStringSubmatch(raw); len(match) > 1 {
		if latencyMs, parseErr := strconv.ParseFloat(match[1], 64); parseErr == nil {
			res.Latency = time.Duration(latencyMs * float64(time.Millisecond))
			res.Success = true
		}
	}

	if err != nil && res.PacketLoss >= 100 {
		res.Success = false
		res.Error = strings.TrimSpace(err.Error())
		if res.Error == "" {
			res.Error = "Хост недоступен"
		}
	} else {
		res.Success = res.PacketLoss < 100
	}

	// Fallback to ARPING for local subnet gateway or LAN targets if ICMP echo is dropped by router firewall
	if (!res.Success || res.PacketLoss >= 100) && d.wifiIface != "" {
		arpOut, arpErr := d.execCmd(ctx, "arping", "-c", "2", "-I", d.wifiIface, target.Host)
		if arpErr == nil || len(arpOut) > 0 {
			rawArp := string(arpOut)
			if m := arpingLatencyRe.FindStringSubmatch(rawArp); len(m) > 1 {
				if latencyMs, parseErr := strconv.ParseFloat(m[1], 64); parseErr == nil {
					res.Latency = time.Duration(latencyMs * float64(time.Millisecond))
					res.Success = true
					res.PacketLoss = 0
					res.Error = ""
				}
			} else if arpingRecvRe.MatchString(rawArp) {
				res.Success = true
				res.PacketLoss = 0
				res.Error = ""
			}
		}
	}

	return res
}

// GetWiFiInfo executes `iw dev wlan0 link` and returns structured Wi-Fi metrics.
func (d *Diagnostics) GetWiFiInfo() (WiFiInfo, error) {
	out, err := d.execCmd(context.Background(), "iw", "dev", d.wifiIface, "link")
	raw := string(out)
	if err != nil && len(raw) == 0 {
		return WiFiInfo{
			Interface: d.wifiIface,
			Connected: false,
			Raw:       err.Error(),
		}, fmt.Errorf("iw command failed: %w", err)
	}

	return ParseWiFiLinkOutput(raw, d.wifiIface), nil
}

var (
	iwBSSIDRe   = regexp.MustCompile(`Connected to ([0-9a-fA-F:]{17})`)
	iwSSIDRe    = regexp.MustCompile(`SSID:\s*(.+)`)
	iwFreqRe    = regexp.MustCompile(`freq:\s*([0-9]+)`)
	iwSignalRe  = regexp.MustCompile(`signal:\s*(-?[0-9]+)\s*dBm`)
	iwTxBitRe   = regexp.MustCompile(`tx bitrate:\s*([0-9.]+)\s*MBit/s`)
	iwRxBytesRe = regexp.MustCompile(`(?:RX|rx bytes):\s*([0-9]+)`)
	iwTxBytesRe = regexp.MustCompile(`(?:TX|tx bytes):\s*([0-9]+)`)
)

// ParseWiFiLinkOutput parses the raw text from `iw dev <iface> link`.
func ParseWiFiLinkOutput(raw, iface string) WiFiInfo {
	info := WiFiInfo{
		Interface: iface,
		Raw:       raw,
	}

	if strings.Contains(raw, "Not connected.") || strings.TrimSpace(raw) == "" {
		info.Connected = false
		return info
	}

	if match := iwBSSIDRe.FindStringSubmatch(raw); len(match) > 1 {
		info.BSSID = strings.ToLower(match[1])
		info.Connected = true
	}

	if match := iwSSIDRe.FindStringSubmatch(raw); len(match) > 1 {
		info.SSID = strings.TrimSpace(match[1])
		info.Connected = true
	}

	if match := iwFreqRe.FindStringSubmatch(raw); len(match) > 1 {
		if freq, err := strconv.Atoi(match[1]); err == nil {
			info.Frequency = freq
		}
	}

	if match := iwSignalRe.FindStringSubmatch(raw); len(match) > 1 {
		if sig, err := strconv.Atoi(match[1]); err == nil {
			info.Signal = sig
		}
	}

	if match := iwTxBitRe.FindStringSubmatch(raw); len(match) > 1 {
		if bit, err := strconv.ParseFloat(match[1], 64); err == nil {
			info.TxBitrate = bit
		}
	}

	if match := iwRxBytesRe.FindStringSubmatch(raw); len(match) > 1 {
		if b, err := strconv.ParseInt(match[1], 10, 64); err == nil {
			info.RxBytes = b
		}
	}

	if match := iwTxBytesRe.FindStringSubmatch(raw); len(match) > 1 {
		if b, err := strconv.ParseInt(match[1], 10, 64); err == nil {
			info.TxBytes = b
		}
	}

	return info
}

// RunSpeedtest downloads a test payload up to speedtestChunkSize and calculates download speed in Mbps.
func (d *Diagnostics) RunSpeedtest(ctx context.Context) (SpeedtestResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.speedtestURL, nil)
	if err != nil {
		return SpeedtestResult{}, fmt.Errorf("failed to create speedtest request: %w", err)
	}

	// Request byte range if server supports it to prevent downloading beyond chunk limit
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", d.speedtestChunkSize-1))
	req.Header.Set("User-Agent", "Yandex-Station-Mini-Speedtest/1.0")

	start := time.Now()
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return SpeedtestResult{}, fmt.Errorf("speedtest download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return SpeedtestResult{}, fmt.Errorf("speedtest server returned HTTP %d", resp.StatusCode)
	}

	// Read limited payload to avoid high memory or bandwidth consumption
	limitedReader := io.LimitReader(resp.Body, d.speedtestChunkSize)
	buf := make([]byte, 32*1024)
	var totalBytes int64

	for {
		n, readErr := limitedReader.Read(buf)
		if n > 0 {
			totalBytes += int64(n)
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return SpeedtestResult{}, fmt.Errorf("error reading speedtest stream: %w", readErr)
		}
	}

	duration := time.Since(start)
	if duration <= 0 {
		duration = time.Millisecond
	}

	speedMbps := (float64(totalBytes) * 8.0) / (duration.Seconds() * 1_000_000.0)

	return SpeedtestResult{
		URL:             d.speedtestURL,
		DownloadedBytes: totalBytes,
		Duration:        duration,
		SpeedMbps:       speedMbps,
	}, nil
}
