// Package proto defines the Hedera v1 wire format: JSON control messages and
// binary PTY frames. See protocol/spec.md at the repository root.
package proto

import (
	"encoding/binary"
	"errors"
)

const (
	// Version is the highest protocol version this build speaks.
	Version = 1
	// MinVersion is the lowest protocol version this build speaks.
	MinVersion = 1
	// Subprotocol is the required WebSocket subprotocol.
	Subprotocol = "hedera.v1"

	// MaxPayload is the largest PTY payload in one binary frame.
	MaxPayload = 64 * 1024
	// MaxTextMessage is the largest control message.
	MaxTextMessage = 1 << 20

	OpOutput byte = 0x01
	OpInput  byte = 0x02

	outputHeader = 1 + 4 + 8
	inputHeader  = 1 + 4
)

// ErrBadFrame reports a binary frame with an unknown opcode, a truncated
// header or an oversized payload.
var ErrBadFrame = errors.New("proto: malformed binary frame")

// Frame is a decoded binary frame. Offset is only meaningful for OpOutput.
type Frame struct {
	Op      byte
	Channel uint32
	Offset  uint64
	Payload []byte
}

// AppendOutput appends an OUTPUT frame to dst.
func AppendOutput(dst []byte, channel uint32, offset uint64, payload []byte) []byte {
	dst = append(dst, OpOutput)
	dst = binary.BigEndian.AppendUint32(dst, channel)
	dst = binary.BigEndian.AppendUint64(dst, offset)
	return append(dst, payload...)
}

// AppendInput appends an INPUT frame to dst.
func AppendInput(dst []byte, channel uint32, payload []byte) []byte {
	dst = append(dst, OpInput)
	dst = binary.BigEndian.AppendUint32(dst, channel)
	return append(dst, payload...)
}

// DecodeFrame parses a binary frame. Payload aliases b.
func DecodeFrame(b []byte) (Frame, error) {
	if len(b) < 1 {
		return Frame{}, ErrBadFrame
	}
	var f Frame
	f.Op = b[0]
	switch f.Op {
	case OpOutput:
		if len(b) < outputHeader {
			return Frame{}, ErrBadFrame
		}
		f.Channel = binary.BigEndian.Uint32(b[1:5])
		f.Offset = binary.BigEndian.Uint64(b[5:13])
		f.Payload = b[outputHeader:]
	case OpInput:
		if len(b) < inputHeader {
			return Frame{}, ErrBadFrame
		}
		f.Channel = binary.BigEndian.Uint32(b[1:5])
		f.Payload = b[inputHeader:]
	default:
		return Frame{}, ErrBadFrame
	}
	if len(f.Payload) > MaxPayload {
		return Frame{}, ErrBadFrame
	}
	return f, nil
}
