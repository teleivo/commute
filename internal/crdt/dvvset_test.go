package crdt

import (
	"slices"
	"testing"

	"github.com/teleivo/assertive/assert"
)

func TestNewDVVSet(t *testing.T) {
	d := NewDVVSet[string]("a")

	assert.Nil(t, d.Values())
	assert.EqualValues(t, d.Join(), VV{})
}

func TestDVVSetValues(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		d := NewDVVSet[string]("a")

		assert.Nil(t, d.Values())
	})

	t.Run("MultipleEntriesMultipleValues", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 4, values: []string{"v5", "v0"}},
				"b": {counter: 0, values: nil},
				"c": {counter: 1, values: []string{"v3"}},
			},
		}

		got := d.Values()
		slices.Sort(got)

		assert.EqualValues(t, got, []string{"v0", "v3", "v5"})
	})

	t.Run("EntryWithNoValues", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: nil},
			},
		}

		assert.Nil(t, d.Values())
	})
}

func TestDVVSetJoin(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		d := NewDVVSet[string]("a")

		assert.EqualValues(t, d.Join(), VV{})
	})

	t.Run("ProjectsCountersDroppingValues", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 4, values: []string{"v5", "v0"}},
				"b": {counter: 0, values: nil},
				"c": {counter: 1, values: []string{"v3"}},
			},
		}

		got := d.Join()

		assert.EqualValues(t, got, VV{"a": 4, "b": 0, "c": 1})
	})
}

func TestDVVSetDiscard(t *testing.T) {
	t.Run("EmptyContextKeepsAllValues", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2", "v1"}},
			},
		}

		d.discard(VV{})

		assert.EqualValues(t, d.Values(), []string{"v2", "v1"})
	})

	t.Run("ContextCoveringAllDiscardsAllValues", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2", "v1"}},
			},
		}

		d.discard(VV{"a": 2})

		assert.Nil(t, d.Values())
	})

	t.Run("ContextCoveringPrefixDiscardsOlderValues", func(t *testing.T) {
		// entry (a, 3, [v3, v2, v1]) with client ctx {a: 1} should keep only
		// the values for counters > 1, i.e. [v3, v2]. Per the paper:
		// first(n-C(r), l) = first(3-1, [v3,v2,v1]) = [v3, v2].
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 3, values: []string{"v3", "v2", "v1"}},
			},
		}

		d.discard(VV{"a": 1})

		got := d.Values()
		assert.EqualValues(t, got, []string{"v3", "v2"})
	})

	t.Run("ContextWithHigherCounterThanEntry", func(t *testing.T) {
		// If C(r) >= n then first(n-C(r), l) = first(0 or negative, l) = [].
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2", "v1"}},
			},
		}

		d.discard(VV{"a": 5})

		assert.Nil(t, d.Values())
	})

	t.Run("ContextMissingIDLeavesEntryUntouched", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2", "v1"}},
				"b": {counter: 1, values: []string{"vb"}},
			},
		}

		d.discard(VV{"a": 1})

		assert.EqualValues(t, d.state["a"].values, []string{"v2"})
		assert.EqualValues(t, d.state["b"].values, []string{"vb"})
	})
}

