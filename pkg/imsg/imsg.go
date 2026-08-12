package imsg

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	Version        = 1
	HeaderSize     = 7               // 1 (version) + 2 (type) + 4 (length)
	MaxPayloadSize = 4 * 1024 * 1024 // 4 MB max payload
)

// Imsg represents a binary message frame with a JSON payload.
type Imsg struct {
	Version uint8
	Type    uint16
	Payload []byte
}

// ReadMessage reads an imsg frame from the reader.
// It reads the 7-byte header first, then reads exactly Length bytes of payload.
func ReadMessage(r io.Reader) (*Imsg, error) {
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("failed to read header: %w", err)
	}

	version := header[0]
	if version != Version {
		return nil, fmt.Errorf("unsupported protocol version: %d", version)
	}

	msgType := binary.BigEndian.Uint16(header[1:3])
	length := binary.BigEndian.Uint32(header[3:7])

	if length > MaxPayloadSize {
		return nil, fmt.Errorf("payload too large: %d bytes (max %d)", length, MaxPayloadSize)
	}

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("failed to read payload: %w", err)
		}
	}

	return &Imsg{
		Version: version,
		Type:    msgType,
		Payload: payload,
	}, nil
}

// WriteMessage writes an imsg frame to the writer.
func WriteMessage(w io.Writer, msg *Imsg) error {
	if len(msg.Payload) > MaxPayloadSize {
		return fmt.Errorf("payload too large: %d bytes (max %d)", len(msg.Payload), MaxPayloadSize)
	}

	header := make([]byte, HeaderSize)
	header[0] = msg.Version
	binary.BigEndian.PutUint16(header[1:3], msg.Type)
	binary.BigEndian.PutUint32(header[3:7], uint32(len(msg.Payload)))

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	if len(msg.Payload) > 0 {
		if _, err := w.Write(msg.Payload); err != nil {
			return fmt.Errorf("failed to write payload: %w", err)
		}
	}

	return nil
}

// NewImsg creates a new imsg with the given type and payload.
func NewImsg(msgType uint16, payload []byte) *Imsg {
	return &Imsg{
		Version: Version,
		Type:    msgType,
		Payload: payload,
	}
}
