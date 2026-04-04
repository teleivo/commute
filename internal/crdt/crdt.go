// Package crdt provides conflict-free replicated data types (CRDTs).
//
// See Shapiro et al., [A comprehensive study of Convergent and Commutative Replicated Data Types].
//
// [A comprehensive study of Convergent and Commutative Replicated Data Types]: https://inria.hal.science/inria-00555588/document
package crdt

import (
	"encoding/json"
	"time"
)

// NodeID uniquely identifies a node in the cluster.
type NodeID string

// Clock returns the current time. It is used by LWWRegister to timestamp writes.
type Clock func() time.Time

// GCounter is a grow-only counter. Each node maintains its own counter that it alone increments.
// The total value is the sum across all nodes. Merge takes the max of each node's counter,
// guaranteeing convergence without coordination.
//
// Overflow is not handled. Per the Shapiro paper, the specification assumes no overflow. A uint64
// counter incrementing once per nanosecond takes ~584 years to overflow.
type GCounter struct {
	nodeID   NodeID
	counters map[NodeID]uint64
}

// NewGCounter creates a GCounter owned by the given node.
func NewGCounter(nodeID NodeID) *GCounter {
	return &GCounter{
		nodeID:   nodeID,
		counters: make(map[NodeID]uint64),
	}
}

// Value returns the counter total across all nodes.
func (g *GCounter) Value() uint64 {
	var sum uint64
	for _, v := range g.counters {
		sum += v
	}
	return sum
}

// Merge incorporates the state of other into g by taking the max of each node's counter.
func (g *GCounter) Merge(other *GCounter) {
	for id, v := range other.counters {
		g.counters[id] = max(g.counters[id], v)
	}
}

// Increment adds n to this node's counter.
func (g *GCounter) Increment(n uint64) {
	g.counters[g.nodeID] += n
}

func (g *GCounter) MarshalJSON() ([]byte, error) {
	if g == nil {
		return []byte("null"), nil
	}

	v := struct {
		Counters map[NodeID]uint64 `json:"counters"`
	}{
		Counters: g.counters,
	}
	return json.Marshal(v)
}

func (g *GCounter) UnmarshalJSON(data []byte) error {
	var v struct {
		Counters map[NodeID]uint64
	}
	err := json.Unmarshal(data, &v)
	if err != nil {
		return err
	}
	g.counters = v.Counters
	return nil
}

// PNCounter is a positive-negative counter that supports both increment and decrement. The value
// is the total increments minus the total decrements across all nodes.
type PNCounter struct {
	inc *GCounter
	dec *GCounter
}

// NewPNCounter creates a PNCounter owned by the given node.
func NewPNCounter(nodeID NodeID) *PNCounter {
	return &PNCounter{
		inc: NewGCounter(nodeID),
		dec: NewGCounter(nodeID),
	}
}

// Value returns the counter total: increments minus decrements across all nodes.
func (pn *PNCounter) Value() int64 {
	return int64(pn.inc.Value()) - int64(pn.dec.Value())
}

// Merge incorporates the state of other into pn.
func (pn *PNCounter) Merge(other *PNCounter) {
	pn.inc.Merge(other.inc)
	pn.dec.Merge(other.dec)
}

// Increment adds n to this node's positive counter.
func (pn *PNCounter) Increment(n uint64) {
	pn.inc.Increment(n)
}

// Decrement adds n to this node's negative counter.
func (pn *PNCounter) Decrement(n uint64) {
	pn.dec.Increment(n)
}

func (pn *PNCounter) MarshalJSON() ([]byte, error) {
	if pn == nil {
		return []byte("null"), nil
	}

	v := struct {
		Inc *GCounter `json:"inc"`
		Dec *GCounter `json:"dec"`
	}{
		Inc: pn.inc,
		Dec: pn.dec,
	}
	return json.Marshal(v)
}

func (pn *PNCounter) UnmarshalJSON(data []byte) error {
	var v struct {
		Inc *GCounter `json:"inc"`
		Dec *GCounter `json:"dec"`
	}
	err := json.Unmarshal(data, &v)
	if err != nil {
		return err
	}
	pn.inc = v.Inc
	pn.dec = v.Dec
	return nil
}

// LWWRegister is a last-writer-wins register. Each write is timestamped; merge picks the highest
// timestamp. Equal timestamps are broken by node ID (lexicographically highest wins).
//
// The Shapiro paper specifies timestamps that are "consistent with causal order," suggesting a
// logical clock. This implementation uses wall clock time, following Riak and Cassandra. Wall
// clocks can drift between nodes, so a node with a fast clock may win over a causally later
// write.
type LWWRegister struct {
	nodeID NodeID
	clock  Clock
	entry  lwwEntry
}

// lwwEntry is the timestamped value with writer identity. Merge compares these.
type lwwEntry struct {
	WriterID  NodeID          `json:"writerId"`
	Timestamp time.Time       `json:"timestamp"`
	Value     json.RawMessage `json:"value"`
}

func (e lwwEntry) After(other lwwEntry) bool {
	return e.Timestamp.After(other.Timestamp) ||
		(e.Timestamp.Equal(other.Timestamp) && e.WriterID > other.WriterID)
}

// NewLWWRegister creates a register owned by the given node using the provided clock.
func NewLWWRegister(nodeID NodeID, clock Clock) *LWWRegister {
	return &LWWRegister{
		nodeID: nodeID,
		clock:  clock,
	}
}

// Value returns the current register value.
func (lww *LWWRegister) Value() json.RawMessage {
	return lww.entry.Value
}

// Merge incorporates the state of other, keeping the value with the higher timestamp.
func (lww *LWWRegister) Merge(other *LWWRegister) {
	if other.entry.After(lww.entry) {
		lww.entry = other.entry
	}
}

// Set writes a new value, timestamped by the register's clock.
func (lww *LWWRegister) Set(value json.RawMessage) {
	lww.entry = lwwEntry{
		WriterID:  lww.nodeID,
		Timestamp: lww.clock(),
		Value:     value,
	}
}

func (lww *LWWRegister) MarshalJSON() ([]byte, error) {
	if lww == nil {
		return []byte("null"), nil
	}
	return json.Marshal(lww.entry)
}

func (lww *LWWRegister) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &lww.entry)
}
