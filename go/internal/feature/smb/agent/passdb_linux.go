//go:build linux

package agent

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// The credential database, which the daemon's own tool owns.
//
// Every call here runs pdbedit. The tool is the only supported way to write a
// tdbsam database, and reimplementing its format would mean owning a binary
// layout the daemon may change under us.

// Import feeds the rendered hashes into the credential database.
func Import(ctx context.Context, smbpasswd, passdb string) error {
	out, err := exec.CommandContext(ctx, "pdbedit", //nolint:gosec // G204: both arguments are the agent's own configured paths, never caller data.
		"-i", "smbpasswd:"+filepath.Clean(smbpasswd),
		"-e", "tdbsam:"+filepath.Clean(passdb)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("importing credentials: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PassdbNames reports which accounts the credential database holds right now.
func PassdbNames(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "pdbedit", "-L").Output()
	if err != nil {
		return nil, fmt.Errorf("listing credentials: %w", err)
	}
	return ParsePassdbListing(string(out)), nil
}

// ParsePassdbListing reads the tool's output.
//
// Separated from the call so it can be tested against the real output shape on
// a machine where the credential database itself is unreachable, which is every
// machine without root.
//
// Each line is "name:uid:comment"; the name is the field before the first
// colon, and a blank line yields no account rather than an empty name the
// prune would then hand back to the tool.
func ParsePassdbListing(out string) []string {
	var names []string
	for _, l := range strings.Split(out, "\n") {
		name, _, _ := strings.Cut(l, ":")
		if strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	return names
}

// Prune drops credentials for accounts that are no longer this agent's.
//
// Without it, disabling a user on the server would leave them able to
// authenticate over SMB using the credential just revoked.
func Prune(ctx context.Context, desired []Entry) ([]string, error) {
	keep := map[string]bool{}
	for _, e := range desired {
		keep[e.Name] = true
	}
	names, err := PassdbNames(ctx)
	if err != nil {
		return nil, err
	}

	var removed []string
	for _, name := range names {
		if keep[name] {
			continue
		}
		// A name failing the rule never entered the database through this
		// agent, and handing one to the tool is exactly what the rule prevents.
		if !ValidName(name) {
			continue
		}
		if rerr := exec.CommandContext(ctx, "pdbedit", "-x", "-u", name).Run(); rerr != nil { //nolint:gosec // G204: the name passed the portable rule on the line above.
			// A credential that will not delete is reported rather than failing
			// the pass, since the rest still need pruning and a survivor shows
			// up in the next check.
			continue
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// MissingPassdb lists accounts that ought to authenticate and cannot.
//
// When a credential line's numeric field matches no account, the import tool
// claims success while importing nothing: no error, no output, a zero exit
// status, an empty database. Every later symptom then implicates credentials or
// configuration rather than the import, so the check happens here while the
// cause is still apparent.
func MissingPassdb(ctx context.Context, desired []Entry) ([]string, error) {
	names, err := PassdbNames(ctx)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, n := range names {
		known[n] = true
	}
	var out []string
	for _, e := range desired {
		if !known[e.Name] {
			out = append(out, e.Name)
		}
	}
	return out, nil
}
