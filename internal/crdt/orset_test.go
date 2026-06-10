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

		d := s.Add("apple", VV{})
		s.Merge(&d)
		assert.True(t, s.Contains("apple"))
		assert.False(t, s.Contains("banana"))

		d = s.Remove("apple", s.CausalContext("apple"))
		s.Merge(&d)
		assert.False(t, s.Contains("apple"))

		d = s.Add("apple", s.CausalContext("apple"))
		s.Merge(&d)
		assert.True(t, s.Contains("apple"))
	})

	t.Run("RemoveWithoutObservedContextLosesToAdd", func(t *testing.T) {
		// A remove with empty context did not observe the add, so by OR-Set's observed-remove
		// semantics the remove is concurrent with the add and the add survives.
		s := NewORSet("a")

		d := s.Add("apple", VV{})
		s.Merge(&d)
		d = s.Remove("apple", VV{})
		s.Merge(&d)

		assert.True(t, s.Contains("apple"))
	})

	t.Run("RemoveOnlyAffectsTarget", func(t *testing.T) {
		s := NewORSet("a")

		d := s.Add("apple", VV{})
		s.Merge(&d)
		d = s.Add("banana", VV{})
		s.Merge(&d)
		d = s.Remove("apple", s.CausalContext("apple"))
		s.Merge(&d)

		assert.False(t, s.Contains("apple"))
		assert.True(t, s.Contains("banana"))
	})

	t.Run("RemoveNonExistentIsNoOp", func(t *testing.T) {
		s := NewORSet("a")

		d := s.Remove("apple", VV{})
		s.Merge(&d)

		assert.False(t, s.Contains("apple"))
	})

	t.Run("IdempotentRemove", func(t *testing.T) {
		// Removing twice with the observed context has the same effect as removing once.
		s := NewORSet("a")

		d := s.Add("apple", VV{})
		s.Merge(&d)
		d = s.Remove("apple", s.CausalContext("apple"))
		s.Merge(&d)
		d = s.Remove("apple", s.CausalContext("apple"))
		s.Merge(&d)

		assert.False(t, s.Contains("apple"))
	})

	t.Run("MultipleElements", func(t *testing.T) {
		s := NewORSet("a")

		d := s.Add("apple", VV{})
		s.Merge(&d)
		d = s.Add("banana", VV{})
		s.Merge(&d)
		d = s.Add("cherry", VV{})
		s.Merge(&d)

		assert.True(t, s.Contains("apple"))
		assert.True(t, s.Contains("banana"))
		assert.True(t, s.Contains("cherry"))
		assert.False(t, s.Contains("durian"))
	})

	t.Run("DuplicateAddStaysInSet", func(t *testing.T) {
		// Adding twice with empty context creates two concurrent add siblings; element is present.
		s := NewORSet("a")

		d := s.Add("apple", VV{})
		s.Merge(&d)
		d = s.Add("apple", VV{})
		s.Merge(&d)

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

		d := s.Add("apple", VV{})
		s.Merge(&d)

		assert.EqualValues(t, s.CausalContext("apple"), VV{"a": 1})
	})

	t.Run("AfterMergeIncludesOtherReplicaDot", func(t *testing.T) {
		// Replica a adds apple; b merges and then reads the context. b should see a's dot.
		a := NewORSet("a")
		d := a.Add("apple", VV{})
		a.Merge(&d)
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
		d := b.Add("apple", VV{})
		b.Merge(&d)

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("MergeFromEmpty", func(t *testing.T) {
		t.Parallel()
		a := NewORSet("a")
		d := a.Add("apple", VV{})
		a.Merge(&d)
		b := NewORSet("b")

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("DisjointElements", func(t *testing.T) {
		t.Parallel()
		a := NewORSet("a")
		d := a.Add("apple", VV{})
		a.Merge(&d)
		b := NewORSet("b")
		d = b.Add("banana", VV{})
		b.Merge(&d)

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
		assert.True(t, a.Contains("banana"))
	})

	t.Run("SameElementFromTwoReplicas", func(t *testing.T) {
		t.Parallel()
		a := NewORSet("a")
		d := a.Add("apple", VV{})
		a.Merge(&d)
		b := NewORSet("b")
		d = b.Add("apple", VV{})
		b.Merge(&d)

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("MergeIsIdempotent", func(t *testing.T) {
		t.Parallel()
		a := NewORSet("a")
		d := a.Add("apple", VV{})
		a.Merge(&d)
		b := NewORSet("b")
		d = b.Add("banana", VV{})
		b.Merge(&d)

		a.Merge(b)
		a.Merge(b)

		assert.True(t, a.Contains("apple"))
		assert.True(t, a.Contains("banana"))
	})

	t.Run("MergeIsCommutative", func(t *testing.T) {
		t.Parallel()
		a1 := NewORSet("a")
		d := a1.Add("apple", VV{})
		a1.Merge(&d)
		b1 := NewORSet("b")
		d = b1.Add("banana", VV{})
		b1.Merge(&d)
		a2 := NewORSet("a")
		d = a2.Add("apple", VV{})
		a2.Merge(&d)
		b2 := NewORSet("b")
		d = b2.Add("banana", VV{})
		b2.Merge(&d)

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
		d := a.Add("apple", VV{})
		a.Merge(&d)

		a.Merge(a)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("ObservedRemoveOnOneReplicaPropagates", func(t *testing.T) {
		t.Parallel()
		// a adds apple and banana, b syncs, b removes apple with observed context, a syncs back.
		a := NewORSet("a")
		d := a.Add("apple", VV{})
		a.Merge(&d)
		d = a.Add("banana", VV{})
		a.Merge(&d)
		b := NewORSet("b")
		b.Merge(a)

		d = b.Remove("apple", b.CausalContext("apple"))
		b.Merge(&d)
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
		d := a.Add("apple", VV{})
		a.Merge(&d)
		b := NewORSet("b")
		b.Merge(a)

		d = b.Remove("apple", b.CausalContext("apple"))
		b.Merge(&d)
		d = a.Add("apple", a.CausalContext("apple"))
		a.Merge(&d)

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
		d := a.Add("apple", VV{})
		a.Merge(&d)
		b := NewORSet("b")
		b.Merge(a)
		c := NewORSet("c")
		c.Merge(a)

		d = b.Remove("apple", b.CausalContext("apple"))
		b.Merge(&d)
		d = c.Remove("apple", c.CausalContext("apple"))
		c.Merge(&d)

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
		d := a.Add("apple", VV{})
		a.Merge(&d)
		b := NewORSet("b")
		d = b.Add("apple", VV{})
		b.Merge(&d)

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
		// And b's causal context seen from a after merge knows both dots.
		assert.EqualValues(t, a.CausalContext("apple"), VV{"a": 1, "b": 1})
	})
}

func TestORSetIsZero(t *testing.T) {
	t.Run("NilState", func(t *testing.T) {
		t.Parallel()
		or := ORSet{}

		assert.True(t, or.IsZero())
	})

	t.Run("Empty", func(t *testing.T) {
		t.Parallel()
		or := NewORSet("a")

		assert.True(t, or.IsZero())
	})

	t.Run("RemoveAbsentElementReturnsZero", func(t *testing.T) {
		t.Parallel()
		or := NewORSet("a")

		d := or.Remove("apple", VV{})

		assert.True(t, d.IsZero())
	})

	t.Run("AfterAdd", func(t *testing.T) {
		t.Parallel()
		or := NewORSet("a")
		d := or.Add("apple", VV{})
		or.Merge(&d)

		assert.False(t, or.IsZero())
	})
}

func TestORSetIsLessOrEqual(t *testing.T) {
	t.Run("BothEmpty", func(t *testing.T) {
		t.Parallel()
		or := NewORSet("a")
		other := NewORSet("b")

		got := or.IsLessOrEqual(other)

		assert.True(t, got)
	})

	t.Run("StrictlyLess", func(t *testing.T) {
		t.Parallel()
		// or has apple added by "a"; other observed or's add and also added apple from "b".
		or := NewORSet("a")
		d := or.Add("apple", VV{})
		or.Merge(&d)
		other := NewORSet("b")
		other.Merge(or)
		d = other.Add("apple", other.CausalContext("apple"))
		other.Merge(&d)

		got := or.IsLessOrEqual(other)

		assert.True(t, got)
	})

	t.Run("ORFullyCoveredByOther", func(t *testing.T) {
		t.Parallel()
		// or has only apple; other has apple (same history) plus banana. The extra element in
		// other is irrelevant: or ⊑ other iff every element in or is ⊑ the corresponding element
		// in other.
		or := NewORSet("a")
		d := or.Add("apple", VV{})
		or.Merge(&d)
		other := NewORSet("b")
		other.Merge(or)
		d = other.Add("banana", VV{})
		other.Merge(&d)

		got := or.IsLessOrEqual(other)

		assert.True(t, got)
	})

	t.Run("EqualState", func(t *testing.T) {
		t.Parallel()
		or := NewORSet("a")
		d := or.Add("apple", VV{})
		or.Merge(&d)
		other := NewORSet("a")
		other.Merge(or)

		got := or.IsLessOrEqual(other)

		assert.True(t, got)
	})

	t.Run("IncomparableORHasExtraElement", func(t *testing.T) {
		t.Parallel()
		// or tracks banana but other does not: other has implicit empty DVVSet for banana, so
		// or's non-empty DVVSet for banana is not ⊑ empty.
		or := NewORSet("a")
		d := or.Add("apple", VV{})
		or.Merge(&d)
		d = or.Add("banana", VV{})
		or.Merge(&d)
		other := NewORSet("b")
		d = other.Add("apple", VV{})
		other.Merge(&d)

		got := or.IsLessOrEqual(other)

		assert.False(t, got)
	})

	t.Run("StrictlyGreater", func(t *testing.T) {
		t.Parallel()
		// or has two adds for apple from "a"; other only has one add for apple from "b".
		or := NewORSet("a")
		d := or.Add("apple", VV{})
		or.Merge(&d)
		d = or.Add("apple", or.CausalContext("apple"))
		or.Merge(&d)
		other := NewORSet("b")
		d = other.Add("apple", VV{})
		other.Merge(&d)

		got := or.IsLessOrEqual(other)

		assert.False(t, got)
	})
}

func TestORSetValues(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		s := NewORSet("a")

		assert.Nil(t, s.Values())
	})

	t.Run("SingleElement", func(t *testing.T) {
		s := NewORSet("a")

		d := s.Add("apple", VV{})
		s.Merge(&d)

		assert.EqualValues(t, s.Values(), []string{"apple"})
	})

	t.Run("MultipleElements", func(t *testing.T) {
		s := NewORSet("a")

		d := s.Add("apple", VV{})
		s.Merge(&d)
		d = s.Add("banana", VV{})
		s.Merge(&d)
		d = s.Add("cherry", VV{})
		s.Merge(&d)

		got := s.Values()
		slices.Sort(got)
		assert.EqualValues(t, got, []string{"apple", "banana", "cherry"})
	})

	t.Run("DuplicateAddReturnsSingle", func(t *testing.T) {
		s := NewORSet("a")

		d := s.Add("apple", VV{})
		s.Merge(&d)
		d = s.Add("apple", VV{})
		s.Merge(&d)

		assert.EqualValues(t, s.Values(), []string{"apple"})
	})

	t.Run("AfterObservedRemove", func(t *testing.T) {
		s := NewORSet("a")

		d := s.Add("apple", VV{})
		s.Merge(&d)
		d = s.Add("banana", VV{})
		s.Merge(&d)
		d = s.Remove("apple", s.CausalContext("apple"))
		s.Merge(&d)

		assert.EqualValues(t, s.Values(), []string{"banana"})
	})

	t.Run("AfterObservedRemoveAll", func(t *testing.T) {
		s := NewORSet("a")

		d := s.Add("apple", VV{})
		s.Merge(&d)
		d = s.Remove("apple", s.CausalContext("apple"))
		s.Merge(&d)

		assert.Nil(t, s.Values())
	})

	t.Run("ReAddAfterObservedRemove", func(t *testing.T) {
		s := NewORSet("a")

		d := s.Add("apple", VV{})
		s.Merge(&d)
		d = s.Remove("apple", s.CausalContext("apple"))
		s.Merge(&d)
		d = s.Add("apple", s.CausalContext("apple"))
		s.Merge(&d)

		assert.EqualValues(t, s.Values(), []string{"apple"})
	})

	t.Run("ConcurrentAddAndRemoveLeavesElement", func(t *testing.T) {
		// Element has a live add sibling concurrent with a remove sibling; Values includes it.
		a := NewORSet("a")
		d := a.Add("apple", VV{})
		a.Merge(&d)
		b := NewORSet("b")
		b.Merge(a)

		d = a.Add("apple", VV{}) // concurrent re-add on a, empty context
		a.Merge(&d)
		d = b.Remove("apple", b.CausalContext("apple")) // remove on b, only observes original add
		b.Merge(&d)

		a.Merge(b)

		assert.EqualValues(t, a.Values(), []string{"apple"})
	})
}

func TestORSetMarshalRoundtrip(t *testing.T) {
	a := NewORSet("a")
	d := a.Add("apple", VV{})
	a.Merge(&d)
	d = a.Add("banana", VV{})
	a.Merge(&d)
	d = a.Add("cherry", VV{})
	a.Merge(&d)
	d = a.Remove("apple", a.CausalContext("apple"))
	a.Merge(&d)

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
	d = b.Add("date", VV{})
	b.Merge(&d)
	assert.EqualValues(t, b.CausalContext("date"), VV{"b": 1})
}

func TestORSetUnmarshalNullState(t *testing.T) {
	// A wire payload with "state": null must unmarshal into a usable ORSet: subsequent writes
	// should not panic with "assignment to entry in nil map".
	s := NewORSet("a")
	err := json.Unmarshal([]byte(`{"state":null}`), s)
	assert.NoError(t, err)

	d := s.Add("apple", VV{})
	s.Merge(&d)

	assert.True(t, s.Contains("apple"))
}
