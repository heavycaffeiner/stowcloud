//go:build linux

package emergency

import (
	"context"
	"errors"
	"time"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
)

// NewAuthenticator adds the one-use recovery-code fallback to ordinary login.
func NewAuthenticator(service *auth.Service, now func() int64) Authenticator {
	return authenticator{Service: service, now: now}
}

type authenticator struct {
	*auth.Service
	now func() int64
}

func (a authenticator) Login(ctx context.Context, req auth.LoginRequest, ttl time.Duration) (auth.Session, error) {
	passwordOnly := req
	passwordOnly.Factor = ""
	session, err := a.Service.Login(ctx, passwordOnly, ttl)
	if err == nil || !errors.Is(err, auth.ErrSecondFactor) || req.Factor == "" {
		return session, err
	}
	userID, lookupErr := a.UserIDByName(ctx, req.Name)
	if lookupErr != nil {
		return session, lookupErr
	}
	accepted, factorErr := a.VerifyTOTP(ctx, userID, req.Factor, a.now())
	if factorErr != nil {
		return session, factorErr
	}
	if !accepted {
		accepted, factorErr = a.UseRecoveryCode(ctx, userID, req.Factor)
		if factorErr != nil {
			return session, factorErr
		}
	}
	if !accepted {
		a.Record(ctx, userID, auth.EventLogin, req.Name, req.IP, req.UA, false)
		return session, auth.ErrCredentials
	}
	return a.CreateSession(ctx, userID, req.IP, req.UA, req.AMR, ttl)
}
