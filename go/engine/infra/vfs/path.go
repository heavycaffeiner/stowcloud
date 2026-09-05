// Package vfs holds the filesystem trust boundary: nothing else in the
// engine opens, renames or unlinks anything inside a share.
//
// The package deliberately performs no path normalization, keeps no
// descriptor cache, and never revalidates a path it already resolved.
// Rewriting "a/../b" to "b" requires being right about every separator,
// every encoding and every Unicode form at once, and one mistake there is an
// escape; refusing the input outright cannot be wrong in that way.
package vfs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/uniname"
)

// Vpath is the wire form a client names a path by: "{share label}/{rest}".
// It is the only one of the three path types that a request or a response
// ever carries.
type Vpath struct{ raw string }

// SharePath is relative to one share, with a grant's own subpath prefix
// already folded in. It is the vocabulary the domain core reads and writes.
type SharePath struct{ raw string }

// SafePath is component-wise and fully validated: the sole vocabulary any
// ShareRoot method accepts.
//
// All three types wrap an unexported field rather than being named string
// types. A named string still converts with a bare cast that reviews clean;
// wrapping it in a struct forces every crossing between vocabularies through
// a function that says, by its name, which direction it goes and what it
// checks along the way.
type SafePath struct{ comps []string }

// ErrInvalidName is the refusal for a component that cannot name anything a
// filesystem this package targets could hold: empty, ".", "..", an embedded
// separator, a NUL byte, an over-length name, or (for a name being minted)
// one of the Windows-portability violations.
var ErrInvalidName = errors.New("vfs: invalid path component")

// ErrReservedName is the refusal for a component carrying one of this
// package's own control prefixes. Kept apart from ErrInvalidName because a
// caller answers the two differently: a reserved name is hidden from a
// listing, not merely malformed.
var ErrReservedName = errors.New("vfs: reserved control-file prefix")

// PathError names the component a validation step refused and why, so a
// path with two hundred components produces a refusal a caller can act on
// rather than a bare "invalid."
type PathError struct {
	Component string
	Detail    string
	Err       error
}

func (e *PathError) Error() string { return fmt.Sprintf("vfs: %q: %s", e.Component, e.Detail) }

func (e *PathError) Unwrap() error { return e.Err }

func invalidName(component, detail string) error {
	return &PathError{Component: component, Detail: detail, Err: ErrInvalidName}
}

func reservedName(component string) error {
	return &PathError{Component: component, Detail: "a control-file prefix", Err: ErrReservedName}
}

// reservedPrefixes are this server's own control names. Held behind a
// function rather than a package variable, because a variable is state a
// caller could reassign, and this table decides what a listing conceals.
func reservedPrefixes() []string {
	return []string{".sctrash", ".scpart-", ".scmeta", ".scindex"}
}

// IsReservedName reports whether name starts with one of this package's
// control prefixes. Exported so the directory listing, the path parser and
// the SMB veto-file configuration all read one table instead of three that
// can drift apart.
func IsReservedName(name string) bool {
	for _, prefix := range reservedPrefixes() {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

const stagingPrefix = ".scpart-"

// stagingName mints a fresh ".scpart-" control name. The suffix comes from
// crypto/rand so two concurrent writes aimed at the same destination cannot
// land on the same staging name; O_EXCL at create time turns any collision
// that does still happen into a refusal rather than a clobber.
func stagingName() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("vfs: mint a staging name: %w", err)
	}
	return stagingPrefix + hex.EncodeToString(suffix[:]), nil
}

// IsStagingName reports a name this package minted for a write still in
// flight, so a maintenance sweep can tell "our staging file" apart from an
// ordinary name that happens to start with a dot.
func IsStagingName(name string) bool {
	return strings.HasPrefix(name, stagingPrefix) && len(name) > len(stagingPrefix)
}

// windowsReservedDeviceNames are the names a Windows or SMB client can never
// open, regardless of extension.
func windowsReservedDeviceNames() []string {
	return []string{
		"CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
	}
}

func isWindowsDeviceName(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	for _, device := range windowsReservedDeviceNames() {
		if strings.EqualFold(stem, device) {
			return true
		}
	}
	return false
}

