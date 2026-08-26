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

// The boot-time sections: what a save has to survive a restart as.
func TestTheBootTimeSectionsSurvive(t *testing.T) {
	st := &memStore{doc: map[string]any{
		"watch": map[string]any{
			"hot_set_max":    float64(8192),
			"full_threshold": float64(99999),
		},
		"homes": map[string]any{"enabled": true, "root": "/srv/homes"},
		"smb": map[string]any{
			"enabled": true, "workgroup": "TESTGRP", "server_name": "testsrv",
			"service_user": "scsvc", "totp_policy": "block", "service_gid": float64(1500),
		},
		"network": map[string]any{
			"app_hosts":       []any{"one.test", "two.test"},
			"trusted_proxies": []any{"10.0.0.0/8"},
		},
	}}

	got := Load(context.Background(), st, base(), quiet())
	if got.WatchHotSetMax != 8192 || got.WatchFullThreshold != 99999 {
		t.Errorf("watch = %d/%d", got.WatchHotSetMax, got.WatchFullThreshold)
	}
	if !got.HomesEnabled || got.HomesRoot != "/srv/homes" {
		t.Errorf("homes = %v %q", got.HomesEnabled, got.HomesRoot)
	}
	if !got.SMBConfigured || !got.SMB.Enabled || got.SMB.Workgroup != "TESTGRP" ||
		got.SMB.TOTPPolicy != "block" || got.SMB.ServiceGID != 1500 {
		t.Errorf("smb = %+v", got.SMB)
	}
	if len(got.AppHosts) != 2 || got.AppHosts[0] != "one.test" {
		t.Errorf("app hosts = %v", got.AppHosts)
	}
	if len(got.TrustedProxy) != 1 {
		t.Errorf("trusted proxies = %v", got.TrustedProxy)
	}
}

// An empty stored host list is never applied.
//
// A host guard with no hosts admits nothing, so loading one would leave a
// server that refuses every request including the one that would fix it. The
// save path refuses an empty list; this is the second half, for a document
// that already holds one.
func TestAnEmptyStoredHostListIsIgnored(t *testing.T) {
	st := &memStore{doc: map[string]any{
		"network": map[string]any{"app_hosts": []any{}},
	}}
	b := base()
	b.AppHosts = []string{"configured.test"}

	got := Load(context.Background(), st, b, quiet())
	if len(got.AppHosts) != 1 || got.AppHosts[0] != "configured.test" {
		t.Fatalf("an empty stored list replaced the configured hosts: %v", got.AppHosts)
	}
}

// A section nobody has saved leaves SMB alone, so the config file's own
// settings are what publish rather than a zero value.
func TestAnUnsavedSMBSectionIsNotApplied(t *testing.T) {
	got := Load(context.Background(), &memStore{}, base(), quiet())
	if got.SMBConfigured {
		t.Fatal("an absent smb section reported as configured")
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
}

// Every default this package hands out has to be a value it would also accept.
//
// It was not. WatchFullThreshold and the SMB service group were left at the Go
// zero value, and both bounds start above zero, so the settings screen showed
// two numbers it would have refused on save.
func TestEveryDefaultIsInsideItsOwnBound(t *testing.T) {
	// A representative base, built the way serve.go builds one.
	v := Values{
		SearchConcurrentSSD:  4,
		SearchConcurrentRot:  2,
		SearchDeadlineSSD:    3000 * time.Millisecond,
		SearchDeadlineRot:    8000 * time.Millisecond,
		ArchiveMaxConcurrent: 10_000,
		WatchHotSetMax:       4096,
		WatchFullThreshold:   50_000,
		RatePerSec:           20,
		RateBurst:            100,
		SMB:                  SMB{ServiceGID: 1000},
	}

	for _, c := range []struct {
		field string
		value int64
		bound Bound
	}{
		{"search.max_concurrent_fast", int64(v.SearchConcurrentSSD), BoundSearchConcurrent()},
		{"search.max_concurrent_slow", int64(v.SearchConcurrentRot), BoundSearchConcurrent()},
		{"search.walk_deadline_fast_ms", v.SearchDeadlineSSD.Milliseconds(), BoundSearchDeadlineMs()},
		{"search.walk_deadline_slow_ms", v.SearchDeadlineRot.Milliseconds(), BoundSearchDeadlineMs()},
		{"archive.max_concurrent", int64(v.ArchiveMaxConcurrent), BoundArchiveEntries()},
		{"watch.hot_set_max", int64(v.WatchHotSetMax), BoundWatchHotSet()},
		{"watch.full_threshold", int64(v.WatchFullThreshold), BoundWatchFullThreshold()},
		{"rate.per_sec", int64(v.RatePerSec), BoundRatePerSec()},
		{"rate.burst", int64(v.RateBurst), BoundRateBurst()},
		{"smb.service_gid", int64(v.SMB.ServiceGID), BoundServiceGID()},
	} {
		if err := Check(c.field, c.value, c.bound); err != nil {
			t.Errorf("the default for %s is a value this package would refuse: %v", c.field, err)
		}
	}
}

// A zero left where a bound starts above zero is the shape of the defect, so
// the check above has to actually catch it.
func TestAZeroDefaultIsCaught(t *testing.T) {
	if err := Check("watch.full_threshold", 0, BoundWatchFullThreshold()); err == nil {
		t.Fatal("zero passed a bound whose floor is 64")
	}
	if err := Check("smb.service_gid", 0, BoundServiceGID()); err == nil {
		t.Fatal("zero passed the service group bound, which excludes root's group")
	}
}
