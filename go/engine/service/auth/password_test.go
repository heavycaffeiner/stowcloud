package auth_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/argon2"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

func TestAHashVerifiesAndIsNotStaleUnderCurrentParameters(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	enc, err := f.svc.Hash(ctx, pw(testPassword))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	ok, stale, err := f.svc.Verify(ctx, enc, pw(testPassword))
	if err != nil || !ok || stale {
		t.Fatalf("Verify = %v, %v, %v", ok, stale, err)
	}
	if ok, _, err = f.svc.Verify(ctx, enc, pw("wrong password entirely")); err != nil || ok {
		t.Fatalf("a wrong password verified: %v, %v", ok, err)
	}
	if auth.Stale(enc) {
		t.Fatal("a fresh hash reports itself stale")
	}
}

// A stored hash is self-describing, so raising the cost still verifies every
// password already on file; the caller is told to rehash instead.
func TestAHashUnderOlderParametersVerifiesAndReportsStale(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	// A hash carrying deliberately lower costs than the current ones, in the
	// same encoding a previous build would have written.
	old := encodeArgon(testPassword, 8192, 1, 1)
	ok, stale, err := f.svc.Verify(ctx, old, pw(testPassword))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok || !stale {
		t.Fatalf("Verify = %v, stale %v, want a match reported stale", ok, stale)
	}
	if !auth.Stale(old) {
		t.Fatal("Stale did not report the older parameters")
	}
}

// A corrupt row must fail that login and nothing else, and must be replaced
// the moment its owner proves the password through another path.
func TestAMalformedHashRefusesWithoutAnErrorAndReportsStale(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	for _, enc := range []string{
		"",
		"not a hash",
		"$argon2id$v=19$m=8192,t=1,p=1$$",
		"$argon2i$v=19$m=8192,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=16$m=8192,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=-1,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=99999999999,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=8192,t=1,p=999$c2FsdA$a2V5",
		"$argon2id$v=19$m=8192,t=1,p=1,x=2$c2FsdA$a2V5",
		"$argon2id$v=19$m=8192,t=1,p=1$c2FsdA$" + strings.Repeat("a", 200),
	} {
		ok, stale, err := f.svc.Verify(ctx, enc, pw(testPassword))
		if err != nil {
			t.Fatalf("Verify(%q) returned an error: %v", enc, err)
		}
		if ok {
			t.Fatalf("Verify(%q) accepted", enc)
		}
		if !stale {
			t.Fatalf("Verify(%q) did not report the row as stale", enc)
		}
	}
}

// Peak memory is the memory cost times the number of invocations in flight,
// so the bound is enforced where the memory is spent.
func TestTheGateBoundsConcurrentInvocations(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	const callers = auth.GateConcurrency * 4
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		task.Go(ctx, "auth: concurrent hash", func() {
			defer wg.Done()
			if _, err := f.svc.Hash(ctx, pw(testPassword)); err != nil {
				t.Errorf("Hash: %v", err)
			}
		})
	}
	wg.Wait()

	if peak := f.svc.PeakConcurrency(); peak > auth.GateConcurrency {
		t.Fatalf("the gate admitted %d at once, want at most %d", peak, auth.GateConcurrency)
	}
}

// A client that has gone away buys nothing. Without the check the select
// picks either ready case, so a cancelled caller could still pay for an
// invocation nobody is waiting for.
func TestACancelledCallerIsNeverAdmittedToTheGate(t *testing.T) {
	f := newFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := f.svc.Hash(ctx, pw(testPassword)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Hash on a cancelled context returned %v", err)
	}
	if _, _, err := f.svc.Verify(ctx, encodeArgon(testPassword, 8192, 1, 1), pw(testPassword)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify on a cancelled context returned %v", err)
	}
}

// encodeArgon renders a stored hash under parameters this build would not
// choose, so the stale path can be exercised against a real older hash rather
// than a mock.
func encodeArgon(password string, memoryKiB, iterations uint32, parallelism uint8) string {
	salt := []byte("sixteen-byte-slt")
	key := argon2.IDKey([]byte(password), salt, iterations, memoryKiB, parallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		memoryKiB, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
}
