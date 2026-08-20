package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
)

// The example config ships with the compose file and is what an operator's
// first run actually uses. One that does not load is a first run that fails,
// and nothing else in this tree reads it.

const exampleConfigPath = "../../../deploy/sc.toml.example"

func TestTheShippedExampleConfigLoads(t *testing.T) {
	cfg, err := Load(exampleConfigPath)
	if err != nil {
		t.Fatalf("the shipped example config does not load: %v", err)
	}
	if cfg.DataDir == "" {
		t.Error("the example names no data directory")
	}
	if len(cfg.AppHosts) == 0 {
		t.Error("the example names no host, which is a refused startup")
	}
	// Not loopback: a published port targets the container's bridge interface,
	// so a server bound to loopback is unreachable from outside its own
	// network namespace whatever the port mapping says.
	if strings.HasPrefix(cfg.Listen, "127.") || strings.HasPrefix(cfg.Listen, "localhost") {
		t.Errorf("the example binds %q, which a published port cannot reach", cfg.Listen)
	}
	// The shipped default refuses to start when a sandbox layer cannot be
	// applied. An example that quietly weakened it would be the setting most
	// deployments end up running with.
	if cfg.Hardening != jail.Required {
		t.Errorf("the example ships hardening %v, want the strict default", cfg.Hardening)
	}
}

// Every key the example sets is one the parser reads. A key nothing consumes
// is a setting an operator changes with no effect, which is worse than one
// that is absent.
func TestTheExampleSetsNoKeyTheParserIgnores(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(exampleConfigPath))
	if err != nil {
		t.Fatalf("reading the example: %v", err)
	}

	// The keys the parser actually declares.
	known := map[string]bool{
		"data_dir": true, "listen": true,
		"app_hosts": true, "content_hosts": true, "trusted_proxy_cidrs": true,
		"per_sec": true, "burst": true,
		"hardening": true,
	}

	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			t.Errorf("line %d is neither a section, a comment nor a setting: %q", i+1, line)
			continue
		}
		key = strings.TrimSpace(key)
		if !known[key] {
			t.Errorf("line %d sets %q, which the parser does not read", i+1, key)
		}
	}
}
