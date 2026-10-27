package soundboard

import (
	"encoding/binary"
	"fmt"
	"io"
)

// WriteWAVHeader writes a standard 44-byte RIFF/WAVE header for mono 16-bit PCM.
func WriteWAVHeader(w io.Writer, pcmByteCount int, sampleRate int, numChannels int, bitsPerSample int) error {
	byteRate := sampleRate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8

	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(pcmByteCount+36))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16) // Subchunk1Size (16 for PCM)
	binary.LittleEndian.PutUint16(header[20:22], 1)  // AudioFormat (1 = PCM)
	binary.LittleEndian.PutUint16(header[22:24], uint16(numChannels))
	binary.LittleEndian.PutUint32(header[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(header[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(header[34:36], uint16(bitsPerSample))
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(pcmByteCount))

	_, err := w.Write(header)
	if err != nil {
		return fmt.Errorf("failed to write wav header: %w", err)
	}
	return nil
}
