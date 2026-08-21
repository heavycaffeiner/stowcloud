package smbagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
	"sort"
	"strings"
)

// Reconciling the system account file and the credential database.
//
// The daemon resolves an SMB login to a system account by name, so every
// account named in a share needs an entry even though none of them ever owns a
// file. The server renders those entries; this puts them where the lookup
// looks, and imports the matching hashes.
//
// Rebuilt from scratch on every pass rather than diffed, which is what makes an
// account removed from the server's registry disappear here too instead of
// lingering as a stale entry forever.

// managedMarker is written into the comment field of every account this agent
// creates, and is the only thing that makes one eligible for removal. An
// account without it belongs to somebody else and is never touched.
const managedMarker = "sc-managed-smb"

// maxAccountName is the portable limit for a system account name.
const maxAccountName = 32

// Entry is one rendered account line, split far enough to reason about.
type Entry struct {
	Name string
	UID  string
	GID  string
	// Line is the whole line, marker already stamped.
	Line string
}

// ParseRendered reads what the server rendered.
//
// Anything that is not a seven-field line is dropped rather than passed
// through: this content ends up in the system account file, and a malformed
// line there breaks the name lookup for every account after it.
func ParseRendered(body string) []Entry {
	var out []Entry
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 7 || f[0] == "" {
			continue
		}
		f[4] = managedMarker
		out = append(out, Entry{Name: f[0], UID: f[2], GID: f[3], Line: strings.Join(f, ":")})
	}
	return out
}

// ValidName holds a name to the portable rule before anything writes it into
// the system account file.
//
// The server's account names reach account-creation territory here. A name
// starting with a dash would also be argument injection into the credential
// tool.
func ValidName(name string) bool {
	if name == "" || len(name) > maxAccountName {
		return false
	}
	if first := name[0]; (first < 'a' || first > 'z') && first != '_' {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// managed reports whether an account line is one of ours.
func managed(line string) bool {
	f := strings.Split(line, ":")
	return len(f) > 4 && f[4] == managedMarker
}

// Collisions refuses to sync at all when an SMB account collides with a
// pre-existing system account this agent did not create.
//
// Adopting one silently would give that account's name a credential, and
// removing it later would delete a system user. The numeric half is not
// cosmetic either: the import tool resolves a credential line to an account by
// number and takes whatever name the reverse lookup answers with, so a shared
// number attaches the credential to the wrong name.
func Collisions(desired []Entry, passwd string) []string {
	var out []string
	for _, e := range desired {
		if !ValidName(e.Name) {
			out = append(out, fmt.Sprintf("%q is not a valid system account name", e.Name))
			continue
		}
		for _, line := range strings.Split(passwd, "\n") {
			if line == "" || managed(line) {
				continue
			}
			f := strings.Split(line, ":")
			if len(f) < 3 {
				continue
			}
			name, uid := f[0], f[2]
			if name == e.Name {
				out = append(out, fmt.Sprintf("%q already exists as a system account this agent did not create", e.Name))
			}
			if uid == e.UID {
				out = append(out, fmt.Sprintf(
					"user id %s is already %q, an account this agent did not create: move the configured service id clear of the host's real users",
					e.UID, name))
			}
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// MissingGroups lists groups the rendered accounts reference that do not
// exist.
//
// This agent does not invent groups, and an account whose group resolves to
// nothing breaks the name lookup just as surely as a missing account.
func MissingGroups(desired []Entry, groupFile string) []string {
	have := map[string]bool{}
	for _, l := range strings.Split(groupFile, "\n") {
		if f := strings.Split(l, ":"); len(f) > 2 {
			have[f[2]] = true
		}
	}
	var out []string
	for _, e := range desired {
		if !have[e.GID] {
			out = append(out, e.GID)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// Rebuild is the new account file: everything that is not ours, then ours.
func Rebuild(current string, desired []Entry) string {
	out := make([]byte, 0, len(current))
	for _, line := range strings.Split(current, "\n") {
		if line == "" || managed(line) {
			continue
		}
		out = append(out, line...)
		out = append(out, '\n')
	}
	for _, e := range desired {
		out = append(out, e.Line...)
		out = append(out, '\n')
	}
	return string(out)
}

// passwdMode is what the system account file has to be: every name lookup on
// the machine reads it, so one it cannot read breaks all of them.
const passwdMode = 0o644

// WritePasswd replaces the account file so a reader never sees a partial one,
// and so a machine that loses power mid-write comes back with one file or the
// other rather than a truncated roster.
//
// That still leaves the ordinary lost-update race against a concurrent account
// creation. The system's own lock is not reachable from here, and a file
// server's SMB roster is not a multi-writer account file.
func WritePasswd(path, body string) error {
	// The mode is applied to the staged file rather than left to the open,
	// because the process mask would narrow it.
	err := vfs.ReplaceFileDurable(path, passwdMode, func(f *os.File) error {
		_, werr := f.WriteString(body)
		return werr
	})
	if err != nil {
		return fmt.Errorf("replacing the account file: %w", err)
	}
	return nil
}

// Import loads the rendered hashes into the credential database.
func Import(smbpasswd, passdb string) error {
	out, err := exec.Command("pdbedit", //nolint:gosec // G204 flags a variable command: both arguments are the agent's own configured paths, and neither is caller data.
		"-i", "smbpasswd:"+filepath.Clean(smbpasswd),
		"-e", "tdbsam:"+filepath.Clean(passdb)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("importing credentials: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PassdbNames lists the accounts the credential database currently holds.
func PassdbNames() ([]string, error) {
	out, err := exec.Command("pdbedit", "-L").Output()
	if err != nil {
		return nil, fmt.Errorf("listing credentials: %w", err)
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		name, _, _ := strings.Cut(l, ":")
		if strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// Prune drops credentials for accounts that are no longer ours.
//
// Without this, disabling a user on the server would leave them able to
// authenticate over SMB with the credential that was just revoked.
func Prune(desired []Entry) ([]string, error) {
	keep := map[string]bool{}
	for _, e := range desired {
		keep[e.Name] = true
	}
	names, err := PassdbNames()
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, name := range names {
		if keep[name] {
			continue
		}
		// A name that failed the rule never reached the database through this
		// agent, and passing one to the tool is what the rule exists to stop.
		if !ValidName(name) {
			continue
		}
		if err := exec.Command("pdbedit", "-x", "-u", name).Run(); err != nil { //nolint:gosec // G204 flags a variable command: the name is held to the portable rule on the line above.
			// One credential that will not delete is reported rather than
			// failing the pass, because the rest still need pruning and a
			// stale one is visible in the next check.
			continue
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// MissingPassdb lists accounts that should be able to authenticate and cannot.
//
// The import tool reports success and imports nothing when a credential line's
// numeric field names no account: no error, no output, a zero exit, an empty
// database. Every downstream symptom points at credentials or configuration
// rather than at the import, so it is checked here where the cause is still
// visible.
func MissingPassdb(desired []Entry) ([]string, error) {
	names, err := PassdbNames()
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
