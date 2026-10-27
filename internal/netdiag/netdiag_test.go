package netdiag

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sampleIWConnected = `Connected to 12:34:56:78:9a:bc (on wlan0)
	SSID: Keenetic-Hero-5G
	freq: 5240
	RX: 987654321 bytes (123456 packets)
	TX: 123456789 bytes (65432 packets)
	signal: -53 dBm
	tx bitrate: 144.4 MBit/s
`

const sampleIWDisconnected = `Not connected.
`

func TestParseWiFiLinkOutput(t *testing.T) {
	t.Run("Connected", func(t *testing.T) {
		info := ParseWiFiLinkOutput(sampleIWConnected, "wlan0")
		if !info.Connected {
			t.Fatal("expected Connected = true")
		}
		if info.SSID != "Keenetic-Hero-5G" {
			t.Errorf("expected SSID 'Keenetic-Hero-5G', got %q", info.SSID)
		}
		if info.BSSID != "12:34:56:78:9a:bc" {
			t.Errorf("expected BSSID '12:34:56:78:9a:bc', got %q", info.BSSID)
		}
		if info.Frequency != 5240 {
			t.Errorf("expected freq 5240, got %d", info.Frequency)
		}
		if info.Signal != -53 {
			t.Errorf("expected signal -53, got %d", info.Signal)
		}
		if info.TxBitrate != 144.4 {
			t.Errorf("expected tx bitrate 144.4, got %f", info.TxBitrate)
		}
		if info.RxBytes != 987654321 {
			t.Errorf("expected RxBytes 987654321, got %d", info.RxBytes)
		}
		if info.TxBytes != 123456789 {
			t.Errorf("expected TxBytes 123456789, got %d", info.TxBytes)
		}
	})

	t.Run("Disconnected", func(t *testing.T) {
		info := ParseWiFiLinkOutput(sampleIWDisconnected, "wlan0")
		if info.Connected {
			t.Error("expected Connected = false")
		}
		if info.SSID != "" {
			t.Errorf("expected empty SSID, got %q", info.SSID)
		}
	})
}

func TestPingHosts(t *testing.T) {
	mockExec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		host := args[len(args)-1]
		if host == "192.168.20.1" {
			out := `PING 192.168.20.1 (192.168.20.1): 56 data bytes
64 bytes from 192.168.20.1: seq=0 ttl=64 time=1.254 ms
64 bytes from 192.168.20.1: seq=1 ttl=64 time=1.142 ms

--- 192.168.20.1 ping statistics ---
2 packets transmitted, 2 packets received, 0% packet loss
round-trip min/avg/max = 1.142/1.198/1.254 ms`
			return []byte(out), nil
		}
		if host == "149.154.166.110" {
			out := `PING 149.154.166.110 (149.154.166.110): 56 data bytes

--- 149.154.166.110 ping statistics ---
2 packets transmitted, 0 packets received, 100% packet loss`
			return []byte(out), fmt.Errorf("exit status 1")
		}
		return []byte("time=15.5 ms\n0% packet loss"), nil
	}

	targets := []PingTarget{
		{Name: "Шлюз", Host: "192.168.20.1"},
		{Name: "Telegram API", Host: "149.154.166.110"},
	}

	d := NewDiagnostics(
		WithPingTargets(targets),
		WithCommandExecutor(mockExec),
	)

	results, err := d.PingHosts(context.Background())
	if err != nil {
		t.Fatalf("PingHosts error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Host 1: 192.168.20.1 should succeed
	r1 := results[0]
	if !r1.Success {
		t.Errorf("expected host 1 to succeed")
	}
	if r1.PacketLoss != 0 {
		t.Errorf("expected 0%% loss, got %f", r1.PacketLoss)
	}
	if r1.Latency < time.Millisecond || r1.Latency > 2*time.Millisecond {
		t.Errorf("unexpected latency: %v", r1.Latency)
	}

	// Host 2: Telegram API has 100% loss
	r2 := results[1]
	if r2.Success {
		t.Errorf("expected host 2 to fail")
	}
	if r2.PacketLoss != 100 {
		t.Errorf("expected 100%% loss, got %f", r2.PacketLoss)
	}
}

func TestPingHostsArpingFallback(t *testing.T) {
	mockExec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "ping" {
			// Router drops ICMP echo completely
			out := `PING 192.168.20.1 (192.168.20.1): 56 data bytes
--- 192.168.20.1 ping statistics ---
2 packets transmitted, 0 packets received, 100% packet loss`
			return []byte(out), fmt.Errorf("exit status 1")
		}
		if name == "arping" {
			out := `ARPING to 192.168.20.1 from 192.168.20.209 via wlan0
Unicast reply from 192.168.20.1 [24:0f:5e:02:02:9a] 1.215ms
Unicast reply from 192.168.20.1 [24:0f:5e:02:02:9a] 3.775ms
Sent 2 probe(s) (1 broadcast(s))
Received 2 reply (0 request(s), 0 broadcast(s))`
			return []byte(out), nil
		}
		return nil, fmt.Errorf("unknown command")
	}

	targets := []PingTarget{
		{Name: "Шлюз (Роутер)", Host: "192.168.20.1"},
	}

	d := NewDiagnostics(
		WithPingTargets(targets),
		WithCommandExecutor(mockExec),
	)

	results, err := d.PingHosts(context.Background())
	if err != nil {
		t.Fatalf("PingHosts error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if !r.Success {
		t.Errorf("expected arping fallback to succeed, error: %s", r.Error)
	}
	if r.PacketLoss != 0 {
		t.Errorf("expected 0%% loss with arping, got %f", r.PacketLoss)
	}
	if r.Latency <= 0 {
		t.Errorf("expected positive latency, got %v", r.Latency)
	}
}

func TestRunSpeedtest(t *testing.T) {
	// Setup test HTTP server generating a 100KB payload
	payloadSize := int64(100 * 1024)
	chunk := strings.Repeat("A", int(payloadSize))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(chunk))
	}))
	defer ts.Close()

	d := NewDiagnostics(
		WithSpeedtestURL(ts.URL),
		WithSpeedtestChunkSize(payloadSize),
		WithHTTPClient(ts.Client()),
	)

	res, err := d.RunSpeedtest(context.Background())
	if err != nil {
		t.Fatalf("RunSpeedtest error: %v", err)
	}

	if res.DownloadedBytes != payloadSize {
		t.Errorf("expected %d bytes downloaded, got %d", payloadSize, res.DownloadedBytes)
	}
	if res.SpeedMbps <= 0 {
		t.Errorf("expected speed > 0 Mbps, got %f", res.SpeedMbps)
	}
}
