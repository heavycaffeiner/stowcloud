// Package vfs is the security core. Every filesystem access resolves through a
// share root's directory descriptor with that share's resolve flags, so the
// kernel enforces confinement inside the same syscall as the open.
//
// Three things are deliberately absent and each one is the second step that
// removes the guarantee: no path normalisation, no descriptor cache, and no
// revalidation of a path that was already resolved. A resolver that rewrites
// "a/../b" into "b" has to be right about every encoding, every separator and
// every Unicode form, and being wrong once is an escape. Rejecting is right by
// not deciding.
package vfs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// stagingPrefix is the one reserved prefix a write ever produces.
const stagingPrefix = ".scpart-"

// stagingName is the control name a write stages under. The random half comes
// from crypto/rand so two concurrent writes to one destination cannot pick the
// same name, and O_EXCL turns a collision into a refusal rather than a clobber.
func stagingName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("name a staging file: %w", err)
	}
	return stagingPrefix + hex.EncodeToString(b[:]), nil
}

// IsStagingName reports a name this package produced for a write in flight. The
// upload orphan sweep needs it, and it is here rather than there so the prefix
// has one definition.
func IsStagingName(name string) bool {
	return strings.HasPrefix(name, stagingPrefix) && len(name) > len(stagingPrefix)
}

// Vpath is what a client names a file by: "{share label}/{rest}". It is the
// only one of the three path types that appears in a request or a response.
//
// The empty Vpath is the virtual root, which names no share and is the one
// path with no components. Every other one starts with a share label.
type Vpath struct{ s string }

// SharePath is relative to a share root with the grant's subpath already on
// the front. It is what the core returns and what the core accepts.
type SharePath struct{ s string }

// SafePath is validated and component-wise, relative to a share root, with
// every component checked against the reserved set. It is the only path type
// this package accepts.
//
// A struct with an unexported field rather than a named string type: a named
// string still converts with a cast that reviews clean, so every crossing
// between the three vocabularies has to go through a function that says which
// direction it is going.
type SafePath struct{ comps []string }

// ErrInvalidName is what a component that cannot name anything refuses with.
var ErrInvalidName = errors.New("invalid path component")

// ErrReservedName is what a client path carrying one of this server's control
// prefixes refuses with. It is separate from ErrInvalidName because the layer
// above answers it differently: a reserved name is unlistable, not malformed.
var ErrReservedName = errors.New("reserved prefix used by this server's control files")

// NameError names the component that was refused and why. A refusal that does
// not say which of two hundred components was wrong is one a caller cannot act
// on.
type NameError struct {
	Component string
	Reason    string
	Err       error
}

func (e *NameError) Error() string {
	return fmt.Sprintf("%q: %s", e.Component, e.Reason)
}

func (e *NameError) Unwrap() error { return e.Err }

// reservedPrefixes names the control files this server owns. A function rather
// than a package-level slice: a table that can be reassigned is ambient state,
// and this one decides what a directory listing hides.
func reservedPrefixes() []string {
	return []string{".sctrash", ".scpart-", ".scmeta", ".scindex"}
}

