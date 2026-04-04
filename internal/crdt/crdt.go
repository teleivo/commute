// Package crdt provides conflict-free replicated data types (CRDTs).
//
// See Shapiro et al., [A comprehensive study of Convergent and Commutative Replicated Data Types].
//
// [A comprehensive study of Convergent and Commutative Replicated Data Types]: https://inria.hal.science/inria-00555588/document
package crdt

import "encoding/json"

// NodeID uniquely identifies a node in the cluster.
type NodeID string

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
