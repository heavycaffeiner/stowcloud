package smb

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

func baseConfig() Config {
	return Config{
		Enabled:     true,
		Workgroup:   "WORKGROUP",
		ServiceUser: "scsvc",
	}
}

func mustRender(t *testing.T, cfg Config, shares []ShareDef) string {
	t.Helper()
	out, err := Render(cfg, shares)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return string(out)
}

// A directive's value, or the empty string when the file has no such line.
func directive(conf, key string) string {
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// An unpinned configuration renders the closed case, because this process runs
// in a different namespace from the one that binds and can only see the wrong
// machine.
func TestAnUnpinnedConfigurationIsClosed(t *testing.T) {
	conf := mustRender(t, baseConfig(), nil)

	if got := directive(conf, "interfaces"); got != "lo" {
		t.Fatalf("interfaces = %q, want loopback only", got)
	}
	if got := directive(conf, "hosts allow"); got != "127.0.0.0/8 ::1/128" {
		t.Fatalf("hosts allow = %q, want loopback only", got)
	}
	// A private block reaching the bind line unpinned would open the server to
	// every network the host is attached to.
	for _, cidr := range privateCIDRs() {
		if cidr == "127.0.0.0/8" || cidr == "::1/128" {
			continue
		}
		if strings.Contains(directive(conf, "interfaces"), cidr) {
			t.Fatalf("the unpinned bind list carries %s", cidr)
		}
	}
}

// The list is advice rather than a restriction without this, so it accompanies
// every interface list.
func TestTheBindRestrictionAccompaniesEveryList(t *testing.T) {
	for _, cfg := range []Config{
		baseConfig(),
		func() Config { c := baseConfig(); c.Interfaces = []string{"192.168.1.10"}; return c }(),
	} {
		conf := mustRender(t, cfg, nil)
		if got := directive(conf, "bind interfaces only"); got != "yes" {
			t.Fatalf("bind interfaces only = %q for %v", got, cfg.Interfaces)
		}
	}
}

// The private list goes into the admission line only. Narrowing what is bound
// must not narrow who may reach the address it bound.
func TestThePrivateListReachesAdmissionAndNotTheBindLine(t *testing.T) {
	cfg := baseConfig()
	cfg.Interfaces = []string{"192.168.1.10"}
	conf := mustRender(t, cfg, nil)

	if got := directive(conf, "interfaces"); got != "lo 192.168.1.10" {
		t.Fatalf("interfaces = %q, want loopback and the pin", got)
	}
	allow := directive(conf, "hosts allow")
	for _, cidr := range privateCIDRs() {
		if !strings.Contains(allow, cidr) {
			t.Fatalf("hosts allow is missing %s: %q", cidr, allow)
		}
	}
	// The bind line carries the pin and nothing else, so a stray block there
	// would be a network the operator did not name.
	if strings.Contains(directive(conf, "interfaces"), "/") {
		t.Fatalf("a CIDR block reached the bind line: %q", directive(conf, "interfaces"))
	}
}

// A pin is the one bind decision this process can check, because the operator
// wrote the address down rather than the machine reporting it.
func TestAGlobalPinIsRefusedWithoutTheOptIn(t *testing.T) {
	cfg := baseConfig()
	cfg.Interfaces = []string{"203.0.113.5"}

	_, err := Render(cfg, nil)
	if !errors.Is(err, ErrBindRefused) {
		t.Fatalf("a global pin gave %v, want a refusal", err)
	}
	// The refusal names the address, which is what an operator can act on.
	if !strings.Contains(err.Error(), "203.0.113.5") {
		t.Fatalf("the refusal does not name the address: %v", err)
	}

	cfg.AllowPublicBind = true
	conf := mustRender(t, cfg, nil)
	if !strings.Contains(directive(conf, "interfaces"), "203.0.113.5") {
		t.Fatal("the opt-in did not admit the pinned address")
	}
}

// An interface name is a valid entry to Samba and an unprovable one here.
func TestAnInterfaceNameIsRefusedRatherThanPassedThrough(t *testing.T) {
	for _, spec := range []string{"eth0", "192.168.1.0/33", "192.168.1.0/lan", ""} {
		cfg := baseConfig()
		cfg.Interfaces = []string{spec}
		if _, err := Render(cfg, nil); !errors.Is(err, ErrBindRefused) {
			t.Errorf("Render with interface %q gave %v, want a refusal", spec, err)
		}
	}
}

// The generated file is a trust boundary in the outbound direction, and a
// share name is not where anyone should discover that Samba's parser has
// opinions.
func TestAnUnrepresentableShareNameIsRefused(t *testing.T) {
	for _, name := range []string{
		"",
		"  ",
		"a\nb",
		"a\rb",
		"pho[tos",
		"photos]",
		// A comment marker would hide every directive after it on the line.
		"photos;",
		"photos#",
		// A substitution is expanded per connection, so this is a different
		// share for every client.
		"photos%U",
		"photos\x00",
		"photos\\",
	} {
		_, err := Render(baseConfig(), []ShareDef{{Name: name, Path: "/srv/x"}})
		if !errors.Is(err, ErrUnsafeValue) {
			t.Errorf("share name %q gave %v, want a refusal", name, err)
		}
	}
}

// A section closed early is the specific thing being prevented, so it gets a
// test that reads the output rather than the error.
func TestAShareNameCannotOpenASectionOfItsOwn(t *testing.T) {
	_, err := Render(baseConfig(), []ShareDef{
		{Name: "ok]\n[global]\n  guest ok = yes\n[x", Path: "/srv/x"},
	})
	if !errors.Is(err, ErrUnsafeValue) {
		t.Fatalf("a name carrying a section header gave %v, want a refusal", err)
	}
}

// Samba reads two blocks whose names differ only in case as one, and the later
// one silently replaces the earlier one along with its account lists.
func TestADuplicateShareNameIsRefused(t *testing.T) {
	_, err := Render(baseConfig(), []ShareDef{
		{Name: "photos", Path: "/srv/a", ValidUsers: []string{"alice"}},
		{Name: "Photos", Path: "/srv/b", ValidUsers: []string{"bob"}},
	})
	if !errors.Is(err, ErrUnsafeValue) {
		t.Fatalf("a duplicate name gave %v, want a refusal", err)
	}
}

// A name carrying whitespace would split into two entries in a space-separated
// list, and the second one is a grant nobody wrote. That makes this an access
// question rather than a formatting one.
func TestANameThatWouldSplitIntoTwoGrantsIsRefused(t *testing.T) {
	for _, who := range []string{"alice bob", "alice\tbob", "alice\nbob", ""} {
		_, err := Render(baseConfig(), []ShareDef{
			{Name: "photos", Path: "/srv/x", ValidUsers: []string{who}},
		})
		if !errors.Is(err, ErrUnsafeValue) {
			t.Errorf("valid users %q gave %v, want a refusal", who, err)
		}
	}
}

// An entry beginning with a modifier asks for a different grant than the one
// it spells.
func TestAListModifierIsRefused(t *testing.T) {
	for _, who := range []string{"@everyone", "+staff", "&netgroup", "-alice"} {
		_, err := Render(baseConfig(), []ShareDef{
			{Name: "photos", Path: "/srv/x", WriteList: []string{who}},
		})
		if !errors.Is(err, ErrUnsafeValue) {
			t.Errorf("write list %q gave %v, want a refusal", who, err)
		}
	}
}

// A relative path is resolved against a directory nobody chose.
func TestARelativeSharePathIsRefused(t *testing.T) {
	for _, p := range []string{"srv/x", "", "./x", "x"} {
		_, err := Render(baseConfig(), []ShareDef{{Name: "photos", Path: p}})
		if !errors.Is(err, ErrUnsafeValue) {
			t.Errorf("share path %q gave %v, want a refusal", p, err)
		}
	}
}

// The sidecar and this server have to agree on ownership, or a file created
// over SMB is unwritable from the web UI and the other way round. The render
// is asserted to emit a mask consistent with the share's policy for every
// policy value.
func TestTheMaskFollowsTheSharePolicyForEveryValue(t *testing.T) {
	for _, modes := range [][2]uint32{
		{0o664, 0o775},
		{0o660, 0o770},
		{0o644, 0o755},
		{0o600, 0o700},
		{0o666, 0o777},
	} {
		conf := mustRender(t, baseConfig(), []ShareDef{{
			Name: "photos", Path: "/srv/x",
			ModeFile: modes[0], ModeDir: modes[1],
		}})
		wantFile := fmt.Sprintf("%04o", modes[0])
		wantDir := fmt.Sprintf("%04o", modes[1])

		if got := directive(conf, "create mask"); got != wantFile {
			t.Errorf("create mask = %q for policy %04o, want %q", got, modes[0], wantFile)
		}
		if got := directive(conf, "directory mask"); got != wantDir {
			t.Errorf("directory mask = %q for policy %04o, want %q", got, modes[1], wantDir)
		}
		// The mask alone only removes bits. Without the forced mode a file
		// created with narrower bits stays narrow, and the agreement the
		// contract exists for does not hold.
		if got := directive(conf, "force create mode"); got != wantFile {
			t.Errorf("force create mode = %q, want %q", got, wantFile)
		}
		if got := directive(conf, "force directory mode"); got != wantDir {
			t.Errorf("force directory mode = %q, want %q", got, wantDir)
		}
	}
}

// A share with no policy inherits the server's own default pair, which is what
// makes the two agree by default.
func TestAnUnsetPolicyInheritsTheServerDefault(t *testing.T) {
	conf := mustRender(t, baseConfig(), []ShareDef{{Name: "photos", Path: "/srv/x"}})
	if got := directive(conf, "create mask"); got != "0664" {
		t.Fatalf("create mask = %q, want the default", got)
	}
	if got := directive(conf, "directory mask"); got != "0775" {
		t.Fatalf("directory mask = %q, want the default", got)
	}
}

// The hardening directives are facts about how this server runs rather than
// settings, so they are emitted unconditionally.
func TestTheHardeningDirectivesAreUnconditional(t *testing.T) {
	conf := mustRender(t, baseConfig(), nil)
	for key, want := range map[string]string{
		"server min protocol":  "SMB3_11",
		"server signing":       "required",
		"smb encrypt":          "required",
		"restrict anonymous":   "2",
		"null passwords":       "no",
		"guest ok":             "no",
		"map to guest":         "never",
		"lanman auth":          "no",
		"raw NTLMv2 auth":      "no",
		"ntlm auth":            "ntlmv2-only",
		"smb ports":            "445",
		"hosts deny":           "0.0.0.0/0",
		"bind interfaces only": "yes",
		// Multichannel would hand an authenticated client the whole bound
		// interface list, which on host networking is the host's.
		"server multi channel support": "no",
	} {
		if got := directive(conf, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// This server's own control files must never be visible over SMB.
func TestTheControlFilesAreHidden(t *testing.T) {
	conf := mustRender(t, baseConfig(), []ShareDef{{Name: "photos", Path: "/srv/x"}})
	veto := directive(conf, "veto files")
	for _, name := range []string{".sctrash", ".scpart-*", ".scmeta", ".scindex"} {
		if !strings.Contains(veto, name) {
			t.Errorf("veto files is missing %s: %q", name, veto)
		}
	}
}

// Another program writing the same files makes a client-side lease a stale
// view rather than a cache.
func TestAnExternallySharedTreeDisablesLeases(t *testing.T) {
	conf := mustRender(t, baseConfig(), []ShareDef{
		{Name: "photos", Path: "/srv/x", SharedExternally: true},
	})
	for _, key := range []string{"oplocks", "level2 oplocks", "kernel oplocks"} {
		if got := directive(conf, key); got != "no" {
			t.Errorf("%s = %q for an externally shared tree, want no", key, got)
		}
	}
}

// The name service refuses anything over its own length, and a name reaching
// the file verbatim is checked as untrusted input twice over.
func TestAnUnusableServerNameIsRefused(t *testing.T) {
	for _, name := range []string{
		"toolongforanetbiosname",
		"has space",
		"has]bracket",
		"has\nnewline",
		"unicode-네임",
	} {
		cfg := baseConfig()
		cfg.ServerName = name
		if _, err := Render(cfg, nil); !errors.Is(err, ErrUnsafeValue) {
			t.Errorf("server name %q gave %v, want a refusal", name, err)
		}
	}

	// An empty name is the default and means the name service stays off.
	conf := mustRender(t, baseConfig(), nil)
	if got := directive(conf, "disable netbios"); got != "yes" {
		t.Fatalf("disable netbios = %q with no name, want yes", got)
	}
}

// With a name the service answers for its own name only: it must not enter
// browser elections, and must not answer a name it does not know by asking
// DNS.
func TestANamedServerAnswersForItsOwnNameOnly(t *testing.T) {
	cfg := baseConfig()
	cfg.ServerName = "stowcloud"
	conf := mustRender(t, cfg, nil)

	for key, want := range map[string]string{
		"netbios name":     "stowcloud",
		"disable netbios":  "no",
		"local master":     "no",
		"preferred master": "no",
		"domain master":    "no",
		"os level":         "0",
		"dns proxy":        "no",
		"wins support":     "no",
	} {
		if got := directive(conf, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// The private classification and the enclosing block have to answer the same
// question the same way: an address the bind gate calls private must have a
// block the admission list can carry, or a client would reach an interface the
// admission list then denies.
func TestEveryPrivateAddressHasAnEnclosingBlock(t *testing.T) {
	for _, s := range []string{
		"10.1.2.3", "172.20.0.5", "192.168.1.10", "127.0.0.1",
		"169.254.3.4", "100.101.102.103", "::1", "fd00::1", "fe80::1",
	} {
		ip := netip.MustParseAddr(s)
		if !IsPrivate(ip) {
			t.Errorf("%s is not classified private", s)
		}
		block := EnclosingPrivateRange(ip)
		if block == "" {
			t.Errorf("%s has no enclosing block", s)
			continue
		}
		// And the block has to be one the admission list actually writes, or
		// the classification admits an address nothing lets through.
		found := false
		for _, c := range privateCIDRs() {
			if c == block {
				found = true
			}
		}
		if !found {
			t.Errorf("%s encloses to %s, which the admission list does not carry", s, block)
		}
	}
}

func TestAGlobalAddressHasNoEnclosingBlock(t *testing.T) {
	for _, s := range []string{
		"8.8.8.8", "1.1.1.1", "203.0.113.5",
		// Just outside each band.
		"172.32.0.1", "172.15.255.255", "100.63.255.255", "100.128.0.1",
		"2001:db8::1",
	} {
		ip := netip.MustParseAddr(s)
		if IsPrivate(ip) {
			t.Errorf("%s is classified private", s)
		}
		if got := EnclosingPrivateRange(ip); got != "" {
			t.Errorf("%s encloses to %q", s, got)
		}
	}
}

// A tailnet address carries a single-host prefix, so its own subnet admits
// nobody while the carrier-NAT block admits the whole tailnet.
func TestTheBlockIsTheEnclosingOneNotTheOnLinkPrefix(t *testing.T) {
	if got := EnclosingPrivateRange(netip.MustParseAddr("100.90.1.1")); got != "100.64.0.0/10" {
		t.Fatalf("a tailnet address encloses to %q", got)
	}
}

// The import tool matches an entry to an account by uid rather than by name,
// so several names on one uid all import as whichever name the reverse lookup
// answers with, leaving one account able to authenticate and the rest not.
func TestASharedUidIsRefusedRatherThanRendered(t *testing.T) {
	_, err := PasswdEntries([]User{
		{Name: "alice", Uid: 1001},
		{Name: "bob", Uid: 1001},
	}, 1000)
	if !errors.Is(err, ErrUnsafeValue) {
		t.Fatalf("a shared uid gave %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "alice") || !strings.Contains(err.Error(), "bob") {
		t.Fatalf("the refusal does not name both accounts: %v", err)
	}
}

// One uid per account, one gid for all of them.
func TestEachAccountGetsItsOwnUidAndTheSharedGid(t *testing.T) {
	out, err := PasswdEntries([]User{
		{Name: "bob", Uid: 1002},
		{Name: "alice", Uid: 1001},
	}, 1000)
	if err != nil {
		t.Fatalf("PasswdEntries: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), out)
	}
	// Sorted, so the same accounts render the same file and a republish that
	// changed nothing is byte-identical.
	if !strings.HasPrefix(lines[0], "alice:x:1001:1000:") {
		t.Errorf("line 0 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "bob:x:1002:1000:") {
		t.Errorf("line 1 = %q", lines[1])
	}
}

// The passwd format is colon-separated with one record per line and has no
// escape at all.
func TestAnUnrepresentableAccountNameIsRefused(t *testing.T) {
	for _, name := range []string{"", "a:b", "a\nb", "a b", "root\x00"} {
		if _, err := PasswdEntries([]User{{Name: name, Uid: 1001}}, 1000); !errors.Is(err, ErrUnsafeValue) {
			t.Errorf("account name %q gave %v, want a refusal", name, err)
		}
	}
}

// Rendering is a pure function of its inputs, so the same inputs give the same
// bytes. A republish that changed nothing must not look like a change.
func TestRenderingIsDeterministic(t *testing.T) {
	cfg := baseConfig()
	cfg.Interfaces = []string{"192.168.1.10", "10.0.0.5"}
	shares := []ShareDef{
		{Name: "photos", Path: "/srv/photos", ValidUsers: []string{"alice", "bob"}},
		{Name: "media", Path: "/srv/media", SharedExternally: true},
	}
	first := mustRender(t, cfg, shares)
	for range 8 {
		if got := mustRender(t, cfg, shares); got != first {
			t.Fatal("the same inputs rendered different bytes")
		}
	}
}

// Nothing a caller supplies may reach the output unchecked, so the rendered
// file never carries a line outside the shape a directive or a section header
// takes.
func TestTheRenderedFileIsOnlyDirectivesAndSections(t *testing.T) {
	cfg := baseConfig()
	cfg.ServerName = "host1"
	cfg.Interfaces = []string{"192.168.1.10"}
	conf := mustRender(t, cfg, []ShareDef{
		{Name: "photos", Path: "/srv/photos", ValidUsers: []string{"alice"}},
	})

	for i, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "", strings.HasPrefix(trimmed, "#"):
		case strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"):
		case strings.Contains(trimmed, "="):
		default:
			t.Errorf("line %d is neither a section, a comment nor a directive: %q", i+1, line)
		}
	}
}

// Rendering reads operator configuration, which is a trust boundary, so it is
// fuzzed. Nothing may panic, and anything that renders must produce a file
// whose section headers are exactly the shares that were asked for.
func FuzzRender(f *testing.F) {
	f.Add("WORKGROUP", "scsvc", "host1", "192.168.1.10", "photos", "/srv/photos", "alice")
	f.Add("W", "s", "", "", "a", "/a", "b")
	f.Add("W", "s", "", "203.0.113.5", "a]\n[global", "/a", "@all")

	f.Fuzz(func(t *testing.T, workgroup, serviceUser, serverName, iface, share, path, who string) {
		cfg := Config{
			Workgroup:   workgroup,
			ServiceUser: serviceUser,
			ServerName:  serverName,
		}
		if iface != "" {
			cfg.Interfaces = []string{iface}
		}
		out, err := Render(cfg, []ShareDef{
			{Name: share, Path: path, ValidUsers: []string{who}},
		})
		if err != nil {
			return
		}

		// Exactly two sections: the global one and the share.
		var sections []string
		for _, line := range strings.Split(string(out), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
				sections = append(sections, trimmed)
			}
		}
		if len(sections) != 2 {
			t.Fatalf("rendered %d sections from one share: %q", len(sections), sections)
		}
		if sections[0] != "[global]" || sections[1] != "["+share+"]" {
			t.Fatalf("sections = %q for share %q", sections, share)
		}
		// And the bind restriction survives whatever was supplied.
		if got := directive(string(out), "bind interfaces only"); got != "yes" {
			t.Fatalf("bind interfaces only = %q", got)
		}
	})
}
