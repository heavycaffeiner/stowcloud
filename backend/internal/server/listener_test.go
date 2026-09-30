//go:build linux

package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	fsatomic "github.com/stowcloud/durablefs"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
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
	for _, want := range []string{"/tls/key.pem", "/tls/cert.pem", fsatomic.Published.String(), fsatomic.PublicationUncertain.String()} {
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
	for _, want := range []string{"/tls/key.pem", "/tls/cert.pem", fsatomic.PublicationUncertain.String(), fsatomic.NotPublished.String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

type fixedApp struct{}

func (fixedApp) ProbeHost() string                       { return "" }
func (fixedApp) OnAppHostChange(func())                  {}
func (fixedApp) OnBindChange(string, bool, func(string)) {}

// A restart request ends Serve with an error, after the listener has served
// and published where it listens.
func TestServeReturnsWhenARestartIsRequested(t *testing.T) {
	dir := t.TempDir()
	router := gin.New()
	router.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	l, err := NewListener(ListenerConfig{DataDir: dir, Address: "127.0.0.1:0", Plain: true, Logger: slog.New(slog.DiscardHandler)}, fixedApp{}, router)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Serve(t.Context()) }()

	probe := filepath.Join(dir, ".probe.json")
	deadline := clock.System().Now().Add(5 * time.Second)
	for {
		if _, serr := os.Stat(probe); serr == nil {
			break
		}
		if clock.System().Now().After(deadline) {
			t.Fatal("the listener never published its address")
		}
		time.Sleep(10 * time.Millisecond)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ReadProbe(probe).Addr+"/ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the listener did not answer: %v", err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the listener answered %d", resp.StatusCode)
	}

	l.RequestRestart()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve returned nil after a restart request")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after a restart request")
	}
}

// Cancelling the context stops Serve without an error.
func TestServeStopsWithItsContext(t *testing.T) {
	l, err := NewListener(ListenerConfig{DataDir: t.TempDir(), Address: "127.0.0.1:0", Plain: true, Logger: slog.New(slog.DiscardHandler)}, fixedApp{}, gin.New())
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- l.Serve(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v after its context ended", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context ended")
	}
}
