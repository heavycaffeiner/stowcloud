// Package good is a fixture. Nothing in it reaches a formatting verb with a
// secret, and vetsecret has to stay quiet about all of it.
package good

import (
	"crypto/sha256"
	"fmt"
	"log/slog"

	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// The redaction, asked for explicitly, which is the point of the rule.
func redacted(s secret.Secret) { fmt.Println(s.String()) }

// A length is not a secret and an operator needs it to diagnose a bad key.
func size(s secret.Secret) { slog.Info("key loaded", "bytes", s.Len()) }

// Using the bytes is what a secret is for.
func use(s secret.Secret) [32]byte { return sha256.Sum256(s.Reveal()) }

func compare(a, b secret.Secret) bool { return a.Equal(b) }
