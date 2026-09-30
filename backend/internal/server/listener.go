//go:build linux

// The listener adapts the product application to Hanami's managed HTTP
// generations. It owns the listener lifecycle, TLS material, and probe file;
// the application owns routes and product policy. Nothing else in the tree
// imports Hanami's lifecycle types.

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	hanamibootstrap "github.com/heavycaffeiner/hanami/bootstrap"
	hanamihttp "github.com/heavycaffeiner/hanami/http"
	hanamiprocess "github.com/heavycaffeiner/hanami/process"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	fsatomic "github.com/stowcloud/durablefs"
)

// ListenerConfig describes the process-local listener selected during bootstrap.
type ListenerConfig struct {
	DataDir string
	Address string
	Pinned  bool
	Plain   bool
	Logger  *slog.Logger
}

// listenerApp is the product surface the listener needs.
type listenerApp interface {
	ProbeHost() string
	OnAppHostChange(func())
	OnBindChange(current string, pinned bool, fn func(string))
}

// Listener owns one managed HTTP endpoint and its generation transitions.
type Listener struct {
	manager    *hanamihttp.Manager
	admission  *hanamibootstrap.Admission
	controller *hanamiprocess.Controller
	server     hanamihttp.ServerConfig
	config     ListenerConfig
	app        listenerApp
	logger     *slog.Logger
	mu         sync.Mutex
}

const (
	listenerStartTimeout = 15 * time.Second
	shutdownTimeout      = 30 * time.Second
)

// NewListener prepares the managed HTTP endpoint for an already mounted router.
func NewListener(config ListenerConfig, app listenerApp, router *gin.Engine) (*Listener, error) {
	if config.DataDir == "" {
		return nil, errors.New("listener data directory is empty")
	}
	if config.Address == "" {
		return nil, errors.New("listener address is empty")
	}
	if app == nil {
		return nil, errors.New("listener application is nil")
	}
	if router == nil {
		return nil, errors.New("listener Gin engine is nil")
	}
	if config.Logger == nil {
		return nil, errors.New("listener logger is nil")
	}
	logger := config.Logger

	protocol := hanamihttp.ProtocolHTTPS
	var cert *tls.Certificate
	if config.Plain {
		protocol = hanamihttp.ProtocolHTTP
	} else {
		value, err := ensureCertificate(config.DataDir, config.Address)
		if err != nil {
			return nil, err
		}
		cert = &value
	}
	serverConfig := hanamihttp.ServerConfig{
		Address:           config.Address,
		Protocol:          protocol,
		Certificate:       cert,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ProbePath:         "/health/ready",
		ProbeIdentity:     "sc-engine",
	}
	admission := hanamibootstrap.NewAdmission()
	controller := hanamiprocess.NewController()
	manager, err := hanamihttp.NewManager(hanamihttp.ManagerConfig{
		Server: serverConfig, Admission: admission, Controller: controller,
	})
	if err != nil {
		return nil, err
	}
	l := &Listener{
		manager: manager, admission: admission, controller: controller,
		server: serverConfig, config: config, app: app, logger: logger,
	}
	app.OnAppHostChange(func() {
		if err := l.publish(); err != nil {
			logger.Error("the health probe snapshot could not be updated", "error", err)
		}
	})
	app.OnBindChange(config.Address, config.Pinned, func(next string) {
		if _, err := l.replaceAddress(context.Background(), next); err != nil {
			logger.Error("the bind address could not be moved", "address", next, "error", err)
			return
		}
		if err := l.publish(); err != nil {
			logger.Error("the health probe snapshot could not be updated", "error", err)
		}
	})
	return l, nil
}

// Serve starts the first generation and admits requests until ctx ends or a
// stop is requested, then drains every generation. A requested stop's reason
// is returned, so a restart request ends the process with an error.
func (l *Listener) Serve(ctx context.Context) error {
	startup, cancel := context.WithTimeout(ctx, listenerStartTimeout)
	err := l.manager.Start(startup)
	cancel()
	if err == nil {
		err = l.publish()
	}
	if err != nil {
		return err
	}
	l.admission.Open()
	var reason error
	select {
	case <-ctx.Done():
	case <-l.controller.Done():
		reason = l.controller.Request().Err
	}
	l.admission.Close()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	return errors.Join(reason, l.manager.Stop(shutdown))
}

