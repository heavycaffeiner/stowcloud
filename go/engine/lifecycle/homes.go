//go:build linux

// Per-account home directories.
//
// The setting existed and did nothing: the stored values were loaded, shown on
// the settings screen and validated, and no code ever called EnableHomes. An
// operator who turned homes on was told each account would get a folder, and
// none appeared. This is what makes the switch mean something.
package lifecycle

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
)

// applyHomes brings the homes share into line with the stored settings.
//
// Called at boot and again on every settings save, because both have to answer
// the same question: does this deployment serve homes, and from where. The core
// treats a second call as a re-registration, so a root that moved takes effect
// without a restart.
//
// A failure is logged rather than returned. Homes are one surface among
// several, and a homes root that cannot be created must not stop a deployment
// from serving the shares it already had.
func (e *Engine) applyHomes(ctx context.Context, values runtimecfg.Values) {
	if !values.HomesEnabled || values.HomesRoot == "" {
		e.Core.DisableHomes()
		return
	}
	if err := e.Core.EnableHomes(ctx, values.HomesRoot); err != nil {
		e.logger.Error("home folders are configured and could not be opened",
			"root", values.HomesRoot, "error", err)
	}
}
