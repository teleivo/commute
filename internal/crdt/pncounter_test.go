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
				d := c.Increment(5)
				c.Merge(&d)
				return c
			},
			want: 5,
		},
		"DecrementOnly": {
			setup: func(id NodeID) *PNCounter {
				c := NewPNCounter(id)
				d := c.Decrement(3)
				c.Merge(&d)
				return c
			},
			want: -3,
		},
		"IncrementAndDecrement": {
			setup: func(id NodeID) *PNCounter {
				c := NewPNCounter(id)
				d := c.Increment(10)
				c.Merge(&d)
				d = c.Decrement(3)
				c.Merge(&d)
				return c
			},
			want: 7,
		},
		"DecrementBelowZero": {
			setup: func(id NodeID) *PNCounter {
				c := NewPNCounter(id)
				d := c.Increment(2)
				c.Merge(&d)
				d = c.Decrement(5)
				c.Merge(&d)
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
				d := c.Increment(5)
				c.Merge(&d)
				return c
			},
			want: 5,
		},
		"DisjointNodes": {
			a: func() *PNCounter {
				c := NewPNCounter("a")
				d := c.Increment(3)
				c.Merge(&d)
				return c
			},
			b: func() *PNCounter {
				c := NewPNCounter("b")
				d := c.Increment(7)
				c.Merge(&d)
				return c
			},
			want: 10,
		},
		"BothIncrementAndDecrement": {
			a: func() *PNCounter {
				c := NewPNCounter("a")
				d := c.Increment(10)
				c.Merge(&d)
				d = c.Decrement(2)
				c.Merge(&d)
				return c
			},
			b: func() *PNCounter {
				c := NewPNCounter("b")
				d := c.Increment(5)
				c.Merge(&d)
				d = c.Decrement(3)
				c.Merge(&d)
				return c
			},
			want: 10,
		},
		"OverlappingTakesMax": {
			a: func() *PNCounter {
				c := NewPNCounter("a")
				d := c.Increment(3)
				c.Merge(&d)
				d = c.Decrement(1)
				c.Merge(&d)
				return c
			},
			b: func() *PNCounter {
				c := NewPNCounter("a")
				d := c.Increment(5)
				c.Merge(&d)
				d = c.Decrement(2)
				c.Merge(&d)
				return c
			},
			want: 3,
		},
		"MergeSelf": {
			a: func() *PNCounter {
				c := NewPNCounter("a")
				d := c.Increment(5)
				c.Merge(&d)
				d = c.Decrement(2)
				c.Merge(&d)
				return c
			},
			b: func() *PNCounter {
				c := NewPNCounter("a")
				d := c.Increment(5)
				c.Merge(&d)
				d = c.Decrement(2)
				c.Merge(&d)
				return c
			},
			want: 3,
		},
		"ThreeNodesMergedPairwise": {
			a: func() *PNCounter {
				c := NewPNCounter("a")
				d := c.Increment(2)
				c.Merge(&d)
				d = c.Decrement(1)
				c.Merge(&d)
				return c
			},
			b: func() *PNCounter {
				// Simulate b having already merged with c.
				b := NewPNCounter("b")
				d := b.Increment(4)
				b.Merge(&d)
				c := NewPNCounter("c")
				d = c.Increment(6)
				c.Merge(&d)
				d = c.Decrement(3)
				c.Merge(&d)
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
