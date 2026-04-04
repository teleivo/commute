package crdt

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/teleivo/assertive/assert"
)

func TestLWWRegisterValue(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		r := NewLWWRegister("a", time.Now)
		assert.EqualValues(t, r.Value(), json.RawMessage(nil))
	})

	t.Run("AfterSet", func(t *testing.T) {
		r := NewLWWRegister("a", time.Now)
		r.Set(json.RawMessage(`"hello"`))
		assert.EqualValues(t, string(r.Value()), `"hello"`)
	})

	t.Run("OverwritesPrevious", func(t *testing.T) {
		ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		r := NewLWWRegister("a", func() time.Time {
			ts = ts.Add(1 * time.Second)
			return ts
		})
		r.Set(json.RawMessage(`"first"`))
		r.Set(json.RawMessage(`"second"`))
		assert.EqualValues(t, string(r.Value()), `"second"`)
	})
}

func TestLWWRegisterMerge(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("LaterTimestampWins", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base.Add(1*time.Second)))
		a.Set(json.RawMessage(`"old"`))

		b := NewLWWRegister("b", fixedClock(base.Add(2*time.Second)))
		b.Set(json.RawMessage(`"new"`))

		a.Merge(b)
		assert.EqualValues(t, string(a.Value()), `"new"`)
	})

	t.Run("EarlierTimestampLoses", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base.Add(2*time.Second)))
		a.Set(json.RawMessage(`"winner"`))

		b := NewLWWRegister("b", fixedClock(base.Add(1*time.Second)))
		b.Set(json.RawMessage(`"loser"`))

		a.Merge(b)
		assert.EqualValues(t, string(a.Value()), `"winner"`)
	})

	t.Run("TiebreakByNodeID", func(t *testing.T) {
		t.Parallel()
		clock := fixedClock(base)

		a := NewLWWRegister("a", clock)
		a.Set(json.RawMessage(`"from-a"`))

		b := NewLWWRegister("b", clock)
		b.Set(json.RawMessage(`"from-b"`))

		a.Merge(b)
		assert.EqualValues(t, string(a.Value()), `"from-b"`)
	})

	t.Run("TiebreakLowerNodeIDLoses", func(t *testing.T) {
		t.Parallel()
		clock := fixedClock(base)

		a := NewLWWRegister("z", clock)
		a.Set(json.RawMessage(`"from-z"`))

		b := NewLWWRegister("a", clock)
		b.Set(json.RawMessage(`"from-a"`))

		a.Merge(b)
		assert.EqualValues(t, string(a.Value()), `"from-z"`)
	})

	t.Run("MergeIntoEmpty", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base))

		b := NewLWWRegister("b", fixedClock(base))
		b.Set(json.RawMessage(`"hello"`))

		a.Merge(b)
		assert.EqualValues(t, string(a.Value()), `"hello"`)
	})

	t.Run("MergeSelf", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base))
		a.Set(json.RawMessage(`"hello"`))

		a.Merge(a)
		assert.EqualValues(t, string(a.Value()), `"hello"`)
	})

	t.Run("MergeIsCommutative", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base.Add(1*time.Second)))
		a.Set(json.RawMessage(`"from-a"`))

		b := NewLWWRegister("b", fixedClock(base.Add(2*time.Second)))
		b.Set(json.RawMessage(`"from-b"`))

		ab := NewLWWRegister("a", fixedClock(base.Add(1*time.Second)))
		ab.Set(json.RawMessage(`"from-a"`))
		ab.Merge(b)

		ba := NewLWWRegister("b", fixedClock(base.Add(2*time.Second)))
		ba.Set(json.RawMessage(`"from-b"`))
		ba.Merge(a)

		assert.EqualValues(t, string(ab.Value()), string(ba.Value()))
	})

	t.Run("MergeIsIdempotent", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base.Add(1*time.Second)))
		a.Set(json.RawMessage(`"hello"`))

		b := NewLWWRegister("b", fixedClock(base.Add(2*time.Second)))
		b.Set(json.RawMessage(`"world"`))

		a.Merge(b)
		first := string(a.Value())
		a.Merge(b)
		second := string(a.Value())

		assert.EqualValues(t, first, second)
	})
}

func fixedClock(t time.Time) Clock {
	return func() time.Time { return t }
}
