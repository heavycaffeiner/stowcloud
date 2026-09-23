package auth

// The one account-name rule.
//
// It used to be three: the setup screen admitted 1..64 characters of a wide
// set, the passwd renderer refused only its own separators, and the account
// tool on the sidecar allowed a POSIX name. The setup rule admitted names the
// other two refuse, and because the renderer refuses the file rather than the
// name, one such account cost every account its file-sharing access at once.
//
// This rule is the intersection, which is the strictest of the three. It
// gates creation only: names already stored are not invalidated, and every
// consumer keeps its own defensive check on what it writes. Validating at the
// boundary does not excuse a format writer from checking its own format.

// UsernameMaxLen is the POSIX portable bound the sidecar's account tool
// enforces, and therefore the bound every name has to fit.
const UsernameMaxLen = 32

// ValidUsername holds a new account name to the one rule every consumer of
// the name can carry: the web screens, the passwd file, the account tools and
// the credential importer.
//
// The error carries a fixed reason and never the input: a validation message
// that echoes what was typed is a reflection primitive.
func ValidUsername(name string) error {
	if len(name) == 0 || len(name) > UsernameMaxLen {
		return ErrNameInvalid
	}
	for i := 0; i < len(name); i++ {
		if !usernameByteOK(name[i], i == 0) {
			return ErrNameInvalid
		}
	}
	return nil
}

// usernameByteOK is the character rule: a lower-case letter or an underscore
// first, then those plus digits and a hyphen. A leading hyphen is what the
// account tools would read as an option rather than a name.
func usernameByteOK(c byte, first bool) bool {
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
