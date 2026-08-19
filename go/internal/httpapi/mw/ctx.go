package mw

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
)

// Origin is which host the request arrived on, decided by HostGuard. Content
// is the no-session origin that serves stored bytes by capability URL; App is
// the authenticated surface.
type Origin uint8

const (
	OriginApp Origin = iota
	OriginContent
)

type originKey struct{}

func withOrigin(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFrom reads the host classification HostGuard made.
func OriginFrom(ctx context.Context) Origin {
	o, _ := ctx.Value(originKey{}).(Origin)
	return o
}

type principalKey struct{}

func withPrincipal(ctx context.Context, p auth.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom reads the authenticated caller. The zero value is absent,
// which the handlers distinguish from a real account because a real account
// id is never zero.
func PrincipalFrom(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	return p, ok
}

type scopeKey struct{}

func withAppPWScope(ctx context.Context, s auth.Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// AppPWScopeFrom reads the app-password scope, present only when the request
// authenticated with an app password rather than a session. A session is
// unrestricted; an app password's scope is what ACLScope checks.
func AppPWScopeFrom(ctx context.Context) (auth.Scope, bool) {
	s, ok := ctx.Value(scopeKey{}).(auth.Scope)
	return s, ok
}

// ScopeFull is the stored value of an app password issued with no scope
// restriction, mirroring the reference implementation's sentinel. The scope
// gate treats it as "the whole account", which is what it meant at issuance.
const ScopeFull uint16 = 0xFFFF
