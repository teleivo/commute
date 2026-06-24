package swim

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
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
	Version uint8
	Kind    messageKind
	Src     string // unresolved UDP host:port of the sender
	Period  uint64 // sender's protocol period counter; echoed back in acks
	Target  string // unresolved UDP host:port of the probe target; only set in ping-req messages
	Events  []Event
}

const (
	// fixed bytes: Version(1) + SrcLen(1) + Kind(1) + Period(8) + TargetLen(1) + EventCount(1)
	messageHeaderSize  = 13
	maxTargetSize      = 47                                             // max IPv6 address with port: [ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff]:65535
	minMessageSize     = messageHeaderSize + 4                          // fixed header + checksum; no Src, no Target
	maxBaseMessageSize = minMessageSize + maxTargetSize + maxTargetSize // fixed header + max Src + max Target + checksum
	maxMessageSize     = maxBaseMessageSize + maxPiggybackEvents*maxEventSize
)

// NewMessage creates a Message with Version set to [messageVersion].
// Pass an empty target for ping and ack messages.
// Panics if target exceeds [maxTargetSize] bytes.
func NewMessage(kind messageKind, src string, period uint64, target string) Message {
	if src == "" {
		panic("swim: src is required")
	}
	if len(target) > maxTargetSize {
		panic(fmt.Sprintf("swim: target address %q exceeds maximum length of %d", target, maxTargetSize))
	}
	return Message{
		Version: messageVersion,
		Src:     src,
		Kind:    kind,
		Period:  period,
		Target:  target,
	}
}

// MarshalBinary encodes the message into its wire format.
func (m *Message) MarshalBinary() (data []byte, err error) {
	eventBytes := len(m.Events) * eventHeaderSize
	for _, e := range m.Events {
		eventBytes += len(e.Node)
	}

	b := make([]byte, minMessageSize+len(m.Src)+len(m.Target)+eventBytes)
	b[0] = messageVersion
	b[1] = uint8(len(m.Src))
	copy(b[2:], m.Src)

	s := 2 + len(m.Src)
	b[s] = uint8(m.Kind)
	binary.BigEndian.PutUint64(b[s+1:s+9], m.Period)
	b[s+9] = uint8(len(m.Target))
	copy(b[s+10:], m.Target)

	events := b[s+10+len(m.Target):]
	events[0] = uint8(len(m.Events))
	events = events[1:]
	for _, e := range m.Events {
		events[0] = uint8(e.Kind)
		events[1] = uint8(len(e.Node))
		copy(events[eventHeaderSize:], e.Node)
		events = events[eventHeaderSize+len(e.Node):]
	}

	h := crc32.NewIEEE()
	h.Write(b[:len(b)-4])
	binary.BigEndian.PutUint32(b[len(b)-4:], h.Sum32())

	return b, nil
}

// UnmarshalBinary decodes a wire-format message into m.
// Returns an error if data is too short, the checksum does not match, the version is unsupported,
// the kind is unknown, the target or a node address exceeds [maxTargetSize], or an event's node
// length exceeds the remaining bytes.
func (m *Message) UnmarshalBinary(data []byte) error {
	if len(data) < minMessageSize {
		return fmt.Errorf("message too short: need at least %d bytes for header, got %d", minMessageSize, len(data))
	}

	h := crc32.NewIEEE()
	h.Write(data[:len(data)-4])
	if checksum := binary.BigEndian.Uint32(data[len(data)-4:]); checksum != h.Sum32() {
		return fmt.Errorf("checksum mismatch: message is corrupted")
	}

	if data[0] != messageVersion {
		return fmt.Errorf("unsupported message version: want %d, got %d", messageVersion, data[0])
	}

	srcLen := int(data[1])
	if srcLen == 0 {
		return fmt.Errorf("src is required")
	}
	src, err := unmarshalString("src", srcLen, data[2:])
	if err != nil {
		return err
	}

	// s points past Version + SrcLen + Src; need Kind(1)+Period(8)+TargetLen(1)+EventCount(1)=11
	// bytes of fixed fields before the checksum
	s := 2 + srcLen
	if s+11 > len(data)-4 {
		return fmt.Errorf("message too short: src too long for remaining header")
	}
	kind := messageKind(data[s])
	switch kind {
	case ping, ack, pingReq:
	default:
		return fmt.Errorf("unknown message kind: %d", data[s])
	}
	period := binary.BigEndian.Uint64(data[s+1 : s+9])

	targetLen := int(data[s+9])
	target, err := unmarshalString("target", targetLen, data[s+10:])
	if err != nil {
		return err
	}

	var events []Event
	eventsInput := data[s+11+targetLen : len(data)-4]
	if eventCount := int(data[s+10+targetLen]); eventCount > 0 {
		events = make([]Event, eventCount)
		for i := range events {
			var e Event
			n, err := e.UnmarshalBinary(eventsInput)
			if err != nil {
				return err
			}
			events[i] = e
			eventsInput = eventsInput[n:]
		}
	}

	m.Version = data[0]
	m.Src = src
	m.Kind = kind
	m.Period = period
	m.Target = target
	m.Events = events

	return nil
}

func unmarshalString(field string, stringLen int, input []byte) (string, error) {
	if stringLen == 0 {
		return "", nil
	}
	if stringLen > len(input) {
		return "", fmt.Errorf("%s length mismatch: header claims %d bytes, got %d", field, stringLen, len(input))
	}
	if stringLen > maxTargetSize {
		return "", fmt.Errorf("%s length %d exceeds maximum of %d", field, stringLen, maxTargetSize)
	}
	return string(input[:stringLen]), nil
}