func TestDVVSetEvent(t *testing.T) {
	t.Run("FirstEventOnEmptyDVVSet", func(t *testing.T) {
		// r not in S, empty context: new entry (r, 1, [v]).
		d := NewDVVSet[string]("a")

		d.event(VV{}, "v1")

		assert.Equals(t, d.state["a"].counter, uint64(1))
		assert.EqualValues(t, d.state["a"].values, []string{"v1"})
	})

	t.Run("BumpsCounterAndPrependsValueForOwnID", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: []string{"v1"}},
			},
		}

		d.event(VV{}, "v2")

		assert.Equals(t, d.state["a"].counter, uint64(2))
		assert.EqualValues(t, d.state["a"].values, []string{"v2", "v1"})
	})

	t.Run("AddsNewOwnEntryWhenOtherIDsPresent", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "b",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: []string{"v1"}},
			},
		}

		d.event(VV{}, "v2")

		assert.Equals(t, d.state["a"].counter, uint64(1))
		assert.EqualValues(t, d.state["a"].values, []string{"v1"})
		assert.Equals(t, d.state["b"].counter, uint64(1))
		assert.EqualValues(t, d.state["b"].values, []string{"v2"})
	})

	t.Run("OtherIDEntryWithContextLessOrEqualIsUnchanged", func(t *testing.T) {
		// For i != r: counter becomes max(n, C(i)). Here C(a)=1 and n=3, so no change.
		d := &DVVSet[string]{
			nodeID: "b",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 3, values: []string{"v1"}},
			},
		}

		d.event(VV{"a": 1}, "v2")

		assert.Equals(t, d.state["a"].counter, uint64(3))
		assert.EqualValues(t, d.state["a"].values, []string{"v1"})
		assert.Equals(t, d.state["b"].counter, uint64(1))
		assert.EqualValues(t, d.state["b"].values, []string{"v2"})
	})

	t.Run("OtherIDEntryCounterBumpedByContextMax", func(t *testing.T) {
		// For i != r: if C(i) > n, bump n to C(i). Values are preserved.
		d := &DVVSet[string]{
			nodeID: "b",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: []string{"v1"}},
			},
		}

		d.event(VV{"a": 5}, "v2")

		assert.Equals(t, d.state["a"].counter, uint64(5))
		assert.EqualValues(t, d.state["a"].values, []string{"v1"})
		assert.Equals(t, d.state["b"].counter, uint64(1))
		assert.EqualValues(t, d.state["b"].values, []string{"v2"})
	})

	t.Run("ContextIDNotInDVVSetIsIgnored", func(t *testing.T) {
		// The set-builder ranges over S: ids only in C (and not r) do not add entries.
		d := NewDVVSet[string]("a")

		d.event(VV{"b": 4}, "v1")

		_, hasB := d.state["b"]
		assert.False(t, hasB, "expected b not added to S, got %v", d.state["b"])
		assert.Equals(t, d.state["a"].counter, uint64(1))
		assert.EqualValues(t, d.state["a"].values, []string{"v1"})
	})

	t.Run("OwnEntryUsesNPlusOneNotMaxWithContext", func(t *testing.T) {
		// Formula for i = r is n+1, not max(n, C(r))+1. Under system invariants
		// C(r) <= n always, so this only matters as a spec-faithfulness check.
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v1"}},
			},
		}

		d.event(VV{"a": 10}, "v2")

		assert.Equals(t, d.state["a"].counter, uint64(3))
		assert.EqualValues(t, d.state["a"].values, []string{"v2", "v1"})
	})

	t.Run("MultipleOtherIDsHandledIndependently", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "c",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"va"}},
				"b": {counter: 5, values: []string{"vb"}},
			},
		}

		d.event(VV{"a": 7, "b": 3}, "vc")

		assert.Equals(t, d.state["a"].counter, uint64(7))
		assert.EqualValues(t, d.state["a"].values, []string{"va"})
		assert.Equals(t, d.state["b"].counter, uint64(5))
		assert.EqualValues(t, d.state["b"].values, []string{"vb"})
		assert.Equals(t, d.state["c"].counter, uint64(1))
		assert.EqualValues(t, d.state["c"].values, []string{"vc"})
	})
}

