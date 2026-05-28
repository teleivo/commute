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

		d := r.Set(json.RawMessage(`"hello"`))
		r.Merge(&d)

		assert.EqualValues(t, string(r.Value()), `"hello"`)
	})

	t.Run("OverwritesPrevious", func(t *testing.T) {
		ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		r := NewLWWRegister("a", func() time.Time {
			ts = ts.Add(1 * time.Second)
			return ts
		})

		d := r.Set(json.RawMessage(`"first"`))
		r.Merge(&d)
		d = r.Set(json.RawMessage(`"second"`))
		r.Merge(&d)

		assert.EqualValues(t, string(r.Value()), `"second"`)
	})
}

func TestLWWRegisterMerge(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("LaterTimestampWins", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base.Add(1*time.Second)))
		da := a.Set(json.RawMessage(`"old"`))
		a.Merge(&da)
		b := NewLWWRegister("b", fixedClock(base.Add(2*time.Second)))
		db := b.Set(json.RawMessage(`"new"`))
		b.Merge(&db)

		a.Merge(b)

		assert.EqualValues(t, string(a.Value()), `"new"`)
	})

	t.Run("EarlierTimestampLoses", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base.Add(2*time.Second)))
		da := a.Set(json.RawMessage(`"winner"`))
		a.Merge(&da)
		b := NewLWWRegister("b", fixedClock(base.Add(1*time.Second)))
		db := b.Set(json.RawMessage(`"loser"`))
		b.Merge(&db)

		a.Merge(b)

		assert.EqualValues(t, string(a.Value()), `"winner"`)
	})

	t.Run("TiebreakByNodeID", func(t *testing.T) {
		t.Parallel()
		clock := fixedClock(base)
		a := NewLWWRegister("a", clock)
		da := a.Set(json.RawMessage(`"from-a"`))
		a.Merge(&da)
		b := NewLWWRegister("b", clock)
		db := b.Set(json.RawMessage(`"from-b"`))
		b.Merge(&db)

		a.Merge(b)

		assert.EqualValues(t, string(a.Value()), `"from-b"`)
	})

	t.Run("TiebreakLowerNodeIDLoses", func(t *testing.T) {
		t.Parallel()
		clock := fixedClock(base)
		a := NewLWWRegister("z", clock)
		da := a.Set(json.RawMessage(`"from-z"`))
		a.Merge(&da)
		b := NewLWWRegister("a", clock)
		db := b.Set(json.RawMessage(`"from-a"`))
		b.Merge(&db)

		a.Merge(b)

		assert.EqualValues(t, string(a.Value()), `"from-z"`)
	})

	t.Run("MergeIntoEmpty", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base))
		b := NewLWWRegister("b", fixedClock(base))
		db := b.Set(json.RawMessage(`"hello"`))
		b.Merge(&db)

		a.Merge(b)

		assert.EqualValues(t, string(a.Value()), `"hello"`)
	})

	t.Run("MergeSelf", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base))
		da := a.Set(json.RawMessage(`"hello"`))
		a.Merge(&da)

		a.Merge(a)

		assert.EqualValues(t, string(a.Value()), `"hello"`)
	})

	t.Run("MergeIsCommutative", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base.Add(1*time.Second)))
		da := a.Set(json.RawMessage(`"from-a"`))
		a.Merge(&da)
		b := NewLWWRegister("b", fixedClock(base.Add(2*time.Second)))
		db := b.Set(json.RawMessage(`"from-b"`))
		b.Merge(&db)

		ab := NewLWWRegister("a", fixedClock(base.Add(1*time.Second)))
		dab := ab.Set(json.RawMessage(`"from-a"`))
		ab.Merge(&dab)
		ab.Merge(b)

		ba := NewLWWRegister("b", fixedClock(base.Add(2*time.Second)))
		dba := ba.Set(json.RawMessage(`"from-b"`))
		ba.Merge(&dba)
		ba.Merge(a)

		assert.EqualValues(t, string(ab.Value()), string(ba.Value()))
	})

	t.Run("SetAfterMergeRestoresLocalNodeID", func(t *testing.T) {
		t.Parallel()
		var tsA, tsB time.Time
		a := NewLWWRegister("a", func() time.Time { return tsA })
		b := NewLWWRegister("b", func() time.Time { return tsB })

		// Node a and b both set at t=1s, b wins tiebreak (b > a).
		tsA = base.Add(1 * time.Second)
		tsB = base.Add(1 * time.Second)
		da := a.Set(json.RawMessage(`"from-a"`))
		a.Merge(&da)
		db := b.Set(json.RawMessage(`"from-b"`))
		b.Merge(&db)

		// After merge, a's entry has writerID "b".
		a.Merge(b)
		assert.EqualValues(t, string(a.Value()), `"from-b"`)

		// Node a sets again at a later time. The writerID should be
		// "a", not the merged "b".
		tsA = base.Add(2 * time.Second)
		da = a.Set(json.RawMessage(`"from-a-again"`))
		a.Merge(&da)

		// a's value should propagate since it has a later timestamp.
		b.Merge(a)
		assert.EqualValues(t, string(b.Value()), `"from-a-again"`)

		// Tiebreak: both set at the same time. b > a lexicographically,
		// so b's value should win.
		tsA = base.Add(3 * time.Second)
		tsB = base.Add(3 * time.Second)
		da = a.Set(json.RawMessage(`"tie-a"`))
		a.Merge(&da)
		db = b.Set(json.RawMessage(`"tie-b"`))
		b.Merge(&db)

		a.Merge(b)
		assert.EqualValues(t, string(a.Value()), `"tie-b"`)
	})

	t.Run("MergeIsIdempotent", func(t *testing.T) {
		t.Parallel()
		a := NewLWWRegister("a", fixedClock(base.Add(1*time.Second)))
		da := a.Set(json.RawMessage(`"hello"`))
		a.Merge(&da)
		b := NewLWWRegister("b", fixedClock(base.Add(2*time.Second)))
		db := b.Set(json.RawMessage(`"world"`))
		b.Merge(&db)

		a.Merge(b)
		first := string(a.Value())

		a.Merge(b)
		second := string(a.Value())

		assert.EqualValues(t, first, second)
	})
}

