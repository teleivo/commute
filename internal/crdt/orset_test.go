package crdt

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/teleivo/assertive/assert"
)

func TestORSetContains(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		s := NewORSet("a")

		assert.False(t, s.Contains("apple"))
	})

	t.Run("AddRemoveAddLifecycle", func(t *testing.T) {
		s := NewORSet("a")

		s.Add("apple", VV{})
		assert.True(t, s.Contains("apple"))
		assert.False(t, s.Contains("banana"))

		s.Remove("apple", s.CausalContext("apple"))
		assert.False(t, s.Contains("apple"))

		s.Add("apple", s.CausalContext("apple"))
		assert.True(t, s.Contains("apple"))
	})

	t.Run("RemoveWithoutObservedContextLosesToAdd", func(t *testing.T) {
		// A remove with empty context did not observe the add, so by OR-Set's observed-remove
		// semantics the remove is concurrent with the add and the add survives.
		s := NewORSet("a")

		s.Add("apple", VV{})
		s.Remove("apple", VV{})

		assert.True(t, s.Contains("apple"))
	})

	t.Run("RemoveOnlyAffectsTarget", func(t *testing.T) {
		s := NewORSet("a")

		s.Add("apple", VV{})
		s.Add("banana", VV{})
		s.Remove("apple", s.CausalContext("apple"))

		assert.False(t, s.Contains("apple"))
		assert.True(t, s.Contains("banana"))
	})

	t.Run("RemoveNonExistentIsNoOp", func(t *testing.T) {
		s := NewORSet("a")

		s.Remove("apple", VV{})

		assert.False(t, s.Contains("apple"))
	})

	t.Run("IdempotentRemove", func(t *testing.T) {
		// Removing twice with the observed context has the same effect as removing once.
		s := NewORSet("a")

		s.Add("apple", VV{})
		s.Remove("apple", s.CausalContext("apple"))
		s.Remove("apple", s.CausalContext("apple"))

		assert.False(t, s.Contains("apple"))
	})

	t.Run("MultipleElements", func(t *testing.T) {
		s := NewORSet("a")

		s.Add("apple", VV{})
		s.Add("banana", VV{})
		s.Add("cherry", VV{})

		assert.True(t, s.Contains("apple"))
		assert.True(t, s.Contains("banana"))
		assert.True(t, s.Contains("cherry"))
		assert.False(t, s.Contains("durian"))
	})

	t.Run("DuplicateAddStaysInSet", func(t *testing.T) {
		// Adding twice with empty context creates two concurrent add siblings; element is present.
		s := NewORSet("a")

		s.Add("apple", VV{})
		s.Add("apple", VV{})

		assert.True(t, s.Contains("apple"))
	})
}

func TestORSetCausalContext(t *testing.T) {
	t.Run("UnknownElementReturnsEmpty", func(t *testing.T) {
		s := NewORSet("a")

		assert.EqualValues(t, s.CausalContext("apple"), VV{})
	})

	t.Run("AfterOneAddContainsOwnDot", func(t *testing.T) {
		s := NewORSet("a")

		s.Add("apple", VV{})

		assert.EqualValues(t, s.CausalContext("apple"), VV{"a": 1})
	})

	t.Run("AfterMergeIncludesOtherReplicaDot", func(t *testing.T) {
		// Replica a adds apple; b merges and then reads the context. b should see a's dot.
		a := NewORSet("a")
		a.Add("apple", VV{})
		b := NewORSet("b")
		b.Merge(a)

		assert.EqualValues(t, b.CausalContext("apple"), VV{"a": 1})
	})
}

