package crdt

import (
	"encoding/json"
	"slices"
)

// DVVSet is a Dotted Version Vector Set: a causality-tracking container that holds a set of
// concurrent values (siblings) along with the version information needed to discard obsolete ones
// on merge. It is the compact representation from §5 of Almeida et al., [Scalable and Accurate
// Causality Tracking for Eventually Consistent Stores], where a DVVSet is defined as a set of
// triples (i, n, l): server id, counter, and a list of siblings whose dots are implicit from the
// list position. The dot of the k-th value in l (0-indexed) is (i, n−k).
//
// [Scalable and Accurate Causality Tracking for Eventually Consistent Stores]: https://inria.hal.science/hal-01287733
type DVVSet[T any] struct {
	nodeID NodeID
	state  map[NodeID]dvvEntry[T]
}

// dvvEntry is the (n, l) pair of a DVVSet triple; its id is the map key in state. Siblings in
// values are head-newest: values[0]'s dot is (id, counter).
type dvvEntry[T any] struct {
	counter uint64
	values  []T
}

// MarshalJSON is defined on dvvEntry because its fields are unexported; encoding/json would
// otherwise produce an empty object.
func (e dvvEntry[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Counter uint64 `json:"counter"`
		Values  []T    `json:"values"`
	}{
		Counter: e.counter,
		Values:  e.values,
	})
}

func (e *dvvEntry[T]) UnmarshalJSON(data []byte) error {
	var v struct {
		Counter uint64 `json:"counter"`
		Values  []T    `json:"values"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.counter = v.Counter
	e.values = v.Values
	return nil
}

// NewDVVSet creates an empty DVVSet owned by the given node.
func NewDVVSet[T any](nodeID NodeID) *DVVSet[T] {
	return &DVVSet[T]{
		nodeID: nodeID,
		state:  make(map[NodeID]dvvEntry[T]),
	}
}

// VV is a version vector: a mapping from node id to the highest counter known for that node.
type VV map[NodeID]uint64

// get returns vv's counter for nodeID, or 0 if nodeID is not in vv. It corresponds to the paper's
// notation C(r), which is 0 for any unmapped id.
func (vv VV) get(nodeID NodeID) uint64 {
	if v, ok := vv[nodeID]; ok {
		return v
	}
	return 0
}

// Values returns all siblings in the DVVSet. The order is not specified.
func (ds *DVVSet[T]) Values() []T {
	var result []T
	for _, v := range ds.state {
		result = append(result, v.values...)
	}
	return result
}

// Join projects the DVVSet to its causal history, a version vector holding the highest counter
// per node id and dropping the siblings. Per the paper:
//
//	join(S) = {(r, n) | (r, n, l) ∈ S}.
func (ds *DVVSet[T]) Join() VV {
	result := make(map[NodeID]uint64, len(ds.state))
	for k, v := range ds.state {
		result[k] = v.counter
	}
	return result
}

// discard drops siblings from ds whose dots are covered by the given version vector vv. For each
// entry (r, n, l), it keeps the first n−vv(r) siblings since dots (r, 1)…(r, vv(r)) are already
// known to vv. Ids only in vv are ignored:
//
//	discard(S, C) = {(r, n, first(n − C(r), l)) | (r, n, l) ∈ S}.
func (ds *DVVSet[T]) discard(vv VV) {
	for k, v := range ds.state {
		if counter, ok := vv[k]; ok {
			if v.counter > counter {
				v.values = v.values[0:min(int(v.counter-counter), len(v.values))]
			} else {
				v.values = nil
			}
			ds.state[k] = v
		}
	}
}

// event records a new write of value on this node, advancing ds's causal history. ds's own entry
// gets a fresh dot (nodeID, n+1) with value prepended. Other entries have their counter bumped to
// max(n, vv(i)) so ds absorbs knowledge carried by vv. Ids only in vv do not produce new entries:
//
//	event(C, S, r, v) = {(i, n+1, [v | l]) | (i, n, l) ∈ S | i = r}
//	                  ∪ {(i, max(n, C(i)), l) | (i, n, l) ∈ S | i ≠ r}.
func (ds *DVVSet[T]) event(vv VV, value T) {
	if v, ok := ds.state[ds.nodeID]; ok {
		ds.state[ds.nodeID] = dvvEntry[T]{
			counter: v.counter + 1,
			values:  slices.Insert(v.values, 0, value),
		}
	} else {
		ds.state[ds.nodeID] = dvvEntry[T]{
			counter: 1,
			values:  []T{value},
		}
	}
	for k, v := range ds.state {
		if k == ds.nodeID {
			continue
		}
		if bumped := max(v.counter, vv.get(k)); bumped != v.counter {
			v.counter = bumped
			ds.state[k] = v
		}
	}
}

// Sync merges the causal history of other into ds, keeping only siblings neither side has
// obsoleted. An id on one side only is kept as-is (the absent side is implicitly (i, 0, [])). An
// id on both sides takes the higher counter and truncates that side's siblings via merge:
//
//	sync(S, S') = {(r, max(n, n'), merge(n, l, n', l')) | r ∈ R, (r, n, l) ∈ S, (r, n', l') ∈ S'}
//	merge(n, l, n', l') = first(n − n' + |l'|, l)    if n ≥ n'
//	                      first(n' − n + |l|, l')   otherwise.
func (ds *DVVSet[T]) Sync(other *DVVSet[T]) {
	ids := make(map[NodeID]struct{}, max(len(ds.state), len(other.state)))
	for k := range ds.state {
		ids[k] = struct{}{}
	}
	for k := range other.state {
		ids[k] = struct{}{}
	}

	for id := range ids {
		if entry, ok := ds.state[id]; ok {
			if otherEntry, ok := other.state[id]; ok {
				result := dvvEntry[T]{}
				if entry.counter >= otherEntry.counter {
					result.counter = entry.counter
					result.values = entry.values[0:min(int(entry.counter-otherEntry.counter)+len(otherEntry.values), len(entry.values))]
				} else {
					result.counter = otherEntry.counter
					result.values = otherEntry.values[0:min(int(otherEntry.counter-entry.counter)+len(entry.values), len(otherEntry.values))]
				}
				ds.state[id] = result
			}
		} else {
			ds.state[id] = other.state[id]
		}
	}
}

// Update serves a put against ds with the given client context vv and new value: it drops siblings
// vv already knows about, then generates a fresh dot for value on this node. This is the put flow from
// paper §6.2: discard obsolete versions, then event to record the new one.
func (ds *DVVSet[T]) Update(vv VV, value T) {
	ds.discard(vv)
	ds.event(vv, value)
}

// MarshalJSON serializes only the replicated state. nodeID identifies the local replica and is
// not part of the data shared across nodes.
func (ds *DVVSet[T]) MarshalJSON() ([]byte, error) {
	if ds == nil {
		return []byte("null"), nil
	}
	v := struct {
		State map[NodeID]dvvEntry[T] `json:"state"`
	}{
		State: ds.state,
	}
	return json.Marshal(v)
}

func (ds *DVVSet[T]) UnmarshalJSON(data []byte) error {
	var v struct {
		State map[NodeID]dvvEntry[T] `json:"state"`
	}
	err := json.Unmarshal(data, &v)
	if err != nil {
		return err
	}
	ds.state = v.State
	return nil
}
