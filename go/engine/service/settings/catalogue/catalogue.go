// Every setting an operator can change, described well enough to draw a form.
//
// The settings screen cannot render a field it knows nothing about: it needs
// the current value, what the field will accept, where the value came from,
// and whether changing it takes effect now or at the next start. A client that
// compiled its own copy of any of that would offer values the server refuses,
// so this is the one description and the screen reads it.
//
// The list here is exactly what runtimecfg.Load reads. A field it loads and
// this omits is one an operator cannot see; a field here that Load ignores is
// a control that stores a value nothing acts on. Both failures are silent from
// the screen's side, which is why they are declared together.
package catalogue

import (
	"math"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
)

// Source says where a field's present value came from.
//
// The screen shows it because "unset" and "set to the default" look identical
// in a form and are not the same fact: one changes when the default does.
type Source string

const (
	// SourceDefault is the compiled-in value, with nothing stored over it.
	SourceDefault Source = "builtin_default"
	// SourceStored is a value an administrator saved.
	SourceStored Source = "admin_override"
)

// Kind is how a field is edited, which decides the control the screen draws.
type Kind string

const (
	KindBool   Kind = "bool"
	KindInt    Kind = "int"
	KindString Kind = "string"
	// KindStrings is a list of strings, which the screen edits as one control
	// per entry rather than a free-text field.
	KindStrings Kind = "string_list"
	// KindChoice is a string with a fixed set of accepted values. It is a
	// string to a client that does not know the kind, which degrades to a text
	// field rather than to nothing.
	KindChoice Kind = "choice"
)

// Range is what a field accepts, where the answer is not simply its kind.
type Range struct {
	Kind Kind `json:"kind"`

	// Min and Max bound an int field. Both zero on every other kind.
	Min *int64 `json:"min,omitempty"`
	Max *int64 `json:"max,omitempty"`

	// Choices lists what a choice field accepts.
	Choices []string `json:"choices,omitempty"`
}

// Field is one setting, as the screen needs it.
type Field struct {
	// Key is the stored path, section and name joined by a dot. The same
	// string the section patch route writes under, so a screen never has to
	// translate between what it displays and what it saves.
	Key string `json:"key"`

	Value any   `json:"value"`
	Range Range `json:"range"`

	Source Source `json:"source"`

	// RestartRequired is a property of the field rather than a claim about
	// this value: it says a change here reaches the running server only at the
	// next start. A screen that could not say so would report a save as taking
	// effect when it had not.
	RestartRequired bool `json:"restart_required"`

	// EmptyMeansKey names what leaving the field empty does, for the fields
	// where empty is itself a setting rather than a gap. Absent elsewhere,
	// where empty simply means unset.
	EmptyMeansKey string `json:"empty_means_key,omitempty"`
}

// Snapshot is the whole surface, in the order a screen shows it.
type Snapshot struct {
	Fields []Field `json:"fields"`
}

