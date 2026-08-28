//go:build linux

package runtimecfg

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/jail"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeStore hands back one document, or an error.
type fakeStore struct {
	doc map[string]any
	err error
}

func (f fakeStore) Settings(context.Context) (map[string]any, error) { return f.doc, f.err }

func loadWith(t *testing.T, doc map[string]any) Values {
	t.Helper()
	return Load(t.Context(), fakeStore{doc: doc}, Defaults(), quiet())
}

// A key nobody has saved runs as the compiled-in default, which is the floor.
func TestZeroFieldsFallBackToDefaults(t *testing.T) {
	got := loadWith(t, map[string]any{})
	want := Defaults()

	if got.Listen != want.Listen || got.Hardening != want.Hardening {
		t.Errorf("the listener or the policy did not default: %q, %v", got.Listen, got.Hardening)
	}
	if got.RatePerSec != want.RatePerSec || got.RateBurst != want.RateBurst {
		t.Errorf("the rate bounds did not default: %v, %d", got.RatePerSec, got.RateBurst)
	}
	if got.SMB.Workgroup == "" || got.SMB.ServiceUser == "" {
		t.Errorf("the SMB defaults are empty: %+v", got.SMB)
	}
	if got.SMBConfigured {
		t.Error("an empty document reported SMB as configured")
	}
}

// A store that cannot be read is the defaults with a line, never a refusal.
func TestAnUnreadableStoreLoadsTheDefaults(t *testing.T) {
	got := Load(t.Context(), fakeStore{err: errors.New("the database is gone")}, Defaults(), quiet())
	if got.Listen != Defaults().Listen {
		t.Errorf("an unreadable store did not fall back: %+v", got)
	}
}

// The boot-time rule: a value outside its bound is clamped with a warning,
// never refused. Refusing here would make a server unbootable over something
// saved weeks ago, and the emergency door that would fix it edits this same
// document.
func TestAnOutOfBoundStoredValueIsClamped(t *testing.T) {
	got := loadWith(t, map[string]any{
		"search": map[string]any{"max_concurrent_fast": float64(1 << 20)},
		"rate":   map[string]any{"per_sec": float64(-5)},
	})

	if want := BoundSearchConcurrent().Max; got.SearchConcurrentSSD != int(want) {
		t.Errorf("a huge concurrency loaded as %d, want the ceiling %d", got.SearchConcurrentSSD, want)
	}
	if want := BoundRatePerSec().Min; got.RatePerSec != float64(want) {
		t.Errorf("a negative rate loaded as %v, want the floor %d", got.RatePerSec, want)
	}
}

// Garbage in the document loads as the default rather than refusing the start.
func TestMalformedStoredValuesLoadAsDefaults(t *testing.T) {
	got := loadWith(t, map[string]any{
		"search":   map[string]any{"max_concurrent_fast": "not a number"},
		"network":  map[string]any{"bind": "no-port-here", "app_hosts": "not a list"},
		"security": map[string]any{"hardening": "paranoid"},
		"homes":    map[string]any{"enabled": "yes please"},
	})
	want := Defaults()

	if got.SearchConcurrentSSD != want.SearchConcurrentSSD {
		t.Errorf("a string concurrency became %d", got.SearchConcurrentSSD)
	}
	if got.Listen != want.Listen {
		t.Errorf("an unbindable address became %q", got.Listen)
	}
	if got.Hardening != want.Hardening {
		t.Errorf("an unknown policy became %v", got.Hardening)
	}
	if got.HomesEnabled {
		t.Error("a non-boolean turned homes on")
	}
	if len(got.AppHosts) != 0 {
		t.Errorf("a non-list host setting became %v", got.AppHosts)
	}
}

// A well-formed document round-trips.
func TestAWellFormedDocumentLoads(t *testing.T) {
	got := loadWith(t, map[string]any{
		"search":  map[string]any{"max_concurrent_fast": float64(8), "walk_deadline_fast_ms": float64(2000)},
		"watch":   map[string]any{"hot_set_max": float64(1024)},
		"rate":    map[string]any{"per_sec": float64(50), "burst": float64(200)},
		"network": map[string]any{"bind": "127.0.0.1:9443", "app_hosts": []any{"files.example.test"}},
		"homes":   map[string]any{"enabled": true, "root": "/srv/homes"},
	})

	if got.SearchConcurrentSSD != 8 {
		t.Errorf("concurrency is %d", got.SearchConcurrentSSD)
	}
	if got.SearchDeadlineSSD != 2*time.Second {
		t.Errorf("the deadline is %v", got.SearchDeadlineSSD)
	}
	if got.WatchHotSetMax != 1024 || got.RatePerSec != 50 || got.RateBurst != 200 {
		t.Errorf("the numbers did not load: %+v", got)
	}
	if got.Listen != "127.0.0.1:9443" {
		t.Errorf("the listener is %q", got.Listen)
	}
	if !got.HomesEnabled || got.HomesRoot != "/srv/homes" {
		t.Errorf("homes did not load: %v, %q", got.HomesEnabled, got.HomesRoot)
	}
}

