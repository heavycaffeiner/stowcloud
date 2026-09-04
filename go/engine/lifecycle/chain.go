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
		Clock:     e.clk(),
		Principal: e.ResolvePrincipal,
		CSRFKey:   e.csrfKey,
		Audit:     nil,
		Access:    accessLog{e},
		// The interface's own inline bootstrap, admitted by hash. Empty in a
		// build without the bundle, which keeps the policy as strict as a
		// server with no pages to serve should be.
		ScriptHashes: spa.InlineScriptHashList(),
	}
}

// accessLog writes the chain's per-request line to the engine's logger, which
// is where the durable log store already sits.
type accessLog struct{ e *Engine }

// Access records one request.
//
// The level is the answer's own class, so a dashboard filtered to warnings
// shows refusals and one filtered to errors shows faults. A success is still
// written: "every request that arrived" is the question an access log exists
// to answer, and a log that holds only failures cannot say whether a client
// ever reached the server at all.
//
// The route name and the redacted path both go in. The name is what a rule is
// written against and the path is what an operator recognises, and neither
// alone is enough to find a request somebody is reporting.
func (a accessLog) Access(e middleware.AccessEvent) {
	attrs := []any{
		"subsystem", "api",
		"request_id", e.Trace,
		"method", e.Method,
		"path", e.Path,
		"status", e.Status,
		"ms", e.Duration.Milliseconds(),
		"client", e.Client.String(),
		"credential", e.Credential.String(),
	}
	if e.Route != "" {
		attrs = append(attrs, "route", e.Route)
	}
	if e.Principal != 0 {
		attrs = append(attrs, "account", e.Principal)
	}
	// Only a failure carries one, and a failure without it is a status code
	// and no next step.
	if e.Cause != "" {
		attrs = append(attrs, "error", e.Cause)
	}

	log := a.e.log()
	switch {
	case e.Status >= 500:
		log.Error("the request failed", attrs...)
	case e.Status >= 400:
		log.Warn("the request was refused", attrs...)
	default:
		log.Info("the request was served", attrs...)
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
		p, scope, id, err := e.Auth.VerifyAppPasswordID(ctx, string(c.Token))
		if err != nil || p.Disabled {
			return middleware.Principal{}, false
		}
		return middleware.Principal{
			UserID:        p.UserID,
			Kind:          c.Kind,
			Mask:          acl.Perms(scope.Perms),
			AppPasswordID: id,
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
