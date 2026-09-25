//go:build linux

package listener

import (
	"errors"
	"strings"
	"testing"

	fsatomic "github.com/stowcloud/durablefs"
)

func TestClassifyDurableResultsAcceptsOnlyFullyPublishedPair(t *testing.T) {
	results := []fsatomic.UnitResult{
		{Unit: fsatomic.Unit{Path: "/tls/key.pem"}, Attempted: true, Outcome: fsatomic.Published},
		{Unit: fsatomic.Unit{Path: "/tls/cert.pem"}, Attempted: true, Outcome: fsatomic.Published},
	}
	if err := classifyDurableResults(results, nil); err != nil {
		t.Fatalf("fully published pair returned error: %v", err)
	}
}

func TestClassifyDurableResultsReportsUncertainPair(t *testing.T) {
	results := []fsatomic.UnitResult{
		{Unit: fsatomic.Unit{Path: "/tls/key.pem"}, Attempted: true, Outcome: fsatomic.Published},
		{Unit: fsatomic.Unit{Path: "/tls/cert.pem"}, Attempted: true, Outcome: fsatomic.PublicationUncertain},
	}
	err := classifyDurableResults(results, nil)
	if err == nil {
		t.Fatal("uncertain pair returned nil error")
	}
	for _, want := range []string{"/tls/key.pem", "/tls/cert.pem", "published", "publication uncertain"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestClassifyDurableResultsPreservesOperationErrorAndPartialResults(t *testing.T) {
	operationErr := errors.New("directory sync failed")
	results := []fsatomic.UnitResult{
		{Unit: fsatomic.Unit{Path: "/tls/key.pem"}, Attempted: true, Outcome: fsatomic.PublicationUncertain},
		{Unit: fsatomic.Unit{Path: "/tls/cert.pem"}, Outcome: fsatomic.NotPublished},
	}
	err := classifyDurableResults(results, operationErr)
	if err == nil || !errors.Is(err, operationErr) {
		t.Fatalf("error %v does not preserve operation error", err)
	}
	for _, want := range []string{"/tls/key.pem", "/tls/cert.pem", "publication uncertain", "not published"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
