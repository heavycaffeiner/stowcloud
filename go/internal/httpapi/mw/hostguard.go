package mw

import (
	"net/http"
	"strings"
)

// HostGuard is step 3. It compares the request's host against a declared
// origin list. The list is configuration, never inference: one server is
// reached under a LAN address, a Tailscale name and a public name through a
// proxy, and a guard that learned the origin from the request it is guarding
// is not a guard.
//
// The refusal is 421, which is what an origin that does not know the host is
// for: the client has sent a request to a server that cannot answer for that
// name, and the 421 tells the client to retry against the right one.
func HostGuard(appHosts, contentHosts []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if i := strings.IndexByte(host, ':'); i >= 0 {
				// The Host header may carry a port; the declared lists do not.
				host = host[:i]
			}
			if eqAny(host, contentHosts) {
				next.ServeHTTP(w, r.WithContext(withOrigin(r.Context(), OriginContent)))
				return
			}
			if eqAny(host, appHosts) {
				next.ServeHTTP(w, r.WithContext(withOrigin(r.Context(), OriginApp)))
				return
			}
			w.Header().Set("Connection", "close")
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
		})
	}
}

func eqAny(host string, list []string) bool {
	for _, h := range list {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}
