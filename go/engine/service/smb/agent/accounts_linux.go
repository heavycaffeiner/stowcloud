//go:build linux

package agent

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/fsatomic"
)

// Bringing the system account file and the credential database into line.
//
// An SMB login is resolved to a system account by name, so every account named
// in a share requires an entry even though none of them will ever own a file.
// The server produces those entries; this puts them where the lookup expects
// them and imports the corresponding hashes.
//
// Every pass rebuilds rather than diffs, which is what makes an account removed
// from the server's registry vanish here too instead of persisting as a stale
// entry indefinitely.

// managedMarker is stamped into the comment field of every account this agent
// creates, and is the sole thing making one eligible for removal. An account
// without it belongs to somebody else and is never touched.
const managedMarker = "sc-managed-smb"

// MaxAccountName is the portable bound for a system account name.
//
// It matches auth's own limit deliberately. That package gates creation, while
// this one is the last check before a name enters the system account file, and
// a parity test holds the two together.
const MaxAccountName = 32

// Entry holds a rendered account line, divided far enough to reason about.
type Entry struct {
	Name string
	UID  string
	GID  string
	// Line is the complete line with the marker already stamped.
	Line string
}

// ParseRendered interprets the account lines the server produced.
//
// Anything other than a seven-field line is discarded rather than passed
// through. This content reaches the system account file, where one malformed
// line breaks the name lookup for every account following it.
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

// ValidName applies the portable rule before a name can be written into the
// system account file.
//
// The server's account names arrive in account-creation territory here. A name
// beginning with a dash would additionally be argument injection into the
// credential tool.
//
// This duplicates auth's creation-time rule on purpose. The agent cannot assume
// every server predates that rule, so it keeps its own copy as the last line of
// defence, and a parity test proves the two agree on every vector.
func ValidName(name string) bool {
	if name == "" || len(name) > MaxAccountName {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !nameByteOK(name[i], i == 0) {
			return false
		}
	}
	return true
}

// nameByteOK is the character rule: a lower-case letter or underscore first,
// then those plus digits and a hyphen. A leading hyphen is what the account
// tools would read as an option rather than a name.
func nameByteOK(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c == '_':
		return true
	case first:
		return false
	case c >= '0' && c <= '9':
		return true
	case c == '-':
		return true
	default:
		return false
	}
}

// managed reports whether an account line is one of this agent's.
func managed(line string) bool {
	f := strings.Split(line, ":")
	return len(f) > 4 && f[4] == managedMarker
}

// Collisions rejects an entire sync when a desired account clashes with a system
// account that already exists and this agent did not create.
//
// Adopting one silently would attach a credential to that account's name, and
// removing it later would delete a system user. The numeric half matters just
// as much: the import tool resolves a credential line to an account by number
// and adopts whichever name the reverse lookup returns, so a shared number
// binds the credential to the wrong name.
//
// The refusal covers the whole batch and stays at this layer. Unlike the
// render's account lists, where one bad name costs one account its access, a
// passwd write applied partway against a collision is precisely how a uid ends
// up owned by the wrong account. With auth's creation-time rule in place this
// is the emergency brake it was meant to be rather than a routine failure.
func Collisions(desired []Entry, passwd string) []string {
	var out []string
	for _, e := range desired {
		if !ValidName(e.Name) {
			out = append(out, fmt.Sprintf("%q is not a valid system account name", e.Name))
			continue
		}
		out = append(out, collisionsFor(e, passwd)...)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// collisionsFor reports what one desired entry collides with.
func collisionsFor(e Entry, passwd string) []string {
	var out []string
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
			out = append(out, fmt.Sprintf(
				"%q already exists as a system account this agent did not create", e.Name))
		}
		if uid == e.UID {
			out = append(out, fmt.Sprintf(
				"user id %s is already %q, an account this agent did not create: "+
					"move the configured service id clear of the host's real users",
				e.UID, name))
		}
	}
	return out
}

// MissingGroups lists groups the rendered accounts reference that do not exist.
//
// This agent invents no groups, and an account whose group resolves to nothing
// breaks the name lookup exactly as a missing account does.
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

// Rebuild produces the new account file: everything that is not this agent's,
// followed by everything that is.
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

// passwdMode is what the system account file must carry: every name lookup on
// the machine reads it, so a mode it cannot read breaks all of them.
const passwdMode = 0o644

// WritePasswd replaces the account file so no reader observes a partial one,
// and so a machine losing power mid-write returns holding one file or the other
// rather than a truncated roster.
//
// The ordinary lost-update race against a concurrent account creation remains.
// The system's own lock is unreachable from here, and a file server's SMB roster
// is not a multi-writer account file.
func WritePasswd(path, body string) error {
	// The mode goes onto the staged file rather than being left to the open call,
	// which the process mask would narrow.
	err := fsatomic.ReplaceFileDurable(path, passwdMode, func(f *os.File) error {
		_, werr := f.WriteString(body)
		return werr
	})
	if err != nil {
		return fmt.Errorf("replacing the account file: %w", err)
	}
	return nil
}
