package presence

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DeviceStatus represents a network device detected on the local network.
type DeviceStatus struct {
	MAC       string    `json:"mac"`
	IP        string    `json:"ip"`
	Name      string    `json:"name"`
	IsOnline  bool      `json:"is_online"`
	IsSelf    bool      `json:"is_self"`
	Interface string    `json:"interface"`
	LastSeen  time.Time `json:"last_seen"`
}

// Option allows configuring Scanner attributes.
type Option func(*Scanner)

// WithARPPath overrides the default /proc/net/arp path (useful for testing).
func WithARPPath(p string) Option {
	return func(s *Scanner) {
		s.arpPath = p
	}
}

// WithSelfMACPath overrides the default /sys/class/net/wlan0/address path.
func WithSelfMACPath(p string) Option {
	return func(s *Scanner) {
		s.selfMacPath = p
	}
}

// WithSubnet sets the target subnet to scan (CIDR format, e.g. "192.168.20.0/24").
func WithSubnet(cidr string) Option {
	return func(s *Scanner) {
		s.subnet = cidr
	}
}

// WithInterface sets the network interface name (default "wlan0").
func WithInterface(iface string) Option {
	return func(s *Scanner) {
		s.ifaceName = iface
	}
}

// WithPingFunc provides a custom probe function for testing or customized ICMP/UDP probing.
func WithPingFunc(fn func(ctx context.Context, ip string) error) Option {
	return func(s *Scanner) {
		s.pingFn = fn
	}
}

// Scanner handles discovery of active devices on the local network.
type Scanner struct {
	mu           sync.RWMutex
	knownDevices map[string]string // Normalized MAC -> Device friendly name
	arpPath      string
	selfMacPath  string
	subnet       string
	ifaceName    string
	pingFn       func(ctx context.Context, ip string) error
}

// NewScanner creates a new presence scanner with sane defaults for Yandex Station Mini.
func NewScanner(opts ...Option) *Scanner {
	s := &Scanner{
		knownDevices: make(map[string]string),
		arpPath:      "/proc/net/arp",
		selfMacPath:  "/sys/class/net/wlan0/address",
		subnet:       "192.168.20.0/24",
		ifaceName:    "wlan0",
	}

	for _, opt := range opts {
		opt(s)
	}

	if s.pingFn == nil {
		s.pingFn = defaultProbeIP
	}

	return s
}

// NormalizeMAC normalizes MAC strings to lowercase colon-separated format (e.g. "aa:bb:cc:dd:ee:ff").
func NormalizeMAC(mac string) string {
	clean := strings.TrimSpace(strings.ToLower(mac))
	clean = strings.ReplaceAll(clean, "-", ":")
	return clean
}

// SetDeviceName associates a user-friendly name with a MAC address.
func (s *Scanner) SetDeviceName(mac, name string) {
	norm := NormalizeMAC(mac)
	if norm == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.knownDevices[norm] = strings.TrimSpace(name)
}

// GetDeviceName retrieves the registered name for a MAC address, if any.
func (s *Scanner) GetDeviceName(mac string) (string, bool) {
	norm := NormalizeMAC(mac)
	s.mu.RLock()
	defer s.mu.RUnlock()
	name, exists := s.knownDevices[norm]
	return name, exists
}

// GetAllKnownDevices returns a copy of all registered MAC-to-Name associations.
func (s *Scanner) GetAllKnownDevices() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]string, len(s.knownDevices))
	for k, v := range s.knownDevices {
		result[k] = v
	}
	return result
}

// SaveKnownDevices serializes known devices to a JSON file.
func (s *Scanner) SaveKnownDevices(path string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}

	data, err := json.MarshalIndent(s.knownDevices, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal known devices: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write known devices to %s: %w", path, err)
	}

	return nil
}

// LoadKnownDevices loads known devices from a JSON file and merges them into memory.
func (s *Scanner) LoadKnownDevices(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Non-existent file is treated as clean empty state
		}
		return fmt.Errorf("failed to read known devices from %s: %w", path, err)
	}

	var loaded map[string]string
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("failed to unmarshal known devices JSON: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for k, v := range loaded {
		norm := NormalizeMAC(k)
		if norm != "" && strings.TrimSpace(v) != "" {
			s.knownDevices[norm] = strings.TrimSpace(v)
		}
	}

	return nil
}

// ReadSelfMAC returns the speaker's own wireless MAC address.
func (s *Scanner) ReadSelfMAC() string {
	// 1. Try reading from sysfs
	if data, err := os.ReadFile(s.selfMacPath); err == nil {
		mac := NormalizeMAC(string(data))
		if mac != "" {
			return mac
		}
	}

	// 2. Fallback to net.Interfaces lookup
	if iface, err := net.InterfaceByName(s.ifaceName); err == nil && len(iface.HardwareAddr) > 0 {
		return NormalizeMAC(iface.HardwareAddr.String())
	}

	return ""
}

