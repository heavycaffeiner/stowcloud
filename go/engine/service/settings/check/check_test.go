//go:build linux

package check

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// find returns the first finding for a field, so a test names what it is
// asserting rather than indexing into a slice.
func find(t *testing.T, findings []Finding, key string) (Finding, bool) {
	t.Helper()
	for _, f := range findings {
		if f.ReasonKey == key {
			return f, true
		}
	}
	return Finding{}, false
}

func mustFind(t *testing.T, findings []Finding, key string) Finding {
	t.Helper()
	f, ok := find(t, findings, key)
	if !ok {
		t.Fatalf("no finding with key %q in %v", key, keysOf(findings))
	}
	return f
}

func mustNotFind(t *testing.T, findings []Finding, key string) {
	t.Helper()
	if _, ok := find(t, findings, key); ok {
		t.Fatalf("unexpected finding %q in %v", key, keysOf(findings))
	}
}

func keysOf(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.ReasonKey)
	}
	return out
}

// The finding that earns this package: a host list without the host the
// administrator is on takes effect immediately, and the request that would undo
// it is the next one refused.
func TestALockingHostListIsRefusedOnTheSettingsScreen(t *testing.T) {
	got := Section(Input{
		Section:  "network",
		Body:     map[string]any{"app_hosts": []any{"elsewhere.example.test"}},
		SelfHost: "files.example.test",
		Lockout:  LockoutBlocks,
	})

	f := mustFind(t, got, keyWouldLockYouOut)
	if !f.Blocking {
		t.Error("the lockout did not refuse the save")
	}
	if host, _ := f.Arg("host"); host != "files.example.test" {
		t.Errorf("the finding named %q, want the host being locked out", host)
	}
	if !Blocked(got) || Refused(got) == nil {
		t.Error("the findings did not add up to a refusal")
	}
	if !errors.Is(Refused(got), ErrRefused) {
		t.Error("the refusal does not match ErrRefused")
	}
}

// The same list on the two screens the guard does not gate warns instead. The
// emergency editor is where somebody goes to repair a list that already locked
// them out, so refusing over the host they are on would refuse every repair.
func TestTheSameListOnlyWarnsWhereTheGuardDoesNotReach(t *testing.T) {
	got := Section(Input{
		Section:  "network",
		Body:     map[string]any{"app_hosts": []any{"elsewhere.example.test"}},
		SelfHost: "files.example.test",
		Lockout:  LockoutWarns,
	})

	f := mustFind(t, got, keyWouldLockYouOut)
	if f.Blocking {
		t.Error("the emergency door refused a repair over the host it was reached on")
	}
	if Blocked(got) {
		t.Error("the save was blocked on a screen the guard does not gate")
	}
	if Refused(got) != nil {
		t.Error("a warning produced a refusal")
	}
}

// A list that does contain the caller's host says nothing about lockout, and
// the match is case-insensitive exactly as the guard's is.
func TestAListThatKeepsTheCallerPasses(t *testing.T) {
	for _, self := range []string{"files.example.test", "FILES.example.test"} {
		got := Section(Input{
			Section:  "network",
			Body:     map[string]any{"app_hosts": []any{"files.example.test", "other.example.test"}},
			SelfHost: self,
			Lockout:  LockoutBlocks,
		})
		mustNotFind(t, got, keyWouldLockYouOut)
	}
}

// There is no wildcard form. A list the guard would reject must not pass the
// check and produce the very lockout the check exists to prevent.
func TestAWildcardDoesNotSatisfyTheLockoutCheck(t *testing.T) {
	got := Section(Input{
		Section:  "network",
		Body:     map[string]any{"app_hosts": []any{"*.example.test", "*"}},
		SelfHost: "files.example.test",
		Lockout:  LockoutBlocks,
	})
	if _, ok := find(t, got, keyWouldLockYouOut); !ok {
		t.Error("a wildcard satisfied the lockout check, which the guard would not honour")
	}
}

