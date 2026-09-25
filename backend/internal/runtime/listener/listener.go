//go:build linux

// Package listener adapts the product application to Hanami's managed HTTP
// generations. It owns the listener lifecycle, TLS material, and probe file;
// the application owns routes and product policy.
package listener

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

	"github.com/gin-gonic/gin"
	hanamibootstrap "github.com/heavycaffeiner/hanami/bootstrap"
	hanamihttp "github.com/heavycaffeiner/hanami/http"
	hanamiprocess "github.com/heavycaffeiner/hanami/process"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	fsatomic "github.com/stowcloud/durablefs"
)

// Config describes the process-local listener selected during bootstrap.
type Config struct {
	DataDir string
	Address string
	Pinned  bool
	Plain   bool
	Logger  *slog.Logger
}

// Application is the product surface the listener needs. It deliberately
// carries no Hanami or Fx types: those remain at the process assembly edge.
type Application interface {
	ProbeHost() string
	OnAppHostChange(func())
	OnBindChange(current string, pinned bool, fn func(string))
}

// Runtime owns one managed HTTP endpoint and its generation transitions.
type Runtime struct {
	manager *hanamihttp.Manager
	server  hanamihttp.ServerConfig
	config  Config
	app     Application
	logger  *slog.Logger
	mu      sync.Mutex
}

// New prepares the managed HTTP endpoint for an already mounted router.
func New(config Config, app Application, router *gin.Engine, admission *hanamibootstrap.Admission, controller *hanamiprocess.Controller) (*Runtime, error) {
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
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

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
		Address:       config.Address,
		Protocol:      protocol,
		Certificate:   cert,
		Handler:       router,
		ProbePath:     "/health/ready",
		ProbeIdentity: "sc-engine",
	}
	manager, err := hanamihttp.NewManager(hanamihttp.ManagerConfig{
		Server: serverConfig, Admission: admission, Controller: controller,
	})
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{manager: manager, server: serverConfig, config: config, app: app, logger: logger}
	app.OnAppHostChange(func() {
		if err := runtime.publish(); err != nil {
			logger.Error("the health probe snapshot could not be updated", "error", err)
		}
	})
	app.OnBindChange(config.Address, config.Pinned, func(next string) {
		if _, err := runtime.replaceAddress(context.Background(), next); err != nil {
			logger.Error("the bind address could not be moved", "address", next, "error", err)
			return
		}
		if err := runtime.publish(); err != nil {
			logger.Error("the health probe snapshot could not be updated", "error", err)
		}
	})
	return runtime, nil
}

// Start begins the initial managed generation and publishes its settled address.
func (runtime *Runtime) Start(ctx context.Context) error {
	if err := runtime.manager.Start(ctx); err != nil {
		return err
	}
	return runtime.publish()
}

// Stop drains and closes all managed generations.
func (runtime *Runtime) Stop(ctx context.Context) error {
	return runtime.manager.Stop(ctx)
}

func (runtime *Runtime) replaceAddress(ctx context.Context, address string) (hanamihttp.ReplaceResult, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	next := runtime.server
	next.Address = address
	result, err := runtime.manager.Replace(ctx, hanamihttp.ReplaceRequest{Server: next})
	if err == nil {
		runtime.server = next
	}
	return result, err
}

func (runtime *Runtime) publish() error {
	current := runtime.manager.Current()
	if current.Address == "" {
		return errors.New("publishing the health probe without a listener")
	}
	return WriteProbe(filepath.Join(runtime.config.DataDir, ".probe.json"), Probe{
		Addr: current.Address, Host: runtime.app.ProbeHost(),
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