// validateExisting is the table applied to a component that already exists
// on disk, or that is being walked toward one. It is not about client
// portability at all; every rule here is about what a resolver can safely
// hand to the kernel.
//
// The Windows-portability rules in validateCreatable are intentionally left
// out: a directory another program already named "CON" or "report:final"
// must stay listable and openable, and running the creation table over an
// existing name would make every such entry list fine and then fail to
// open.
func validateExisting(name string) error {
	switch {
	case name == "":
		return invalidName(name, "a component cannot be empty")
	case name == ".":
		return invalidName(name, "'.' is refused rather than resolved, since resolving it is the bypass")
	case name == "..":
		return invalidName(name, "'..' is refused rather than resolved, since resolving it is the bypass")
	case strings.Contains(name, "/"):
		return invalidName(name, "a component cannot contain '/'")
	case strings.IndexByte(name, 0) >= 0:
		return invalidName(name, "a NUL byte would truncate the string the kernel receives")
	case len(name) > limits.NameBytes:
		return limits.Exceed("name bytes", limits.NameBytes, int64(len(name)))
	case IsReservedName(name):
		return reservedName(name)
	default:
		return nil
	}
}

// existingRulesExceptReserved runs every validateExisting check other than
// the reserved-prefix refusal, since validateCreatable applies that
// separately, conditioned on rejectReserved.
func existingRulesExceptReserved(name string) error {
	err := validateExisting(name)
	if errors.Is(err, ErrReservedName) {
		return nil
	}
	return err
}

// validateCreatable is the table for a component this package is about to
// mint, layering the Windows-portability rules on top of validateExisting so
// nothing minted here is a name no SMB or Windows client could ever open.
//
// rejectReserved is false only from JoinControl: that is the one function
// permitted to produce a reserved prefix, because every parser that accepts
// client input refuses that prefix outright, so a caller cannot reach
// JoinControl by way of untrusted data.
func validateCreatable(name string, rejectReserved bool) error {
	if err := existingRulesExceptReserved(name); err != nil {
		return err
	}
	for i := 0; i < len(name); i++ {
		if b := name[i]; b <= 0x1F || b == 0x7F {
			return invalidName(name, "a control byte is refused in a name this package creates")
		}
	}
	switch {
	case strings.Contains(name, ":"):
		return invalidName(name, "':' is the NTFS alternate-data-stream separator")
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return invalidName(name, "a trailing '.' or space is reinterpreted by Windows")
	case isWindowsDeviceName(name):
		return invalidName(name, "a Windows device name (CON, PRN, AUX, NUL, COM1-9, LPT1-9)")
	case rejectReserved && IsReservedName(name):
		return reservedName(name)
	default:
		return nil
	}
}

// checkPathSize bounds a whole path string before it is split, so a hostile
// length costs one comparison instead of one allocation per component.
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

// splitValidated bounds, splits, normalizes and validates a share-relative
// path string against the existing-name table. The empty string is the
// root and yields no components; anything starting with "/" is refused as
// an absolute path, which is not this vocabulary's concern.
//
// Normalization runs after the split and before the per-component
// validation, never the other way around: a decoder that turned some byte
// sequence into "/" or a NUL is not a component boundary this package ever
// looked at, so running validateExisting on the normalized components, not
// the raw ones, is what turns that byte sequence into an ordinary refusal
// instead of a bypass.
//
// The normalized path is returned as a freshly joined string rather than
// the input, since a legacy code page can decode to more or fewer bytes
// than it started as; the per-component limits.NameBytes bound inside
// validateExisting is therefore checked against the bytes that actually
// reach the kernel, not the bytes the client sent. When every component
// was already normal, uniname.Components hands back the same slice it was
// given, and this returns the input string unchanged rather than paying
// for a join that would only reproduce it.
func splitValidated(s string) (string, []string, error) {
	if err := checkPathSize(s); err != nil {
		return "", nil, err
	}
	if strings.HasPrefix(s, "/") {
		return "", nil, invalidName(s, "a share-relative path may not begin with '/'")
	}
	if s == "" {
		return "", nil, nil
	}
	if err := checkComponentCount(strings.Count(s, "/") + 1); err != nil {
		return "", nil, err
	}
	comps := strings.Split(s, "/")
	normalized := uniname.Components(comps)
	for _, c := range normalized {
		if err := validateExisting(c); err != nil {
			return "", nil, err
		}
	}
	if &normalized[0] == &comps[0] {
		return s, comps, nil
	}
	return strings.Join(normalized, "/"), normalized, nil
}