func TestLWWRegisterIsLessOrEqual(t *testing.T) {
	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		lww   *LWWRegister
		other *LWWRegister
		want  bool
	}{
		"OlderTimestamp": {
			lww:   &LWWRegister{entry: lwwEntry{WriterID: "a", Timestamp: t1, Value: []byte(`"x"`)}},
			other: &LWWRegister{entry: lwwEntry{WriterID: "a", Timestamp: t2, Value: []byte(`"y"`)}},
			want:  true,
		},
		"SameTimestampLowerWriterID": {
			lww:   &LWWRegister{entry: lwwEntry{WriterID: "a", Timestamp: t1, Value: []byte(`"x"`)}},
			other: &LWWRegister{entry: lwwEntry{WriterID: "b", Timestamp: t1, Value: []byte(`"y"`)}},
			want:  true,
		},
		"EqualState": {
			lww:   &LWWRegister{entry: lwwEntry{WriterID: "a", Timestamp: t1, Value: []byte(`"x"`)}},
			other: &LWWRegister{entry: lwwEntry{WriterID: "a", Timestamp: t1, Value: []byte(`"x"`)}},
			want:  true,
		},
		"SameTimestampHigherWriterID": {
			lww:   &LWWRegister{entry: lwwEntry{WriterID: "b", Timestamp: t1, Value: []byte(`"y"`)}},
			other: &LWWRegister{entry: lwwEntry{WriterID: "a", Timestamp: t1, Value: []byte(`"x"`)}},
			want:  false,
		},
		"NewerTimestamp": {
			lww:   &LWWRegister{entry: lwwEntry{WriterID: "a", Timestamp: t2, Value: []byte(`"y"`)}},
			other: &LWWRegister{entry: lwwEntry{WriterID: "a", Timestamp: t1, Value: []byte(`"x"`)}},
			want:  false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := tc.lww.IsLessOrEqual(tc.other)

			assert.EqualValues(t, got, tc.want)
		})
	}
}

func fixedClock(t time.Time) Clock {
	return func() time.Time { return t }
}
