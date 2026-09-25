//go:build linux

package backends

import (
	"errors"
	"fmt"
	"testing"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vault"
)

func TestEachContainerRefusalCarriesItsOwnKind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		kind string
	}{
		{"wrong password", vault.ErrWrongPassword, "passphrase"},
		{"unsupported volume", vault.ErrUnsupportedVolume, "container_unsupported"},
		{"corrupt header", vault.ErrHeaderCorrupt, "container_corrupt"},
		{"invalid header fields", vault.ErrHeaderFieldsInvalid, "container_corrupt"},
		{"unsupported filesystem", vault.ErrUnsupportedFilesystem, "container_filesystem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := core.RejectionKind(classifyVaultOpen(fmt.Errorf("opening the container: %w", tc.err)))
			if got != tc.kind {
				t.Errorf("%v reports kind %q, want %q", tc.err, got, tc.kind)
			}
		})
	}
}

func TestAnUnrecognisedFailureIsNotClaimedByTheVaultClassifier(t *testing.T) {
	t.Parallel()
	other := errors.New("some other failure")
	if got := classifyVaultOpen(other); !errors.Is(got, other) {
		t.Fatalf("an unrelated failure was rewritten to %v", got)
	}
}
