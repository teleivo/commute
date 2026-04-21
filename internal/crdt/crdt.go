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

// ORSet is an observed-remove set. Causality for each element is tracked in a per-element DVVSet
// whose siblings are add/remove markers (true for add, false for remove). A concurrent Add and
// Remove of the same element lets the Add win, because both markers survive Sync as concurrent
// siblings and Contains reports presence when an add marker is still live.
type ORSet struct {
	nodeID NodeID
	state  map[string]*DVVSet[bool]
}

// NewORSet creates an empty OR-Set owned by the given node.
func NewORSet(nodeID NodeID) *ORSet {
	return &ORSet{
		nodeID: nodeID,
		state:  make(map[string]*DVVSet[bool]),
	}
}

// Contains reports whether value is in the set by inspecting its DVVSet siblings.
func (or *ORSet) Contains(value string) bool {
	state, added := or.state[value]
	if !added {
		return false
	}
	for _, v := range state.Values() {
		if !v {
			return false
		}
	}

	// TODO add has to come before removed which this here relies on. ok? otherwise I need to check
	// if Values has at least one true
	return true
}

// Values returns all elements currently in the set. Order is not specified.
func (or *ORSet) Values() []string {
	var values []string
	for value := range or.state {
		if or.Contains(value) {
			values = append(values, value)
		}
	}
	return values
}

// Add records an add of value on this node by advancing value's DVVSet with an add marker.
func (or *ORSet) Add(value string) {
	state, ok := or.state[value]
	if !ok {
		state = NewDVVSet[bool](or.nodeID)
	}
	// TODO wire VV through from client
	state.Update(VV{}, true)
	or.state[value] = state
}

// Remove records a remove of value on this node by advancing value's DVVSet with a remove marker.
// If value was never added it is a no-op, matching the OR-Set precondition that a remove observes
// a prior add.
func (or *ORSet) Remove(value string) {
	state, added := or.state[value]
	if !added {
		// TODO in U-set its a precondition that it has been added but not in OR-set is it? or still
		// because remove does not generate an id so
		return
	}
	// TODO wire VV through from client
	state.Update(VV{}, false)
}

// Merge incorporates the state of other into or by syncing per-element DVVSets. Elements only in
// other are adopted as-is; elements in both are merged via DVVSet.Sync, which preserves causally
// concurrent add/remove siblings.
func (or *ORSet) Merge(other *ORSet) {
	for k, v := range other.state {
		if _, ok := or.state[k]; !ok {
			or.state[k] = v
		} else {
			or.state[k].Sync(other.state[k])
		}
	}
}

func (or *ORSet) MarshalJSON() ([]byte, error) {
	if or == nil {
		return []byte("null"), nil
	}
	v := struct {
		State map[string]*DVVSet[bool] `json:"state"`
	}{
		State: or.state,
	}
	return json.Marshal(v)
}

func (or *ORSet) UnmarshalJSON(data []byte) error {
	var v struct {
		State map[string]*DVVSet[bool] `json:"state"`
	}
	err := json.Unmarshal(data, &v)
	if err != nil {
		return err
	}
	or.state = v.State
	return nil
}
