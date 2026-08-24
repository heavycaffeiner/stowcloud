package smb

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The generated file is a trust boundary in the outbound direction. Every
// value interpolated into it is checked here, and one that cannot be
// represented safely is refused rather than escaped: the configuration format
// has no escape that survives its own line-continuation and variable
// substitution rules, so an escaping scheme here would be a guess about
// somebody else's parser.
//
// A share name is not where anyone should be discovering that Samba's parser
// has opinions.

// The characters that end a value early or change what the rest of the file
// means. A value carrying any of them is refused.
//
// A newline or a carriage return ends the directive, and everything after it
// is read as a fresh directive at the same scope. A bracket at the start of a
// line opens a section, so a value carrying one can close the current section
// and open another. A trailing backslash is a line continuation, which
// swallows the directive on the next line into this value. A comment marker
// makes the remainder of the line disappear.
const (
	unsafeAnywhere = "\n\r\x00[]"
	unsafeComment  = ";#"
)

// checkSafeValue refuses a value that cannot be interpolated.
func checkSafeValue(field, v string) error {
	unsafe := func(reason string) error {
		return &UnsafeError{Field: field, Value: v, Reason: reason}
	}

	if !utf8.ValidString(v) {
		return unsafe("not valid UTF-8")
	}
	if i := strings.IndexAny(v, unsafeAnywhere); i >= 0 {
		return unsafe("contains a character that would end the directive or open a section")
	}
	if strings.ContainsAny(v, unsafeComment) {
		return unsafe("contains a comment marker, which would hide the rest of the line")
	}
	// A trailing backslash continues the line, so the directive written below
	// this one becomes part of this value.
	if strings.HasSuffix(v, `\`) {
		return unsafe("ends in a backslash, which continues onto the next line")
	}
	// Samba substitutes a variable wherever one appears, so a value carrying
	// one is not the value the operator wrote. A path holding the connecting
	// user's name expands per connection, which is a different directory for
	// every client.
	if strings.Contains(v, "%") {
		return unsafe("contains a substitution marker, which Samba expands per connection")
	}
	// A control character has no meaning here and several of them do have one
	// to a terminal reading the file back.
	for _, r := range v {
		if unicode.IsControl(r) {
			return unsafe("contains a control character")
		}
	}
	return nil
}

// checkSafeName refuses a value that also has to survive being one item in a
// space-separated list, which is how the account lists are written.
//
// A name carrying a space would split into two names, and the second one is a
// grant nobody wrote. That makes whitespace an access-control question here
// rather than a formatting one.
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
	// An entry beginning with one of these is a modifier to Samba rather than
	// a name: the sign forms negate or add, and the sigil forms name a group
	// through the name service. A name that arrives with one attached is
	// asking for a different grant than the one it spells.
	if strings.ContainsAny(v[:1], "+-&@") {
		return &UnsafeError{
			Field:  field,
			Value:  v,
			Reason: "begins with a list modifier, which changes what the entry grants",
		}
	}
	return nil
}

// checkSafePath refuses a share path that cannot be interpolated, and one that
// is not absolute.
//
// A relative path is resolved by Samba against its own working directory,
// which is not a directory anyone chose, so it is refused here rather than
// pointing somewhere nobody predicted.
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

// checkServerName refuses a name the name service cannot carry.
//
// An allow-list rather than a list of banned characters, because every name
// anyone would type fits inside it, and because the name service itself
// refuses anything over fifteen characters.
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

// serverNameMaxRunes is what the name service itself accepts.
const serverNameMaxRunes = 15
