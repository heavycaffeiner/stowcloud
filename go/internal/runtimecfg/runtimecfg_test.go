package runtimecfg

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// The settings an administrator moves.
//
// What these hold is one property: a value saved from the interface is the
// value the server runs with, now and after a restart. Before this the patch
// was written to the settings table, nothing read it back, and the response
// said "applied" for a change that had taken effect nowhere.

// memStore is the settings table, in memory.
type memStore struct{ doc map[string]any }

func (m *memStore) Settings(context.Context) (map[string]any, error) {
	if m.doc == nil {
		m.doc = map[string]any{}
	}
	return m.doc, nil
}

func (m *memStore) MergeSettings(_ context.Context, section string, value any) error {
	if m.doc == nil {
		m.doc = map[string]any{}
	}
	m.doc[section] = value
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func base() Values {
	v := Defaults()
	v.RatePerSec, v.RateBurst = 20, 100
	return v
}

// The config file is the floor. A key nobody has overridden keeps the
// operator's own value rather than reverting to the compiled-in one.
func TestAnUnsetKeyKeepsTheConfigFilesValue(t *testing.T) {
	got := Load(context.Background(), &memStore{}, base(), quiet())
	if got.RatePerSec != 20 || got.RateBurst != 100 {
		t.Fatalf("the config file's rate was lost: %v/%v", got.RatePerSec, got.RateBurst)
	}
	if got.SearchConcurrentSSD != Defaults().SearchConcurrentSSD {
		t.Fatalf("an unset key did not take the default: %d", got.SearchConcurrentSSD)
	}
}

// A saved value is what the server runs with. This is the whole point.
func TestAStoredOverrideWins(t *testing.T) {
	st := &memStore{doc: map[string]any{
		"search": map[string]any{
			"max_concurrent_fast":   float64(9),
			"walk_deadline_fast_ms": float64(7000),
		},
		"rate": map[string]any{"per_sec": float64(55)},
	}}

	got := Load(context.Background(), st, base(), quiet())
	if got.SearchConcurrentSSD != 9 {
		t.Errorf("search concurrency = %d, want the saved 9", got.SearchConcurrentSSD)
	}
	if got.SearchDeadlineSSD != 7*time.Second {
		t.Errorf("the walk deadline = %v, want the saved 7s", got.SearchDeadlineSSD)
	}
	if got.RatePerSec != 55 {
		t.Errorf("the rate = %v, want the saved 55", got.RatePerSec)
	}
	// The half of the section nobody saved keeps the config file's value.
	if got.RateBurst != 100 {
		t.Errorf("the burst = %d, want the config file's 100", got.RateBurst)
	}
}

// A stored value outside its bound is clamped rather than refused. Refusing
// here makes a server unbootable over something saved weeks ago; taking it
// would defeat the bound.
func TestAStoredValueOutsideItsBoundIsClamped(t *testing.T) {
	st := &memStore{doc: map[string]any{
		"search": map[string]any{"max_concurrent_fast": float64(9999)},
	}}

	got := Load(context.Background(), st, base(), quiet())
	if got.SearchConcurrentSSD != int(BoundSearchConcurrent().Max) {
		t.Fatalf("a stored 9999 became %d, want the bound's max %d",
			got.SearchConcurrentSSD, BoundSearchConcurrent().Max)
	}
}

// A value of the wrong shape is ignored rather than guessed at, and it must
// not take the rest of the section down with it.
func TestAMalformedStoredValueIsIgnored(t *testing.T) {
	st := &memStore{doc: map[string]any{
		"search": map[string]any{
			"max_concurrent_fast": "not a number",
			"max_concurrent_slow": float64(3),
		},
	}}

	got := Load(context.Background(), st, base(), quiet())
	if got.SearchConcurrentSSD != Defaults().SearchConcurrentSSD {
		t.Errorf("a malformed value was taken: %d", got.SearchConcurrentSSD)
	}
	if got.SearchConcurrentRot != 3 {
		t.Errorf("a malformed sibling took down a good value: %d", got.SearchConcurrentRot)
	}
}

// A save is validated where somebody is watching, and refused naming the
// field rather than silently becoming a different number.
func TestCheckRefusesOutsideTheBoundAndAcceptsInside(t *testing.T) {
	b := BoundSearchConcurrent()
	if err := Check("max_concurrent_fast", b.Max+1, b); err == nil {
		t.Error("a value past the bound was accepted")
	}
	if err := Check("max_concurrent_fast", b.Min-1, b); err == nil {
		t.Error("a value below the bound was accepted")
	}
	if err := Check("max_concurrent_fast", b.Max, b); err != nil {
		t.Errorf("the bound's own maximum was refused: %v", err)
	}
}

// Setting values pushes them into the live components. A holder that stores
// and does not apply is the defect this package exists for, one layer up.
func TestSetAppliesToTheLiveComponents(t *testing.T) {
	h := New(base())
	var got Values
	var calls int
	h.OnApply(func(v Values) { got = v; calls++ })

	next := base()
	next.SearchConcurrentSSD = 11
	h.Set(next)

	if calls != 1 {
		t.Fatalf("apply ran %d times, want once", calls)
	}
	if got.SearchConcurrentSSD != 11 {
		t.Fatalf("the live components got %d", got.SearchConcurrentSSD)
	}
	if h.Get().SearchConcurrentSSD != 11 {
		t.Fatal("the holder did not keep the value it applied")
	}
	// The base is what the config file said, and a save does not move it:
	// it is what a revert returns to.
	if h.Base().SearchConcurrentSSD != base().SearchConcurrentSSD {
		t.Fatal("a save moved the config file's own value")
	}
}
