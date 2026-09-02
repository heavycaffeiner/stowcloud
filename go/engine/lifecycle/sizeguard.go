//go:build linux

// The database size guard.
//
// The setting existed and did nothing: the switch and its two bounds were
// stored, validated and shown, and no code ever constructed a guard. An
// operator who turned it on was told the databases would stop accepting writes
// past the ceiling, and nothing measured them. This is what makes the switch
// mean something.
package lifecycle

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/sizeguard"
)

// applySizeGuard starts, stops or re-configures the sampler.
//
// Called at boot and again on every settings save. A running sampler holds the
// configuration it was started with, so a change stops it and starts another
// rather than mutating what a goroutine is reading.
//
// Turning the guard off unblocks the databases. Leaving them blocked would
// outlive the setting that blocked them, and an operator raising a ceiling to
// recover a full deployment would find the writes still refused.
func (e *Engine) applySizeGuard(ctx context.Context, values runtimecfg.Values) {
	e.guardMu.Lock()
	defer e.guardMu.Unlock()

	if e.guardStop != nil {
		e.guardStop()
		e.guardStop = nil
	}

	files := make([]sizeguard.File, 0, len(e.files))
	for _, f := range e.files {
		files = append(files, f)
	}
	guard := sizeguard.New(e.dataDir, files)
	// The same three numbers under two names: the settings package must not
	// import the store, and the store must not import the settings.
	cfg := sizeguard.Config{
		MinFreeBytes: values.DBGuard.MinFreeBytes,
		MaxBytes:     values.DBGuard.MaxBytes,
		Interval:     values.DBGuard.Interval,
	}

	if !cfg.Enabled() {
		// Whatever the last sample decided is withdrawn with the setting.
		guard.Unblock()
		return
	}

	// Detached from the caller: this outlives the request that saved the
	// setting, and a browser navigating away must not stop the sampler.
	loop, stop := context.WithCancel(context.WithoutCancel(ctx))
	e.guardStop = stop
	task.Go(loop, "the database size guard", func() {
		guard.Run(loop, cfg, func(st sizeguard.State) {
			if st.Blocked {
				e.logger.Error("the databases are refusing writes",
					"reason", st.Reason,
					"store_bytes", st.StoreBytes,
					"available_bytes", st.AvailableBytes)
				return
			}
			e.logger.Info("the databases are accepting writes again",
				"store_bytes", st.StoreBytes,
				"available_bytes", st.AvailableBytes)
		})
	})
}

// stopSizeGuard halts the sampler, for shutdown.
func (e *Engine) stopSizeGuard() {
	e.guardMu.Lock()
	defer e.guardMu.Unlock()
	if e.guardStop != nil {
		e.guardStop()
		e.guardStop = nil
	}
}
