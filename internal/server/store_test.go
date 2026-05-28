package server_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/teleivo/assertive/assert"
	"github.com/teleivo/assertive/require"
	"github.com/teleivo/commute/internal/crdt"
	"github.com/teleivo/commute/internal/server"
)

func TestStoreDeltaNothingToSend(t *testing.T) {
	st := server.NewStore(crdt.NodeID("a"), time.Now)
	st.IncrementCounter("visits", 1)

	b, ok := st.Delta("b")

	require.True(t, ok)
	st.Ack("b", ackOf(unmarshalDelta(t, b)))

	_, ok = st.Delta("b")

	assert.False(t, ok)
}

func TestStoreDeltaFullStateForUnknownPeer(t *testing.T) {
	st := server.NewStore(crdt.NodeID("a"), time.Now)
	st.IncrementCounter("visits", 5)
	st.IncrementCounter("visits", 3)

	b, ok := st.Delta("b")

	require.True(t, ok)
	msg := unmarshalDelta(t, b)
	require.NotNil(t, msg.Counters["visits"])
	assert.EqualValues(t, msg.Counters["visits"].Value(), int64(8))
}

func TestStoreDeltaCoversExactlyTheGap(t *testing.T) {
	st := server.NewStore(crdt.NodeID("a"), time.Now)
	st.IncrementCounter("visits", 5)

	// Peer b acks after the first write.
	b, ok := st.Delta("b")
	require.True(t, ok)
	st.Ack("b", ackOf(unmarshalDelta(t, b)))

	// Second write on a different key happens after the ack.
	st.IncrementCounter("likes", 3)

	b, ok = st.Delta("b")
	require.True(t, ok)
	msg := unmarshalDelta(t, b)

	// Delta must carry only the key written after the ack, not the already-acked key.
	assert.NotNil(t, msg.Counters["likes"])
	assert.Nil(t, msg.Counters["visits"])
}

func TestStoreAckStaleDoesNotRollBack(t *testing.T) {
	st := server.NewStore(crdt.NodeID("a"), time.Now)
	st.IncrementCounter("visits", 1)
	st.IncrementCounter("visits", 1)

	b, ok := st.Delta("b")
	require.True(t, ok)
	st.Ack("b", ackOf(unmarshalDelta(t, b)))

	// A stale ack with a lower seq arrives (e.g. reordered).
	st.Ack("b", server.AckMessage{CountersSeq: 1})

	// Delta should still return ok=false: the higher ack must not have been rolled back.
	_, ok = st.Delta("b")
	assert.False(t, ok)
}

func TestStoreRemoveSetAbsentElementDoesNotProducePhantomEntry(t *testing.T) {
	a := server.NewStore(crdt.NodeID("a"), time.Now)
	a.AddSet("fruits", "apple", crdt.VV{})

	delta, ok := a.Delta("b")
	require.True(t, ok)
	a.Ack("b", ackOf(unmarshalDelta(t, delta)))

	// Remove an element that was never in the set. ORSet.Remove returns an empty delta.
	// This must not be stored — b has already acked everything, so Delta("b") must
	// return ok=false (nothing new to send).
	a.RemoveSet("fruits", "mango", crdt.VV{})

	_, ok = a.Delta("b")
	assert.False(t, ok)
}

func ackOf(msg server.Message) server.AckMessage {
	return server.AckMessage{
		CountersSeq:  msg.CountersSeq,
		RegistersSeq: msg.RegistersSeq,
		SetsSeq:      msg.SetsSeq,
	}
}

func unmarshalDelta(t *testing.T, delta []byte) server.Message {
	t.Helper()
	var msg server.Message
	require.NoError(t, json.Unmarshal(delta, &msg))
	return msg
}
