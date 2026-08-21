package server

import (
	"os"
	"path/filepath"
	"reflect"
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

// parserKeys walks the TOML struct and collects every key it declares.
func parserKeys(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		if t.Kind() != reflect.Struct {
			return
		}
		for i := range t.NumField() {
			f := t.Field(i)
			if tag := f.Tag.Get("toml"); tag != "" {
				out[tag] = true
			}
			walk(f.Type)
		}
	}
	walk(t)
	return out
}

// Every key the example sets is one the parser reads. A key nothing consumes
// is a setting an operator changes with no effect, which is worse than one
// that is absent.
func TestTheExampleSetsNoKeyTheParserIgnores(t *testing.T) {
	body, err := os.ReadFile(filepath.Clean(exampleConfigPath))
	if err != nil {
		t.Fatalf("reading the example: %v", err)
	}

	// Read off the parser's own struct rather than listed here. A second list
	// is a second thing to forget: this test exists because a key nothing
	// reads is a setting an operator changes with no effect, and a hand-kept
	// list of known keys has exactly that failure itself.
	known := parserKeys(reflect.TypeOf(raw{}))

	for i, line := range strings.Split(string(body), "\n") {
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

// The example with SMB turned on still parses.
//
// It ships turned off, so nothing else here exercises those keys, and a
// default that only validates while it is off is one an operator finds broken
// the moment they enable it.
func TestTheExampleValidatesWithSMBTurnedOn(t *testing.T) {
	body, err := os.ReadFile(filepath.Clean(exampleConfigPath))
	if err != nil {
		t.Fatalf("reading the example: %v", err)
	}

	on := strings.Replace(string(body), "enabled = false", "enabled = true", 1)
	if on == string(body) {
		t.Fatal("the example no longer has an SMB switch to turn on")
	}

	path := filepath.Join(t.TempDir(), "sc.toml")
	if werr := os.WriteFile(path, []byte(on), 0o600); werr != nil { //nolint:gosec // G703 traces the temporary directory into a write: it is the test framework's own, not caller data.
		t.Fatal(werr)
	}

	cfg, lerr := Load(path)
	if lerr != nil {
		t.Fatalf("the example does not validate with SMB on: %v", lerr)
	}
	if !cfg.SMB.Render.Enabled {
		t.Error("SMB parsed as off after being turned on")
	}
	if cfg.SMB.Render.ServiceUser == "" {
		t.Error("no service account, which every connection runs as")
	}
	// The closed network case, because this process cannot see the host's
	// devices. The sidecar expands it in the namespace that can.
	if len(cfg.SMB.Render.Interfaces) != 0 {
		t.Errorf("the example pins %v, which turns off the sidecar's detection", cfg.SMB.Render.Interfaces)
	}
}