// An empty app host list serves nobody.
func TestAnEmptyAppHostListIsRefused(t *testing.T) {
	got := Section(Input{Section: "network", Body: map[string]any{"app_hosts": []any{}}})
	if f := mustFind(t, got, keyHostListEmpty); !f.Blocking {
		t.Error("an empty host list was not refused")
	}
}

// One TLS name cannot both carry the session application and be the
// cookie-free content origin.
func TestAHostInBothRolesIsRefused(t *testing.T) {
	got := Section(Input{
		Section: "network",
		Body: map[string]any{
			"app_hosts":     []any{"files.example.test"},
			"content_hosts": []any{"FILES.example.test"},
		},
		SelfHost: "files.example.test",
	})
	if f := mustFind(t, got, keyHostRoleConflict); !f.Blocking {
		t.Error("a host claimed by both roles was allowed")
	}
}

func TestMalformedNetworkValuesAreRefused(t *testing.T) {
	got := Section(Input{
		Section: "network",
		Body: map[string]any{
			"app_hosts":            []any{"https://bad.example.test/path"},
			"allowed_origins":      []any{"not an origin"},
			"bind":                 "no-port-here",
			"trusted_proxies":      []any{"10.0.0.0/999"},
			"compat_canonical_url": "https://not-an-app-host.example.test",
		},
	})
	for _, key := range []string{
		keyInvalidHost, keyInvalidOrigin, keyInvalidBindAddress,
		keyInvalidCIDR, keyCanonicalNotAppHost,
	} {
		if f := mustFind(t, got, key); !f.Blocking {
			t.Errorf("%q did not refuse the save", key)
		}
	}
}

// A wrong-typed list is a refusal rather than a silently ignored field.
func TestAWrongTypedListIsRefused(t *testing.T) {
	got := Section(Input{Section: "network", Body: map[string]any{"app_hosts": "files.example.test"}})
	if !Blocked(got) {
		t.Errorf("a string where a list belongs was accepted: %v", keysOf(got))
	}
}

// A range covering 0.0.0.0/0 is refused: treating all network peers as trusted
// proxies allows external callers to spoof client addresses and access private
// or emergency endpoints.
func TestAProxyRangeCoveringEverythingIsRefused(t *testing.T) {
	got := Section(Input{
		Section: "network",
		Body:    map[string]any{"trusted_proxies": []any{"0.0.0.0/0"}},
	})
	if f := mustFind(t, got, keyProxyIsEverything); !f.Blocking {
		t.Error("a wide proxy range must refuse the save")
	}
}

// One proxy is written as its address, which is the form the loader accepts
// and the header resolver reads back as a single-host prefix.
//
// This gate had a prefix-only rule of its own, so the spelling every other
// layer documents could not be saved: a deployment behind one proxy had no
// way to trust it, and the client address stayed the proxy's for the audit
// log and the rate limiter both.
func TestABareProxyAddressIsAccepted(t *testing.T) {
	for _, entry := range []string{"192.168.0.126", "10.88.0.1", "::1", "127.0.0.1"} {
		got := Section(Input{
			Section: "network",
			Body:    map[string]any{"trusted_proxies": []any{entry}},
		})
		for _, f := range got {
			if f.Field == "trusted_proxies" && f.Blocking {
				t.Errorf("the address %q was refused: %+v", entry, f)
			}
		}
	}

	// A spelling that is neither is still refused.
	got := Section(Input{
		Section: "network",
		Body:    map[string]any{"trusted_proxies": []any{"192.168.0.999"}},
	})
	if f := mustFind(t, got, keyInvalidCIDR); !f.Blocking {
		t.Error("an unparseable entry did not refuse the save")
	}
}

// The numeric bounds come from one table, so the screen, the checker and the
// loader cannot disagree about what is acceptable.
func TestNumbersOutsideTheirBoundAreRefusedWithTheRange(t *testing.T) {
	got := Section(Input{
		Section: "search",
		Body:    map[string]any{"max_concurrent_fast": float64(1 << 20)},
	})
	f := mustFind(t, got, keyOutOfRange)
	if !f.Blocking {
		t.Error("an out-of-range number was accepted")
	}
	// The message has to say what the range is, or the person reading it has
	// to guess a value and try again.
	if _, ok := f.Arg("min"); !ok {
		t.Error("the refusal did not carry the minimum")
	}
	if max, ok := f.Arg("max"); !ok || max == "" {
		t.Error("the refusal did not carry the maximum")
	}
	if field, _ := f.Arg("field"); field != "max_concurrent_fast" {
		t.Errorf("the refusal named %q", field)
	}
}

