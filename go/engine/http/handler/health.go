// Linux only, for the same reason as the rest of this package.
//go:build linux

// The health projection.
//
// Unauthenticated, so what it says is what a stranger learns about a
// deployment. The whole design is the restriction: a fixed vocabulary of
// reason tokens and nothing else. No path, no address, no account name and no
// error text, because each of those is a fact about this installation that a
// caller with no credential has not earned.
package handler

import (
	"sort"
	"strings"
)

// HealthStatus is the one-word summary.
type HealthStatus string

const (
	// HealthOK is a deployment with nothing to report.
	HealthOK HealthStatus = "ok"

	// HealthDegraded is serving, with something reduced.
	HealthDegraded HealthStatus = "degraded"

	// HealthFailing is not serving its purpose.
	HealthFailing HealthStatus = "failing"
)

// HealthReason is one fixed token. The type exists so a caller cannot pass a
// formatted string where a token belongs, which is how error text reaches an
// unauthenticated response.
type HealthReason string

// The complete vocabulary. A reason not in this list does not reach a client:
// the projection drops it rather than passing an unknown value through, since
// an unknown value is the shape an accidental interpolation would take.
const (
	ReasonStateDatabase   HealthReason = "state_database"
	ReasonCacheDatabase   HealthReason = "cache_database"
	ReasonJournalDatabase HealthReason = "journal_database"
	ReasonShareUnservable HealthReason = "share_unservable"
	ReasonIndexStale      HealthReason = "index_stale"
	ReasonPreviewWorkers  HealthReason = "preview_workers"
	ReasonSMBAgent        HealthReason = "smb_agent"
	ReasonStorageFull     HealthReason = "storage_full"
	ReasonSetupIncomplete HealthReason = "setup_incomplete"
)

// knownReasons is the vocabulary as a set, for the drop rule.
func knownReasons() map[HealthReason]bool {
	return map[HealthReason]bool{
		ReasonStateDatabase:   true,
		ReasonCacheDatabase:   true,
		ReasonJournalDatabase: true,
		ReasonShareUnservable: true,
		ReasonIndexStale:      true,
		ReasonPreviewWorkers:  true,
		ReasonSMBAgent:        true,
		ReasonStorageFull:     true,
		ReasonSetupIncomplete: true,
	}
}

// Health is the entire response body.
type Health struct {
	Status  HealthStatus   `json:"status"`
	Reasons []HealthReason `json:"reasons"`
}

// HealthOf builds the projection from what the services reported.
//
// Sorted and deduplicated, so the same deployment state produces the same
// bytes: an unstable order is a difference a caller could read as a change,
// and a repeated token says how many subsystems hit one condition, which is
// more than the token itself says.
func HealthOf(status HealthStatus, reasons []HealthReason) Health {
	known := knownReasons()
	seen := make(map[HealthReason]bool, len(reasons))
	out := make([]HealthReason, 0, len(reasons))

	for _, r := range reasons {
		if !known[r] || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })

	if status != HealthOK && status != HealthDegraded && status != HealthFailing {
		// An unrecognised status is reported as failing rather than passed
		// through. Whatever produced it is not something to describe to an
		// unauthenticated caller, and "ok" would be the wrong guess.
		status = HealthFailing
	}
	return Health{Status: status, Reasons: out}
}

// SafeHealthToken reports whether a string is one of the fixed tokens.
//
// The check a caller applies at the seam where a service reports a reason. It
// is deliberately not a validity check on the shape of a token: a value that
// merely looks like one is exactly what an interpolated error string would be.
func SafeHealthToken(s string) bool {
	return knownReasons()[HealthReason(strings.TrimSpace(s))]
}
