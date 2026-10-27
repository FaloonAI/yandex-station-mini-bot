package presence

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const mockARPTable = `IP address       HW type     Flags       HW address            Mask     Device
192.168.20.1     0x1         0x2         aa:bb:cc:11:22:33     *        wlan0
192.168.20.50    0x1         0x0         00:00:00:00:00:00     *        wlan0
192.168.20.105   0x1         0x2         12:34:56:78:9A:BC     *        wlan0
192.168.20.110   0x1         0x2         de:ad:be:ef:ca:fe     *        wlan0
`

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff"},
		{"aa-bb-cc-dd-ee-ff", "aa:bb:cc:dd:ee:ff"},
		{"  12:34:56:AB:CD:EF  ", "12:34:56:ab:cd:ef"},
		{"", ""},
	}

	for _, tc := range tests {
		got := NormalizeMAC(tc.input)
		if got != tc.expected {
			t.Errorf("NormalizeMAC(%q) = %q, expected %q", tc.input, got, tc.expected)
		}
	}
}

func TestKnownDevicesSaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	savePath := filepath.Join(tmpDir, "devices.json")

	scanner := NewScanner()
	scanner.SetDeviceName("AA:BB:CC:11:22:33", "Роутер Keenetic")
	scanner.SetDeviceName("12:34:56:78:9a:bc", "Faloon (iPhone)")

	if err := scanner.SaveKnownDevices(savePath); err != nil {
		t.Fatalf("SaveKnownDevices failed: %v", err)
	}

	newScanner := NewScanner()
	if err := newScanner.LoadKnownDevices(savePath); err != nil {
		t.Fatalf("LoadKnownDevices failed: %v", err)
	}

	name, found := newScanner.GetDeviceName("aa:bb:cc:11:22:33")
	if !found || name != "Роутер Keenetic" {
		t.Errorf("expected 'Роутер Keenetic', got %q, found=%v", name, found)
	}

	name, found = newScanner.GetDeviceName("12-34-56-78-9A-BC")
	if !found || name != "Faloon (iPhone)" {
		t.Errorf("expected 'Faloon (iPhone)', got %q, found=%v", name, found)
	}
}

func TestScanWithMockARP(t *testing.T) {
	tmpDir := t.TempDir()
	arpFile := filepath.Join(tmpDir, "arp")
	macFile := filepath.Join(tmpDir, "address")

	if err := os.WriteFile(arpFile, []byte(mockARPTable), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(macFile, []byte("de:ad:be:ef:ca:fe\n"), 0644); err != nil {
		t.Fatal(err)
	}

	probedCount := 0
	mockProbe := func(ctx context.Context, ip string) error {
		probedCount++
		return nil
	}

	scanner := NewScanner(
		WithARPPath(arpFile),
		WithSelfMACPath(macFile),
		WithSubnet("192.168.20.0/29"), // 6 hosts (.1 .. .6) for fast unit test
		WithPingFunc(mockProbe),
	)

	scanner.SetDeviceName("12:34:56:78:9a:bc", "Faloon (iPhone)")

	devices, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	// Incomplete entry 192.168.20.50 (flags 0x0) should be skipped
	// Remaining: .1, .105, .110 (3 valid devices)
	if len(devices) != 3 {
		t.Fatalf("expected 3 valid devices, got %d", len(devices))
	}

	// Self device should be sorted first: 192.168.20.110 (de:ad:be:ef:ca:fe)
	if !devices[0].IsSelf {
		t.Errorf("expected first device to be self, got IP %s", devices[0].IP)
	}
	if devices[0].MAC != "de:ad:be:ef:ca:fe" {
		t.Errorf("expected self MAC de:ad:be:ef:ca:fe, got %s", devices[0].MAC)
	}

	// Check device name resolution
	var foundIphone bool
	for _, d := range devices {
		if d.MAC == "12:34:56:78:9a:bc" {
			foundIphone = true
			if d.Name != "Faloon (iPhone)" {
				t.Errorf("expected device name 'Faloon (iPhone)', got %q", d.Name)
			}
		}
	}
	if !foundIphone {
		t.Error("iPhone device was not found in scan results")
	}

	if probedCount == 0 {
		t.Error("expected mock probe to be called")
	}
}

func TestSubnetGeneration(t *testing.T) {
	s := NewScanner(WithSubnet("192.168.1.0/30"))
	ips := s.generateSubnetIPs()

	// /30 has 2 usable host IPs (.1 and .2)
	if len(ips) != 2 {
		t.Fatalf("expected 2 host IPs for /30, got %d: %v", len(ips), ips)
	}
	if ips[0] != "192.168.1.1" || ips[1] != "192.168.1.2" {
		t.Errorf("unexpected IPs: %v", ips)
	}
}

func TestSubnetGetSetAndDetect(t *testing.T) {
	s := NewScanner(WithSubnet("192.168.20.0/24"))
	if s.GetSubnet() != "192.168.20.0/24" {
		t.Errorf("expected 192.168.20.0/24, got %s", s.GetSubnet())
	}

	if err := s.SetSubnet("10.0.0.0/24"); err != nil {
		t.Fatalf("SetSubnet failed: %v", err)
	}
	if s.GetSubnet() != "10.0.0.0/24" {
		t.Errorf("expected 10.0.0.0/24, got %s", s.GetSubnet())
	}

	// Normalizes host IP to network CIDR
	if err := s.SetSubnet("192.168.1.155/24"); err != nil {
		t.Fatalf("SetSubnet with host IP failed: %v", err)
	}
	if s.GetSubnet() != "192.168.1.0/24" {
		t.Errorf("expected normalized 192.168.1.0/24, got %s", s.GetSubnet())
	}

	if err := s.SetSubnet("invalid-cidr"); err == nil {
		t.Error("expected error for invalid CIDR, got nil")
	}

	// Test DetectLocalSubnet doesn't panic
	subnet, err := DetectLocalSubnet("")
	if err == nil && subnet == "" {
		t.Error("expected non-empty subnet if err is nil")
	}
}