func TestNumbersInsideTheirBoundPass(t *testing.T) {
	got := Section(Input{Section: "search", Body: map[string]any{"max_concurrent_fast": float64(8)}})
	mustNotFind(t, got, keyOutOfRange)
}

// A field the bounds table does not govern is left alone here rather than
// refused, because the section checks own the non-numeric ones.
func TestAnUngovernedFieldIsNotBoundsChecked(t *testing.T) {
	got := Section(Input{Section: "search", Body: map[string]any{"something_else": float64(9e9)}})
	mustNotFind(t, got, keyOutOfRange)
}

// A hardening policy this build does not have is refused rather than clamped:
// an administrator who asked for a weaker one would otherwise silently get a
// stronger one, or the reverse.
func TestAnUnknownHardeningPolicyIsRefused(t *testing.T) {
	got := Section(Input{Section: "security", Body: map[string]any{"hardening": "paranoid"}})
	if f := mustFind(t, got, keyUnknownHardening); !f.Blocking {
		t.Error("an unknown policy was accepted")
	}

	for _, ok := range []string{"required", "preferred", "off"} {
		got := Section(Input{Section: "security", Body: map[string]any{"hardening": ok}})
		mustNotFind(t, got, keyUnknownHardening)
	}
}

// Both limits are inert until the switch is on, so enabling it with neither one
// set produces a control that looks like it bounds the volume and does not.
func TestASizeGuardWithNoBoundIsRefused(t *testing.T) {
	got := Section(Input{Section: "db", Body: map[string]any{"size_guard": true}})
	if f := mustFind(t, got, keyGuardHasNoBound); !f.Blocking {
		t.Error("a guard that cannot trip was accepted")
	}

	withBound := Section(Input{
		Section: "db",
		Body:    map[string]any{"size_guard": true, "max_bytes": float64(1 << 30)},
	})
	mustNotFind(t, withBound, keyGuardHasNoBound)

	// Off with no bounds is just off.
	off := Section(Input{Section: "db", Body: map[string]any{"size_guard": false}})
	mustNotFind(t, off, keyGuardHasNoBound)
}

// Single sign-on turned on and incomplete is a button that fails for whoever
// presses it, which is worse than refusing the save.
func TestIncompleteSingleSignOnIsRefused(t *testing.T) {
	got := Section(Input{Section: "oidc", Body: map[string]any{"enabled": true}})
	if !Blocked(got) {
		t.Fatalf("an empty provider was accepted: %v", keysOf(got))
	}
	fields := map[string]bool{}
	for _, f := range Blocking(got) {
		fields[f.Field] = true
	}
	for _, want := range []string{"issuer", "client_id"} {
		if !fields[want] {
			t.Errorf("the refusal did not name %q", want)
		}
	}
}

// The token endpoint carries a client secret, so plain HTTP is refused.
func TestAnIssuerMustBeHTTPS(t *testing.T) {
	got := Section(Input{
		Section: "oidc",
		Body: map[string]any{
			"enabled": true, "client_id": "stowcloud", "issuer": "http://idp.example.test",
		},
	})
	if f := mustFind(t, got, keyIssuerMustBeHTTPS); !f.Blocking {
		t.Error("a plaintext issuer was accepted")
	}

	ok := Section(Input{
		Section: "oidc",
		Body: map[string]any{
			"enabled": true, "client_id": "stowcloud", "issuer": "https://idp.example.test",
		},
	})
	if Blocked(ok) {
		t.Errorf("a complete provider was refused: %v", keysOf(ok))
	}
}

