// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
)

// Events answers GET /api/events: the change-channel upgrade. The hub owns
// the socket from here on; a build without a wired hub (the tests, a mount
// that was not assembled) answers the recognized-not-implemented code rather
// than a nil call.
func Events(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if d.Events == nil {
			return &apierr.RequestError{Status: http.StatusNotImplemented,
				Code: apierr.CodeNotImplemented, Message: "not implemented in this build", Key: "events.unavailable"}
		}
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		d.Events(w, r, uid)
		// The upgrade hijacked the connection; there is no HTTP response to
		// write, so the handler returns success and the socket is the answer.
		return nil
	})
}