// Content hosts round-trip, and a host claimed by both roles is dropped from
// the content list at boot rather than refusing the start.
func TestContentHostsRoundTripAndOverlapDropsAtBoot(t *testing.T) {
	got := loadWith(t, map[string]any{
		"network": map[string]any{
			"app_hosts":     []any{"app.example.test"},
			"content_hosts": []any{"content.example.test"},
		},
	})
	if len(got.ContentHosts) != 1 || got.ContentHosts[0] != "content.example.test" {
		t.Errorf("the content hosts did not round-trip: %v", got.ContentHosts)
	}

	overlapped := loadWith(t, map[string]any{
		"network": map[string]any{
			"app_hosts":     []any{"both.example.test", "app.example.test"},
			"content_hosts": []any{"BOTH.example.test", "content.example.test"},
		},
	})
	if len(overlapped.AppHosts) != 2 {
		t.Errorf("the app list lost an entry: %v", overlapped.AppHosts)
	}
	// The app role carries the session, so it keeps the name.
	if len(overlapped.ContentHosts) != 1 || overlapped.ContentHosts[0] != "content.example.test" {
		t.Errorf("the overlapping content host was not dropped: %v", overlapped.ContentHosts)
	}
}

// A malformed stored host is dropped without refusing the start.
func TestAMalformedStoredHostIsDropped(t *testing.T) {
	got := loadWith(t, map[string]any{
		"network": map[string]any{
			"app_hosts": []any{"good.example.test", "https://bad.example.test/path", "also:8443"},
		},
	})
	if len(got.AppHosts) != 1 || got.AppHosts[0] != "good.example.test" {
		t.Errorf("the malformed entries were not dropped: %v", got.AppHosts)
	}
}

// The canonical URL has to name a declared app host, and one that does not is
// left unset rather than refusing the start.
func TestTheCanonicalURLMustNameAnAppHost(t *testing.T) {
	good := loadWith(t, map[string]any{
		"network": map[string]any{
			"app_hosts":            []any{"app.example.test"},
			"compat_canonical_url": "https://app.example.test",
		},
	})
	if good.CompatCanonicalURL != "https://app.example.test" {
		t.Errorf("a valid canonical URL did not load: %q", good.CompatCanonicalURL)
	}

	bad := loadWith(t, map[string]any{
		"network": map[string]any{
			"app_hosts":            []any{"app.example.test"},
			"compat_canonical_url": "https://elsewhere.example.test",
		},
	})
	if bad.CompatCanonicalURL != "" {
		t.Errorf("a canonical URL outside the app hosts loaded as %q", bad.CompatCanonicalURL)
	}
}

// The guard's switch is what applies the bounds, so the numbers can be set
// before it is turned on.
func TestTheSizeGuardNeedsItsSwitchAndABound(t *testing.T) {
	off := loadWith(t, map[string]any{
		"db": map[string]any{"max_bytes": float64(1 << 30)},
	})
	if off.DBGuard.Enabled() {
		t.Errorf("the guard applied without its switch: %+v", off.DBGuard)
	}

	on := loadWith(t, map[string]any{
		"db": map[string]any{"size_guard": true, "max_bytes": float64(1 << 30)},
	})
	if !on.DBGuard.Enabled() || on.DBGuard.MaxBytes != 1<<30 {
		t.Errorf("the guard did not load: %+v", on.DBGuard)
	}

	// On with neither bound is a control that protects nothing, and it stays
	// off rather than pretending.
	empty := loadWith(t, map[string]any{"db": map[string]any{"size_guard": true}})
	if empty.DBGuard.Enabled() {
		t.Errorf("a guard with no bound reported itself enabled: %+v", empty.DBGuard)
	}
}

