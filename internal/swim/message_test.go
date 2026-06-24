package swim

import (
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/teleivo/assertive/assert"
	"github.com/teleivo/assertive/require"
)

func TestMessageHeaderSize(t *testing.T) {
	msg := Message{Version: messageVersion, Kind: ping, Src: "a"}

	data, err := msg.MarshalBinary()

	require.NoError(t, err)
	assert.EqualValues(t, len(data), minMessageSize+1)
}

func TestMessageRoundTrip(t *testing.T) {
	tests := map[string]Message{
		"Ping":               NewMessage(ping, "node-0:7946", 42, ""),
		"Ack":                NewMessage(ack, "node-0:7946", 7, ""),
		"PingReqWithTarget":  NewMessage(pingReq, "node-0:7946", 3, "node-1:7946"),
		"PingReqEmptyTarget": NewMessage(pingReq, "node-0:7946", 1, ""),
		"PingWithEvents": {
			Version: messageVersion,
			Src:     "node-0:7946",
			Kind:    ping,
			Period:  5,
			Events: []Event{
				{Kind: Dead, Incarnation: 3, Node: "192.168.1.1:7946"},
				{Kind: Alive, Incarnation: 7, Node: "192.168.1.2:7946"},
				{Kind: Suspect, Incarnation: 1, Node: "192.168.1.3:7946"},
			},
		},
		"PingReqWithTargetAndEvents": {
			Version: messageVersion,
			Src:     "node-0:7946",
			Kind:    pingReq,
			Period:  3,
			Target:  "node-1:7946",
			Events: []Event{
				{Kind: Suspect, Incarnation: 2, Node: "192.168.1.1:7946"},
			},
		},
	}

	for name, msg := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := msg.MarshalBinary()
			require.NoError(t, err)

			var got Message
			require.NoError(t, got.UnmarshalBinary(data))

			assert.EqualValues(t, got, msg)
		})
	}
}

func TestNewMessagePanicsOnEmptySrc(t *testing.T) {
	defer func() {
		assert.NotNil(t, recover(), "expected NewMessage to panic on empty src")
	}()

	NewMessage(ping, "", 1, "")
}

func TestNewMessagePanicsOnOversizedTarget(t *testing.T) {
	defer func() {
		assert.NotNil(t, recover(), "expected NewMessage to panic on oversized target")
	}()

	NewMessage(pingReq, "node-0:7946", 1, strings.Repeat("a", maxTargetSize+1))
}

