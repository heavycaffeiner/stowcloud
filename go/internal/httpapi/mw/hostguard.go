package mw

import (
	"net/http"
	"strings"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
)

// HostSet is the live declared-origin list. HostGuard reads it per request
// and the settings surface writes it, so an administrator's patch to the
// host list applies without a restart. The list is configuration, never
// inference: one server is reached under a LAN address, a Tailscale name and
// a public name through a proxy, and a guard that learned the origin from the
// request it is guarding is not a guard.
type HostSet struct {
	mu  sync.RWMutex
	app []string
}

// NewHostSet builds the holder from boot configuration.
func NewHostSet(app []string) *HostSet {
	return &HostSet{app: app}
}

// App returns the current app-origin list.
func (h *HostSet) App() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.app...)
}

// Set replaces the list. Only the settings surface calls it.
func (h *HostSet) Set(app []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.app = app
}

// HostGuard is step 3. It compares the request's host against a declared
// origin list.
//
// The refusal is 421, which is what an origin that does not know the host is
// for: the client has sent a request to a server that cannot answer for that
// name, and the 421 tells the client to retry against the right one.
//
// An empty list is the first boot, and it is admitted from the local network
// only. Nothing has named this deployment yet, so there is no name to compare
// against, and refusing every host would leave a server nobody can reach to
// tell it what its name is. What is behind the guard at that point is the
// setup form; the address the request came from is what bounds it, so a
// deployment exposed to the internet before anyone has configured it is not
// offering that form to the internet.
//
// Setup writes the list, and from then on this is the origin check it has
// always been, from anywhere.
func HostGuard(hosts *HostSet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if i := strings.IndexByte(host, ':'); i >= 0 {
				// The Host header may carry a port; the declared lists do not.
				host = host[:i]
			}
			declared := hosts.App()
			admit := eqAny(host, declared)
			if len(declared) == 0 {
				admit = smb.IsPrivate(ClientFrom(r.Context()))
			}
			if admit {
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
