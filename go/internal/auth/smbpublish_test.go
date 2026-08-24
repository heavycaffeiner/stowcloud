package auth

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// The credential paths reaching the sidecar.
//
// Writing the rendered file is not the whole job. smbd does not authenticate
// against that file: the sidecar imports it into the credential database, so a
// file written with nobody told is a revocation that lands whenever something
// else happens to publish. These tests are about the telling.

// countingPublisher records how many times the sink ran.
func countingPublisher(s *Service) *atomic.Int64 {
	var n atomic.Int64
	s.SetSMBPublisher(func(context.Context) { n.Add(1) })
	return &n
}

// Disabling an account is the case that matters most: it is a revocation, and
// one that stops at the rendered file leaves the account working over SMB.
func TestDisablingAnAccountReachesThePublisher(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw1"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Wired after the account exists, so what is counted is this one act.
	n := countingPublisher(s)
	if err := s.DisableAccount(ctx, 1); err != nil {
		t.Fatalf("DisableAccount: %v", err)
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("the publisher ran %d times for a disable, want once", got)
	}
}

// Every credential-changing path, not the one somebody remembered. The sink
// exists so a new path gets this for free; the test is what says the existing
// ones have it.
func TestEveryCredentialPathReachesThePublisher(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		act  func(*Service) error
	}{
		{"set a password", func(s *Service) error {
			return s.SetPassword(ctx, 1, secret.New([]byte("pw2")))
		}},
		{"disable", func(s *Service) error { return s.DisableAccount(ctx, 1) }},
		{"enable", func(s *Service) error { return s.EnableAccount(ctx, 1) }},
		{"turn SMB off", func(s *Service) error { return s.SetSMBAccess(ctx, 1, false) }},
		{"link a provider identity", func(s *Service) error { return s.LinkOIDC(ctx, 1) }},
		{"unlink it", func(s *Service) error { return s.UnlinkOIDC(ctx, 1) }},
		{"delete the account", func(s *Service) error { return s.DeleteUser(ctx, 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openService(t, nil)
			if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw1"))); err != nil {
				t.Fatalf("CreateUser: %v", err)
			}
			n := countingPublisher(s)
			if err := tc.act(s); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got := n.Load(); got == 0 {
				t.Fatalf("%s did not reach the publisher", tc.name)
			}
		})
	}
}

// The publisher asks this service for the credential files, so the render it
// calls must not call the publisher back. Without the split this is an
// unbounded recursion on every publish.
func TestPublishPassdbDoesNotCallThePublisherBack(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw1"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	n := countingPublisher(s)
	if err := s.PublishPassdb(ctx); err != nil {
		t.Fatalf("PublishPassdb: %v", err)
	}
	if got := n.Load(); got != 0 {
		t.Fatalf("rendering the file called the publisher %d times", got)
	}
}

// A deployment with no sidecar has no publisher, and every path still works.
func TestNoPublisherIsNotAFailure(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw1"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.DisableAccount(ctx, 1); err != nil {
		t.Fatalf("disabling with no publisher wired: %v", err)
	}
}
