//go:build !linux

package speaker

func setHardwareGestures(enable bool) error {
	return nil
}
