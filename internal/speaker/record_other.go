//go:build !linux

package speaker

import (
	"fmt"
)

// RecordRoom stub for non-Linux development and CI testing environments.
func (c *Controller) RecordRoom(durationSec int) (string, error) {
	return "", fmt.Errorf("hardware microphone capture is only supported on Linux (ALSA)")
}