func TestDVVSetSync(t *testing.T) {
	t.Run("EmptyIntoEmpty", func(t *testing.T) {
		d := NewDVVSet[string]("a")
		other := NewDVVSet[string]("b")

		d.Sync(other)

		assert.Equals(t, len(d.state), 0, "expected empty state, got %v", d.state)
	})

	t.Run("EmptyReceiverAbsorbsOther", func(t *testing.T) {
		d := NewDVVSet[string]("a")
		other := &DVVSet[string]{
			nodeID: "b",
			state: map[NodeID]dvvEntry[string]{
				"b": {counter: 1, values: []string{"v1"}},
			},
		}

		d.Sync(other)

		assert.Equals(t, d.state["b"].counter, uint64(1))
		assert.EqualValues(t, d.state["b"].values, []string{"v1"})
	})

	t.Run("EmptyOtherLeavesReceiverUnchanged", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2", "v1"}},
			},
		}
		other := NewDVVSet[string]("b")

		d.Sync(other)

		assert.Equals(t, d.state["a"].counter, uint64(2))
		assert.EqualValues(t, d.state["a"].values, []string{"v2", "v1"})
	})

	t.Run("DisjointIDsAreUnioned", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: []string{"va"}},
			},
		}
		other := &DVVSet[string]{
			nodeID: "b",
			state: map[NodeID]dvvEntry[string]{
				"b": {counter: 1, values: []string{"vb"}},
			},
		}

		d.Sync(other)

		assert.Equals(t, d.state["a"].counter, uint64(1))
		assert.EqualValues(t, d.state["a"].values, []string{"va"})
		assert.Equals(t, d.state["b"].counter, uint64(1))
		assert.EqualValues(t, d.state["b"].values, []string{"vb"})
	})

	t.Run("SameIDSameCounterSameValuesIsIdempotent", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2", "v1"}},
			},
		}
		other := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2", "v1"}},
			},
		}

		d.Sync(other)

		assert.Equals(t, d.state["a"].counter, uint64(2))
		assert.EqualValues(t, d.state["a"].values, []string{"v2", "v1"})
	})

	t.Run("HigherCounterDominatesWhenCoverageIsAlsoDominant", func(t *testing.T) {
		// Receiver: (a, 2, [v2]) — counter 2, oldest-kept dot is 2.
		// Other:    (a, 1, [v1]) — counter 1, oldest-kept dot is 1.
		// Receiver's N-len = 2-1 = 1 >= 1-1 = 0, so keep receiver unchanged.
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2"}},
			},
		}
		other := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: []string{"v1"}},
			},
		}

		d.Sync(other)

		assert.Equals(t, d.state["a"].counter, uint64(2))
		assert.EqualValues(t, d.state["a"].values, []string{"v2"})
	})

	t.Run("LowerCounterIsAbsorbed", func(t *testing.T) {
		// Inverse of above: receiver has lower counter, other dominates fully.
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: []string{"v1"}},
			},
		}
		other := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2"}},
			},
		}

		d.Sync(other)

		assert.Equals(t, d.state["a"].counter, uint64(2))
		assert.EqualValues(t, d.state["a"].values, []string{"v2"})
	})

	t.Run("HigherCounterTruncatesWhenLowSideHasExtraSiblings", func(t *testing.T) {
		// Mirrors Erlang: sync([W,Z]) where W={a,1,[]}, Z={a,2,[v2,v1]}.
		// Z has higher counter 2. W has counter 1 but no values, meaning W already
		// knows about dot (a,1) and it was not kept. So merging drops v1 from Z.
		// Expected result per Erlang test: {a, 2, [v2]}.
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: nil},
			},
		}
		other := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2", "v1"}},
			},
		}

		d.Sync(other)

		assert.Equals(t, d.state["a"].counter, uint64(2))
		assert.EqualValues(t, d.state["a"].values, []string{"v2"})
	})

	t.Run("ConcurrentSiblingsOnSameIDFromBothSides", func(t *testing.T) {
		// Both sides advanced a to counter 2, but kept different siblings:
		// receiver: (a, 2, [v2a, v1]), other: (a, 2, [v2b, v1]).
		// Counters equal, coverage equal (N-len = 0 both sides), keep either.
		// The Erlang merge returns L1 in the N1 >= N2 + coverage-equal branch.
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2a", "v1"}},
			},
		}
		other := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 2, values: []string{"v2b", "v1"}},
			},
		}

		d.Sync(other)

		assert.Equals(t, d.state["a"].counter, uint64(2))
		// Either side's list is valid per the paper when coverage matches; the
		// Erlang impl returns L1 (the receiver's). Accept either receiver or other values.
		got := d.state["a"].values
		if !slices.Equal(got, []string{"v2a", "v1"}) && !slices.Equal(got, []string{"v2b", "v1"}) {
			t.Errorf("got %v, want receiver or other siblings", got)
		}
	})

	t.Run("MultipleIDsMixedMergeBranches", func(t *testing.T) {
		// Exercises several branches at once:
		//   a: only in receiver, kept
		//   b: only in other, absorbed
		//   c: in both, receiver has higher counter and dominates
		//   d: in both, other has higher counter and dominates
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: []string{"va"}},
				"c": {counter: 3, values: []string{"vc3"}},
				"d": {counter: 1, values: []string{"vd1"}},
			},
		}
		other := &DVVSet[string]{
			nodeID: "b",
			state: map[NodeID]dvvEntry[string]{
				"b": {counter: 1, values: []string{"vb"}},
				"c": {counter: 2, values: []string{"vc2"}},
				"d": {counter: 3, values: []string{"vd3"}},
			},
		}

		d.Sync(other)

		assert.Equals(t, d.state["a"].counter, uint64(1))
		assert.EqualValues(t, d.state["a"].values, []string{"va"})
		assert.Equals(t, d.state["b"].counter, uint64(1))
		assert.EqualValues(t, d.state["b"].values, []string{"vb"})
		assert.Equals(t, d.state["c"].counter, uint64(3))
		assert.EqualValues(t, d.state["c"].values, []string{"vc3"})
		assert.Equals(t, d.state["d"].counter, uint64(3))
		assert.EqualValues(t, d.state["d"].values, []string{"vd3"})
	})

	t.Run("DoesNotMutateOther", func(t *testing.T) {
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: []string{"va"}},
			},
		}
		other := &DVVSet[string]{
			nodeID: "b",
			state: map[NodeID]dvvEntry[string]{
				"b": {counter: 1, values: []string{"vb"}},
			},
		}

		d.Sync(other)

		// other should be untouched.
		assert.Equals(t, len(other.state), 1, "expected other.state size 1, got %v", other.state)
		assert.Equals(t, other.state["b"].counter, uint64(1))
		assert.EqualValues(t, other.state["b"].values, []string{"vb"})
	})
}