// Scan performs a high-speed concurrent network scan and returns all discovered online devices.
func (s *Scanner) Scan(ctx context.Context) ([]DeviceStatus, error) {
	// 1. Determine IP list to ping
	ips := s.generateSubnetIPs()

	// 2. Concurrently ping all IPs to populate kernel ARP table (~1-2 seconds)
	s.pingSubnet(ctx, ips)

	// 3. Parse /proc/net/arp
	devices, err := s.parseARPTable()
	if err != nil {
		return nil, fmt.Errorf("failed to read ARP table: %w", err)
	}

	selfMAC := s.ReadSelfMAC()
	now := time.Now()

	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range devices {
		devices[i].LastSeen = now
		devices[i].IsOnline = true

		if selfMAC != "" && devices[i].MAC == selfMAC {
			devices[i].IsSelf = true
			if devices[i].Name == "" || strings.HasPrefix(devices[i].Name, "Устройство") {
				devices[i].Name = "Яндекс Станция Mini (Эта колонка)"
			}
			continue
		}

		if name, ok := s.knownDevices[devices[i].MAC]; ok && name != "" {
			devices[i].Name = name
		} else if devices[i].Name == "" {
			// Sane defaults based on IP
			if strings.HasSuffix(devices[i].IP, ".1") {
				devices[i].Name = "Роутер / Шлюз"
			} else {
				devices[i].Name = fmt.Sprintf("Устройство (%s)", devices[i].IP)
			}
		}
	}

	// Sort results: Self first, then router, then alphabetically by name/IP
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].IsSelf != devices[j].IsSelf {
			return devices[i].IsSelf
		}
		if strings.HasSuffix(devices[i].IP, ".1") != strings.HasSuffix(devices[j].IP, ".1") {
			return strings.HasSuffix(devices[i].IP, ".1")
		}
		return devices[i].IP < devices[j].IP
	})

	return devices, nil
}

// generateSubnetIPs parses the configured subnet and generates host IPs.
func (s *Scanner) generateSubnetIPs() []string {
	_, ipNet, err := net.ParseCIDR(s.subnet)
	if err != nil {
		// Fallback to standard 192.168.20.1..254
		ips := make([]string, 0, 254)
		for i := 1; i <= 254; i++ {
			ips = append(ips, fmt.Sprintf("192.168.20.%d", i))
		}
		return ips
	}

	var ips []string
	ip := ipNet.IP.Mask(ipNet.Mask)

	for {
		ip = nextIP(ip)
		if !ipNet.Contains(ip) {
			break
		}
		// Skip network (.0) and broadcast (.255 in /24)
		if isBroadcastOrNetwork(ip, ipNet) {
			continue
		}
		ips = append(ips, ip.String())
	}

	return ips
}

func nextIP(ip net.IP) net.IP {
	next := make(net.IP, len(ip))
	copy(next, ip)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next
}

func isBroadcastOrNetwork(ip net.IP, n *net.IPNet) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	net4 := n.IP.To4()
	if net4 == nil {
		return false
	}

	// Check if IP is network address
	isNet := true
	isBcast := true
	for i := 0; i < 4; i++ {
		netByte := net4[i] & n.Mask[i]
		bcastByte := net4[i] | ^n.Mask[i]
		if v4[i] != netByte {
			isNet = false
		}
		if v4[i] != bcastByte {
			isBcast = false
		}
	}

	return isNet || isBcast
}

// pingSubnet pings target IPs concurrently using a worker pool.
func (s *Scanner) pingSubnet(ctx context.Context, ips []string) {
	const maxConcurrency = 64
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for _, ip := range ips {
		select {
		case <-ctx.Done():
			return
		default:
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(target string) {
			defer wg.Done()
			defer func() { <-sem }()

			// Probe target to elicit ARP request
			_ = s.pingFn(ctx, target)
		}(ip)
	}

	wg.Wait()
	// Allow small window for ARP response packets to arrive in kernel cache
	time.Sleep(100 * time.Millisecond)
}

// defaultProbeIP sends a fast UDP datagram to trigger kernel ARP resolution.
// This is 100x faster than forking 'ping' processes and eliminates CPU load spikes.
func defaultProbeIP(ctx context.Context, ip string) error {
	d := net.Dialer{Timeout: 200 * time.Millisecond}
	conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(ip, "9"))
	if err == nil {
		_, _ = conn.Write([]byte{0})
		_ = conn.Close()
	}
	return nil
}

// parseARPTable reads and parses entries from /proc/net/arp.
func (s *Scanner) parseARPTable() ([]DeviceStatus, error) {
	file, err := os.Open(s.arpPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var devices []DeviceStatus
	scanner := bufio.NewScanner(file)

	// Format:
	// IP address       HW type     Flags       HW address            Mask     Device
	// 192.168.20.1     0x1         0x2         aa:bb:cc:dd:ee:ff     *        wlan0
	isHeader := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if isHeader {
			isHeader = false
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}

		ip := fields[0]
		flags := fields[2]
		mac := NormalizeMAC(fields[3])
		dev := fields[5]

		// Flags: 0x2 = ATF_COM (completed), 0x0 = incomplete
		if flags == "0x0" || flags == "0" {
			continue
		}

		// Filter out null or broadcast MACs
		if mac == "" || mac == "00:00:00:00:00:00" || mac == "ff:ff:ff:ff:ff:ff" {
			continue
		}

		devices = append(devices, DeviceStatus{
			IP:        ip,
			MAC:       mac,
			Interface: dev,
			IsOnline:  true,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return devices, nil
}
