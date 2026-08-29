//go:build linux && compat_nc

package compat

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// wired builds a wiring holding exactly the named ports.
func wired(ports ...Port) Wiring {
	w := Wiring{Present: map[Port]bool{}}
	for _, p := range ports {
		w.Present[p] = true
	}
	return w
}

// allPorts returns every port the matrix names.
func allPorts() []Port {
	seen := map[Port]bool{}
	var out []Port
	for _, f := range Features() {
		ports, _ := Requires(f)
		for _, p := range ports {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// The rule the whole file exists for: no feature is advertised unless every
// port it needs is attached.
//
// A capability is a promise. A client reading one stops looking for another
// way to do the thing and reports a failure to the user when the endpoint
// answers 500 because something was left nil.
func TestNoFeatureIsAdvertisedWithAMissingPort(t *testing.T) {
	full := wired(allPorts()...)

	for _, f := range Features() {
		ports, ok := Requires(f)
		if !ok {
			t.Fatalf("%s is not in the matrix", f)
		}
		if len(ports) == 0 {
			continue
		}

		// Drop each of the feature's ports in turn. Every one of them has to
		// be load-bearing, or it is not really a requirement.
		for _, drop := range ports {
			partial := wired(allPorts()...)
			delete(partial.Present, drop)

			for _, got := range Advertised(partial) {
				if got == f {
					t.Errorf("%s was advertised with %s missing", f, drop)
				}
			}
			if err := Validate([]Feature{f}, partial); err == nil {
				t.Errorf("%s validated with %s missing", f, drop)
			} else {
				if !strings.Contains(err.Error(), f.String()) {
					t.Errorf("%s: the refusal does not name the feature: %v", f, err)
				}
				if !strings.Contains(err.Error(), drop.String()) {
					t.Errorf("%s: the refusal does not name %s: %v", f, drop, err)
				}
			}
		}
	}

	// And with everything attached, every feature is offered: otherwise the
	// test above would pass on a matrix that advertises nothing at all.
	if len(Advertised(full)) != len(Features()) {
		t.Errorf("a fully wired deployment offers %d of %d features",
			len(Advertised(full)), len(Features()))
	}
}

// A feature with no ports needs none, and is offered even on an empty wiring.
// Status answers from configuration alone.
func TestAPortlessFeatureIsAlwaysOffered(t *testing.T) {
	empty := Wiring{Present: map[Port]bool{}}

	found := false
	for _, f := range Advertised(empty) {
		if f == FeatureStatus {
			found = true
		}
	}
	if !found {
		t.Error("status was withheld from an unwired deployment")
	}
	if err := Validate([]Feature{FeatureStatus}, empty); err != nil {
		t.Errorf("status refused on an empty wiring: %v", err)
	}
}

// What each feature declares it needs, stated rather than derived from the
// table under test. The loop above drops each declared port and would pass
// unchanged on a matrix that declares too few, so the requirements themselves
// are written down here.
func TestTheDeclaredRequirements(t *testing.T) {
	want := map[Feature][]Port{
		FeatureStatus:           {},
		FeatureUser:             {PortAccount},
		FeatureSearch:           {PortFiles, PortSearch, PortFavorites},
		FeaturePreview:          {PortFiles, PortContentOrigin},
		FeatureAppPassword:      {PortAppPassword},
		FeatureUserGroupSharing: {PortFiles, PortSharing},
		FeaturePublicLinks:      {PortFiles, PortLinks},
		FeatureFiles:            {PortFiles},
		FeatureChunkedUpload:    {PortFiles, PortUploads},
		FeatureTrash:            {PortTrash},
		FeatureLoginFlow:        {PortLoginFlow, PortAppPassword},
	}

	if len(Features()) != len(want) {
		t.Errorf("the matrix holds %d features, this test knows %d", len(Features()), len(want))
	}

	for f, wantPorts := range want {
		got, ok := Requires(f)
		if !ok {
			t.Errorf("%s is not in the matrix", f)
			continue
		}

		gotSet := map[Port]bool{}
		for _, p := range got {
			gotSet[p] = true
		}
		for _, p := range wantPorts {
			if !gotSet[p] {
				t.Errorf("%s does not require %s", f, p)
			}
		}
		if len(got) != len(wantPorts) {
			t.Errorf("%s requires %v, want %v", f, got, wantPorts)
		}
	}
}

// Every problem is reported at once rather than one restart at a time.
func TestEveryUnwiredFeatureIsReportedTogether(t *testing.T) {
	empty := Wiring{Present: map[Port]bool{}}
	want := []Feature{FeatureUser, FeatureTrash, FeatureAppPassword}

	err := Validate(want, empty)
	if err == nil {
		t.Fatal("an empty wiring validated three port-needing features")
	}
	for _, f := range want {
		if !strings.Contains(err.Error(), f.String()) {
			t.Errorf("the refusal does not name %s: %v", f, err)
		}
	}
}

// A feature outside the matrix is refused rather than silently allowed. An
// unknown name with no requirements would otherwise validate vacuously.
func TestAFeatureOutsideTheMatrixIsRefused(t *testing.T) {
	full := wired(allPorts()...)

	err := Validate([]Feature{Feature(200)}, full)
	if !errors.Is(err, ErrMissingPort) {
		t.Errorf("an unknown feature validated: %v", err)
	}
	if !strings.Contains(err.Error(), "not in the matrix") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// Every feature and port has a usable name, since a refusal an operator cannot
// act on is a number.
func TestEveryFeatureAndPortIsNamed(t *testing.T) {
	for _, f := range Features() {
		name := f.String()
		if name == "" || name == "unset" || strings.HasPrefix(name, "Feature(") {
			t.Errorf("a feature has no usable name: %q", name)
		}
	}
	for _, p := range allPorts() {
		name := p.String()
		if name == "" || name == "unset" || strings.HasPrefix(name, "Port(") {
			t.Errorf("a port has no usable name: %q", name)
		}
	}
}

// The document is derived from the wiring, so a deployment cannot turn on a
// capability whose port is missing.
func TestCapabilitiesFollowTheWiring(t *testing.T) {
	cases := []struct {
		name    string
		wiring  Wiring
		present []string
		absent  []string
	}{
		{
			name:    "nothing wired",
			wiring:  Wiring{Present: map[Port]bool{}},
			present: []string{"core", "files"},
			absent:  []string{"files_sharing", "chunked_upload"},
		},
		{
			name:    "links only",
			wiring:  wired(PortFiles, PortLinks),
			present: []string{"files_sharing", "public"},
			absent:  []string{"group_sharing"},
		},
		{
			name:    "grants only",
			wiring:  wired(PortFiles, PortSharing),
			present: []string{"files_sharing", "group_sharing"},
			absent:  []string{"public"},
		},
		{
			name:    "uploads",
			wiring:  wired(PortFiles, PortUploads),
			present: []string{"chunked_upload"},
			absent:  []string{"files_sharing"},
		},
		{
			name:    "everything",
			wiring:  wired(allPorts()...),
			present: []string{"files_sharing", "public", "group_sharing", "chunked_upload"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := emit(t, Envelope{
				Version: V2, Status: StatusOKv2,
				Data: Capabilities(c.wiring, "1.0"),
			}, FormatJSON)

			for _, key := range c.present {
				if !strings.Contains(body, `"`+key+`"`) {
					t.Errorf("%s is missing: %s", key, body)
				}
			}
			for _, key := range c.absent {
				if strings.Contains(body, `"`+key+`"`) {
					t.Errorf("%s was advertised: %s", key, body)
				}
			}
		})
	}
}

// A block whose feature is off is omitted, not emitted empty. An empty sharing
// block tells a client sharing exists, and every attempt then fails.
func TestAnUnservedBlockIsOmittedNotEmpty(t *testing.T) {
	body := emit(t, Envelope{
		Version: V2, Status: StatusOKv2,
		Data: Capabilities(Wiring{Present: map[Port]bool{}}, "1.0"),
	}, FormatJSON)

	if strings.Contains(body, "files_sharing") {
		t.Errorf("an unwired deployment advertised sharing: %s", body)
	}
}

// A boolean capability tracks its own feature rather than being a constant.
// Advertising undelete without a trash port means a client offers restore and
// the request then fails.
func TestEachBooleanCapabilityTracksItsFeature(t *testing.T) {
	cases := []struct {
		key  string
		port Port
	}{
		{"undelete", PortTrash},
		{"bigfilechunking", PortUploads},
	}

	read := func(w Wiring, key string) bool {
		body := emit(t, Envelope{
			Version: V2, Status: StatusOKv2, Data: Capabilities(w, "1.0"),
		}, FormatJSON)

		var into struct {
			OCS struct {
				Data struct {
					Capabilities struct {
						Files map[string]any `json:"files"`
					} `json:"capabilities"`
				} `json:"data"`
			} `json:"ocs"`
		}
		if err := json.Unmarshal([]byte(body), &into); err != nil {
			t.Fatalf("the document does not parse: %v\n%s", err, body)
		}
		flag, ok := into.OCS.Data.Capabilities.Files[key].(bool)
		if !ok {
			t.Fatalf("%s is missing or not a boolean: %#v", key, into.OCS.Data.Capabilities.Files[key])
		}
		return flag
	}

	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			// With everything attached the key is true.
			if !read(wired(allPorts()...), c.key) {
				t.Errorf("%s is false on a fully wired deployment", c.key)
			}
			// With its own port removed it is false.
			without := wired(allPorts()...)
			delete(without.Present, c.port)
			if read(without, c.key) {
				t.Errorf("%s stayed true with %s missing", c.key, c.port)
			}
			// And on an empty wiring it is false.
			if read(Wiring{Present: map[Port]bool{}}, c.key) {
				t.Errorf("%s is true on an unwired deployment", c.key)
			}
		})
	}
}

// Resharing is false because grant chains are not offered. That is the truth
// rather than a default, so it is stated where sharing is served at all.
func TestResharingIsFalseWhereSharingIsServed(t *testing.T) {
	body := emit(t, Envelope{
		Version: V2, Status: StatusOKv2,
		Data: Capabilities(wired(allPorts()...), "1.0"),
	}, FormatJSON)

	if !strings.Contains(body, `"resharing":false`) {
		t.Errorf("resharing is not stated false: %s", body)
	}
}

// The document is real JSON with the shape a client reads.
func TestTheCapabilitiesDocumentParses(t *testing.T) {
	body := emit(t, Envelope{
		Version: V2, Status: StatusOKv2,
		Data: Capabilities(wired(allPorts()...), "9.9.9"),
	}, FormatJSON)

	var into struct {
		OCS struct {
			Data struct {
				Version struct {
					String string `json:"string"`
				} `json:"version"`
				Capabilities struct {
					Files struct {
						BigFileChunking bool `json:"bigfilechunking"`
						Undelete        bool `json:"undelete"`
					} `json:"files"`
					Sharing struct {
						Enabled   bool `json:"api_enabled"`
						Resharing bool `json:"resharing"`
					} `json:"files_sharing"`
				} `json:"capabilities"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal([]byte(body), &into); err != nil {
		t.Fatalf("the document does not parse: %v\n%s", err, body)
	}

	data := into.OCS.Data
	if data.Version.String != "9.9.9" {
		t.Errorf("version is %q", data.Version.String)
	}
	if !data.Capabilities.Files.BigFileChunking || !data.Capabilities.Files.Undelete {
		t.Errorf("a fully wired deployment withheld a file capability: %#v", data.Capabilities.Files)
	}
	if !data.Capabilities.Sharing.Enabled {
		t.Error("sharing is not enabled on a fully wired deployment")
	}
	if data.Capabilities.Sharing.Resharing {
		t.Error("resharing was advertised true")
	}
}

// The same wiring always produces the same document, so a client caching one
// does not see it change under it.
func TestTheCapabilitiesDocumentIsDeterministic(t *testing.T) {
	w := wired(allPorts()...)

	first := emit(t, Envelope{Version: V2, Status: StatusOKv2, Data: Capabilities(w, "1.0")}, FormatJSON)
	for i := 0; i < 30; i++ {
		if got := emit(t, Envelope{Version: V2, Status: StatusOKv2, Data: Capabilities(w, "1.0")}, FormatJSON); got != first {
			t.Fatalf("the same wiring produced two documents:\n%s\n%s", first, got)
		}
	}
}

// Whatever subset of ports is attached, the document parses and never
// advertises a feature the wiring cannot serve.
func FuzzCapabilities(f *testing.F) {
	f.Add(uint16(0))
	f.Add(uint16(0xFFFF))
	f.Add(uint16(0b101010))

	f.Fuzz(func(t *testing.T, mask uint16) {
		ports := allPorts()
		w := Wiring{Present: map[Port]bool{}}
		for i, p := range ports {
			if i < 16 && mask&(1<<uint(i)) != 0 {
				w.Present[p] = true
			}
		}

		body := emit(t, Envelope{
			Version: V2, Status: StatusOKv2, Data: Capabilities(w, "1.0"),
		}, FormatJSON)

		var into map[string]any
		if err := json.Unmarshal([]byte(body), &into); err != nil {
			t.Fatalf("mask %d produced JSON that does not parse: %v", mask, err)
		}

		// Whatever is advertised must validate against the same wiring.
		if err := Validate(Advertised(w), w); err != nil {
			t.Errorf("mask %d advertised something it cannot serve: %v", mask, err)
		}

		// And a sharing block only appears when a sharing feature is on.
		sharing := false
		for _, adv := range Advertised(w) {
			if adv == FeatureUserGroupSharing || adv == FeaturePublicLinks {
				sharing = true
			}
		}
		if got := strings.Contains(body, "files_sharing"); got != sharing {
			t.Errorf("mask %d: sharing block %v, want %v", mask, got, sharing)
		}
	})
}