// IsReservedName reports whether name carries a control prefix. Exported
// because the directory listing, the path parser and the SMB veto-files
// configuration all have to agree, and two copies of this table drift.
func IsReservedName(name string) bool {
	for _, p := range reservedPrefixes() {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// windowsReserved is the device-name table. Extension-insensitive, because
// "con.txt" is just as reserved as "CON" to a Windows or SMB client.
func windowsReserved() []string {
	return []string{
		"CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
	}
}

func isWindowsReserved(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	for _, r := range windowsReserved() {
		if strings.EqualFold(base, r) {
			return true
		}
	}
	return false
}

// validateExisting is what a component has to satisfy to name something that is
// already on disk.
//
// These are the rules that are not about taste: "/" would silently become two
// components, "." and ".." would escape the share, NUL truncates the C string
// the kernel eventually sees, and an over-long component cannot exist on any
// filesystem this runs on. The Windows portability table in validateCreatable
// is deliberately not applied here, because a rule about Windows has no
// business deciding whether a directory that already exists on a Linux disk can
// be opened: applying it made every "Mods ", "CON" and "report:final" somebody
// else's tool created list fine and then fail to open.
func validateExisting(name string) error {
	switch {
	case name == "":
		return &NameError{Component: name, Reason: "an empty component", Err: ErrInvalidName}
	case name == "." || name == "..":
		return &NameError{Component: name,
			Reason: "'.' and '..' are rejected, never resolved: resolving one is what creates the bypass",
			Err:    ErrInvalidName}
	case strings.Contains(name, "/"):
		return &NameError{Component: name, Reason: "a component may not contain '/'", Err: ErrInvalidName}
	case strings.IndexByte(name, 0) >= 0:
		return &NameError{Component: name,
			Reason: "NUL truncates the C string the kernel is handed",
			Err:    ErrInvalidName}
	case len(name) > limits.NameBytes:
		return limits.Exceed("name bytes", limits.NameBytes, int64(len(name)))
	case IsReservedName(name):
		return &NameError{Component: name, Reason: "a control-file prefix", Err: ErrReservedName}
	}
	return nil
}

// validateCreated is the table for a name this server is about to mint. It adds
// the Windows portability rules to validateExisting's set, so that nothing here
// creates a name no SMB or Windows client could ever open.
func validateCreated(name string) error { return validateCreatable(name, true) }

// validateControl is the same table minus the reserved-prefix rejection, and it
// is the only way to produce a control name. The rejection exists to keep
// user-supplied names from colliding with ours, so lifting it for our own is
// not a hole; every other rule still applies.
func validateControl(name string) error { return validateCreatable(name, false) }

func validateCreatable(name string, rejectReserved bool) error {
	if err := validateExistingWithoutReserved(name); err != nil {
		return err
	}
	for i := 0; i < len(name); i++ {
		if b := name[i]; b <= 0x1F || b == 0x7F {
			return &NameError{Component: name,
				Reason: "control characters are not allowed in a name we create",
				Err:    ErrInvalidName}
		}
	}
	switch {
	case strings.Contains(name, ":"):
		return &NameError{Component: name,
			Reason: "':' is the NTFS alternate-data-stream separator",
			Err:    ErrInvalidName}
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return &NameError{Component: name,
			Reason: "a trailing '.' or space is reinterpreted by Windows",
			Err:    ErrInvalidName}
	case isWindowsReserved(name):
		return &NameError{Component: name,
			Reason: "a Windows device name (CON, PRN, AUX, NUL, COM1-9, LPT1-9)",
			Err:    ErrInvalidName}
	case rejectReserved && IsReservedName(name):
		return &NameError{Component: name, Reason: "a control-file prefix", Err: ErrReservedName}
	}
	return nil
}

func validateExistingWithoutReserved(name string) error {
	err := validateExisting(name)
	if errors.Is(err, ErrReservedName) {
		return nil
	}
	return err
}

// checkPathSize bounds a whole path before anything splits it, so a hostile
// length costs one comparison rather than one allocation per component.
func checkPathSize(s string) error {
	if len(s) > limits.PathBytes {
		return limits.Exceed("path bytes", limits.PathBytes, int64(len(s)))
	}
	return nil
}

func checkComponentCount(n int) error {
	if n > limits.PathComponents {
		return limits.Exceed("path components", limits.PathComponents, int64(n))
	}
	return nil
}

// splitValidated bounds, splits and validates a slash-separated relative path
// with the traversal table. The empty string is the root and yields no
// components.
func splitValidated(s string) ([]string, error) {
	if err := checkPathSize(s); err != nil {
		return nil, err
	}
	if strings.HasPrefix(s, "/") {
		return nil, &NameError{Component: s,
			Reason: "an absolute path names something outside the share",
			Err:    ErrInvalidName}
	}
	if s == "" {
		return nil, nil
	}
	if err := checkComponentCount(strings.Count(s, "/") + 1); err != nil {
		return nil, err
	}
	parts := strings.Split(s, "/")
	for _, part := range parts {
		if err := validateExisting(part); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

// ParseVpath validates a client-supplied path: "{share label}/{rest}". It is
// the trust boundary for every path that arrives over the wire, and it refuses
// rather than repairs: an empty component, "." or "..", NUL, a component over
// the name bound, a path over the byte bound, more components than the
// component bound, and any component carrying a reserved prefix.
//
// The empty string parses to the virtual root, which is the one path naming no
// share.
func ParseVpath(s string) (Vpath, error) {
	if _, err := splitValidated(s); err != nil {
		return Vpath{}, err
	}
	return Vpath{s: s}, nil
}

// NewVpath joins a share label and a share-relative path into the form a client
// sees. It is the crossing back out of the core's vocabulary.
func NewVpath(label string, rest SharePath) (Vpath, error) {
	if err := validateExisting(label); err != nil {
		return Vpath{}, err
	}
	if rest.s == "" {
		return ParseVpath(label)
	}
	return ParseVpath(label + "/" + rest.s)
}

func (p Vpath) String() string { return p.s }

// IsRoot reports the virtual root, which names no share.
func (p Vpath) IsRoot() bool { return p.s == "" }

// Label is the share label. Empty for the virtual root.
func (p Vpath) Label() string {
	label, _, _ := strings.Cut(p.s, "/")
	return label
}

// Rest is everything below the share label, in the core's vocabulary.
func (p Vpath) Rest() SharePath {
	_, rest, _ := strings.Cut(p.s, "/")
	return SharePath{s: rest}
}

// ParseSharePath validates a path already inside one share. Same table as
// ParseVpath, applied to a path that no longer carries a label.
func ParseSharePath(s string) (SharePath, error) {
	if _, err := splitValidated(s); err != nil {
		return SharePath{}, err
	}
	return SharePath{s: s}, nil
}

func (p SharePath) String() string { return p.s }

func (p SharePath) IsRoot() bool { return p.s == "" }

// Safe is the crossing into the only vocabulary this package accepts.
func (p SharePath) Safe() (SafePath, error) {
	comps, err := splitValidated(p.s)
	if err != nil {
		return SafePath{}, err
	}
	return SafePath{comps: comps}, nil
}

// ParseSafePath validates a share-relative path such as "a/b/c". Never a
// leading slash, which would make it look absolute.
func ParseSafePath(s string) (SafePath, error) {
	comps, err := splitValidated(s)
	if err != nil {
		return SafePath{}, err
	}
	return SafePath{comps: comps}, nil
}

// RootPath is the share root, the path with no components.
func RootPath() SafePath { return SafePath{} }

// Components returns a copy. A SafePath is validated once and has to stay that
// way, and handing out the backing slice hands out a validated path a caller
// can edit.
func (p SafePath) Components() []string { return slices.Clone(p.comps) }

func (p SafePath) Len() int { return len(p.comps) }

func (p SafePath) IsRoot() bool { return len(p.comps) == 0 }

// Name is the last component, empty at the root.
func (p SafePath) Name() string {
	if len(p.comps) == 0 {
		return ""
	}
	return p.comps[len(p.comps)-1]
}

// Parent drops the last component. The parent of the root is the root.
func (p SafePath) Parent() SafePath {
	if len(p.comps) == 0 {
		return SafePath{}
	}
	return SafePath{comps: p.comps[: len(p.comps)-1 : len(p.comps)-1]}
}

// Join appends a component this server is minting, so the creation table
// applies.
func (p SafePath) Join(name string) (SafePath, error) {
	if err := validateCreated(name); err != nil {
		return SafePath{}, err
	}
	return p.push(name)
}

// JoinExisting appends a component that already exists on disk. Walking a
// directory and joining each entry is traversal rather than creation, and the
// creation table there refuses every name somebody else's tool already wrote
// and the listing already showed.
func (p SafePath) JoinExisting(name string) (SafePath, error) {
	if err := validateExisting(name); err != nil {
		return SafePath{}, err
	}
	return p.push(name)
}

// JoinControl appends one of this server's own control-file names. It is the
// only function that may produce a reserved prefix, and user input cannot reach
// it because every parser rejects that prefix outright.
//
// An earlier revision of this design had no such function, so a caller that
// needed a part file inside a share subdirectory disguised the name to get past
// validation. The disguise defeated the reserved-name filter and put part files
// in every listing, in the web UI and over WebDAV, for the duration of every
// upload.
func (p SafePath) JoinControl(name string) (SafePath, error) {
	if err := validateControl(name); err != nil {
		return SafePath{}, err
	}
	return p.push(name)
}

func (p SafePath) push(name string) (SafePath, error) {
	if err := checkComponentCount(len(p.comps) + 1); err != nil {
		return SafePath{}, err
	}
	total := len(name)
	for _, c := range p.comps {
		total += len(c) + 1
	}
	if total > limits.PathBytes {
		return SafePath{}, limits.Exceed("path bytes", limits.PathBytes, int64(total))
	}
	out := make([]string, 0, len(p.comps)+1)
	out = append(out, p.comps...)
	return SafePath{comps: append(out, name)}, nil
}

// HasPrefix reports whether other is p or sits beneath it, component-wise. This
// is what grant inheritance asks, and it is component-wise rather than string-
// wise because "ab" is not beneath "a".
func (p SafePath) HasPrefix(other SafePath) bool {
	if len(other.comps) > len(p.comps) {
		return false
	}
	return slices.Equal(other.comps, p.comps[:len(other.comps)])
}

// Equal compares component-wise.
func (p SafePath) Equal(other SafePath) bool { return slices.Equal(p.comps, other.comps) }

// Under reports whether p is at or beneath other.
//
// This is a component-wise comparison rather than a string prefix test, so
// "ab" is not under "a" and a caller cannot descend into a sibling by naming
// one whose path happens to start with the same bytes.
func (p SafePath) Under(other SafePath) bool {
	if len(p.comps) < len(other.comps) {
		return false
	}
	return slices.Equal(p.comps[:len(other.comps)], other.comps)
}

// String is "a/b/c". Never a leading slash; the root is "".
func (p SafePath) String() string { return strings.Join(p.comps, "/") }

// Share is the crossing back into the core's vocabulary.
func (p SafePath) Share() SharePath { return SharePath{s: p.String()} }
