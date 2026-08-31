package smb

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Characters that terminate a value prematurely or alter the meaning of the
// surrounding file. Any value containing one is rejected.
//
// Newlines and carriage returns close the directive, leaving whatever follows
// to be parsed as a new directive at the same level. A bracket beginning a line
// starts a section, letting a value close the current section and open a
// different one. A backslash at the end continues the line, absorbing the
// following directive into this value. A comment marker erases the rest of the
// line.
const (
	unsafeAnywhere = "\n\r\x00[]"
	unsafeComment  = ";#"
	// listModifiers begin an entry that is a modifier to Samba rather than a
	// name: the sign forms negate or add, and the sigil forms name a group
	// through the name service.
	listModifiers = "+-&@"
)

// serverNameMaxRunes is the limit imposed by the name service.
const serverNameMaxRunes = 15

// checkSafeValue rejects values that cannot be interpolated.
func checkSafeValue(field, v string) error {
	unsafe := func(reason string) error {
		return &UnsafeError{Field: field, Value: v, Reason: reason}
	}

	if !utf8.ValidString(v) {
		return unsafe("not valid UTF-8")
	}
	if strings.ContainsAny(v, unsafeAnywhere) {
		return unsafe("contains a character that would end the directive or open a section")
	}
	if strings.ContainsAny(v, unsafeComment) {
		return unsafe("contains a comment marker, which would hide the rest of the line")
	}
	// A backslash at the end continues the line, folding the directive on the
	// following line into this value.
	if strings.HasSuffix(v, `\`) {
		return unsafe("ends in a backslash, which continues onto the next line")
	}
	// Samba substitutes a variable wherever one appears, so a value carrying one
	// is not the value the operator wrote. A path holding the connecting user's
	// name expands per connection, which is a different directory for every
	// client.
	if strings.Contains(v, "%") {
		return unsafe("contains a substitution marker, which Samba expands per connection")
	}
	// A control character has no meaning here and several of them do have one to
	// a terminal reading the file back.
	if strings.ContainsFunc(v, unicode.IsControl) {
		return unsafe("contains a control character")
	}
	return nil
}

// checkSafeName rejects values that must additionally survive as a single item
// in a space-separated list, the format the account lists use.
//
// A name containing a space would divide into two names, the second granting
// access nobody intended. Whitespace is therefore an authorization concern
// here, not a formatting one.
func checkSafeName(field, v string) error {
	if v == "" {
		return &UnsafeError{Field: field, Value: v, Reason: "must not be empty"}
	}
	if err := checkSafeValue(field, v); err != nil {
		return err
	}
	if strings.ContainsFunc(v, unicode.IsSpace) {
		return &UnsafeError{
			Field:  field,
			Value:  v,
			Reason: "contains whitespace, which would split it into two entries in a list",
		}
	}
	// A name that arrives with a modifier attached is asking for a different
	// grant than the one it spells.
	if strings.ContainsAny(v[:1], listModifiers) {
		return &UnsafeError{
			Field:  field,
			Value:  v,
			Reason: "begins with a list modifier, which changes what the entry grants",
		}
	}
	return nil
}

// checkSafePath rejects share paths that cannot be interpolated, along with any
// that are not absolute.
//
// Samba resolves a relative path against its own working directory, a location
// nobody selected, so such paths are rejected here rather than resolving
// somewhere unforeseen.
func checkSafePath(v string) error {
	if v == "" {
		return &UnsafeError{Field: "share path", Value: v, Reason: "must not be empty"}
	}
	if err := checkSafeValue("share path", v); err != nil {
		return err
	}
	if !strings.HasPrefix(v, "/") {
		return &UnsafeError{
			Field:  "share path",
			Value:  v,
			Reason: "must be absolute, or Samba resolves it against a directory nobody chose",
		}
	}
	return nil
}

// checkServerName rejects names the name service cannot represent.
//
// Validation uses an allow-list instead of a set of forbidden characters: any
// name a person would plausibly enter falls inside it, and the name service
// itself rejects anything longer than fifteen characters.
func checkServerName(v string) error {
	if v == "" {
		return nil
	}
	unsafe := func(reason string) error {
		return &UnsafeError{Field: "smb.server_name", Value: v, Reason: reason}
	}
	if utf8.RuneCountInString(v) > serverNameMaxRunes {
		return unsafe("a NetBIOS name is at most 15 characters")
	}
	for _, r := range v {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return unsafe("only ASCII letters, digits, '-' and '_' are allowed")
		}
	}
	return nil
}

// checkPasswdName rejects names the account file cannot represent.
//
// Records are one per line with colon-separated fields and no escaping
// mechanism whatsoever, so a name containing either character is rejected
// instead of being emitted.
func checkPasswdName(v string) error {
	if v == "" {
		return &UnsafeError{Field: "smb user", Value: v, Reason: "must not be empty"}
	}
	if strings.ContainsAny(v, ":\n\r\x00") {
		return &UnsafeError{
			Field:  "smb user",
			Value:  v,
			Reason: "contains a separator the passwd format has no escape for",
		}
	}
	return checkSafeName("smb user", v)
}
