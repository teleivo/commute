package swim

import (
	"encoding/binary"
	"fmt"
	"math"
)

type messageKind uint8

const (
	ping messageKind = iota
	ack
	pingReq
)

func (a messageKind) String() string {
	switch a {
	case ping:
		return "ping"
	case ack:
		return "ack"
	case pingReq:
		return "ping-req"
	default:
		panic(fmt.Sprintf("unknown kind %d", uint8(a)))
	}
}

const messageVersion uint8 = 1

// Message is a SWIM protocol message sent and received over UDP.
type Message struct {
	Version   uint8
	Kind      messageKind
	Period    uint64 // sender's protocol period counter; echoed back in acks
	TargetLen uint8  // byte length of Target; 0 for ping and ack
	Target    []byte // target peer address; only set in ping-req messages
}

// NewMessage creates a Message with Version set to [messageVersion].
// Pass an empty target for ping and ack messages.
// Panics if target exceeds [math.MaxUint8] bytes.
func NewMessage(kind messageKind, period uint64, target string) Message {
	if len(target) > math.MaxUint8 {
		panic(fmt.Sprintf("swim: target address %q exceeds maximum length of %d", target, math.MaxUint8))
	}
	m := Message{
		Version:   messageVersion,
		Kind:      kind,
		Period:    period,
		TargetLen: uint8(len(target)),
	}
	if len(target) > 0 {
		m.Target = []byte(target)
	}
	return m
}

const (
	messageHeaderSize = 11 // 1 (Version) + 1 (Kind) + 8 (Period) + 1 (TargetLen)
	maxTargetSize     = 47 // max IPv6 address with port: [ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff]:65535
	maxMessageSize    = messageHeaderSize + maxTargetSize
)

// MarshalBinary encodes the message into its wire format.
func (m *Message) MarshalBinary() (data []byte, err error) {
	b := make([]byte, messageHeaderSize+len(m.Target))
	b[0] = messageVersion
	b[1] = byte(m.Kind)
	binary.BigEndian.PutUint64(b[2:10], m.Period)
	b[10] = m.TargetLen
	copy(b[11:], m.Target)
	return b, nil
}

// UnmarshalBinary decodes a wire-format message into m.
// Returns an error if data is too short, the version is unsupported, the kind
// is unknown, or TargetLen does not match the remaining bytes.
func (m *Message) UnmarshalBinary(data []byte) error {
	if len(data) < messageHeaderSize {
		return fmt.Errorf("message too short: need at least %d bytes for header, got %d", messageHeaderSize, len(data))
	}
	if data[0] != messageVersion {
		return fmt.Errorf("unsupported message version: want %d, got %d", messageVersion, data[0])
	}
	kind := messageKind(data[1])
	switch kind {
	case ping, ack, pingReq:
	default:
		return fmt.Errorf("unknown message kind: %d", data[1])
	}
	targetLen := int(data[10])
	if len(data[messageHeaderSize:]) != targetLen {
		return fmt.Errorf("target length mismatch: header claims %d bytes, got %d", targetLen, len(data[messageHeaderSize:]))
	}

	m.Version = data[0]
	m.Kind = kind
	m.Period = binary.BigEndian.Uint64(data[2:10])
	m.TargetLen = data[10]
	if m.TargetLen > 0 {
		m.Target = data[messageHeaderSize:]
	}
	return nil
}