// ParseVpath is the trust boundary for a path that arrived over the wire.
// It strips exactly one leading slash and validates what remains against
// the existing-name table; nothing else about the input is repaired except
// normalization.
//
// The leading slash is accepted because a client's own URL model is rooted
// ("/label/rest"), while this package's is not, so "/documents/a.txt" and
// "documents/a.txt" have to name the same virtual path. Stripping it does
// not weaken anything: this path is virtual and cannot escape a share
// either way. What the slash could have meant instead, a host path, is
// still refused by the ordinary mechanism: "/etc/passwd" parses to the
// share label "etc", which no grant names, and resolves not-found exactly
// like any unknown label would.
//
// The empty string, and "/" after the strip, both parse to the virtual
// root: the one Vpath naming no share at all.
//
// The result is normalized to NFC UTF-8 here, at the point every protocol
// this server speaks crosses into its own vocabulary, so no protocol layer
// upstream (WebDAV, the HTTP API, anything added later) has to remember to
// do it itself.
func ParseVpath(s string) (Vpath, error) {
	s = strings.TrimPrefix(s, "/")
	normalized, _, err := splitValidated(s)
	if err != nil {
		return Vpath{}, err
	}
	return Vpath{raw: normalized}, nil
}

// NewVpath joins a share label back onto a SharePath, crossing out of the
// core's vocabulary into the wire form.
func NewVpath(label string, rest SharePath) (Vpath, error) {
	if err := validateExisting(label); err != nil {
		return Vpath{}, err
	}
	if rest.raw == "" {
		return ParseVpath(label)
	}
	return ParseVpath(label + "/" + rest.raw)
}

// String is the wire spelling: "{share label}/{rest}", with no leading
// slash.
func (p Vpath) String() string { return p.raw }

// IsRoot reports the virtual root, the Vpath naming no share.
func (p Vpath) IsRoot() bool { return p.raw == "" }

// Name is the last path component, empty at the virtual root.
func (p Vpath) Name() string {
	if i := strings.LastIndexByte(p.raw, '/'); i >= 0 {
		return p.raw[i+1:]
	}
	return p.raw
}

// Label is the share label this path names, empty at the virtual root.
func (p Vpath) Label() string {
	label, _, _ := strings.Cut(p.raw, "/")
	return label
}

// Rest is everything under the share label, in the core's vocabulary.
func (p Vpath) Rest() SharePath {
	_, rest, _ := strings.Cut(p.raw, "/")
	return SharePath{raw: rest}
}

// ParseSharePath validates a path that is already inside one share, against
// the same existing-name table ParseVpath uses, minus the label it no
// longer carries.
//
// The result is normalized to NFC UTF-8 here, the same trust-boundary
// reasoning as ParseVpath: a caller reaching this function directly, rather
// than by way of ParseVpath, still deserves one normalized spelling rather
// than having to remember to ask for it.
func ParseSharePath(s string) (SharePath, error) {
	normalized, _, err := splitValidated(s)
	if err != nil {
		return SharePath{}, err
	}
	return SharePath{raw: normalized}, nil
}

func (p SharePath) String() string { return p.raw }

// IsRoot reports the share root itself: zero components below the share.
func (p SharePath) IsRoot() bool { return p.raw == "" }

// Components splits the path, for the one caller that has to compare it
// against another path component by component. Nil at the share root.
//
// The components are already validated, since a SharePath cannot exist
// without having passed the table, so this repeats no checking.
func (p SharePath) Components() []string {
	if p.raw == "" {
		return nil
	}
	return strings.Split(p.raw, "/")
}

// Safe crosses into the one vocabulary this package's syscalls accept.
//
// p.raw already passed through splitValidated once, by way of
// ParseSharePath or NewVpath, so this second pass only re-derives the
// component slice; normalization is idempotent, so it costs nothing beyond
// the split.
func (p SharePath) Safe() (SafePath, error) {
	_, comps, err := splitValidated(p.raw)
	if err != nil {
		return SafePath{}, err
	}
	return SafePath{comps: comps}, nil
}

// ParseSafePath validates a share-relative path such as "a/b/c" directly.
// A leading slash is refused here, since accepting one would make a
// filesystem-facing path look absolute.
//
// The result is normalized to NFC UTF-8 here, the same trust-boundary
// reasoning as ParseVpath: this is another point where client-supplied
// bytes cross into this package's own vocabulary, and every such crossing
// normalizes so nothing downstream has to.
func ParseSafePath(s string) (SafePath, error) {
	_, comps, err := splitValidated(s)
	if err != nil {
		return SafePath{}, err
	}
	return SafePath{comps: comps}, nil
}

// RootPath is the share root: the SafePath with zero components.
func RootPath() SafePath { return SafePath{} }

// Components returns a defensive copy. A SafePath is validated exactly
// once, and handing out the backing slice would hand a caller a way to edit
// an already-validated path in place.
func (p SafePath) Components() []string { return slices.Clone(p.comps) }

// Len is the component count.
func (p SafePath) Len() int { return len(p.comps) }