// Single sign-on that is on and incomplete is off with a line, not a refused
// start: a server that will not boot is a deployment where nobody signs in.
func TestIncompleteSingleSignOnStaysOff(t *testing.T) {
	got := loadWith(t, map[string]any{
		"oidc": map[string]any{"enabled": true, "issuer": "https://idp.example.test"},
	})
	if got.OIDC != nil {
		t.Errorf("an incomplete provider loaded: %+v", got.OIDC)
	}

	full := loadWith(t, map[string]any{
		"oidc": map[string]any{
			"enabled": true, "issuer": "https://idp.example.test", "client_id": "stowcloud",
			"display_name": "Company sign-in",
		},
	})
	if full.OIDC == nil {
		t.Fatal("a complete provider did not load")
	}
	if full.OIDC.Issuer != "https://idp.example.test" || full.OIDC.ClientID != "stowcloud" {
		t.Errorf("the provider loaded wrong: %+v", full.OIDC)
	}
	if full.OIDCDisplayName != "Company sign-in" {
		t.Errorf("the display name is %q", full.OIDCDisplayName)
	}
}

// SMB settings that cannot be rendered leave SMB off with a line, which is a
// deployment with no shares over SMB rather than one that will not start.
func TestUnrenderableSMBSettingsLeaveItOff(t *testing.T) {
	got := loadWith(t, map[string]any{
		"smb": map[string]any{"enabled": true, "workgroup": "BAD\nGROUP"},
	})
	if !got.SMBConfigured {
		t.Error("a saved smb section did not mark the deployment configured")
	}
	if got.SMB.Enabled {
		t.Error("an unrenderable configuration stayed enabled")
	}

	ok := loadWith(t, map[string]any{
		"smb": map[string]any{"enabled": true, "workgroup": "OFFICE", "service_gid": float64(2000)},
	})
	if !ok.SMB.Enabled || ok.SMB.Workgroup != "OFFICE" || ok.SMBServiceGID != 2000 {
		t.Errorf("a renderable configuration did not load: %+v, gid %d", ok.SMB, ok.SMBServiceGID)
	}
}

// Every numeric field carries a bound, so the screen can validate it and the
// checker can refuse it. A field without one is a field neither can.
func TestTheBoundsTableCoversEveryNumericField(t *testing.T) {
	bounds := Bounds()
	if len(bounds) == 0 {
		t.Fatal("the bounds table is empty")
	}
	for field, b := range bounds {
		if b.Min > b.Max {
			t.Errorf("the bound for %q is inverted: %+v", field, b)
		}
		if b.Min < 0 {
			t.Errorf("the bound for %q admits a negative: %+v", field, b)
		}
	}

	// The numeric fields the loader actually reads, named here so a field added
	// to one and not the other is a failure rather than a silent gap.
	for _, field := range []string{
		FieldSearchConcurrentFast, FieldSearchConcurrentSlow,
		FieldSearchDeadlineFast, FieldSearchDeadlineSlow,
		FieldArchiveMaxConcurrent, FieldWatchHotSet, FieldWatchFullThreshold,
		FieldRatePerSec, FieldRateBurst, FieldSMBServiceGID,
	} {
		if _, ok := bounds[field]; !ok {
			t.Errorf("the numeric field %q has no bound", field)
		}
	}

	// And the defaults sit inside their own bounds, or a fresh server would
	// start outside what it refuses to save.
	d := Defaults()
	for field, value := range map[string]int64{
		FieldSearchConcurrentFast: int64(d.SearchConcurrentSSD),
		FieldSearchConcurrentSlow: int64(d.SearchConcurrentRot),
		FieldSearchDeadlineFast:   d.SearchDeadlineSSD.Milliseconds(),
		FieldSearchDeadlineSlow:   d.SearchDeadlineRot.Milliseconds(),
		FieldArchiveMaxConcurrent: int64(d.ArchiveMaxConcurrent),
		FieldWatchHotSet:          int64(d.WatchHotSetMax),
		FieldRatePerSec:           int64(d.RatePerSec),
		FieldRateBurst:            int64(d.RateBurst),
		FieldSMBServiceGID:        int64(d.SMBServiceGID),
	} {
		if b := bounds[field]; !b.Contains(value) {
			t.Errorf("the default for %q is %d, outside its own bound %+v", field, value, b)
		}
	}
}

