// Package limits defines bounds for OIDC provider traffic and state.
package limits

import (
	"errors"
	"fmt"
	"time"
)

const (
	OIDCResponseBytes  = 256 << 10
	OIDCRequestTimeout = 10 * time.Second
	OIDCConnectTimeout = 5 * time.Second
	OIDCJWKSKeys       = 32
	OIDCTokenBytes     = 16 << 10
	OIDCClockSkew      = 2 * time.Minute
	OIDCDiscoveryTTL   = time.Hour
	OIDCJWKSTTL        = time.Hour
	OIDCFlowLifetime   = 10 * time.Minute
	OIDCFlowTTL        = 10 * time.Minute
)

var ErrTooLarge = errors.New("limit exceeded")

type Exceeded struct {
	Limit      string
	Bound, Got int64
}

func (e *Exceeded) Error() string {
	return fmt.Sprintf("%s: %d exceeds the limit of %d", e.Limit, e.Got, e.Bound)
}
func (e *Exceeded) Is(target error) bool { return target == ErrTooLarge }
func Exceed(limit string, bound, got int64) error {
	return &Exceeded{Limit: limit, Bound: bound, Got: got}
}
