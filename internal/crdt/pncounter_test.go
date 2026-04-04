package crdt

import (
	"testing"

	"github.com/teleivo/assertive/assert"
)

func TestPNCounterValue(t *testing.T) {
	tests := map[string]struct {
		setup func(NodeID) *PNCounter
		want  int64
	}{
		"Empty": {
			setup: func(id NodeID) *PNCounter { return NewPNCounter(id) },
			want:  0,
		},
		"IncrementOnly": {
			setup: func(id NodeID) *PNCounter {
				c := NewPNCounter(id)
				c.Increment(5)
				return c
			},
			want: 5,
		},
		"DecrementOnly": {
			setup: func(id NodeID) *PNCounter {
				c := NewPNCounter(id)
				c.Decrement(3)
				return c
			},
			want: -3,
		},
		"IncrementAndDecrement": {
			setup: func(id NodeID) *PNCounter {
				c := NewPNCounter(id)
				c.Increment(10)
				c.Decrement(3)
				return c
			},
			want: 7,
		},
		"DecrementBelowZero": {
			setup: func(id NodeID) *PNCounter {
				c := NewPNCounter(id)
				c.Increment(2)
				c.Decrement(5)
				return c
			},
			want: -3,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := tc.setup("a").Value()
			assert.EqualValues(t, got, tc.want)
		})
	}
}

func TestPNCounterMerge(t *testing.T) {
	tests := map[string]struct {
		a    func() *PNCounter
		b    func() *PNCounter
		want int64
	}{
		"BothEmpty": {
			a:    func() *PNCounter { return NewPNCounter("a") },
			b:    func() *PNCounter { return NewPNCounter("b") },
			want: 0,
		},
		"MergeIntoEmpty": {
			a: func() *PNCounter { return NewPNCounter("a") },
			b: func() *PNCounter {
				c := NewPNCounter("b")
				c.Increment(5)
				return c
			},
			want: 5,
		},
		"DisjointNodes": {
			a: func() *PNCounter {
				c := NewPNCounter("a")
				c.Increment(3)
				return c
			},
			b: func() *PNCounter {
				c := NewPNCounter("b")
				c.Increment(7)
				return c
			},
			want: 10,
		},
		"BothIncrementAndDecrement": {
			a: func() *PNCounter {
				c := NewPNCounter("a")
				c.Increment(10)
				c.Decrement(2)
				return c
			},
			b: func() *PNCounter {
				c := NewPNCounter("b")
				c.Increment(5)
				c.Decrement(3)
				return c
			},
			want: 10,
		},
		"OverlappingTakesMax": {
			a: func() *PNCounter {
				c := NewPNCounter("a")
				c.Increment(3)
				c.Decrement(1)
				return c
			},
			b: func() *PNCounter {
				c := NewPNCounter("a")
				c.Increment(5)
				c.Decrement(2)
				return c
			},
			want: 3,
		},
		"MergeSelf": {
			a: func() *PNCounter {
				c := NewPNCounter("a")
				c.Increment(5)
				c.Decrement(2)
				return c
			},
			b: func() *PNCounter {
				c := NewPNCounter("a")
				c.Increment(5)
				c.Decrement(2)
				return c
			},
			want: 3,
		},
		"ThreeNodesMergedPairwise": {
			a: func() *PNCounter {
				c := NewPNCounter("a")
				c.Increment(2)
				c.Decrement(1)
				return c
			},
			b: func() *PNCounter {
				// Simulate b having already merged with c.
				b := NewPNCounter("b")
				b.Increment(4)
				c := NewPNCounter("c")
				c.Increment(6)
				c.Decrement(3)
				b.Merge(c)
				return b
			},
			want: 8,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := tc.a()
			a.Merge(tc.b())
			assert.EqualValues(t, a.Value(), tc.want)
		})
	}
}
