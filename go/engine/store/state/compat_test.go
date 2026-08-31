//go:build linux

package state_test

import (
	"context"
	"errors"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The compatibility layer's durable rows: the deployment identity and the
// key-value pairs behind it.
//
// The identity is what a client uses to decide it is talking to the same
// server. Two tests matter: it is stable across reads, and two processes
// racing to mint one agree on a single value rather than each keeping its own.

// The identity is minted once and then never changes.
func TestTheInstanceIDIsStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, _ := open(t)

	first, err := d.InstanceID(ctx)
	if err != nil {
		t.Fatalf("reading the identity: %v", err)
	}
	if first == "" {
		t.Fatal("the identity is empty")
	}

	second, err := d.InstanceID(ctx)
	if err != nil {
		t.Fatalf("reading it again: %v", err)
	}
	if second != first {
		t.Errorf("the identity changed from %s to %s", first, second)
	}
}

// Two identities are different values. A mint that produced the same string
// for every deployment would make one client's server look like another's.
func TestTwoDatabasesMintDifferentIdentities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d1, _ := open(t)
	d2, _ := open(t)

	a, err := d1.InstanceID(ctx)
	if err != nil {
		t.Fatalf("the first identity: %v", err)
	}
	b, err := d2.InstanceID(ctx)
	if err != nil {
		t.Fatalf("the second identity: %v", err)
	}
	if a == b {
		t.Errorf("two deployments share the identity %s", a)
	}
}

// A pre-existing value wins over a mint. The identity is durable: a fresh
// process opening an existing deployment must read what the last one wrote,
// not mint its own.
func TestAPreExistingIdentitySurvives(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, _ := open(t)

	wrote, err := d.PutCompatKeyIfAbsent(ctx, "instance_id", "pre-existing")
	if err != nil || !wrote {
		t.Fatalf("seeding: wrote=%v err=%v", wrote, err)
	}

	got, err := d.InstanceID(ctx)
	if err != nil {
		t.Fatalf("reading it: %v", err)
	}
	if got != "pre-existing" {
		t.Errorf("the identity was replaced with %q", got)
	}
}

// A put onto an existing key writes nothing, and reports that it did not.
// The caller that mints an identity relies on this: two racers must agree on
// one value, and both believing they wrote it is how they split.
func TestPuttingAnExistingKeyWritesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, _ := open(t)

	wrote, err := d.PutCompatKeyIfAbsent(ctx, "k", "first")
	if err != nil || !wrote {
		t.Fatalf("the first put: wrote=%v err=%v", wrote, err)
	}

	wrote, err = d.PutCompatKeyIfAbsent(ctx, "k", "second")
	if err != nil {
		t.Fatalf("the second put: %v", err)
	}
	if wrote {
		t.Error("the second put reported that it wrote")
	}

	got, err := d.CompatKey(ctx, "k")
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if got != "first" {
		t.Errorf("the value is %q, want the first one", got)
	}
}

// An absent key is its own answer, distinct from a storage failure. The
// caller that mints on first read needs to tell them apart.
func TestAnAbsentKeyIsAnErrorOfItsOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, _ := open(t)

	_, err := d.CompatKey(ctx, "nothing")
	if !errors.Is(err, state.ErrNoCompatKey) {
		t.Errorf("an absent key answered %v, want ErrNoCompatKey", err)
	}
}