func TestClampAndContains(t *testing.T) {
	b := Bound{Min: 10, Max: 20}
	for _, c := range []struct{ in, want int64 }{{5, 10}, {10, 10}, {15, 15}, {20, 20}, {25, 20}} {
		if got := b.Clamp(c.in); got != c.want {
			t.Errorf("Clamp(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	if b.Contains(9) || !b.Contains(10) || !b.Contains(20) || b.Contains(21) {
		t.Error("Contains does not match the bound")
	}
}

// The holder is read by every subsystem, so concurrent readers under a writer
// have to be safe. Under -race this is what proves it.
func TestTheHolderIsSafeUnderConcurrentReaders(t *testing.T) {
	h := New(Defaults())

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		task.Go(t.Context(), "settings: holder reader", func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if v := h.Get(); v.Listen == "" {
					t.Error("a reader saw an empty value")
					return
				}
			}
		})
	}

	for i := range 200 {
		v := Defaults()
		v.RateBurst = 100 + i
		h.Set(v)
	}
	close(stop)
	wg.Wait()
}

// The update callback runs outside the lock. A callback that reads the holder
// is the obvious thing to write, and under the lock it would deadlock.
func TestTheApplyCallbackRunsOutsideTheLock(t *testing.T) {
	h := New(Defaults())

	done := make(chan Values, 1)
	h.OnApply(func(v Values) {
		// Reading the holder from inside the callback is the deadlock fixture.
		done <- h.Get()
	})

	want := Defaults()
	want.RateBurst = 4242
	h.Set(want)

	select {
	case got := <-done:
		if got.RateBurst != want.RateBurst {
			t.Errorf("the callback read %d, want the value that was just set", got.RateBurst)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the callback deadlocked reading the holder")
	}
}

// A holder with no callback installed still sets.
func TestSetWithoutACallback(t *testing.T) {
	h := New(Defaults())
	v := Defaults()
	v.RateBurst = 7
	h.Set(v)
	if h.Get().RateBurst != 7 {
		t.Error("the value did not take")
	}
}

// The values struct has no field the loader silently ignores. Reflection
// rather than a list, so a field added later is caught here.
func TestEveryValuesFieldIsReachable(t *testing.T) {
	// The fields that are deliberately not loaded from the document: they are
	// derived or set by the wiring rather than stored.
	derived := map[string]bool{
		"SMBConfigured":   true,
		"OIDCDisplayName": true,
	}

	rt := reflect.TypeOf(Values{})
	loaded := loadWith(t, fullDocument())
	empty := Values{}

	for i := range rt.NumField() {
		name := rt.Field(i).Name
		if derived[name] {
			continue
		}
		got := reflect.ValueOf(loaded).Field(i)
		zero := reflect.ValueOf(empty).Field(i)
		if got.IsZero() && zero.IsZero() {
			t.Errorf("the field %q is still zero after loading a full document, so nothing reads it", name)
		}
	}
}

// fullDocument names every stored setting, so the reflection test above can see
// which fields never move.
func fullDocument() map[string]any {
	return map[string]any{
		"search": map[string]any{
			"max_concurrent_fast": float64(8), "max_concurrent_slow": float64(4),
			"walk_deadline_fast_ms": float64(2000), "walk_deadline_slow_ms": float64(4000),
		},
		"archive": map[string]any{"max_concurrent": float64(500)},
		"watch":   map[string]any{"hot_set_max": float64(1024), "full_threshold": float64(9000)},
		"rate":    map[string]any{"per_sec": float64(50), "burst": float64(200)},
		"network": map[string]any{
			"bind":      "127.0.0.1:9443",
			"app_hosts": []any{"app.example.test"}, "content_hosts": []any{"content.example.test"},
			"trusted_proxies":      []any{"10.0.0.0/8"},
			"allowed_origins":      []any{"https://other.example.test"},
			"compat_canonical_url": "https://app.example.test",
		},
		"homes":    map[string]any{"enabled": true, "root": "/srv/homes"},
		"security": map[string]any{"hardening": "preferred"},
		"db":       map[string]any{"size_guard": true, "max_bytes": float64(1 << 30)},
		"oidc": map[string]any{
			"enabled": true, "issuer": "https://idp.example.test", "client_id": "stowcloud",
		},
		"smb": map[string]any{
			"enabled": true, "workgroup": "OFFICE", "server_name": "storage",
			"service_user": "svc", "service_gid": float64(2000),
			"totp_policy": "block", "config_dir": "/config/smb",
			"agent_socket": "/run/smb.sock", "interfaces": []any{"192.168.1.10"},
		},
	}
}

// The hardening policy round-trips through the document.
func TestTheHardeningPolicyLoads(t *testing.T) {
	got := loadWith(t, map[string]any{"security": map[string]any{"hardening": "preferred"}})
	if got.Hardening != jail.Preferred {
		t.Errorf("the policy loaded as %v", got.Hardening)
	}
}
