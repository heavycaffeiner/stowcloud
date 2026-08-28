package smb

import (
	"errors"
	"strings"
	"testing"
)

// baseConfig is a configuration that renders, so a test can change one field
// and see that field refused.
func baseConfig() Config {
	return Config{Enabled: true, Workgroup: "WORKGROUP", ServiceUser: "scsvc"}
}

func baseShare() ShareDef {
	return ShareDef{Name: "files", Path: "/srv/files", ValidUsers: []string{"alice"}}
}

func mustRender(t *testing.T, cfg Config, shares []ShareDef) (string, Result) {
	t.Helper()
	out, res, err := Render(cfg, shares)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return string(out), res
}

// The defaults live in this package and nowhere else, which is what ends the
// drift between two checkers probing with their own inline copies.
func TestDefaultsAreOwnedHere(t *testing.T) {
	got := Config{}.WithDefaults()
	if got.Workgroup != DefaultWorkgroup || got.ServiceUser != DefaultServiceUser {
		t.Errorf("the defaults came through as %q and %q", got.Workgroup, got.ServiceUser)
	}
	// A configured value survives.
	mine := Config{Workgroup: "OFFICE", ServiceUser: "svc"}.WithDefaults()
	if mine.Workgroup != "OFFICE" || mine.ServiceUser != "svc" {
		t.Errorf("a configured value was overwritten: %+v", mine)
	}
	// An empty configuration validates, because the defaults fill it.
	if err := Validate(Config{}); err != nil {
		t.Errorf("an empty configuration did not validate under its own defaults: %v", err)
	}
}

// Validate refuses what Render refuses, which is the parity the shared entry
// point exists for.
func TestValidateRefusesWhatRenderRefuses(t *testing.T) {
	cases := map[string]Config{
		"a workgroup with a newline":   {Workgroup: "WORK\nGROUP"},
		"a workgroup with a bracket":   {Workgroup: "WORK[GROUP"},
		"a workgroup with a comment":   {Workgroup: "WORK;GROUP"},
		"a workgroup with whitespace":  {Workgroup: "WORK GROUP"},
		"a service user with a space":  {ServiceUser: "svc account"},
		"a service user with a sigil":  {ServiceUser: "@svc"},
		"a server name too long":       {ServerName: strings.Repeat("a", serverNameMaxRunes+1)},
		"a server name with a dot":     {ServerName: "my.server"},
		"a public pin without opt-in":  {Interfaces: []string{"8.8.8.8"}},
		"an interface that is no addr": {Interfaces: []string{"not-an-address"}},
	}

	for name, partial := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig()
			// Overlay only the field under test, so each case is one change.
			if partial.Workgroup != "" {
				cfg.Workgroup = partial.Workgroup
			}
			if partial.ServiceUser != "" {
				cfg.ServiceUser = partial.ServiceUser
			}
			cfg.ServerName = partial.ServerName
			cfg.Interfaces = partial.Interfaces

			verr := Validate(cfg)
			_, _, rerr := Render(cfg, []ShareDef{baseShare()})

			if verr == nil {
				t.Errorf("Validate accepted it")
			}
			if rerr == nil {
				t.Errorf("Render accepted it")
			}
			if (verr == nil) != (rerr == nil) {
				t.Errorf("the two disagree: Validate %v, Render %v", verr, rerr)
			}
		})
	}

	// And parity in the other direction: what one accepts, so does the other.
	cfg := baseConfig()
	cfg.Interfaces = []string{"192.168.1.10"}
	if err := Validate(cfg); err != nil {
		t.Errorf("Validate refused a private pin: %v", err)
	}
	if _, _, err := Render(cfg, []ShareDef{baseShare()}); err != nil {
		t.Errorf("Render refused a private pin: %v", err)
	}
}