// RequestRestart ends Serve so the supervisor starts a fresh process. The
// product never replaces its own image.
func (l *Listener) RequestRestart() {
	l.controller.RequestExternalRestart(errors.New("product restart requested"))
}

func (l *Listener) replaceAddress(ctx context.Context, address string) (hanamihttp.ReplaceResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	next := l.server
	next.Address = address
	result, err := l.manager.Replace(ctx, hanamihttp.ReplaceRequest{Server: next})
	if err == nil {
		l.server = next
	}
	return result, err
}

func (l *Listener) publish() error {
	current := l.manager.Current()
	if current.Address == "" {
		return errors.New("publishing the health probe without a listener")
	}
	return WriteProbe(filepath.Join(l.config.DataDir, ".probe.json"), Probe{
		Addr: current.Address, Host: l.app.ProbeHost(), Plain: l.config.Plain,
	}, durableWriter)
}

func durableWriter(paths []string, modes []uint32, write func(int, *os.File) error) error {
	if len(paths) != len(modes) {
		return fmt.Errorf("durable publication received %d paths and %d modes", len(paths), len(modes))
	}
	units := make([]fsatomic.Unit, len(paths))
	for i, path := range paths {
		units[i] = fsatomic.Unit{Path: path, Mode: modes[i]}
	}
	results, err := fsatomic.ReplaceFilesDurable(units, write)
	return classifyDurableResults(results, err)
}

// classifyDurableResults keeps the outcome of each destination visible to the
// caller. In particular, a directory-sync failure does not mean that a file
// was left unchanged: durablefs reports that state as PublicationUncertain.
// TLS publication is authoritative, so any result other than Published must
// prevent startup from accepting the newly generated pair.
func classifyDurableResults(results []fsatomic.UnitResult, operationErr error) error {
	if len(results) == 0 {
		if operationErr != nil {
			return fmt.Errorf("durable publication failed without unit results: %w", operationErr)
		}
		return errors.New("durable publication returned no unit results")
	}

	allPublished := true
	details := make([]string, len(results))
	for i, result := range results {
		if result.Outcome != fsatomic.Published {
			allPublished = false
		}
		attempted := "not attempted"
		if result.Attempted {
			attempted = "attempted"
		}
		details[i] = fmt.Sprintf("%s: %s (%s)", result.Unit.Path, result.Outcome, attempted)
	}

	if allPublished && operationErr == nil {
		return nil
	}
	if operationErr != nil {
		return fmt.Errorf("durable publication failed (%s): %w", strings.Join(details, "; "), operationErr)
	}
	return fmt.Errorf("durable publication incomplete (%s)", strings.Join(details, "; "))
}

// ensureCertificate loads or mints the listener's self-signed pair. The names
// are the bind address and localhost only: a deployment reached under its own
// name is expected to sit behind a proxy that holds a trusted certificate, and
// regenerating this pair when hosts change would break any client that
// pinned it.
func ensureCertificate(dataDir, address string) (tls.Certificate, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	if host == "" {
		host = "127.0.0.1"
	}
	hosts := []string{host}
	if host != "localhost" {
		hosts = append(hosts, "localhost")
	}
	paths := TLSPaths{Cert: filepath.Join(dataDir, "tls", "cert.pem"), Key: filepath.Join(dataDir, "tls", "key.pem")}
	if err := os.MkdirAll(filepath.Dir(paths.Cert), 0o700); err != nil {
		return tls.Certificate{}, fmt.Errorf("creating the TLS directory: %w", err)
	}
	_, statErr := os.Stat(paths.Cert)
	firstBoot := errors.Is(statErr, os.ErrNotExist)
	return EnsureTLS(paths, hosts, clock.System(), firstBoot, durableWriter)
}
