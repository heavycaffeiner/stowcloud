// Linux only, because what it tests is.
//go:build linux

package handler

import "testing"

// A filename cannot break out of the header it is pasted into.
//
// The plain form is a quoted string, so a name carrying a quote or a backslash
// ends it early and everything after is read as header syntax. Both are mapped
// out; the extended form beside it carries the real name.
func TestAFilenameCannotEscapeTheDispositionHeader(t *testing.T) {
	for _, name := range []string{
		`in"jected"; x=y`,
		`back\slash`,
		"carriage\rreturn",
		"line\nfeed",
		"tab\there",
	} {
		got := contentDisposition(name)
		quoted := got[len(`attachment; filename="`):]
		end := 0
		for i := range len(quoted) {
			if quoted[i] == '"' {
				end = i
				break
			}
		}
		// Everything up to the closing quote has to be one clean run: no
		// quote, no backslash, no control character.
		for i := range end {
			c := quoted[i]
			if c == '"' || c == '\\' || c < 0x20 || c == 0x7f {
				t.Errorf("%q left byte %#x inside the quoted name: %s", name, c, got)
			}
		}
	}
}

// The name still arrives, through the extended form.
//
// The plain form can only carry ASCII, so a name outside it would be lost
// entirely without the second parameter beside it.
func TestTheRealNameSurvivesInTheExtendedForm(t *testing.T) {
	// U+00E9 and U+4E2D: one two-byte and one three-byte character, written
	// as escapes because this tree keeps its Go source ASCII.
	const name = "caf\u00e9-\u4e2d.pdf"
	got := contentDisposition(name)
	if !contains(got, "filename*=UTF-8''") {
		t.Fatalf("no extended form: %s", got)
	}
	if !contains(got, "caf%C3%A9-%E4%B8%AD.pdf") {
		t.Errorf("the name is not percent-encoded in the extended form: %s", got)
	}
	// And the plain form is pure ASCII, since that is all it may hold.
	for i := range len(got) {
		if got[i] > 127 {
			t.Fatalf("a non-ASCII byte reached the header: %s", got)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
