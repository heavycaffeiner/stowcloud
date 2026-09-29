package catalogue

import (
	"slices"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/admin/settings/runtimecfg"
)

// Every key resolves to a section, which is what the patch route writes under.
// A key resolving nowhere would save into nothing and report success.
func TestEveryKeyResolvesToASection(t *testing.T) {
	for _, f := range Of(runtimecfg.Defaults(), map[string]any{}).Fields {
		section, name, ok := splitKey(f.Key)
		if !ok {
			// The unprefixed network fields, which the lookup files under the
			// network section.
			section, name = networkSection, f.Key
		}
		if section == "" || name == "" {
			t.Errorf("%q splits into %q and %q", f.Key, section, name)
		}
	}
}

// Numeric fields carry the bounds the checker enforces, so the form refuses
// what the save would refuse rather than sending it and reporting an error.
func TestNumericFieldsCarryTheBoundsTheCheckerUses(t *testing.T) {
	fields := byKey(Of(runtimecfg.Defaults(), map[string]any{}).Fields)

	for key, bound := range runtimecfg.Bounds() {
		f, ok := fields[key]
		if !ok {
			t.Errorf("%q is bounded and not described", key)
			continue
		}
		if f.Range.Min == nil || f.Range.Max == nil {
			t.Errorf("%q carries no range, so the form cannot validate it", key)
			continue
		}
		if *f.Range.Min != bound.Min || *f.Range.Max != bound.Max {
			t.Errorf("%q is described as [%d,%d] and enforced as [%d,%d]",
				key, *f.Range.Min, *f.Range.Max, bound.Min, bound.Max)
		}
	}
}

// Every numeric field's present value sits inside its own bound.
//
// A form that opens on a value its own validator rejects is one an operator
// cannot save without first changing a field they never meant to touch. It
// also means the displayed number is not what the server is running on: the
// watcher substitutes its own default for an unset threshold, and reporting
// the zero would say the server rescans everything on the first change.
func TestEveryNumericValueIsInsideItsOwnBound(t *testing.T) {
	for _, f := range Of(runtimecfg.Defaults(), map[string]any{}).Fields {
		if f.Range.Kind != KindInt || f.Range.Min == nil || f.Range.Max == nil {
			continue
		}
		v, ok := f.Value.(int64)
		if !ok {
			t.Errorf("%q holds %T, not an integer", f.Key, f.Value)
			continue
		}
		if v < *f.Range.Min || v > *f.Range.Max {
			t.Errorf("%q opens on %d, outside its own bound [%d,%d]",
				f.Key, v, *f.Range.Min, *f.Range.Max)
		}
	}
}

// A list field is never null, so the screen draws an empty control rather than
// testing the field first.
func TestListFieldsAreEmptyRatherThanNull(t *testing.T) {
	for _, f := range Of(runtimecfg.Defaults(), map[string]any{}).Fields {
		if f.Range.Kind != KindStrings {
			continue
		}
		v, ok := f.Value.([]string)
		if !ok {
			t.Errorf("%q holds %T, not a list", f.Key, f.Value)
			continue
		}
		if v == nil {
			t.Errorf("%q is null rather than empty", f.Key)
		}
	}
}

// A choice field's present value is one of the choices it offers. A form whose
// select has no matching option shows a blank where a real setting is.
func TestAChoiceFieldsValueIsOneOfItsChoices(t *testing.T) {
	for _, f := range Of(runtimecfg.Defaults(), map[string]any{}).Fields {
		if f.Range.Kind != KindChoice {
			continue
		}
		v, ok := f.Value.(string)
		if !ok {
			t.Errorf("%q holds %T, not a string", f.Key, f.Value)
			continue
		}
		if !slices.Contains(f.Range.Choices, v) {
			t.Errorf("%q is %q, which is not among %v", f.Key, v, f.Range.Choices)
		}
	}
}

