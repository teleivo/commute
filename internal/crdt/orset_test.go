package crdt

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/teleivo/assertive/assert"
)

func TestORSetContains(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		s := NewORSet()

		assert.False(t, s.Contains("apple"))
	})

	t.Run("AfterAdd", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")

		assert.True(t, s.Contains("apple"))
	})

	t.Run("NotAdded", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")

		assert.False(t, s.Contains("banana"))
	})

	t.Run("AfterAddAndRemove", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")
		s.Remove("apple")

		assert.False(t, s.Contains("apple"))
	})

	t.Run("RemoveNonExistent", func(t *testing.T) {
		s := NewORSet()

		s.Remove("apple")

		assert.False(t, s.Contains("apple"))
	})

	t.Run("ReAddAfterRemove", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")
		s.Remove("apple")
		s.Add("apple")

		assert.True(t, s.Contains("apple"))
	})

	t.Run("MultipleElements", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")
		s.Add("banana")
		s.Add("cherry")

		assert.True(t, s.Contains("apple"))
		assert.True(t, s.Contains("banana"))
		assert.True(t, s.Contains("cherry"))
		assert.False(t, s.Contains("durian"))
	})

	t.Run("RemoveOnlyAffectsTarget", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")
		s.Add("banana")
		s.Remove("apple")

		assert.False(t, s.Contains("apple"))
		assert.True(t, s.Contains("banana"))
	})

	t.Run("DuplicateAdd", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")
		s.Add("apple")

		assert.True(t, s.Contains("apple"))
	})
}

func TestORSetMerge(t *testing.T) {
	t.Run("BothEmpty", func(t *testing.T) {
		t.Parallel()
		a := NewORSet()
		b := NewORSet()

		a.Merge(b)

		assert.False(t, a.Contains("apple"))
	})

	t.Run("MergeIntoEmpty", func(t *testing.T) {
		t.Parallel()
		a := NewORSet()
		b := NewORSet()
		b.Add("apple")

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("MergeFromEmpty", func(t *testing.T) {
		t.Parallel()
		a := NewORSet()
		a.Add("apple")
		b := NewORSet()

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("DisjointElements", func(t *testing.T) {
		t.Parallel()
		a := NewORSet()
		a.Add("apple")
		b := NewORSet()
		b.Add("banana")

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
		assert.True(t, a.Contains("banana"))
	})

	t.Run("SameElement", func(t *testing.T) {
		t.Parallel()
		a := NewORSet()
		a.Add("apple")
		b := NewORSet()
		b.Add("apple")

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("MergeIsIdempotent", func(t *testing.T) {
		t.Parallel()
		a := NewORSet()
		a.Add("apple")
		b := NewORSet()
		b.Add("banana")

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
		assert.True(t, a.Contains("banana"))

		a.Merge(b)

		assert.True(t, a.Contains("apple"))
		assert.True(t, a.Contains("banana"))
	})

	t.Run("MergeIsCommutative", func(t *testing.T) {
		t.Parallel()
		a1 := NewORSet()
		a1.Add("apple")
		b1 := NewORSet()
		b1.Add("banana")
		a2 := NewORSet()
		a2.Add("apple")
		b2 := NewORSet()
		b2.Add("banana")

		a1.Merge(b1)
		b2.Merge(a2)

		assert.EqualValues(t, a1.Contains("apple"), b2.Contains("apple"))
		assert.EqualValues(t, a1.Contains("banana"), b2.Contains("banana"))
	})

	t.Run("MergeSelf", func(t *testing.T) {
		t.Parallel()
		a := NewORSet()
		a.Add("apple")

		a.Merge(a)

		assert.True(t, a.Contains("apple"))
	})

	t.Run("RemovePropagates", func(t *testing.T) {
		t.Parallel()
		a := NewORSet()
		a.Add("apple")
		a.Add("banana")
		b := NewORSet()
		b.Merge(a)

		b.Remove("apple")
		a.Merge(b)

		assert.False(t, a.Contains("apple"))
		assert.True(t, a.Contains("banana"))
	})

	t.Run("ConcurrentAddWinsOverRemove", func(t *testing.T) {
		t.Parallel()
		// Node a adds apple.
		a := NewORSet()
		a.Add("apple")
		// Node b receives apple via merge.
		b := NewORSet()
		b.Merge(a)

		// Node b removes apple (observing the dot from a).
		b.Remove("apple")
		// Concurrently, node a re-adds apple (fresh unique tag).
		a.Add("apple")

		// After merging, the concurrent add should win.
		a.Merge(b)
		assert.True(t, a.Contains("apple"))
		// Commutative: same result from b's perspective.
		b.Merge(a)
		assert.True(t, b.Contains("apple"))
	})

	t.Run("ConcurrentRemovesBothApply", func(t *testing.T) {
		t.Parallel()
		// Node a adds apple.
		a := NewORSet()
		a.Add("apple")
		// Both b and c receive apple via merge.
		b := NewORSet()
		b.Merge(a)
		c := NewORSet()
		c.Merge(a)

		// Both b and c remove apple concurrently.
		b.Remove("apple")
		c.Remove("apple")

		// Merge everything together.
		a.Merge(b)
		a.Merge(c)
		assert.False(t, a.Contains("apple"))
	})
}

func TestORSetValues(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		s := NewORSet()

		assert.EqualValues(t, s.Values(), []string(nil))
	})

	t.Run("SingleElement", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")

		assert.EqualValues(t, s.Values(), []string{"apple"})
	})

	t.Run("MultipleElements", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")
		s.Add("banana")
		s.Add("cherry")

		got := s.Values()
		slices.Sort(got)
		assert.EqualValues(t, got, []string{"apple", "banana", "cherry"})
	})

	t.Run("DuplicateAddReturnsSingle", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")
		s.Add("apple")

		assert.EqualValues(t, s.Values(), []string{"apple"})
	})

	t.Run("AfterRemove", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")
		s.Add("banana")
		s.Remove("apple")

		assert.EqualValues(t, s.Values(), []string{"banana"})
	})

	t.Run("AfterRemoveAll", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")
		s.Remove("apple")

		assert.EqualValues(t, s.Values(), []string(nil))
	})

	t.Run("ReAddAfterRemove", func(t *testing.T) {
		s := NewORSet()

		s.Add("apple")
		s.Remove("apple")
		s.Add("apple")

		assert.EqualValues(t, s.Values(), []string{"apple"})
	})
}

func TestORSetMarshalRoundtrip(t *testing.T) {
	a := NewORSet()
	a.Add("apple")
	a.Add("banana")
	a.Add("cherry")
	a.Remove("apple")

	data, err := json.Marshal(a)
	assert.NoError(t, err)

	b := NewORSet()
	err = json.Unmarshal(data, b)
	assert.NoError(t, err)

	assert.False(t, b.Contains("apple"))
	assert.True(t, b.Contains("banana"))
	assert.True(t, b.Contains("cherry"))
	assert.False(t, b.Contains("durian"))
}