// A provider that is off is not checked, so an administrator can fill the form
// in over several saves.
func TestAProviderThatIsOffIsNotChecked(t *testing.T) {
	got := Section(Input{Section: "oidc", Body: map[string]any{"enabled": false, "issuer": ""}})
	if Blocked(got) {
		t.Errorf("a disabled provider was checked: %v", keysOf(got))
	}
}

// Homes is probed by writing, because "the directory exists" and "this process
// can create folders in it" are different questions and only the second one
// decides whether the feature works.
func TestAWritableHomesRootPasses(t *testing.T) {
	dir := t.TempDir()
	got := Section(Input{Section: "homes", Body: map[string]any{"enabled": true, "root": dir}})
	if f := mustFind(t, got, keyDirIsWritable); f.Blocking {
		t.Error("a writable directory refused the save")
	}
	// The probe file does not survive the check.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the probe left %d files behind", len(entries))
	}
}

// Permission bits do not answer this on their own, so the probe writes.
func TestAnUnwritableHomesRootIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, where the mode bits do not refuse a write")
	}
	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}

	got := Section(Input{Section: "homes", Body: map[string]any{"enabled": true, "root": dir}})
	if f := mustFind(t, got, keyDirNotWritable); !f.Blocking {
		t.Error("an unwritable homes root was accepted")
	}
}

// A root that does not exist yet is fine when its parent takes a file: the
// server creates it.
func TestAMissingHomesRootPassesWhenItsParentIsWritable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "homes")
	got := Section(Input{Section: "homes", Body: map[string]any{"enabled": true, "root": root}})
	if f := mustFind(t, got, keyDirWillBeCreated); f.Blocking {
		t.Error("a creatable root refused the save")
	}
}

func TestAMissingHomesParentIsRefused(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nope", "homes")
	got := Section(Input{Section: "homes", Body: map[string]any{"enabled": true, "root": root}})
	if f := mustFind(t, got, keyDirDoesNotExist); !f.Blocking {
		t.Error("a root under a missing parent was accepted")
	}
}

func TestAFileWhereTheHomesRootBelongsIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Section(Input{Section: "homes", Body: map[string]any{"enabled": true, "root": path}})
	if f := mustFind(t, got, keyPathIsNotADirectory); !f.Blocking {
		t.Error("a file was accepted as the homes root")
	}
}

func TestARelativeHomesRootIsRefused(t *testing.T) {
	got := Section(Input{Section: "homes", Body: map[string]any{"enabled": true, "root": "homes"}})
	if f := mustFind(t, got, keyPathMustBeAbsolute); !f.Blocking {
		t.Error("a relative root was accepted")
	}
}

// Turning homes on without naming a root probes the default under the data
// directory, which is the path the server would actually use.
func TestTurningHomesOnProbesTheDefaultRoot(t *testing.T) {
	data := t.TempDir()
	got := Section(Input{
		Section: "homes",
		Body:    map[string]any{"enabled": true},
		DataDir: data,
	})
	f := mustFind(t, got, keyDirWillBeCreated)
	if path, _ := f.Arg("path"); path != filepath.Join(data, "homes") {
		t.Errorf("the probe looked at %q, want the default root", path)
	}
}

// SMB validation goes through the one dry-validate entry point rather than a
// second copy of the renderer's defaults, which is the only way the preview and
// the publish cannot disagree.
func TestAWorkgroupTheRendererRefusesIsRefusedHere(t *testing.T) {
	got := Section(Input{
		Section: "smb",
		Body:    map[string]any{"workgroup": "BAD\nGROUP\n[global]"},
	})
	f := mustFind(t, got, keySMBRenderFailed)
	if !f.Blocking {
		t.Error("a workgroup that would inject a section was accepted")
	}
	if reason, _ := f.Arg("error"); reason == "" {
		t.Error("the refusal did not carry the renderer's reason")
	}

	ok := Section(Input{Section: "smb", Body: map[string]any{"workgroup": "OFFICE"}})
	mustNotFind(t, ok, keySMBRenderFailed)
}

