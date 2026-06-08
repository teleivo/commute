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
	msg := NewMessage(ping, 0, "")
	data, err := msg.MarshalBinary()
	require.NoError(t, err)
	assert.EqualValues(t, minMessageSize, len(data))
}

func TestMessageRoundTrip(t *testing.T) {
	tests := map[string]Message{
		"Ping":               NewMessage(ping, 42, ""),
		"Ack":                NewMessage(ack, 7, ""),
		"PingReqWithTarget":  NewMessage(pingReq, 3, "127.0.0.1:7946"),
		"PingReqEmptyTarget": NewMessage(pingReq, 1, ""),
	}

	for name, msg := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := msg.MarshalBinary()
			require.NoError(t, err)

			var got Message
			require.NoError(t, got.UnmarshalBinary(data))

			assert.EqualValues(t, msg, got)
		})
	}
}

func TestNewMessagePanicsOnOversizedTarget(t *testing.T) {
	target := strings.Repeat("a", maxTargetSize+1)
	defer func() {
		assert.NotNil(t, recover(), "expected NewMessage to panic on oversized target")
	}()
	NewMessage(pingReq, 1, target)
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
				msg := NewMessage(pingReq, 1, "abc")
				b, _ := msg.MarshalBinary()
				b[headerSize-1] = 20 // tamper TargetLen to claim 20 bytes
				// recompute checksum over tampered payload
				h := crc32.NewIEEE()
				h.Write(b[:len(b)-4])
				binary.BigEndian.PutUint32(b[len(b)-4:], h.Sum32())
				return b
			}(),
		},
		"ChecksumMismatch": {
			data: func() []byte {
				msg := NewMessage(ping, 1, "")
				b, _ := msg.MarshalBinary()
				b[2] ^= 0xff // corrupt a byte in Period
				return b
			}(),
		},
		"TargetExceedsMaxSize": {
			// build a wire message whose target is maxTargetSize+1 bytes, bypassing NewMessage
			data: func() []byte {
				target := strings.Repeat("a", maxTargetSize+1)
				b := make([]byte, messageHeaderSize+len(target)+4)
				b[0] = messageVersion
				b[1] = byte(pingReq)
				b[10] = uint8(len(target))
				copy(b[messageHeaderSize:], target)
				h := crc32.NewIEEE()
				h.Write(b[:len(b)-4])
				binary.BigEndian.PutUint32(b[len(b)-4:], h.Sum32())
				return b
			}(),
		},
		"UnknownVersion": {
			data: func() []byte {
				b := make([]byte, headerSize)
				b[0] = messageVersion + 1
				b[1] = byte(ping)
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