func TestDVVSetUpdate(t *testing.T) {
	t.Run("FirstWriteWithEmptyContext", func(t *testing.T) {
		// Fresh client, fresh server: discard is a no-op, event generates (a, 1, [v1]).
		d := NewDVVSet[string]("a")

		d.Update(VV{}, "v1")

		assert.Equals(t, d.state["a"].counter, uint64(1))
		assert.EqualValues(t, d.state["a"].values, []string{"v1"})
	})

	t.Run("ReplacesSiblingClientAlreadyKnew", func(t *testing.T) {
		// Server has (a, 1, [v1]); client read it and writes v2 with ctx {a:1}.
		// Discard drops v1; event then generates (a, 2, [v2]).
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: []string{"v1"}},
			},
		}

		d.Update(VV{"a": 1}, "v2")

		assert.Equals(t, d.state["a"].counter, uint64(2))
		assert.EqualValues(t, d.state["a"].values, []string{"v2"})
	})

	t.Run("ConcurrentWriteKeepsSibling", func(t *testing.T) {
		// Server has (a, 1, [v1]); client writes v2 with empty context (didn't read v1).
		// Discard is a no-op since vv is empty; event prepends v2, keeping v1 as concurrent.
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: []string{"v1"}},
			},
		}

		d.Update(VV{}, "v2")

		assert.Equals(t, d.state["a"].counter, uint64(2))
		assert.EqualValues(t, d.state["a"].values, []string{"v2", "v1"})
	})

	t.Run("DropsSiblingOnlyForCoveredID", func(t *testing.T) {
		// Server has (a, 1, [va]) and (b, 1, [vb]). Client writes on a with ctx {a:1}.
		// Discard drops va but leaves vb; event generates (a, 2, [v2]) and bumps/keeps b.
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"a": {counter: 1, values: []string{"va"}},
				"b": {counter: 1, values: []string{"vb"}},
			},
		}

		d.Update(VV{"a": 1}, "v2")

		assert.Equals(t, d.state["a"].counter, uint64(2))
		assert.EqualValues(t, d.state["a"].values, []string{"v2"})
		assert.Equals(t, d.state["b"].counter, uint64(1))
		assert.EqualValues(t, d.state["b"].values, []string{"vb"})
	})

	t.Run("AbsorbsContextKnowledgeForOtherID", func(t *testing.T) {
		// Server has (b, 1, [vb]) but never heard of c. Client on a writes with ctx {c: 3}.
		// Set-builder for event ranges over S, so c is not added. But if ctx also covers b,
		// discard drops vb.
		d := &DVVSet[string]{
			nodeID: "a",
			state: map[NodeID]dvvEntry[string]{
				"b": {counter: 1, values: []string{"vb"}},
			},
		}

		d.Update(VV{"b": 1, "c": 3}, "va")

		assert.Equals(t, d.state["a"].counter, uint64(1))
		assert.EqualValues(t, d.state["a"].values, []string{"va"})
		assert.Equals(t, d.state["b"].counter, uint64(1))
		assert.Nil(t, d.state["b"].values)
		_, hasC := d.state["c"]
		assert.False(t, hasC, "expected c not added, got %v", d.state["c"])
	})
}