// Of describes every settable field against the values now in force.
//
// stored is the document as saved, which decides each field's source. values
// is what the server actually resolved, which is what the screen displays: a
// stored value outside its bound is clamped at load, and showing the raw
// stored number would tell an operator the server is running on something it
// is not.
func Of(values runtimecfg.Values, stored map[string]any) Snapshot {
	bounds := runtimecfg.Bounds()
	src := sourceLookup(stored)

	intField := func(key string, value int64, restart bool) Field {
		f := Field{
			Key: key, Value: value, Source: src(key),
			Range: Range{Kind: KindInt}, RestartRequired: restart,
		}
		if b, ok := bounds[key]; ok {
			minV, maxV := b.Min, b.Max
			f.Range.Min, f.Range.Max = &minV, &maxV
		}
		return f
	}
	str := func(key, value string, restart bool, emptyKey string) Field {
		return Field{
			Key: key, Value: value, Source: src(key),
			Range: Range{Kind: KindString}, RestartRequired: restart,
			EmptyMeansKey: emptyKey,
		}
	}
	list := func(key string, value []string, restart bool, emptyKey string) Field {
		// Never nil: the screen renders a list control and a null would make
		// it test the field before drawing an empty one.
		if value == nil {
			value = []string{}
		}
		return Field{
			Key: key, Value: value, Source: src(key),
			Range: Range{Kind: KindStrings}, RestartRequired: restart,
			EmptyMeansKey: emptyKey,
		}
	}
	boolean := func(key string, value bool, restart bool) Field {
		return Field{
			Key: key, Value: value, Source: src(key),
			Range: Range{Kind: KindBool}, RestartRequired: restart,
		}
	}
	choice := func(key, value string, choices []string, restart bool) Field {
		return Field{
			Key: key, Value: value, Source: src(key),
			Range: Range{Kind: KindChoice, Choices: choices}, RestartRequired: restart,
		}
	}

	fields := []Field{
		// Search and archive reach the running service through its own setter,
		// so a change applies without a restart.
		intField("search.max_concurrent_fast", int64(values.SearchConcurrentSSD), false),
		intField("search.max_concurrent_slow", int64(values.SearchConcurrentRot), false),
		intField("search.walk_deadline_fast_ms", values.SearchDeadlineSSD.Milliseconds(), false),
		intField("search.walk_deadline_slow_ms", values.SearchDeadlineRot.Milliseconds(), false),
		intField("archive.max_concurrent", int64(values.ArchiveMaxConcurrent), false),

		// The name index is opened at startup, so switching it on takes a
		// restart before a build has anywhere to go. It is not part of the
		// resolved values, being read straight from the document by whoever
		// opens the index, so its own present state is passed in.
		boolean("search.name_index_enabled", indexEnabled(stored), true),

		// The limiter holds a bucket per client and is updated in place.
		intField("rate.per_sec", int64(values.RatePerSec), false),
		intField("rate.burst", int64(values.RateBurst), false),

		// The watcher reads both when it starts, so a change waits for one.
		//
		// Unset resolves to the watcher's own default rather than being shown
		// as zero. Zero is not a threshold the watcher uses: it substitutes its
		// default, so displaying the raw value would tell an operator the
		// server rescans everything on the first change, which it does not.
		intField("watch.hot_set_max",
			positiveOr(int64(values.WatchHotSetMax), defaultWatchHotSet), true),
		intField("watch.full_threshold",
			positiveOr(int64(values.WatchFullThreshold), defaultWatchFullThreshold), true),

		// The host lists and the proxy ranges are read per request, so they
		// take effect at once. The bind address and the canonical URL are
		// consulted while the listener is built.
		// Three of these carry no section prefix. The stored document keeps
		// them under `network`, and the screen has always addressed them by the
		// bare name: the key is what a client matches on, so it is the name the
		// screen knows rather than the path the document uses.
		list("app_hosts", values.AppHosts, false,
			"settings.empty_app_hosts_first_boot"),
		list("content_hosts", values.ContentHosts, false, ""),
		list("allowed_origins", values.AllowedOrigins, false, ""),
		list("trusted_proxies", values.TrustedProxy, false,
			"settings.empty_trusted_proxies"),
		str("bind", values.Listen, true, ""),
		str("compat_canonical_url", values.CompatCanonicalURL, false, ""),

		// The homes share is registered once, at startup.
		boolean("homes.enabled", values.HomesEnabled, true),
		str("homes.root", values.HomesRoot, true, ""),

		// The sandbox is applied before anything serves.
		choice("security.hardening", values.Hardening.String(),
			[]string{"required", "preferred", "off"}, true),

		// The size guard's switch is what applies its numbers, so the numbers
		// can be set before it is turned on.
		boolean("db.size_guard", values.DBGuard.Enabled(), true),
		intField("db.max_bytes", byteCount(values.DBGuard.MaxBytes), true),
		intField("db.min_free_bytes", byteCount(values.DBGuard.MinFreeBytes), true),

		// A settings save publishes to the sidecar, which renders these and
		// acts on the switch: on starts the daemon, off stops it and prunes
		// the credentials. So they apply without a restart.
		boolean("smb.enabled", values.SMB.Enabled, false),
		str("smb.server_name", values.SMB.ServerName, false, ""),
		str("smb.workgroup", values.SMB.Workgroup, false, ""),
		boolean("smb.allow_public_bind", values.SMB.AllowPublicBind, false),
		str("smb.service_user", values.SMB.ServiceUser, false, ""),
		intField("smb.service_gid", int64(values.SMBServiceGID), false),
		// These two decide whether there is a sidecar to talk to at all, which
		// is read once when the publisher is built.
		str("smb.config_dir", values.SMBConfigDir, true, ""),
		str("smb.agent_socket", values.SMBSocket, true, ""),
		// The policy reaches the auth service directly. Two values, and an
		// unrecognised one is read as blocking: the names are the contract
		// rather than a suggestion, so the screen offers exactly them.
		choice("smb.totp_policy", values.SMBTOTPPolicy,
			[]string{runtimecfg.DefaultSMBTOTPPolicy, "block"}, false),

		// The provider is rebuilt when settings load, so these apply without a
		// restart. The switch is what makes the rest take effect: the loader
		// reads nothing else in this section while it is off.
		boolean("oidc.enabled", values.OIDC != nil, false),
		str("oidc.issuer", oidcString(values, func(o *runtimecfg.OIDC) string { return o.Issuer }), false, ""),
		str("oidc.client_id", oidcString(values, func(o *runtimecfg.OIDC) string { return o.ClientID }), false, ""),
		str("oidc.display_name", values.OIDCDisplayName, false, ""),
		str("oidc.ca_cert_file", oidcString(values, func(o *runtimecfg.OIDC) string { return o.CACertFile }), false, ""),
		boolean("oidc.allow_private_endpoints",
			values.OIDC != nil && values.OIDC.AllowPrivateEndpoints, false),
	}
	return Snapshot{Fields: fields}
}

