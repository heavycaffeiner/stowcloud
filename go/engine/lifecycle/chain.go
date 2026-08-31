//go:build linux

// Binding the middleware chain to this engine's services.
//
// The chain decides; this supplies what it decides over. Every crossing here
// is one direction: the chain asks a question and a service answers it, and no
// service learns that there is a request.
package lifecycle

import (
	"context"
	"net/netip"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/spa"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// everyPermission is the full mask a session carries.
//
// Written out rather than inverted from zero: a bitwise complement would also
// set bits nothing defines, and a future bit would then be granted to every
// session before anyone decided it should be.
const everyPermission = acl.Read | acl.Write | acl.Create | acl.Delete |
	acl.Rename | acl.Move | acl.Share | acl.Download

// deps builds what the chain needs from this engine.
func (e *Engine) deps() middleware.Deps {
	return middleware.Deps{
		Hosts:     e.hosts,
		Trusted:   e.trustedProxies,
		Limiter:   e.limiter,
		Principal: e.ResolvePrincipal,
		CSRFKey:   e.csrfKey,
		Audit:     nil,
		// The interface's own inline bootstrap, admitted by hash. Empty in a
		// build without the bundle, which keeps the policy as strict as a
		// server with no pages to serve should be.
		ScriptHashes: spa.InlineScriptHashList(),
	}
}

// hosts reports the names this deployment answers to.
//
// Read per request rather than captured, so a settings save takes effect on
// the next request instead of at the next listener swap.
func (e *Engine) hosts() middleware.Hosts {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.appHosts
}

// trustedProxies reports the networks whose forwarding headers are believed.
//
// Empty means no proxy is trusted, which makes the peer address the client
// address. That is the safe default: believing a header from an untrusted peer
// lets any caller claim any address, and the rate limiter and the audit log
// both read what this decides.
func (e *Engine) trustedProxies() []netip.Prefix {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.trusted
}

// csrfKey returns the deployment's durable derivation key.
//
// Nil until the master key is opened. The chain refuses every mutation needing
// a token rather than letting one through unchecked, which is the right answer
// for a server whose key is not available: a mutation admitted without a token
// is one a cross-site form could have sent.
func (e *Engine) csrfKey() []byte {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.csrf
}

// ResolvePrincipal reports what a credential proves.
//
// The two credential kinds are answered by two different service calls, and
// neither may stand in for the other: a session is the account itself and
// carries every permission, while an app password is a delegation and carries
// only the bits it was granted. Reading one as the other would hand every app
// password full control of its account.
//
// Exported because it is the one crossing where a wire credential becomes an
// identity, and a decision that load-bearing should be reachable by a test
// that does not have to stand up a listener to ask it.
func (e *Engine) ResolvePrincipal(c middleware.Credential) (middleware.Principal, bool) {
	// The chain has no request context to give, and this is a lookup with a
	// bounded cost against a local database.
	ctx := context.Background()

	switch c.Kind {
	case middleware.CredentialSessionCookie:
		p, err := e.Auth.LookupSession(ctx, secret.New(c.Token))
		if err != nil || p.Disabled {
			return middleware.Principal{}, false
		}
		return middleware.Principal{
			UserID: p.UserID,
			Kind:   c.Kind,
			// A session is the account, so it holds every bit. An app
			// password is where a narrower mask comes from.
			Mask: everyPermission,
		}, true

	case middleware.CredentialBasicApp, middleware.CredentialBearerApp:
		p, scope, err := e.Auth.VerifyAppPassword(ctx, string(c.Token))
		if err != nil || p.Disabled {
			return middleware.Principal{}, false
		}
		return middleware.Principal{
			UserID: p.UserID,
			Kind:   c.Kind,
			Mask:   acl.Perms(scope.Perms),
		}, true

	case middleware.CredentialNone:
		return middleware.Principal{}, false

	default:
		// A kind this build does not know is not authenticated. Treating it
		// as anonymous is the safe reading: an unknown credential proves
		// nothing, and the alternative is admitting whatever it claims.
		return middleware.Principal{}, false
	}
}