func TestUnknownSMBPoliciesAndRootGIDAreRefused(t *testing.T) {
	got := Section(Input{
		Section: "smb",
		Body:    map[string]any{"totp_policy": "allow_anything", "service_gid": float64(0)},
	})
	if f := mustFind(t, got, keyUnknownTOTPPolicy); !f.Blocking {
		t.Error("an unknown policy was accepted")
	}
	if f := mustFind(t, got, keyGIDZeroIsRoot); !f.Blocking {
		t.Error("the root group was accepted as the service group")
	}

	for _, policy := range []string{"require_separate", "block"} {
		ok := Section(Input{Section: "smb", Body: map[string]any{"totp_policy": policy}})
		mustNotFind(t, ok, keyUnknownTOTPPolicy)
	}
}

// An interface pin the renderer would refuse is refused here too, through
// the same dry-validate entry point workgroup uses.
func TestAPublicInterfacePinIsRefusedHere(t *testing.T) {
	got := Section(Input{
		Section: "smb",
		Body:    map[string]any{"interfaces": []any{"203.0.113.7"}},
	})
	f := mustFind(t, got, keySMBRenderFailed)
	if !f.Blocking {
		t.Error("a public interface pin was accepted")
	}
	if f.Field != "interfaces" {
		t.Errorf("the refusal named field %q, want interfaces", f.Field)
	}

	ok := Section(Input{Section: "smb", Body: map[string]any{"interfaces": []any{"192.168.1.10"}}})
	mustNotFind(t, ok, keySMBRenderFailed)
}

// A wrong-typed interfaces list is refused, the same as any other list field.
func TestAWrongTypedInterfacesListIsRefused(t *testing.T) {
	got := Section(Input{
		Section: "smb",
		Body:    map[string]any{"interfaces": "192.168.1.10"},
	})
	if !Blocked(got) {
		t.Errorf("a string where a list belongs was accepted: %v", keysOf(got))
	}
}

// The sidecar's directory is only worth probing when SMB is being turned on,
// and its absence is reported rather than refused: the server runs without a
// sidecar, and refusing would make the setting unsaveable on a host where the
// sidecar starts later.
func TestAMissingSMBConfigDirIsReportedNotRefused(t *testing.T) {
	got := Section(Input{
		Section:      "smb",
		Body:         map[string]any{"enabled": true, "workgroup": "OFFICE"},
		SMBConfigDir: filepath.Join(t.TempDir(), "absent"),
	})
	if f := mustFind(t, got, keySMBDirUnavailable); f.Blocking {
		t.Error("a missing sidecar directory refused the save")
	}
	if Blocked(got) {
		t.Errorf("the save was refused: %v", keysOf(Blocking(got)))
	}
}

func TestAWritableSMBConfigDirSaysNothing(t *testing.T) {
	got := Section(Input{
		Section:      "smb",
		Body:         map[string]any{"enabled": true, "workgroup": "OFFICE"},
		SMBConfigDir: t.TempDir(),
	})
	mustNotFind(t, got, keySMBDirUnavailable)
}

// Turning SMB off does not probe the sidecar at all.
func TestTheSidecarIsNotProbedWhenSMBIsOff(t *testing.T) {
	got := Section(Input{
		Section:      "smb",
		Body:         map[string]any{"enabled": false},
		SMBConfigDir: filepath.Join(t.TempDir(), "absent"),
	})
	mustNotFind(t, got, keySMBDirUnavailable)
}

// A watch bound above what the kernel will grant is accepted by the interface
// and then silently not honoured, which surfaces much later as changes that do
// not appear until something else triggers a rescan.
func TestAWatchBoundAboveTheKernelLimitIsReported(t *testing.T) {
	limit, ok := kernelWatchLimit()
	if !ok {
		t.Skip("this kernel does not report an inotify watch limit")
	}

	over := Section(Input{
		Section: "watch",
		Body:    map[string]any{"hot_set_max": float64(int64(limit) + 1)},
	})
	f := mustFind(t, over, keyAboveWatchLimit)
	if f.Blocking {
		t.Error("a bound above the kernel limit refused the save")
	}
	if got, _ := f.Arg("limit"); got == "" {
		t.Error("the finding did not carry the kernel's limit")
	}

	// A bound the kernel will honour reports that it will.
	under := Section(Input{Section: "watch", Body: map[string]any{"hot_set_max": float64(64)}})
	mustFind(t, under, keyWithinWatchLimit)
}