func TestMessageUnmarshalBinaryError(t *testing.T) {
	const headerSize = messageHeaderSize

	tests := map[string]struct {
		data []byte
	}{
		"Empty": {
			data: []byte{},
		},
		"TruncatedBeforeTargetLen": {
			data: make([]byte, headerSize-1),
		},
		"TargetLenExceedsRemainingData": {
			// valid pingReq with 3-byte target, but TargetLen tampered to claim 20 bytes
			data: func() []byte {
				src := "node-0:7946"
				msg := NewMessage(pingReq, src, 1, "abc")
				b, _ := msg.MarshalBinary()
				targetLenOffset := 2 + len(src) + 9 // Version + SrcLen + Src + Kind + Period
				b[targetLenOffset] = 20             // tamper TargetLen to claim 20 bytes
				// recompute checksum over tampered payload
				h := crc32.NewIEEE()
				h.Write(b[:len(b)-4])
				binary.BigEndian.PutUint32(b[len(b)-4:], h.Sum32())
				return b
			}(),
		},
		"ChecksumMismatch": {
			data: func() []byte {
				src := "node-0:7946"
				msg := NewMessage(ping, src, 1, "")
				b, _ := msg.MarshalBinary()
				periodOffset := 2 + len(src) + 1 // Version + SrcLen + Src + Kind
				b[periodOffset] ^= 0xff          // corrupt first byte of Period
				return b
			}(),
		},
		"TargetExceedsMaxSize": {
			// build a wire message whose target is maxTargetSize+1 bytes, bypassing NewMessage
			data: func() []byte {
				src := "node-0:7946"
				target := strings.Repeat("a", maxTargetSize+1)
				s := 2 + len(src) // offset past Version + SrcLen + Src
				b := make([]byte, minMessageSize+len(src)+len(target))
				b[0] = messageVersion
				b[1] = uint8(len(src))
				copy(b[2:], src)
				b[s] = byte(pingReq)
				b[s+9] = uint8(len(target))
				copy(b[s+10:], target)
				h := crc32.NewIEEE()
				h.Write(b[:len(b)-4])
				binary.BigEndian.PutUint32(b[len(b)-4:], h.Sum32())
				return b
			}(),
		},
		"UnknownVersion": {
			data: func() []byte {
				msg := NewMessage(ping, "node-0:7946", 1, "")
				b, _ := msg.MarshalBinary()
				b[0] = messageVersion + 1
				h := crc32.NewIEEE()
				h.Write(b[:len(b)-4])
				binary.BigEndian.PutUint32(b[len(b)-4:], h.Sum32())
				return b
			}(),
		},
		"UnknownKind": {
			data: func() []byte {
				src := "node-0:7946"
				msg := NewMessage(ping, src, 1, "")
				b, _ := msg.MarshalBinary()
				b[2+len(src)] = 0xff // Kind byte
				h := crc32.NewIEEE()
				h.Write(b[:len(b)-4])
				binary.BigEndian.PutUint32(b[len(b)-4:], h.Sum32())
				return b
			}(),
		},
		"EventNodeLenExceedsRemainingData": {
			// valid ping with one event, but the event's NodeLen is tampered to claim more bytes
			data: func() []byte {
				src := "node-0:7946"
				msg := Message{
					Version: messageVersion,
					Src:     src,
					Kind:    ping,
					Period:  1,
					Events:  []Event{{Kind: Dead, Node: "192.168.1.1:7946"}},
				}
				b, _ := msg.MarshalBinary()
				// locate the event's NodeLen byte: past Version+SrcLen+Src+Kind+Period+TargetLen+Target+EventCount+event Kind+event Incarnation
				eventNodeLenOffset := 2 + len(src) + 10 + 1 + 1 + 8
				b[eventNodeLenOffset] = b[eventNodeLenOffset] + 10 // claim 10 more bytes than present
				// recompute checksum
				h := crc32.NewIEEE()
				h.Write(b[:len(b)-4])
				binary.BigEndian.PutUint32(b[len(b)-4:], h.Sum32())
				return b
			}(),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got Message
			assert.NotNil(t, got.UnmarshalBinary(tc.data))
		})
	}
}

func TestUnmarshalBinaryPanicsWhenSrcLenPushesFixedFieldsBeyondBuffer(t *testing.T) {
	// A crafted packet passes the static minMessageSize check and unmarshalString's srcLen check
	// (srcLen <= len(data)-2), but srcLen is large enough that the 11 fixed bytes after Src
	// (Kind+Period+TargetLen+EventCount) overlap the checksum or fall outside the buffer.
	// UnmarshalBinary should return an error, not panic.
	//
	// Use a minimal buffer (minMessageSize bytes) but claim srcLen=6, so s=8 and s+11=19 > 13
	// (len(data)-4=13), which puts Kind+Period outside the payload.
	srcLen := 6
	b := make([]byte, minMessageSize) // 17 bytes total, payload ends at byte 13
	b[0] = messageVersion
	b[1] = uint8(srcLen)
	copy(b[2:], strings.Repeat("a", srcLen))
	h := crc32.NewIEEE()
	h.Write(b[:len(b)-4])
	binary.BigEndian.PutUint32(b[len(b)-4:], h.Sum32())

	var got Message
	assert.NotNil(t, got.UnmarshalBinary(b))
}

func TestUnmarshalBinaryRejectsEmptySrc(t *testing.T) {
	// A wire message with srcLen=0 passes UnmarshalBinary today and produces msg.Src="".
	// In Listen, ackAddr = msg.Src resolves to "" causing the ack to be silently dropped.
	// UnmarshalBinary should reject empty Src.
	src := ""
	b := make([]byte, minMessageSize+len(src))
	s := 2 + len(src)
	b[0] = messageVersion
	b[1] = uint8(len(src))
	b[s] = byte(ping)
	// Period, TargetLen, EventCount left as zero
	h := crc32.NewIEEE()
	h.Write(b[:len(b)-4])
	binary.BigEndian.PutUint32(b[len(b)-4:], h.Sum32())

	var got Message
	assert.NotNil(t, got.UnmarshalBinary(b))
}
