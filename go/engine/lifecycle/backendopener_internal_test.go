//go:build linux

package lifecycle

import (
	"errors"
	"fmt"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vault"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// Each way a container refuses to open reports its own kind.
//
// The admin screen switches on these strings and falls back to "the disk it
// is on may not be mounted" for anything it does not recognise. That guess
// was once the only thing an operator saw for all of these, and it is wrong
// for every one: a wrong passphrase, a damaged header, a filesystem this
// build cannot read and a file that is not a container are four different
// things to go and fix.
func TestEachContainerRefusalCarriesItsOwnKind(t *testing.T) {
	for _, c := range []struct {
		err  error
		kind string
	}{
		{vault.ErrWrongPassword, "passphrase"},
		{vault.ErrUnsupportedVolume, "container_unsupported"},
		{vault.ErrHeaderCorrupt, "container_corrupt"},
		{vault.ErrHeaderFieldsInvalid, "container_corrupt"},
		{vault.ErrUnsupportedFilesystem, "container_filesystem"},
	} {
		// Wrapped, because the driver returns these under its own context
		// and a classifier matching only the bare sentinel would miss every
		// real call.
		got := core.RejectionKind(classifyVaultOpen(fmt.Errorf("opening the container: %w", c.err)))
		if got != c.kind {
			t.Errorf("%v reports kind %q, want %q", c.err, got, c.kind)
		}
	}
}

// Anything else keeps the sentinel it already carries.
//
// A container file that is genuinely absent is a missing path, and the
// filesystem layer's own answer for that is better than a vault-specific one.
func TestAnUnrecognisedFailureIsNotClaimedByTheVaultClassifier(t *testing.T) {
	other := errors.New("some other failure")
	if got := classifyVaultOpen(other); !errors.Is(got, other) {
		t.Errorf("an unrelated failure was rewritten to %v", got)
	}
}
