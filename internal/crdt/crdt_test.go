package crdt

import (
	"testing"

	"github.com/teleivo/assertive/assert"
)

func TestGCounterValue(t *testing.T) {
	tests := map[string]struct {
		counter *GCounter
		want    uint64
	}{
		"Empty": {
			counter: NewGCounter("a"),
			want:    0,
		},
		"SingleNode": {
			counter: &GCounter{
				nodeID:   "a",
				counters: map[NodeID]uint64{"a": 5},
			},
			want: 5,
		},
		"MultipleNodes": {
			counter: &GCounter{
				nodeID:   "a",
				counters: map[NodeID]uint64{"a": 3, "b": 7, "c": 2},
			},
			want: 12,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := tc.counter.Value()
			assert.EqualValues(t, got, tc.want)
		})
	}
}

func TestGCounterIncrement(t *testing.T) {
	g := NewGCounter("a")

	d := g.Increment(1)
	g.Merge(&d)

	assert.EqualValues(t, g.Value(), uint64(1))

	d = g.Increment(1)
	g.Merge(&d)
	d = g.Increment(5)
	g.Merge(&d)

	assert.EqualValues(t, g.Value(), uint64(7))
}

func TestGCounterMerge(t *testing.T) {
	tests := map[string]struct {
		a    *GCounter
		b    *GCounter
		want uint64
	}{
		"BothEmpty": {
			a:    NewGCounter("a"),
			b:    NewGCounter("b"),
			want: 0,
		},
		"MergeIntoEmpty": {
			a: NewGCounter("a"),
			b: &GCounter{
				nodeID:   "b",
				counters: map[NodeID]uint64{"b": 5},
			},
			want: 5,
		},
		"DisjointNodes": {
			a: &GCounter{
				nodeID:   "a",
				counters: map[NodeID]uint64{"a": 3},
			},
			b: &GCounter{
				nodeID:   "b",
				counters: map[NodeID]uint64{"b": 7},
			},
			want: 10,
		},
		"OverlappingTakesMax": {
			a: &GCounter{
				nodeID:   "a",
				counters: map[NodeID]uint64{"a": 3, "b": 5},
			},
			b: &GCounter{
				nodeID:   "b",
				counters: map[NodeID]uint64{"a": 1, "b": 9},
			},
			want: 12,
		},
		"MergeSelf": {
			a: &GCounter{
				nodeID:   "a",
				counters: map[NodeID]uint64{"a": 5},
			},
			b: &GCounter{
				nodeID:   "a",
				counters: map[NodeID]uint64{"a": 5},
			},
			want: 5,
		},
		"ThreeNodes": {
			a: &GCounter{
				nodeID:   "a",
				counters: map[NodeID]uint64{"a": 2, "b": 4, "c": 1},
			},
			b: &GCounter{
				nodeID:   "b",
				counters: map[NodeID]uint64{"a": 3, "b": 3, "c": 6},
			},
			want: 13,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tc.a.Merge(tc.b)
			assert.EqualValues(t, tc.a.Value(), tc.want)
		})
	}
}