// The kernel's own file is host input: a limit this test can read is a number.
func TestTheKernelWatchLimitIsReadAsANumber(t *testing.T) {
	limit, ok := kernelWatchLimit()
	if !ok {
		t.Skip("this kernel does not report an inotify watch limit")
	}
	if limit <= 0 {
		t.Errorf("the limit read as %d", limit)
	}
	t.Logf("this kernel grants %d watches per user", limit)
}

// A section with nothing to say says so, rather than answering with an empty
// list a screen would render as blank.
func TestACleanSectionReportsThatItPassed(t *testing.T) {
	got := Section(Input{Section: "paths", Body: map[string]any{}})
	if len(got) != 1 {
		t.Fatalf("a clean section produced %v", keysOf(got))
	}
	if f := got[0]; f.ReasonKey != keyCheckPassed || f.Blocking {
		t.Errorf("the clean answer is %+v", f)
	}
}

// A mistyped section name must not persist anything. Absent the allow-list the
// store would accept the new name, and the interface would then display a
// setting that no part of the engine consults.
func TestOnlyKnownSectionsAreAccepted(t *testing.T) {
	for _, s := range Sections() {
		if !Known(s) {
			t.Errorf("the listed section %q is not known", s)
		}
	}
	for _, s := range []string{"", "netwrok", "SECURITY", "../etc"} {
		if Known(s) {
			t.Errorf("the unknown section %q was accepted", s)
		}
	}

	// The two sections that were missing while the snapshot advertised them as
	// editable, so the screen offered a control whose save answered "no such
	// section".
	for _, s := range []string{"rate", "security"} {
		if !Known(s) {
			t.Errorf("the section %q is missing from the allow-list", s)
		}
	}
}

// The refusal carries the findings, so the presentation layer renders them
// without a second probe.
func TestTheRefusalCarriesItsFindings(t *testing.T) {
	findings := []Finding{
		advisory("network", "trusted_proxies", keyProxyIsEverything),
		blocking("network", "bind", keyInvalidBindAddress),
	}

	err := Refused(findings)
	if err == nil {
		t.Fatal("a blocking finding did not refuse")
	}
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatal("the refusal is not a RefusedError")
	}
	if len(refused.Findings) != 2 {
		t.Errorf("the refusal carries %d findings, want both", len(refused.Findings))
	}
	// The message names what refused, so a log line is usable on its own.
	if !strings.Contains(err.Error(), keyInvalidBindAddress) {
		t.Errorf("the message does not name the refusal: %q", err)
	}
	// And not what merely warned.
	if strings.Contains(err.Error(), keyProxyIsEverything) {
		t.Errorf("the message names an advisory finding: %q", err)
	}

	if Refused(Advisory(findings)) != nil {
		t.Error("advisory findings alone produced a refusal")
	}
	if len(Advisory(findings)) != 1 || len(Blocking(findings)) != 1 {
		t.Error("the split does not partition the findings")
	}
}

func TestArgReadsByName(t *testing.T) {
	f := Finding{Args: []string{"host", "files.example.test", "limit", "8192"}}
	if v, ok := f.Arg("limit"); !ok || v != "8192" {
		t.Errorf("Arg(limit) = %q, %v", v, ok)
	}
	if _, ok := f.Arg("missing"); ok {
		t.Error("a missing argument reported present")
	}
	// An odd trailing name has no value, and reading it must not panic.
	odd := Finding{Args: []string{"host"}}
	if _, ok := odd.Arg("host"); ok {
		t.Error("a name with no value reported present")
	}
}

func TestHostOnlyDropsThePort(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"files.example.test", "files.example.test"},
		{"files.example.test:8443", "files.example.test"},
		{"FILES.example.test:8443", "files.example.test"},
		{"[::1]:8443", "::1"},
		{"[::1]", "::1"},
	} {
		if got := HostOnly(c.in); got != c.want {
			t.Errorf("HostOnly(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