// IsRoot reports zero components.
func (p SafePath) IsRoot() bool { return len(p.comps) == 0 }

// Name reports the leaf component; the root path has none.
func (p SafePath) Name() string {
	if len(p.comps) == 0 {
		return ""
	}
	return p.comps[len(p.comps)-1]
}

// Parent removes the leaf component; asking the root for its own parent
// returns the root again, since there is nothing above it to return.
func (p SafePath) Parent() SafePath {
	if len(p.comps) == 0 {
		return SafePath{}
	}
	// The three-index slice caps capacity at the current length, so a later
	// Join through this parent allocates a new backing array instead of
	// silently overwriting the child's own last component.
	return SafePath{comps: p.comps[: len(p.comps)-1 : len(p.comps)-1]}
}

// Join appends a component this package is about to mint, so the creation
// table (Windows portability included) applies.
//
// The name is normalized to NFC UTF-8 before the table runs, for the same
// trust-boundary reason splitValidated normalizes: a caller reaching Join
// directly, rather than through ParseSafePath, is still minting a name a
// client just typed, and it has to land on disk in the one spelling every
// other entry point produces. JoinExisting and JoinControl do not
// normalize, and for opposite reasons: JoinExisting reads a name out of a
// real directory listing, and those exact bytes, not a normalized
// approximation of them, are what the next syscall needs to find the entry
// again; JoinControl mints this package's own ASCII-only control names,
// where normalization is the identity, so skipping it costs nothing and
// keeps JoinControl free of a dependency the other two already carry.
func (p SafePath) Join(name string) (SafePath, error) {
	name = uniname.Normalize(name)
	if err := validateCreatable(name, true); err != nil {
		return SafePath{}, err
	}
	return p.push(name)
}

// JoinExisting appends a component already present on disk. Walking a
// directory listing and joining each returned name is traversal, not
// creation, so the creation table (which would refuse plenty of names
// another program already wrote and this package's own listing just
// showed) does not apply.
func (p SafePath) JoinExisting(name string) (SafePath, error) {
	if err := validateExisting(name); err != nil {
		return SafePath{}, err
	}
	return p.push(name)
}

// JoinControl appends one of this package's own control names. It is the
// only function that may produce a reserved prefix; no parser that accepts
// client input can reach it, since every one of them refuses that prefix
// outright before a caller could ever hand a reserved-looking name in.
func (p SafePath) JoinControl(name string) (SafePath, error) {
	if err := validateCreatable(name, false); err != nil {
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
	next := make([]string, len(p.comps), len(p.comps)+1)
	copy(next, p.comps)
	return SafePath{comps: append(next, name)}, nil
}

// HasPrefix reports whether other names p itself or an ancestor of p,
// compared component-wise. A string-prefix test would let "ab" pass as
// beneath "a", which lets a caller holding only a grant on "a" reach a
// sibling directory whose name happens to share a byte prefix.
func (p SafePath) HasPrefix(other SafePath) bool {
	if len(other.comps) > len(p.comps) {
		return false
	}
	return slices.Equal(other.comps, p.comps[:len(other.comps)])
}

// Under reports whether p sits at or beneath other, component-wise, for the
// same reason HasPrefix is component-wise: "ab" is never under "a".
func (p SafePath) Under(other SafePath) bool {
	if len(p.comps) < len(other.comps) {
		return false
	}
	return slices.Equal(p.comps[:len(other.comps)], other.comps)
}

// Equal reports whether both paths have identical components.
func (p SafePath) Equal(other SafePath) bool { return slices.Equal(p.comps, other.comps) }

// String joins the components with "/". The root's string is "".
func (p SafePath) String() string { return strings.Join(p.comps, "/") }

// Share crosses back into the core's vocabulary.
func (p SafePath) Share() SharePath { return SharePath{raw: p.String()} }

// RefusedNames is the whole-name half of the creation table, exported so a
// protocol surface advertising "these names are refused" to a client reads
// it from the table that actually enforces it. A name advertised as legal
// and then refused leaves a sync client retrying forever without
// converging.
func RefusedNames() []string { return windowsReservedDeviceNames() }

// RefusedNameCharacters is the character half of the same table.
//
// It omits characters this package accepts even though a Windows-hosted
// server would refuse them: an asterisk or a question mark names an
// ordinary file on the filesystems this package targets, and advertising
// either as refused would push a client into renaming something this
// package would have accepted as given. Individual control bytes are
// likewise left off the list; the table still refuses them, but naming
// thirty-odd unprintable characters one by one helps nobody reading this
// list.
func RefusedNameCharacters() []string {
	return []string{"/", ":"}
}