// Each refused character class refuses, named field by field.
func TestEachRefusedCharacterClassRefuses(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"a newline", "a\nb"},
		{"a carriage return", "a\rb"},
		{"a NUL", "a\x00b"},
		{"an opening bracket", "a[b"},
		{"a closing bracket", "a]b"},
		{"a semicolon comment", "a;b"},
		{"a hash comment", "a#b"},
		{"a trailing backslash", `ab\`},
		{"a substitution marker", "a%Ub"},
		{"a control character", "a\x07b"},
		{"invalid UTF-8", "a\xffb"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A share path carries every class, so it is the one field that can
			// exercise all of them.
			share := baseShare()
			share.Path = "/srv/" + c.value
			_, _, err := Render(baseConfig(), []ShareDef{share})
			if !errors.Is(err, ErrUnsafeValue) {
				t.Fatalf("got %v, want ErrUnsafeValue", err)
			}
			var unsafe *UnsafeError
			if !errors.As(err, &unsafe) {
				t.Fatalf("the error is not an UnsafeError: %v", err)
			}
			if unsafe.Field == "" || unsafe.Reason == "" {
				t.Errorf("the refusal names nothing actionable: %+v", unsafe)
			}
		})
	}
}

// A share path has to be absolute, or Samba resolves it against a directory
// nobody chose.
func TestAShapePathMustBeAbsoluteAndPresent(t *testing.T) {
	for _, path := range []string{"", "relative/path", "./here"} {
		share := baseShare()
		share.Path = path
		if _, _, err := Render(baseConfig(), []ShareDef{share}); !errors.Is(err, ErrUnsafeValue) {
			t.Errorf("the path %q was accepted: %v", path, err)
		}
	}
}

// Two share names that differ only by case or surrounding space are one block
// to Samba, and the later one silently replaces the earlier along with its
// account lists.
func TestDuplicateShareNamesRefuse(t *testing.T) {
	for _, second := range []string{"files", "FILES", " files ", "Files"} {
		shares := []ShareDef{baseShare(), {Name: second, Path: "/srv/other"}}
		_, _, err := Render(baseConfig(), shares)
		if !errors.Is(err, ErrUnsafeValue) {
			t.Errorf("a second share named %q was accepted", second)
		}
	}

	// An empty name is refused too.
	if _, _, err := Render(baseConfig(), []ShareDef{{Name: "  ", Path: "/srv/x"}}); err == nil {
		t.Error("a blank share name was accepted")
	}
}

// The deliberate change: an account name that fails the check is dropped from
// its list with a warning naming it, rather than failing the whole file. A
// legacy name must cost one account its SMB access, not everyone's.
func TestABadAccountNameDegradesPerEntry(t *testing.T) {
	share := baseShare()
	share.ValidUsers = []string{"alice", "bad name", "bob"}
	share.ReadList = []string{"carol", "+modifier"}
	share.WriteList = []string{"dave"}

	out, res := mustRender(t, baseConfig(), []ShareDef{share})

	// The file still renders, and the good names are in it.
	for _, want := range []string{"alice", "bob", "carol", "dave"} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered file lost the valid name %q", want)
		}
	}
	// The bad ones are gone from the output.
	for _, gone := range []string{"bad name", "+modifier"} {
		if strings.Contains(out, gone) {
			t.Errorf("the rendered file carries the refused name %q", gone)
		}
	}
	// And they are reported rather than silently omitted.
	if len(res.Dropped) != 2 {
		t.Fatalf("reported %d dropped names: %+v", len(res.Dropped), res.Dropped)
	}
	for _, d := range res.Dropped {
		if d.Share != "files" || d.Field == "" || d.Reason == "" {
			t.Errorf("a dropped name is not fully described: %+v", d)
		}
	}
}

// A bad share name still refuses the batch: config fields are operator input,
// few in number, and a config with one bad field is a config to fix.
func TestABadShareNameStillRefusesTheBatch(t *testing.T) {
	shares := []ShareDef{baseShare(), {Name: "bad\nname", Path: "/srv/other"}}
	if _, _, err := Render(baseConfig(), shares); !errors.Is(err, ErrUnsafeValue) {
		t.Errorf("got %v, want the whole batch refused", err)
	}
}

// The rendered file carries the hardening that is a fact about how this server
// runs rather than a setting.
func TestTheRenderedFileCarriesItsHardening(t *testing.T) {
	out, _ := mustRender(t, baseConfig(), []ShareDef{baseShare()})

	for _, want := range []string{
		"server min protocol = SMB3_11",
		"server signing = required",
		"smb encrypt = required",
		"null passwords = no",
		"guest ok = no",
		"map to guest = never",
		"lanman auth = no",
		"bind interfaces only = yes",
		"hosts deny = 0.0.0.0/0",
		"server multi channel support = no",
		"veto files = " + vetoNames,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered file is missing %q", want)
		}
	}
}

// With no pinned interface the scope is the closed case: loopback only, which
// the sidecar expands in the namespace that can see the devices.
func TestNoPinnedInterfaceRendersTheClosedCase(t *testing.T) {
	out, _ := mustRender(t, baseConfig(), []ShareDef{baseShare()})
	if !strings.Contains(out, "interfaces = lo\n") {
		t.Error("the closed case does not bind loopback only")
	}
	if !strings.Contains(out, "hosts allow = 127.0.0.0/8 ::1/128") {
		t.Error("the closed case admits more than loopback")
	}
}

// A pinned interface narrows what is bound and leaves the admission list wide:
// the two answer different questions.
func TestAPinnedInterfaceDoesNotNarrowAdmission(t *testing.T) {
	cfg := baseConfig()
	cfg.Interfaces = []string{"192.168.1.10"}
	out, _ := mustRender(t, cfg, []ShareDef{baseShare()})

	if !strings.Contains(out, "interfaces = lo 192.168.1.10") {
		t.Error("the pin did not reach the bind line")
	}
	if strings.Contains(out, "hosts allow = 127.0.0.0/8 ::1/128") {
		t.Error("pinning an interface narrowed the admission list")
	}
	if !strings.Contains(out, "hosts allow = 10.0.0.0/8") {
		t.Error("the admission list is not the private set")
	}
}

// A public pin needs the explicit opt-in, because whether a public address
// exists at all is only knowable in the namespace that binds.
func TestAPublicPinNeedsTheOptIn(t *testing.T) {
	cfg := baseConfig()
	cfg.Interfaces = []string{"8.8.8.8"}
	if _, _, err := Render(cfg, []ShareDef{baseShare()}); !errors.Is(err, ErrBindRefused) {
		t.Fatalf("got %v, want ErrBindRefused", err)
	}

	cfg.AllowPublicBind = true
	if _, _, err := Render(cfg, []ShareDef{baseShare()}); err != nil {
		t.Errorf("the opt-in did not admit a public pin: %v", err)
	}
}

// A server name turns the name service on; without one it stays off and clients
// use the address.
func TestTheServerNameControlsTheNameService(t *testing.T) {
	out, _ := mustRender(t, baseConfig(), []ShareDef{baseShare()})
	if !strings.Contains(out, "disable netbios = yes") {
		t.Error("without a name the name service is not off")
	}

	cfg := baseConfig()
	cfg.ServerName = "storage"
	named, _ := mustRender(t, cfg, []ShareDef{baseShare()})
	for _, want := range []string{
		"netbios name = storage",
		"disable netbios = no",
		"local master = no",
		"dns proxy = no",
	} {
		if !strings.Contains(named, want) {
			t.Errorf("the named case is missing %q", want)
		}
	}
}

// A share whose policy names no mode pair inherits the server's own, which is
// what makes a file created over SMB writable from the web UI.
func TestShareModesFallBackToTheServerDefault(t *testing.T) {
	out, _ := mustRender(t, baseConfig(), []ShareDef{baseShare()})
	if !strings.Contains(out, "create mask = 0664") || !strings.Contains(out, "directory mask = 0775") {
		t.Error("the default mode pair is not the server's own")
	}

	share := baseShare()
	share.ModeFile, share.ModeDir = 0o600, 0o700
	custom, _ := mustRender(t, baseConfig(), []ShareDef{share})
	if !strings.Contains(custom, "create mask = 0600") || !strings.Contains(custom, "directory mask = 0700") {
		t.Error("a share's own policy did not reach the file")
	}
}

// A share another program writes turns off the leases that would show a stale
// view.
func TestAnExternallySharedTreeDisablesLeases(t *testing.T) {
	share := baseShare()
	share.SharedExternally = true
	out, _ := mustRender(t, baseConfig(), []ShareDef{share})
	for _, want := range []string{"oplocks = no", "level2 oplocks = no", "kernel oplocks = no"} {
		if !strings.Contains(out, want) {
			t.Errorf("an externally shared tree is missing %q", want)
		}
	}
}

// The account file is colon-separated with no escape, so a name carrying a
// separator is refused rather than written.
func TestPasswdEntriesRefuseUnwritableNames(t *testing.T) {
	for _, name := range []string{"", "has:colon", "has\nnewline", "has space", "@group"} {
		if _, err := PasswdEntries([]User{{Name: name, Uid: 1000}}, 100); err == nil {
			t.Errorf("the name %q was written", name)
		}
	}
}

// A shared uid makes the import keep one account and leave the rest unable to
// authenticate, silently. It is refused rather than rendered.
func TestPasswdEntriesRefuseASharedUid(t *testing.T) {
	users := []User{{Name: "alice", Uid: 1000}, {Name: "bob", Uid: 1000}}
	_, err := PasswdEntries(users, 100)
	if !errors.Is(err, ErrUnsafeValue) {
		t.Fatalf("got %v, want ErrUnsafeValue", err)
	}
	if !strings.Contains(err.Error(), "uid") {
		t.Errorf("the refusal does not name the cause: %v", err)
	}
}

// Entries are sorted, so the file is the same for the same accounts however
// they arrived.
func TestPasswdEntriesAreSortedAndComplete(t *testing.T) {
	users := []User{{Name: "carol", Uid: 1002}, {Name: "alice", Uid: 1000}, {Name: "bob", Uid: 1001}}
	out, err := PasswdEntries(users, 100)
	if err != nil {
		t.Fatalf("PasswdEntries: %v", err)
	}
	got := string(out)

	wantOrder := []string{"alice:", "bob:", "carol:"}
	at := -1
	for _, w := range wantOrder {
		i := strings.Index(got, w)
		if i < 0 {
			t.Fatalf("the file is missing %q:\n%s", w, got)
		}
		if i < at {
			t.Errorf("the entries are not sorted:\n%s", got)
		}
		at = i
	}
	if !strings.Contains(got, "alice:x:1000:100::/nonexistent:/usr/sbin/nologin") {
		t.Errorf("an entry is not in the expected shape:\n%s", got)
	}
	// A caller's slice is not reordered underneath it.
	if users[0].Name != "carol" {
		t.Error("PasswdEntries sorted the caller's own slice")
	}
}
