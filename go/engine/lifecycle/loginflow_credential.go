//go:build linux

package lifecycle

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// syncCredentialSource adapts the auth service to what the device login needs
// at delivery time.
//
// The credential is minted when a poll succeeds rather than when a flow is
// approved, so the plaintext exists for exactly one response: an abandoned or
// expired flow leaves nothing live behind.
type syncCredentialSource struct {
	auth *auth.Service
}

// MintSyncCredential implements FlowCredential.
func (s *syncCredentialSource) MintSyncCredential(ctx context.Context, user int64) (string, error) {
	// The name a client sees in its credentials list. The device login is the
	// only caller, so the name is enough to tell these credentials apart from
	// one a person created by hand.
	token, _, err := s.auth.CreateSyncCredential(ctx, user, "device login")
	return token, err
}
