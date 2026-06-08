package swim

import (
	"testing"

	"github.com/teleivo/assertive/assert"
	"github.com/teleivo/assertive/require"
)

func TestMessageHeaderSize(t *testing.T) {
	msg := NewMessage(ping, 0, "")
	data, err := msg.MarshalBinary()
	require.NoError(t, err)
	assert.EqualValues(t, messageHeaderSize, len(data))
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
			// complete header claiming targetLen=20, but only 3 target bytes follow
			data: func() []byte {
				b := make([]byte, headerSize+3)
				b[0] = messageVersion
				b[1] = byte(pingReq)
				b[headerSize-1] = 20 // TargetLen claims 20 bytes
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
