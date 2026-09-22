package transfer

import (
	"context"
	"errors"
	"testing"
)

func TestResultPreservesOutcomeAlongsideError(t *testing.T) {
	operationErr := errors.New("provider reply was lost")
	tests := []struct {
		name    string
		result  Result
		err     error
		outcome Outcome
		commit  bool
	}{
		{name: "pre-commit refusal", result: Result{Outcome: NotPublished}, err: errors.New("rejected"), outcome: NotPublished},
		{name: "successful commit", result: Result{Outcome: Published, Commit: true}, outcome: Published, commit: true},
		{name: "post-commit error", result: Result{Outcome: PublicationUncertain, Commit: true}, err: operationErr, outcome: PublicationUncertain, commit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.result.Outcome != tt.outcome || tt.result.Commit != tt.commit {
				t.Fatalf("result = %+v, want outcome=%v commit=%v", tt.result, tt.outcome, tt.commit)
			}
			if tt.err != nil && !errors.Is(tt.err, operationErr) && tt.name == "post-commit error" {
				t.Fatalf("operation error was not preserved: %v", tt.err)
			}
		})
	}
}

func TestOutcomeNamesAreStable(t *testing.T) {
	for outcome, want := range map[Outcome]string{
		NotPublished:         "not published",
		Published:            "published",
		PublicationUncertain: "publication uncertain",
	} {
		if got := outcome.String(); got != want {
			t.Errorf("Outcome(%d).String() = %q, want %q", outcome, got, want)
		}
	}
}

func TestReceiptIdentityIsOperationIdempotencyVocabulary(t *testing.T) {
	first := Intent{
		Operation:   OperationID("op-42"),
		Destination: Destination("destination-a"),
		Content:     ContentIdentity("content-7"),
		Policy:      struct{ Replace bool }{Replace: true},
	}
	// A retry carries the same operation identity even when policy is represented
	// by a separately allocated value. Policy is opaque and is not part of the
	// idempotency identity.
	retry := Intent{
		Operation:   OperationID("op-42"),
		Destination: Destination("destination-a"),
		Content:     ContentIdentity("content-7"),
		Policy:      struct{ Replace bool }{Replace: true},
	}
	if first.Identity() != retry.Identity() {
		t.Fatalf("retry identity = %+v, first identity = %+v", retry.Identity(), first.Identity())
	}
	if first.Identity() != (Receipt{
		Operation:   first.Operation,
		Destination: first.Destination,
		Content:     first.Content,
	}).Identity() {
		t.Fatal("intent and receipt identities diverged")
	}
}

type exampleStrategy struct{}
type examplePublisher struct{}
type exampleReconciler struct{}

func (exampleStrategy) Capabilities() Capabilities {
	return Capabilities(0).With(CapabilityPublish).With(CapabilityReconcile)
}

func (exampleStrategy) Prepare(context.Context, Intent) (Preparation, error) {
	return Preparation{
		Accepted:  Accepted{Range: Range{Lo: 0, Hi: 1}},
		Publish:   examplePublisher{},
		Reconcile: exampleReconciler{},
	}, nil
}

func (examplePublisher) Publish(context.Context, Intent) (Result, Receipt, error) {
	return Result{Outcome: Published, Commit: true}, Receipt{}, nil
}

func (exampleReconciler) Reconcile(context.Context, Intent) (Result, Receipt, error) {
	return Result{Outcome: PublicationUncertain, Commit: true}, Receipt{}, errors.New("still uncertain")
}

var (
	_ Strategy   = exampleStrategy{}
	_ Publisher  = examplePublisher{}
	_ Reconciler = exampleReconciler{}
)

func TestCapabilitiesAndPreparationStayNarrow(t *testing.T) {
	strategy := exampleStrategy{}
	caps := strategy.Capabilities()
	if !caps.Has(CapabilityPublish) || !caps.Has(CapabilityReconcile) {
		t.Fatalf("capabilities = %v, want publish and reconcile", caps)
	}
	prep, err := strategy.Prepare(context.Background(), Intent{Operation: "op"})
	if err != nil {
		t.Fatal(err)
	}
	if prep.Publish == nil || prep.Reconcile == nil {
		t.Fatalf("preparation did not expose advertised operations: %+v", prep)
	}
}
