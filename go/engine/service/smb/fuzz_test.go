package smb

import (
	"strings"
	"testing"
)

// The injection property.
//
// The strategy is refusal rather than escaping, so the property to prove is
// that whatever Render accepts cannot introduce a directive or a section the
// caller did not give it. A value that would has to have been refused instead.

// directivesIn parses the rendered file the way a reader does: a line opening
// with a bracket is a section, and a line holding an equals sign outside a
// comment is a directive.
func directivesIn(out string) (sections, directives []string) {
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
			// A commented directive is not a directive.
			continue
		case strings.HasPrefix(line, "["):
			sections = append(sections, line)
		default:
			if name, _, ok := strings.Cut(line, "="); ok {
				directives = append(directives, strings.TrimSpace(name))
			}
		}
	}
	return sections, directives
}

// A commented directive is not a directive, which the parser above has to agree
// with or the fuzz property below proves nothing.
func TestACommentedDirectiveIsNotADirective(t *testing.T) {
	_, directives := directivesIn("" +
		"[global]\n" +
		"  # workgroup = COMMENTED\n" +
		"  ; server string = ALSO COMMENTED\n" +
		"  workgroup = REAL\n")

	for _, d := range directives {
		if d == "workgroup" {
			continue
		}
		t.Errorf("the parser read a commented line as the directive %q", d)
	}
	if len(directives) != 1 {
		t.Errorf("got %d directives, want only the uncommented one: %v", len(directives), directives)
	}
}

// baselineShape is what the file holds when every interpolated value is inert:
// the shape a fuzz case is compared against.
func baselineShape() (sections, directives map[string]int, err error) {
	out, _, rerr := Render(
		Config{Workgroup: "WG", ServiceUser: "svc"},
		[]ShareDef{{Name: "share", Path: "/srv/share", ValidUsers: []string{"alice"}}})
	if rerr != nil {
		return nil, nil, rerr
	}
	s, d := directivesIn(string(out))
	return countOf(s), countOf(d), nil
}

func countOf(in []string) map[string]int {
	out := make(map[string]int, len(in))
	for _, v := range in {
		out[v]++
	}
	return out
}

// Render never emits a value that re-parses as a directive or a section it was
// not given, and never panics.
func FuzzRenderNeverInjects(f *testing.F) {
	f.Add("WG", "svc", "share", "/srv/share", "alice")
	f.Add("WORK GROUP", "svc", "share", "/srv/share", "alice")
	f.Add("WG", "svc", "share]\n[evil", "/srv/share", "alice")
	f.Add("WG", "svc", "share", "/srv/x\n  guest ok = yes", "alice")
	f.Add("WG", "svc", "share", "/srv/share", "alice bob")
	f.Add("WG", "svc\n  guest ok = yes", "share", "/srv/share", "alice")
	f.Add("WG", "svc", "share", "/srv/share", "+everyone")

	baseSections, baseDirectives, err := baselineShape()
	if err != nil {
		f.Fatalf("the baseline did not render: %v", err)
	}

	f.Fuzz(func(t *testing.T, workgroup, serviceUser, shareName, sharePath, account string) {
		out, res, err := Render(
			Config{Workgroup: workgroup, ServiceUser: serviceUser},
			[]ShareDef{{Name: shareName, Path: sharePath, ValidUsers: []string{account}}})
		if err != nil {
			// A refusal is the designed answer for anything unrepresentable.
			return
		}

		sections, directives := directivesIn(string(out))

		// The file holds exactly the sections it was given: [global] and the one
		// share. A value that opened another would have to have been refused.
		if len(sections) != len(baseSections) {
			t.Errorf("the file holds %d sections, want %d\ninput: %q %q %q %q %q\n%s",
				len(sections), len(baseSections),
				workgroup, serviceUser, shareName, sharePath, account, out)
		}
		for _, s := range sections {
			if s != "[global]" && s != "["+shareName+"]" {
				t.Errorf("a section nobody gave: %q\n%s", s, out)
			}
		}

		// And exactly the directives, in the same counts. An interpolated value
		// that ended its line early would add one or drop one.
		got := countOf(directives)
		for name, want := range baseDirectives {
			if got[name] != want {
				t.Errorf("the directive %q appears %d times, want %d\ninput: %q %q %q %q %q\n%s",
					name, got[name], want,
					workgroup, serviceUser, shareName, sharePath, account, out)
			}
		}
		for name := range got {
			if _, known := baseDirectives[name]; !known {
				t.Errorf("a directive nobody gave: %q\ninput: %q %q %q %q %q\n%s",
					name, workgroup, serviceUser, shareName, sharePath, account, out)
			}
		}

		// A dropped account is reported rather than silently omitted.
		for _, d := range res.Dropped {
			if d.Name != account {
				t.Errorf("a name nobody supplied was reported dropped: %+v", d)
			}
		}
	})
}

// PasswdEntries never writes a record that would parse as two, and never
// panics.
func FuzzPasswdEntriesNeverInjects(f *testing.F) {
	f.Add("alice", uint32(1000))
	f.Add("has:colon", uint32(1000))
	f.Add("has\nnewline", uint32(1000))
	f.Add("", uint32(0))

	f.Fuzz(func(t *testing.T, name string, uid uint32) {
		out, err := PasswdEntries([]User{{Name: name, Uid: uid}}, 100)
		if err != nil {
			return
		}
		got := string(out)

		// One account is one record, which is one line.
		lines := strings.Count(strings.TrimSuffix(got, "\n"), "\n") + 1
		if got == "" {
			lines = 0
		}
		if lines != 1 {
			t.Errorf("one account produced %d lines: %q", lines, got)
		}
		// And that record has exactly the field count the format defines.
		if fields := strings.Count(strings.TrimSuffix(got, "\n"), ":"); fields != 6 {
			t.Errorf("the record holds %d separators, want 6: %q", fields, got)
		}
	})
}

// Validate never panics, and agrees with Render on the configuration fields.
func FuzzValidateAgreesWithRender(f *testing.F) {
	f.Add("WG", "svc", "server", "192.168.1.1")
	f.Add("WORK\nGROUP", "svc", "server", "")
	f.Add("WG", "", "", "not-an-address")

	f.Fuzz(func(t *testing.T, workgroup, serviceUser, serverName, iface string) {
		cfg := Config{Workgroup: workgroup, ServiceUser: serviceUser, ServerName: serverName}
		if iface != "" {
			cfg.Interfaces = []string{iface}
		}

		verr := Validate(cfg)
		_, _, rerr := Render(cfg, []ShareDef{{Name: "s", Path: "/srv/s"}})

		// The share is inert, so any disagreement is about the configuration
		// fields, which is exactly what the shared entry point promises to
		// answer identically.
		if (verr == nil) != (rerr == nil) {
			t.Errorf("Validate and Render disagree on %+v: %v versus %v", cfg, verr, rerr)
		}
	})
}
