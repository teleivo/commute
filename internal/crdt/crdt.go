// Package crdt provides conflict-free replicated data types (CRDTs).
//
// None of the types in this package are safe for concurrent use; callers that share a CRDT
// across goroutines must serialize access externally.
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

// IsZero reports whether the counter has no entries.
func (g *GCounter) IsZero() bool {
	return len(g.counters) == 0
}

// Increment returns a delta that adds n to this node's counter.
func (g *GCounter) Increment(n uint64) GCounter {
	return GCounter{
		counters: map[NodeID]uint64{
			g.nodeID: g.counters[g.nodeID] + n,
		},
	}
}

// Merge incorporates the state of other into g by taking the max of each node's counter.
func (g *GCounter) Merge(other GCounter) {
	if g.counters == nil {
		g.counters = make(map[NodeID]uint64)
	}
	for id, v := range other.counters {
		g.counters[id] = max(g.counters[id], v)
	}
}

// IsLessOrEqual reports whether g's causal history is subsumed by other's, i.e. g ⊑ other.
func (g *GCounter) IsLessOrEqual(other GCounter) bool {
	for id, v := range g.counters {
		if v > other.counters[id] {
			return false
		}
	}
	return true
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
	inc GCounter
	dec GCounter
}

// NewPNCounter creates a PNCounter owned by the given node.
func NewPNCounter(nodeID NodeID) *PNCounter {
	return &PNCounter{
		inc: *NewGCounter(nodeID),
		dec: *NewGCounter(nodeID),
	}
}

// Value returns the counter total: increments minus decrements across all nodes.
func (pn *PNCounter) Value() int64 {
	return int64(pn.inc.Value()) - int64(pn.dec.Value())
}

// Increment returns a delta that adds n to this node's positive counter.
func (pn *PNCounter) Increment(n uint64) PNCounter {
	return PNCounter{
		inc: pn.inc.Increment(n),
	}
}

// Decrement returns a delta that adds n to this node's negative counter.
func (pn *PNCounter) Decrement(n uint64) PNCounter {
	return PNCounter{
		dec: pn.dec.Increment(n),
	}
}

// Merge incorporates the state of other into pn.
func (pn *PNCounter) Merge(other *PNCounter) {
	pn.inc.Merge(other.inc)
	pn.dec.Merge(other.dec)
}

// IsLessOrEqual reports whether pn's causal history is subsumed by other's, i.e. pn ⊑ other.
func (pn *PNCounter) IsLessOrEqual(other *PNCounter) bool {
	return pn.inc.IsLessOrEqual(other.inc) && pn.dec.IsLessOrEqual(other.dec)
}

func (pn *PNCounter) MarshalJSON() ([]byte, error) {
	if pn == nil {
		return []byte("null"), nil
	}

	v := struct {
		Inc *GCounter `json:"inc,omitzero"`
		Dec *GCounter `json:"dec,omitzero"`
	}{
		Inc: &pn.inc,
		Dec: &pn.dec,
	}
	return json.Marshal(v)
}