// The fields a change cannot reach without a restart say so.
//
// Asserted on the ones whose holders are built at startup, because a screen
// that reported them as live would tell an operator a change had taken effect
// when the running server was still on the old value.
func TestStartupOnlyFieldsAreMarkedRestartRequired(t *testing.T) {
	fields := byKey(Of(runtimecfg.Defaults(), map[string]any{}).Fields)

	for _, key := range []string{
		// Landlock and seccomp have no syscall that undoes an installed
		// ruleset, so a weaker policy chosen after the fact cannot be
		// applied by the process already running under the old one.
		"security.hardening",
	} {
		f, ok := fields[key]
		if !ok {
			t.Errorf("%q is not described", key)
			continue
		}
		if !f.RestartRequired {
			t.Errorf("%q is read at startup and is not marked as needing a restart", key)
		}
	}

	// And the ones that really are live are not marked, so the screen does not
	// ask for a restart nothing needs. smb.enabled is here because a settings
	// save publishes to the sidecar; homes.* because a save registers or
	// withdraws the homes share, which the core accepts at any time.
	// smb.agent_socket and smb.config_dir are here because a save publishes
	// to the sidecar regardless of this flag, and the publisher re-reads the
	// document fresh rather than holding a startup snapshot. watch.hot_set_max
	// and watch.full_threshold are here because the watcher exposes a live
	// setter for both bounds. search.name_index_enabled is here because a
	// save now attaches or detaches the index and its updater directly.
	for _, key := range []string{
		"rate.per_sec", "app_hosts", "smb.totp_policy", "smb.enabled",
		"homes.enabled", "homes.root", "db.size_guard", "bind",
		"smb.agent_socket", "smb.config_dir",
		"watch.hot_set_max", "watch.full_threshold",
		"search.name_index_enabled",
	} {
		if f, ok := fields[key]; ok && f.RestartRequired {
			t.Errorf("%q applies live and is marked as needing a restart", key)
		}
	}
}

// A patch is judged by the fields it names, not by its section.
//
// Sections are mixed: oidc rebuilds its provider when settings load, and
// smb.totp_policy reaches the auth service directly, while their neighbours
// are assembled at startup. Judging by section reported both as needing a
// restart and skipped the reload, so the change sat stored and inert until
// the container went down. That is the defect an operator sees as "some
// settings only apply after a restart".
func TestARestartIsJudgedByTheFieldsAPatchNames(t *testing.T) {
	cases := []struct {
		section string
		body    map[string]any
		want    bool
		why     string
	}{
		{"oidc", map[string]any{"issuer": "https://id.example"}, false,
			"the provider is rebuilt when settings load"},
		{"smb", map[string]any{"totp_policy": "block"}, false,
			"the policy reaches the auth service directly"},
		{"smb", map[string]any{"enabled": true}, false,
			"a settings save publishes, and the agent starts or stops the daemon"},
		{"smb", map[string]any{"config_dir": "/etc/samba"}, false,
			"a save publishes regardless of this flag, and the publisher re-reads the document fresh"},
		{"smb", map[string]any{"enabled": true, "config_dir": "/etc/samba"}, false,
			"both fields in the patch apply live"},
		{"rate", map[string]any{"per_sec": 25}, false,
			"the limiter is updated in place"},
		{"network", map[string]any{"app_hosts": []string{"x"}}, false,
			"the host lists are read per request"},
		{"network", map[string]any{"bind": ":8443"}, false,
			"a save binds the new address and drains the old one"},
		{"symlink-policy", map[string]any{"policy": "deny"}, false,
			"read from the share row, not the settings document"},
		{"smb", map[string]any{"totp_policy": "block", "force": true}, false,
			"force is a request flag, not a stored setting"},
		{"smb", map[string]any{"invented_field": 1}, true,
			"an unknown field cannot be shown to apply live"},
	}

	for _, c := range cases {
		if got := RestartRequiredFor(c.section, c.body); got != c.want {
			t.Errorf("%s %v needs a restart: %v, want %v (%s)",
				c.section, c.body, got, c.want, c.why)
		}
	}
}

// No key is described twice. A duplicate renders two controls for one setting,
// and whichever is saved second wins.
func TestNoKeyIsDescribedTwice(t *testing.T) {
	seen := map[string]int{}
	for _, f := range Of(runtimecfg.Defaults(), map[string]any{}).Fields {
		seen[f.Key]++
	}
	for key, times := range seen {
		if times > 1 {
			t.Errorf("%q is described %d times", key, times)
		}
	}
}

// An empty-means key is a catalogue key rather than a sentence, so the screen
// can translate it.
func TestEmptyMeansKeysAreCatalogueKeys(t *testing.T) {
	for _, f := range Of(runtimecfg.Defaults(), map[string]any{}).Fields {
		if f.EmptyMeansKey == "" {
			continue
		}
		if !strings.HasPrefix(f.EmptyMeansKey, "settings.") {
			t.Errorf("%q carries %q, which is not a catalogue key",
				f.Key, f.EmptyMeansKey)
		}
	}
}

func byKey(fields []Field) map[string]Field {
	out := make(map[string]Field, len(fields))
	for _, f := range fields {
		out[f.Key] = f
	}
	return out
}