// RestartRequiredFor reports whether a patch needs a restart to take effect.
//
// Per field rather than per section, because the two disagree: every oidc
// field is rebuilt when settings load, and smb.totp_policy reaches the auth
// service directly, while their neighbours in the same sections are assembled
// once at startup. Judging by section reported both as needing a restart, so
// a provider change sat stored and inert until somebody restarted the
// container.
//
// A field this build does not know is a restart: an unknown key cannot be
// shown to apply live, and the safe direction is telling an operator to
// restart for a change that was already live rather than reporting a dead
// change as in effect.
func RestartRequiredFor(section string, body map[string]any) bool {
	// Read somewhere other than the settings document, so reloading it would
	// apply nothing and a restart would change nothing either. symlink-policy
	// is per share and read from the share row; paths is builtin and
	// read-only. Both take effect as soon as they are written.
	if section == "symlink-policy" || section == "paths" {
		return false
	}

	known := restartByKey()
	for name := range body {
		if name == "force" {
			// A confirmation flag the handler consumes, not a stored setting.
			continue
		}
		key := section + "." + name
		if section == networkSection {
			// The network fields are stored under their bare names, which is
			// also how the catalogue spells them.
			key = name
		}
		restart, found := known[key]
		if !found || restart {
			return true
		}
	}
	return false
}

// restartByKey is every field's restart property, keyed as the catalogue
// spells it.
//
// Built from the catalogue itself so the two cannot drift: a field added
// above is judged by the flag written beside it rather than by a second list
// somebody has to remember to update.
func restartByKey() map[string]bool {
	fields := Of(runtimecfg.Defaults(), map[string]any{}).Fields
	out := make(map[string]bool, len(fields))
	for _, f := range fields {
		out[f.Key] = f.RestartRequired
	}
	return out
}

// indexEnabled reads the name index switch out of the stored document.
//
// Read here rather than taken from the resolved values because nothing
// resolves it: the index is opened straight from this setting at startup, so
// the document is where its state lives. A value of the wrong shape is off,
// the same answer as absent.
func indexEnabled(stored map[string]any) bool {
	section, ok := stored["search"].(map[string]any)
	if !ok {
		return false
	}
	enabled, ok := section["name_index_enabled"].(bool)
	return ok && enabled
}

// oidcString reads one provider field, answering empty where no provider is
// configured. A nil provider is the ordinary case, not a failure.
func oidcString(values runtimecfg.Values, get func(*runtimecfg.OIDC) string) string {
	if values.OIDC == nil {
		return ""
	}
	return get(values.OIDC)
}

// The watcher's own fallbacks, for the two fields it substitutes when they
// arrive unset. Named here so the screen reports what the watcher will use
// rather than the zero that stands for "nothing chosen".
const (
	defaultWatchHotSet        = 4096
	defaultWatchFullThreshold = 50_000
)

// byteCount narrows a stored size for display. Saturating rather than
// wrapping: a size past the signed range is not one any disk holds, and a
// negative one would render as a nonsensical limit an operator cannot correct
// without knowing what it wrapped from.
func byteCount(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// positiveOr answers the fallback for a value the reader treats as unset.
func positiveOr(v, fallback int64) int64 {
	if v <= 0 {
		return fallback
	}
	return v
}

// networkSection is where the unprefixed keys live in the stored document.
// The screen knows them by their bare names; the document files them here.
const networkSection = "network"

// sourceLookup reports whether a key was saved, by looking it up in the stored
// document rather than comparing against the default.
//
// Comparing values would call a stored value that happens to equal the default
// unset, which is wrong in the one direction that matters: the operator set it
// deliberately, and it must not silently follow the default when that changes.
func sourceLookup(stored map[string]any) func(string) Source {
	return func(key string) Source {
		section, name, ok := splitKey(key)
		if !ok {
			// An unprefixed key is a network field, stored under that section.
			section, name = networkSection, key
		}
		sec, ok := stored[section].(map[string]any)
		if !ok {
			return SourceDefault
		}
		if _, present := sec[name]; present {
			return SourceStored
		}
		return SourceDefault
	}
}

// splitKey cuts a key at its first dot. A key without one names no section and
// belongs to nothing, which is a programming error rather than an input.
func splitKey(key string) (section, name string, ok bool) {
	for i := range len(key) {
		if key[i] == '.' {
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}