func TestORSetMerge(t *testing.T) {
	t.Run("BothEmpty", func(t *testing.T) {
		t.Parallel()
		a := NewORSet("a")
		b := NewORSet("b")

		a.Merge(b)

		assert.False(t, a.Contains("apple"))
	})

	t.Run("MergeIntoEmpty", func(t *testing.T) {
		t.Parallel()
		a := NewORSet("a")
		b := NewORSet("b")
		b.Add("apple", VV{})

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("MergeFromEmpty", func(t *testing.T) {
		t.Parallel()
		a := NewORSet("a")
		a.Add("apple", VV{})
		b := NewORSet("b")

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("DisjointElements", func(t *testing.T) {
		t.Parallel()
		a := NewORSet("a")
		a.Add("apple", VV{})
		b := NewORSet("b")
		b.Add("banana", VV{})

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
		assert.True(t, a.Contains("banana"))
	})

	t.Run("SameElementFromTwoReplicas", func(t *testing.T) {
		t.Parallel()
		a := NewORSet("a")
		a.Add("apple", VV{})
		b := NewORSet("b")
		b.Add("apple", VV{})

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("MergeIsIdempotent", func(t *testing.T) {
		t.Parallel()
		a := NewORSet("a")
		a.Add("apple", VV{})
		b := NewORSet("b")
		b.Add("banana", VV{})

		a.Merge(b)
		a.Merge(b)

		assert.True(t, a.Contains("apple"))
		assert.True(t, a.Contains("banana"))
	})

	t.Run("MergeIsCommutative", func(t *testing.T) {
		t.Parallel()
		a1 := NewORSet("a")
		a1.Add("apple", VV{})
		b1 := NewORSet("b")
		b1.Add("banana", VV{})
		a2 := NewORSet("a")
		a2.Add("apple", VV{})
		b2 := NewORSet("b")
		b2.Add("banana", VV{})

		a1.Merge(b1)
		b2.Merge(a2)

		assert.True(t, a1.Contains("apple"))
		assert.True(t, a1.Contains("banana"))
		assert.True(t, b2.Contains("apple"))
		assert.True(t, b2.Contains("banana"))
	})

	t.Run("MergeSelf", func(t *testing.T) {
		t.Parallel()
		a := NewORSet("a")
		a.Add("apple", VV{})

		a.Merge(a)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("ObservedRemoveOnOneReplicaPropagates", func(t *testing.T) {
		t.Parallel()
		// a adds apple and banana, b syncs, b removes apple with observed context, a syncs back.
		a := NewORSet("a")
		a.Add("apple", VV{})
		a.Add("banana", VV{})
		b := NewORSet("b")
		b.Merge(a)

		b.Remove("apple", b.CausalContext("apple"))
		a.Merge(b)

		assert.False(t, a.Contains("apple"))
		assert.True(t, a.Contains("banana"))
	})

	t.Run("ConcurrentAddWinsOverRemove", func(t *testing.T) {
		t.Parallel()
		// a adds apple; b observes via merge; then concurrently:
		//   b removes apple (observing a's dot)
		//   a re-adds apple (fresh dot, unknown to b)
		// After merge, the concurrent add survives because b's remove did not observe it.
		a := NewORSet("a")
		a.Add("apple", VV{})
		b := NewORSet("b")
		b.Merge(a)

		b.Remove("apple", b.CausalContext("apple"))
		a.Add("apple", a.CausalContext("apple"))

		a.Merge(b)
		b.Merge(a)
		assert.True(t, a.Contains("apple"))
		assert.True(t, b.Contains("apple"))
		assert.EqualValues(t, a.CausalContext("apple"), VV{"a": 2, "b": 1})
		assert.EqualValues(t, b.CausalContext("apple"), VV{"a": 2, "b": 1})
	})

	t.Run("ConcurrentObservedRemovesBothDropAdd", func(t *testing.T) {
		t.Parallel()
		// a adds apple; b and c each merge to observe the add; both remove concurrently with
		// their observed context. The add is obsolete on both sides; after merging all three,
		// apple is gone.
		a := NewORSet("a")
		a.Add("apple", VV{})
		b := NewORSet("b")
		b.Merge(a)
		c := NewORSet("c")
		c.Merge(a)

		b.Remove("apple", b.CausalContext("apple"))
		c.Remove("apple", c.CausalContext("apple"))

		a.Merge(b)
		a.Merge(c)
		assert.False(t, a.Contains("apple"))
		assert.EqualValues(t, a.CausalContext("apple"), VV{"a": 1, "b": 1, "c": 1})
	})

	t.Run("ConcurrentAddsFromDifferentReplicasBothSurvive", func(t *testing.T) {
		t.Parallel()
		// a and b independently add apple with empty context; their dots are distinct.
		// After merge, both add siblings survive; element is present.
		a := NewORSet("a")
		a.Add("apple", VV{})
		b := NewORSet("b")
		b.Add("apple", VV{})

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
		// And b's causal context seen from a after merge knows both dots.
		assert.EqualValues(t, a.CausalContext("apple"), VV{"a": 1, "b": 1})
	})
}

func TestORSetValues(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		s := NewORSet("a")

		assert.Nil(t, s.Values())
	})

	t.Run("SingleElement", func(t *testing.T) {
		s := NewORSet("a")

		s.Add("apple", VV{})

		assert.EqualValues(t, s.Values(), []string{"apple"})
	})

	t.Run("MultipleElements", func(t *testing.T) {
		s := NewORSet("a")

		s.Add("apple", VV{})
		s.Add("banana", VV{})
		s.Add("cherry", VV{})

		got := s.Values()
		slices.Sort(got)
		assert.EqualValues(t, got, []string{"apple", "banana", "cherry"})
	})

	t.Run("DuplicateAddReturnsSingle", func(t *testing.T) {
		s := NewORSet("a")

		s.Add("apple", VV{})
		s.Add("apple", VV{})

		assert.EqualValues(t, s.Values(), []string{"apple"})
	})

	t.Run("AfterObservedRemove", func(t *testing.T) {
		s := NewORSet("a")

		s.Add("apple", VV{})
		s.Add("banana", VV{})
		s.Remove("apple", s.CausalContext("apple"))

		assert.EqualValues(t, s.Values(), []string{"banana"})
	})

	t.Run("AfterObservedRemoveAll", func(t *testing.T) {
		s := NewORSet("a")

		s.Add("apple", VV{})
		s.Remove("apple", s.CausalContext("apple"))

		assert.Nil(t, s.Values())
	})

	t.Run("ReAddAfterObservedRemove", func(t *testing.T) {
		s := NewORSet("a")

		s.Add("apple", VV{})
		s.Remove("apple", s.CausalContext("apple"))
		s.Add("apple", s.CausalContext("apple"))

		assert.EqualValues(t, s.Values(), []string{"apple"})
	})

	t.Run("ConcurrentAddAndRemoveLeavesElement", func(t *testing.T) {
		// Element has a live add sibling concurrent with a remove sibling; Values includes it.
		a := NewORSet("a")
		a.Add("apple", VV{})
		b := NewORSet("b")
		b.Merge(a)

		a.Add("apple", VV{})                        // concurrent re-add on a, empty context
		b.Remove("apple", b.CausalContext("apple")) // remove on b, only observes original add

		a.Merge(b)

		assert.EqualValues(t, a.Values(), []string{"apple"})
	})
}

func TestORSetMarshalRoundtrip(t *testing.T) {
	a := NewORSet("a")
	a.Add("apple", VV{})
	a.Add("banana", VV{})
	a.Add("cherry", VV{})
	a.Remove("apple", a.CausalContext("apple"))

	data, err := json.Marshal(a)
	assert.NoError(t, err)

	b := NewORSet("b")
	err = json.Unmarshal(data, b)
	assert.NoError(t, err)

	assert.False(t, b.Contains("apple"))
	assert.True(t, b.Contains("banana"))
	assert.True(t, b.Contains("cherry"))
	assert.False(t, b.Contains("durian"))

	got := b.Values()
	slices.Sort(got)
	assert.EqualValues(t, got, []string{"banana", "cherry"})

	// b's own node id is preserved across unmarshal; subsequent writes use "b" not "a".
	b.Add("date", VV{})
	assert.EqualValues(t, b.CausalContext("date"), VV{"b": 1})
}

func TestORSetUnmarshalNullState(t *testing.T) {
	// A wire payload with "state": null must unmarshal into a usable ORSet: subsequent writes
	// should not panic with "assignment to entry in nil map".
	s := NewORSet("a")
	err := json.Unmarshal([]byte(`{"state":null}`), s)
	assert.NoError(t, err)

	s.Add("apple", VV{})

	assert.True(t, s.Contains("apple"))
}
