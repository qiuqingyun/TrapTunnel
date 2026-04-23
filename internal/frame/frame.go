package frame

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	// HeaderSize is the number of bytes occupied by the wire header.
	HeaderSize = 10
	// MetaSize is the number of bytes occupied by node_id + sequence_id.
	MetaSize = 6
	// DefaultMaxTotalLength matches the legacy receiver safety check.
	DefaultMaxTotalLength = 10 * 1024 * 1024
)

// Frame is the common wire unit used between senders, relays and sinks.
type Frame struct {
	NodeID   uint16
	Sequence uint32
	Payload  []byte
}

// Clone returns an independent copy suitable for fanout delivery.
func (f Frame) Clone() Frame {
	payload := make([]byte, len(f.Payload))
	copy(payload, f.Payload)
	f.Payload = payload
	return f
}

// TotalLength returns the body size written after the initial 4-byte length prefix.
func (f Frame) TotalLength() uint32 {
	return uint32(MetaSize + len(f.Payload))
}

// MarshalBinary serializes the frame into the legacy wire format.
func (f Frame) MarshalBinary() ([]byte, error) {
	return f.MarshalBinaryWithLimit(DefaultMaxTotalLength)
}

// MarshalBinaryWithLimit serializes the frame using the provided size guard.
func (f Frame) MarshalBinaryWithLimit(maxTotalLength uint32) ([]byte, error) {
	if maxTotalLength > 0 && uint32(len(f.Payload)+MetaSize) > maxTotalLength {
		return nil, fmt.Errorf("frame too large: %d", len(f.Payload)+MetaSize)
	}
	buf := make([]byte, HeaderSize+len(f.Payload))
	binary.BigEndian.PutUint32(buf[0:4], f.TotalLength())
	binary.BigEndian.PutUint16(buf[4:6], f.NodeID)
	binary.BigEndian.PutUint32(buf[6:10], f.Sequence)
	copy(buf[10:], f.Payload)
	return buf, nil
}

// WriteTo serializes and writes the frame to the provided writer.
func (f Frame) WriteTo(w io.Writer) error {
	wire, err := f.MarshalBinary()
	if err != nil {
		return err
	}
	_, err = w.Write(wire)
	return err
}

// WriteToWithLimit serializes and writes the frame to the provided writer using a custom size guard.
func (f Frame) WriteToWithLimit(w io.Writer, maxTotalLength uint32) error {
	wire, err := f.MarshalBinaryWithLimit(maxTotalLength)
	if err != nil {
		return err
	}
	_, err = w.Write(wire)
	return err
}

// UnmarshalBinary parses a full frame from the provided wire bytes.
func UnmarshalBinary(data []byte) (Frame, error) {
	if len(data) < HeaderSize {
		return Frame{}, fmt.Errorf("short frame header: %d", len(data))
	}

	totalLen := binary.BigEndian.Uint32(data[0:4])
	if totalLen < MetaSize {
		return Frame{}, fmt.Errorf("invalid total length: %d", totalLen)
	}
	if totalLen > DefaultMaxTotalLength {
		return Frame{}, fmt.Errorf("frame too large: %d", totalLen)
	}

	expected := 4 + int(totalLen)
	if len(data) != expected {
		return Frame{}, fmt.Errorf("frame length mismatch: got %d want %d", len(data), expected)
	}

	payload := make([]byte, len(data[10:expected]))
	copy(payload, data[10:expected])

	return Frame{
		NodeID:   binary.BigEndian.Uint16(data[4:6]),
		Sequence: binary.BigEndian.Uint32(data[6:10]),
		Payload:  payload,
	}, nil
}

// Decoder incrementally decodes wire frames from a TCP byte stream.
type Decoder struct {
	buffer         []byte
	MaxTotalLength uint32
}

// NewDecoder creates a stream decoder with the default size guard.
func NewDecoder() *Decoder {
	return &Decoder{MaxTotalLength: DefaultMaxTotalLength}
}

// Push appends bytes and returns every complete frame currently buffered.
func (d *Decoder) Push(chunk []byte) ([]Frame, error) {
	d.buffer = append(d.buffer, chunk...)
	frames := make([]Frame, 0)

	for len(d.buffer) >= 4 {
		totalLen := binary.BigEndian.Uint32(d.buffer[0:4])
		if totalLen < MetaSize {
			return nil, fmt.Errorf("invalid total length: %d", totalLen)
		}
		if d.MaxTotalLength > 0 && totalLen > d.MaxTotalLength {
			return nil, fmt.Errorf("frame too large: %d", totalLen)
		}

		frameLen := 4 + int(totalLen)
		if len(d.buffer) < frameLen {
			break
		}

		frameData := make([]byte, frameLen)
		copy(frameData, d.buffer[:frameLen])
		parsed, err := UnmarshalBinary(frameData)
		if err != nil {
			return nil, err
		}
		frames = append(frames, parsed)
		d.buffer = d.buffer[frameLen:]
	}

	return frames, nil
}
