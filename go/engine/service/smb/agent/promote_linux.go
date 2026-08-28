//go:build linux

package agent

import (
	"fmt"
	"os"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/fsatomic"
)

// The two configuration writes, which have different fates on power loss and
// therefore different implementations.
//
// The candidate is scratch: it is regenerated on every pass and the daemon
// never reads it, so a torn one self-heals. The promoted file is what the
// daemon reads at every start and reload, so a torn one stops smbd entirely.
//
// The old code wrote both with a plain os.WriteFile while the durable primitive
// sat two functions away in the same call graph, already used for the account
// file. This is the distinction that was missing, and marking both as durable
// would erase it just as thoroughly as marking neither.

// promotedMode is what the daemon reads the configuration as. It runs as its
// own user and the file holds no secret, so it is world-readable.
const promotedMode = 0o644

// candidateMode is scratch belonging to this agent alone.
const candidateMode = 0o600

// WriteCandidate stages a configuration for the validator.
//
// Deliberately a plain write. The candidate is validation scratch: every pass
// regenerates it before use and the daemon never opens it, so a torn one costs
// nothing and is replaced rather than repaired. Making it durable would spend a
// sync on a file whose only reader is the validator this pass is about to run.
func WriteCandidate(path, body string) error {
	if err := os.WriteFile(path, []byte(body), candidateMode); err != nil {
		return fmt.Errorf("writing the candidate configuration: %w", err)
	}
	return nil
}

// Promote replaces the configuration the daemon reads, durably.
//
// This is the write the durability distinction exists for. The daemon reads
// this file at every start and every reload, so a torn one does not degrade
// smbd, it stops it: the parser fails and no share is served at all. A staged
// write with an atomic rename means a machine losing power mid-promotion comes
// back holding either the previous configuration or the new one, never a
// fragment of either.
func Promote(path, body string) error {
	err := fsatomic.ReplaceFileDurable(path, promotedMode, func(f *os.File) error {
		_, werr := f.WriteString(body)
		return werr
	})
	if err != nil {
		return fmt.Errorf("promoting the configuration: %w", err)
	}
	return nil
}
