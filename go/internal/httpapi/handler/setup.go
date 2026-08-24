// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// SetupOutcome is what a completed bootstrap returns: the account is created,
// and the client then authenticates through the one credential-issuing path.
type SetupOutcome struct {
	UserID   int64
	Username string
}

// SetupError is every way the bootstrap can refuse, each mapping to exactly
// one wire code in the handler.
type SetupError struct {
	Kind  SetupErrorKind
	Field string
}

func (e SetupError) Error() string { return e.Kind.String() }

// SetupErrorKind is the refusal category. The wire code is a function of it.
type SetupErrorKind int

const (
	SetupCompleted SetupErrorKind = iota
	SetupExpired
	SetupInvalidToken
	SetupInvalidUsername
	SetupWeakPassword
)

func (k SetupErrorKind) String() string {
	switch k {
	case SetupCompleted:
		return "setup is already complete"
	case SetupExpired:
		return "the setup token has expired"
	case SetupInvalidToken:
		return "invalid setup token"
	case SetupInvalidUsername:
		return "invalid username"
	case SetupWeakPassword:
		return "password is too short"
	}
	return "setup refused"
}

// Setup is the setup surface the Deps wire in. The interface is what the
// handler needs and nothing else, so a test can substitute a closed gate.
type Setup interface {
	IsRequired(ctx context.Context) bool
	Complete(ctx context.Context, token, username string, pw secret.Secret, ip string) (SetupOutcome, error)
}

// SetupState answers GET /api/setup: a bare boolean, the one thing an
// unauthenticated caller may learn about this server's state.
func SetupState(d Deps, gate Setup) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		// The field is the one the client reads. It was "open" here and
		// "required" on the wire the frontend was written against, so the
		// first-run screen never appeared: the client asked whether setup was
		// required, got an object without that field, read the absence as
		// false, and sent the person to a sign-in screen for an account that
		// does not exist yet.
		return writeJSON(w, http.StatusOK, map[string]bool{"required": gate.IsRequired(r.Context())})
	})
}

type setupRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// SetupComplete spends the one-time token and creates the first
// administrator. Every refusal maps to one stable code, and the gate closes
// permanently on success, so a token recovered from a log or a backup after
// setup is worth nothing.
func SetupComplete(d Deps, gate Setup) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var req setupRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if req.Username == "" || req.Password == "" {
			return apierr.BadRequest("setup.fields", "username")
		}
		outcome, err := gate.Complete(r.Context(), req.Token, req.Username,
			secret.New([]byte(req.Password)), mw.ClientFrom(r.Context()).String())
		if err != nil {
			var se SetupError
			if errors.As(err, &se) {
				switch se.Kind {
				case SetupCompleted:
					return &apierr.RequestError{Status: http.StatusGone,
						Code: apierr.CodeSetupCompleted, Message: "first-run setup is already complete", Key: "setup.completed"}
				case SetupExpired:
					return &apierr.RequestError{Status: http.StatusForbidden,
						Code: apierr.CodeSetupTokenExpired, Message: "the setup token has expired", Key: "setup.token_expired"}
				case SetupInvalidToken:
					return &apierr.RequestError{Status: http.StatusForbidden,
						Code: apierr.CodeSetupInvalidToken, Message: "invalid setup token", Key: "setup.invalid_token"}
				case SetupInvalidUsername:
					return &apierr.RequestError{Status: http.StatusUnprocessableEntity,
						Code: apierr.CodeSetupInvalidUser, Message: "invalid username", Key: "setup.invalid_username",
						Args: []apierr.Arg{{Name: "reason", Value: se.Field}}}
				case SetupWeakPassword:
					return &apierr.RequestError{Status: http.StatusUnprocessableEntity,
						Code: apierr.CodeSetupWeakPassword, Message: "password is too short", Key: "setup.weak_password",
						Args: []apierr.Arg{{Name: "min_length", Value: "10"}}}
				}
			}
			return err
		}
		// Deliberately not a session: the account is created, and the client
		// then authenticates through the one credential-issuing path.
		return writeJSON(w, http.StatusOK, map[string]any{
			"user": map[string]any{"id": outcome.UserID, "name": outcome.Username},
		})
	})
}
