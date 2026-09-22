package auth_test

import (
	"os"
	"testing"
)

// The key-file variables are cleared once for the whole binary. Every fixture
// resolves its key path from StoreDir, and a developer or runner with either
// exported would otherwise send all of them to one path outside t.TempDir(),
// or fail them all with ErrKeyEnvForbidden. Done here rather than with
// t.Setenv because these tests run in parallel, which t.Setenv forbids.
func TestMain(m *testing.M) {
	if err := os.Unsetenv("SC_MASTER_KEY"); err != nil {
		panic(err)
	}
	if err := os.Unsetenv("SC_MASTER_KEY_FILE"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
