package mw

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/route"
)

// ACLScope is step 9. It enforces what an app password may reach, reading the
// requirement from the route table: the same table the mux serves, so the
// gate and the dispatch cannot disagree about which route a request hit.
//
// The virtual-path ACL check is not here. It happens inside core.Resolve,
// because that check needs the resolved share and the two would otherwise
// disagree.
func ACLScope(lookup route.Lookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req, ok := lookup(r.Method, r.URL.Path)
			if !ok {
				// No route owns this request; the mux answers it. The gate has
				// nothing to gate.
				next.ServeHTTP(w, r)
				return
			}
			scope, hasScope := AppPWScopeFrom(r.Context())
			if !hasScope {
				// A session is the whole account. The route's own checks and
				// the ACL decide what it may do.
				next.ServeHTTP(w, r)
				return
			}
			switch req.Access {
			case route.AccessAny:
				next.ServeHTTP(w, r)
			case route.AccessSelfAdmin:
				deny(w)
			case route.AccessPerms:
				if scope.Perms == ScopeFull || aclPermsCovered(scope.Perms, uint16(req.Perms)) {
					next.ServeHTTP(w, r)
					return
				}
				deny(w)
			}
		})
	}
}

func aclPermsCovered(scope, want uint16) bool {
	return scope&want == want
}

func deny(w http.ResponseWriter) {
	apierr.Write(w, http.StatusForbidden,
		apierr.NewError(apierr.CodeACLDenied, "permission denied", "acl.scope_denied"))
}
