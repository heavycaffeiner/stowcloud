// Package bad is a fixture. Every call in it puts a secret somewhere a secret
// must not go, and vetsecret has to report all of them.
package bad

import (
	"fmt"
	"log"
	"log/slog"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

type creds struct {
	User string
	Pass secret.Secret
}

// The verb decides how it renders, and %d does not go through String.
func direct(s secret.Secret) string { return fmt.Sprintf("%d", s) }

// A pointer carries the same bytes.
func pointer(s *secret.Secret) { fmt.Printf("%v", s) }

// The struct is what actually happens: a config or a request dump.
func nested(c creds) { slog.Info("login", "creds", c) }

// The accessor is the one way the bytes leave the type.
func revealed(s secret.Secret) { log.Println(string(s.Reveal())) }

// Nested inside an expression is still reaching a formatting verb.
func buried(s secret.Secret) { fmt.Println("key: " + string(s.Reveal())) }
