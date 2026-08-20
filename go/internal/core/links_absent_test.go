package core

import (
	"context"
	"errors"
	"testing"
)

// A token that names nothing is absent, not gone.
//
// Reporting it as gone asserts it once existed, which lets a stranger sort
// guesses into the ones that were real links and the ones that never were. The
// distinction only shows up from outside, which is why it survived until two
// implementations were compared.
func TestATokenThatNamesNothingIsAbsentRatherThanGone(t *testing.T) {
	c, _, _ := testCore(t)

	for _, token := range []string{
		"not-a-real-token",
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"",
	} {
		_, _, err := c.LinkPublic(context.Background(), token)
		if errors.Is(err, ErrLinkExpired) {
			t.Errorf("the token %q reports as gone, which says it once existed", token)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("the token %q gave %v, want it reported absent", token, err)
		}
	}
}
