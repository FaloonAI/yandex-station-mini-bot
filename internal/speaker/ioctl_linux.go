//go:build linux

package speaker

import (
	"fmt"
	"os"
	"syscall"
)

const (
	vl53l0xIoctlInit = 0x7001
	vl53l0xIoctlStop = 0x7005
)

func setHardwareGestures(enable bool) error {
	f, err := os.OpenFile("/dev/stmvl53l0x_ranging", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open /dev/stmvl53l0x_ranging failed: %w", err)
	}
	defer f.Close()

	cmd := vl53l0xIoctlInit
	if !enable {
		cmd = vl53l0xIoctlStop
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(cmd), 0)
	if errno != 0 {
		return fmt.Errorf("VL53L0X ioctl(0x%x) failed: %v", cmd, errno)
	}
	return nil
}