func (pn *PNCounter) UnmarshalJSON(data []byte) error {
	var v struct {
		Inc GCounter `json:"inc,omitzero"`
		Dec GCounter `json:"dec,omitzero"`
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

// Set returns a delta containing a new value timestamped by the register's clock.
func (lww *LWWRegister) Set(value json.RawMessage) LWWRegister {
	return LWWRegister{
		entry: lwwEntry{
			WriterID:  lww.nodeID,
			Timestamp: lww.clock(),
			Value:     value,
		},
	}
}

// Merge incorporates the state of other, keeping the value with the higher timestamp.
func (lww *LWWRegister) Merge(other *LWWRegister) {
	if other.entry.After(lww.entry) {
		lww.entry = other.entry
	}
}

// IsLessOrEqual reports whether lww's entry is not causally after other's, i.e. lww ⊑ other.
func (lww *LWWRegister) IsLessOrEqual(other *LWWRegister) bool {
	return !lww.entry.After(other.entry)
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

// ORSet is an observed-remove set. A remove affects only adds that are in its causal past, so a
// remove concurrent with an add cannot hide that add: the element stays in the set.
type ORSet struct {
	nodeID NodeID
	// state maps each element to its per-element DVVSet of add/remove markers (true = add, false = remove).
	state map[string]*DVVSet[bool]
}

// NewORSet creates an empty OR-Set owned by the given node.
func NewORSet(nodeID NodeID) *ORSet {
	return &ORSet{
		nodeID: nodeID,
		state:  make(map[string]*DVVSet[bool]),
	}
}

// IsZero reports whether the set has no entries.
func (or *ORSet) IsZero() bool {
	return len(or.state) == 0
}

// CausalContext returns the version vector summarizing all add/remove events this replica has
// observed for value. Pass it back as the vv argument to a subsequent Add or Remove so concurrent
// ops from other replicas are detected correctly. Returns an empty VV if value is unknown here.
func (or *ORSet) CausalContext(value string) VV {
	v, ok := or.state[value]
	if !ok {
		return VV{}
	}
	return v.Join()
}

// CausalContexts returns a freshly allocated map of per-element causal contexts: one VV per
// element this replica has observed. Useful when reading the entire set so each element's context
// can later be passed back to Add or Remove.
func (or *ORSet) CausalContexts() map[string]VV {
	vvs := make(map[string]VV, len(or.state))
	for k, v := range or.state {
		vvs[k] = v.Join()
	}
	return vvs
}

// Contains reports whether value is in the set by inspecting its DVVSet siblings.
func (or *ORSet) Contains(value string) bool {
	state, added := or.state[value]
	if !added {
		return false
	}
	for _, v := range state.Values() {
		if v {
			return true
		}
	}

	return false
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

// Add returns a delta that adds value to the set. vv is the causal context the caller observed
// (typically from a prior [ORSet.CausalContext]); siblings it covers are discarded, concurrent ones
// survive.
func (or *ORSet) Add(value string, vv VV) ORSet {
	var ds *DVVSet[bool]
	if state, ok := or.state[value]; ok {
		ds = state.Clone()
	} else {
		ds = NewDVVSet[bool]()
	}
	ds.Update(or.nodeID, vv, true)
	return ORSet{
		state: map[string]*DVVSet[bool]{
			value: ds,
		},
	}
}

// Remove returns a delta that removes value from the set. vv is the causal context the caller
// observed (typically from a prior [ORSet.CausalContext]); add siblings it covers are dropped,
// concurrent adds survive. Returns an empty delta if value was never added.
func (or *ORSet) Remove(value string, vv VV) ORSet {
	state, added := or.state[value]
	if !added { // value was never added
		return ORSet{}
	}
	ds := state.Clone()
	ds.Update(or.nodeID, vv, false)
	return ORSet{
		state: map[string]*DVVSet[bool]{
			value: ds,
		},
	}
}

// Merge incorporates the state of other into or by syncing per-element DVVSets. Elements only in
// other are adopted as-is; elements in both are merged via DVVSet.Sync, which preserves causally
// concurrent add/remove siblings.
func (or *ORSet) Merge(other *ORSet) {
	for k, v := range other.state {
		if _, ok := or.state[k]; !ok {
			or.state[k] = v.Clone()
		} else {
			or.state[k].Sync(other.state[k])
		}
	}
}

// IsLessOrEqual reports whether or's causal history is subsumed by other's, i.e. or ⊑ other.
// The partial order lifts element-wise from DVVSet: or ⊑ other iff for every element tracked by
// or, its per-element DVVSet is ⊑ the corresponding DVVSet in other. Elements absent from other
// have an implicit empty DVVSet, so any non-empty per-element DVVSet in or with no matching entry
// in other means or ⊄ other. Elements only in other are irrelevant:
//
//	or ⊑ other ⟺ ∀ e ∈ or. or.state[e] ⊑ other.state[e]
func (or *ORSet) IsLessOrEqual(other *ORSet) bool {
	for k, v := range or.state {
		ds, ok := other.state[k]
		if !ok || !v.IsLessOrEqual(*ds) {
			return false
		}
	}
	return true
}

// Clone returns a deep copy of or.
func (or *ORSet) Clone() *ORSet {
	s := make(map[string]*DVVSet[bool], len(or.state))
	for k, v := range or.state {
		s[k] = v.Clone()
	}
	return &ORSet{nodeID: or.nodeID, state: s}
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
	if v.State == nil {
		v.State = make(map[string]*DVVSet[bool])
	}
	or.state = v.State
	return nil
}
