// The server is Linux only by design: a share root is an openat2 handle and
// the sandbox is seccomp and Landlock.
//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/oidc"
	"github.com/heavycaffeiner/stowcloud/go/internal/preview"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/index"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/service"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/server"
	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
	"github.com/heavycaffeiner/stowcloud/go/internal/smbagent"
	"github.com/heavycaffeiner/stowcloud/go/internal/smbpublish"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
	"github.com/heavycaffeiner/stowcloud/go/internal/upload"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
	"github.com/heavycaffeiner/stowcloud/go/internal/watch"
)

// jailSpec is the domain the server runs under: the data directory it owns,
// and every share it serves. Nothing else is reachable.
//
// Execute is left unhandled because the sandbox becomes process-wide through an
// exec, and a domain that denied it would deny that. Removing the syscall
// entirely is the filter's job in the step after, which denies it harder.
func jailSpec(cfg *server.Config, shareHosts []string) jail.Spec {
	spec := jail.Spec{ExceptExec: true}
	spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: cfg.DataDir})
	// Where the server renders smb.conf and the credential file for the
	// sidecar to read. Left out of the domain, it was the one directory the
	// server writes that the sandbox denied: the mount was correct, the owner
	// was correct, the mode was correct, and every publish failed with
	// "replacing smb.conf: open /config/smb: permission denied" from the
	// kernel rather than from the filesystem.
	//
	// Granted only when SMB is on. A grant names a path that has to exist, and
	// the default names the sidecar's mount point: on a deployment that is not
	// running one, granting it unconditionally is a first boot that refuses to
	// start over a directory nothing was going to use.
	if cfg.SMB.Render.Enabled && cfg.SMB.ConfigDir != "" {
		spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: cfg.SMB.ConfigDir})
	}
	// The sidecar's control socket, which the server connects to rather than
	// creates. Connecting to a unix socket is a filesystem operation, so the
	// directory holding it has to be in the domain or every push to the
	// sidecar is refused the same way.
	if sock := cfg.SMB.AgentSocket; cfg.SMB.Render.Enabled && sock != "" {
		if dir := filepath.Dir(sock); dir != "" && dir != "/" {
			spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: dir})
		}
	}
	// Every share is granted by its host path. A domain built from the data
	// directory alone would deny every share the server exists to serve, which
	// is a sandbox that only works on a deployment with no shares.
	//
	// Each share's parent is granted rather than the share itself, and that is
	// what lets a folder added from the admin screen work without a restart. A
	// Landlock domain cannot be widened once it is installed, so a share added
	// later could never be reached: it was registered, granted and listed, and
	// every attempt to open it answered permission denied. A path_beneath rule
	// covers what appears under it afterwards, so granting the directory the
	// shares live in covers the next sibling too.
	//
	// This widens the domain by one level and no further. The parent of a share
	// is the directory an operator mounts their folders into, which is a
	// deliberate boundary rather than an arbitrary one, and "/" is never
	// granted: a share directly under it keeps its own narrow rule.
	seen := map[string]bool{}
	for _, host := range shareHosts {
		if host == "" {
			continue
		}
		path := shareGrantPath(host)
		if seen[path] {
			continue
		}
		seen[path] = true
		spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: path})
	}
	return spec
}

// shareGrantPath is the directory the domain grants for a share.
//
// The share's parent, so a sibling added later is inside the domain already
// and needs no restart. A share whose parent is "/" grants the share itself:
// granting the root would put the whole filesystem in the domain, which is the
// one thing this sandbox exists to prevent.
func shareGrantPath(host string) string {
	clean := filepath.Clean(host)
	parent := filepath.Dir(clean)
	if parent == "/" || parent == "." || parent == clean {
		return clean
	}
	return parent
}

// inJail reports whether a path is inside a domain built from these grants.
//
// A path_beneath rule covers everything under the directory it names, so this
// is what decides whether a share added now is reachable now or only after the
// process is rebuilt. It is a string comparison on cleaned paths because a
// Landlock domain cannot be asked what it holds: what it grants is what was
// handed to it, and this is that list.
func inJail(spec jail.Spec, host string) bool {
	clean := filepath.Clean(host)
	for _, g := range spec.GrantBeneath {
		granted := filepath.Clean(g.Path)
		if clean == granted {
			return true
		}
		// Under it, component-wise: "/srv/media2" is not inside "/srv/media",
		// and a plain prefix test says it is.
		if strings.HasPrefix(clean, strings.TrimSuffix(granted, "/")+"/") {
			return true
		}
	}
	return false
}

// The domain is built from the share paths in the database rather than from
// the core, because the core cannot be built before the sandbox: doing so is
// what made everything above the sandbox run twice.
//
// A first boot has no shares, so the domain grants only the data directory.
// Nothing is lost by that: a share created afterwards is granted through its
// parent, and the first one under a parent the domain has never seen is what
// the restart exists for.

// bootSettings reads what the sandbox has to be built from, before the store
// is opened for real.
//
// It opens the store, takes the settings and the share paths, and closes it
// again: this runs before the sandbox, so it runs twice, and holding the
// descriptor across the re-exec would leave it in the replaced image.
//
// A store that will not open answers the compiled-in defaults with no shares.
// The open below is the one that reports the failure; refusing here would
// report it from the copy of the process that is about to be replaced.
func bootSettings(dataDir string, clk clock.Clock, log *slog.Logger) (runtimecfg.Values, []string) {
	st, err := store.Open(dataDir, store.Options{Clock: clk})
	if err != nil {
		return runtimecfg.Defaults(), nil
	}
	defer func() {
		_ = st.Close() //nolint:errcheck // nothing was written, and the open below is the one that matters.
	}()
	ctx := context.Background()
	values := runtimecfg.Load(ctx, st.State(), runtimecfg.Defaults(), log)
	var hosts []string
	if rows, lerr := st.State().ListShares(ctx); lerr == nil {
		for _, row := range rows {
			hosts = append(hosts, row.Host)
		}
	}
	return values, hosts
}

// grantEveryShare gives one account full access to every registered share.
//
// It exists for the first administrator and for nothing else. A share is only
// reachable through a grant, the admin screen that creates grants is itself
// behind the interface, and a fresh deployment starts with none: without this
// the first run produces an account that signs in, sees an empty interface,
// and has no way to give itself anything.
//
// Every bit, because this is the administrator: a first account that could
// read but not write would be a different dead end.
//
// The evaluator is reloaded afterwards. A grant live in the database and
// absent from the process serving the request is a permission decision that
// depends on which half was asked, and the first thing this account does is
// ask.

func grantEveryShare(
	ctx context.Context, c *core.Core, st *store.Store, ev *acl.Evaluator,
	uid int64, clk clock.Clock,
) error {
	const all = acl.Read | acl.Write | acl.Create | acl.Delete |
		acl.Rename | acl.Move | acl.Share | acl.Download

	for _, def := range c.Shares() {
		if _, err := acl.CreateGrant(ctx, st.State().SQL(), acl.Grant{
			User:    uid,
			Share:   int64(def.ID),
			Allow:   all,
			Inherit: true,
			// Labeled with the share's own name, which is what the interface
			// shows as the folder: an unlabeled grant falls back to a
			// generated "share-N".
			Label: def.Name,
		}, clk.Nanos()); err != nil {
			return fmt.Errorf("granting %q to the first administrator: %w", def.Name, err)
		}
	}
	return ev.LoadFromState(ctx, st.State().SQL())
}

// runServe starts the server: settings, store, master key, core domain, the
// setup gate, the listener, and a graceful shutdown on SIGINT or SIGTERM.
func runServe(args []string, stderr io.Writer) int {
	dataDir, emergencyOnly, uerr := serveArgs(args)
	if uerr != nil {
		say(stderr, "stowcloud %s: serve: %v\n\n", version, uerr)
		say(stderr, "usage: stowcloud serve [--data-dir DIR] [--emergency]\n\n")
		say(stderr, "  Starts the server. Everything it serves is configured from the\n")
		say(stderr, "  web interface and stored in the database under DIR, which\n")
		say(stderr, "  defaults to %s. A directory with no settings yet is a first\n", defaultDataDir)
		say(stderr, "  boot: the server comes up on %s and serves the setup form.\n\n", runtimecfg.DefaultListen)
		say(stderr, "  --emergency brings up only the settings editor, on the stored\n")
		say(stderr, "  address. It touches nothing the server proper owns, so it is\n")
		say(stderr, "  what repairs a stored setting that stops the server starting.\n")
		return exitUsage
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	clk := clock.System()
	ctx := context.Background()

	if emergencyOnly {
		return runEmergency(ctx, dataDir, log, stderr)
	}

	// The atomic path resolver is checked before anything is opened. It is a
	// refusal under every hardening policy, the one that turns hardening off
	// included, because that policy means a weaker sandbox is accepted and not
	// that a path resolver which can be raced is.
	if rerr := vfs.RequireResolver(vfs.Probe()); rerr != nil {
		say(stderr, "stowcloud %s: serve: %v\n", version, rerr)
		return exitConfig
	}

	// What the sandbox is built from, read before it goes on. The settings are
	// read again below, from the store this process keeps open; this pass is
	// only what the domain needs, and it runs in the copy of the process the
	// re-exec replaces.
	bootValues, shareHosts := bootSettings(dataDir, clk, log)
	cfg := server.FromValues(dataDir, bootValues, "")
	// Held so the share surface can tell a folder that is reachable now from
	// one that needs the process rebuilt. It is the same spec the domain was
	// built from, which is the only thing that knows what it grants.
	domain := jailSpec(cfg, shareHosts)

	// The sandbox goes on before anything else is opened.
	//
	// It replaces the process image, so everything above this point runs
	// twice and everything below it runs once. That is why it sits here
	// rather than just before the listener: with it later, the store was
	// opened twice, the master key was opened twice, and a first run minted
	// and printed two setup tokens, only the second of which was the live one.
	jailStatus, jerr := jail.Apply(cfg.Hardening, domain)
	if jerr != nil {
		code := jail.Refuse(stderr, jailStatus)
		// The one failure that cannot degrade into the repair door: nothing is
		// open yet, and opening it after refusing to confine the process is
		// exactly what the refusal said not to do. So the log names the step
		// instead. A required policy this kernel cannot satisfy is a stored
		// setting, and it is the one most likely to put a deployment in a
		// restart loop with nowhere to click.
		emergencyHint(stderr, dataDir)
		return code
	}
	log.Info("hardening", "status", jailStatus.String())

	// The data directory is one server's, and the lock is what says so.
	//
	// It is taken here rather than assumed because two processes writing these
	// databases is a real shape now: `serve --emergency` opens the same files
	// to repair them, and a repair applied to a document a running server then
	// overwrites is worse than a refusal. The lock is on the open descriptor,
	// so a crash drops it and nothing has to be cleaned up.
	lock, lockErr := store.LockInstance(cfg.DataDir)
	if lockErr != nil {
		say(stderr, "stowcloud %s: serve: %v\n", version, lockErr)
		say(stderr, "  Another stowcloud is using this directory. Stop it first.\n")
		return exitConfig
	}
	defer func() {
		if rerr := lock.Release(); rerr != nil {
			log.Warn("releasing the data directory lock", "error", rerr)
		}
	}()

	st, serr := store.Open(cfg.DataDir, store.Options{Clock: clk})
	if serr != nil {
		say(stderr, "stowcloud %s: serve: opening the store: %v\n", version, serr)
		return exitConfig
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			say(stderr, "stowcloud %s: serve: closing the store: %v\n", version, cerr)
		}
	}()

	// The credential file lives beside the rendered configuration, so the
	// sidecar reads both from one mounted directory. Empty when SMB is off,
	// which is what stops every credential change re-rendering a file nothing
	// reads.
	passdbPath := ""
	if cfg.SMB.Render.Enabled {
		passdbPath = filepath.Join(cfg.SMB.ConfigDir, "smbpasswd")
	}
	authSvc := auth.New(auth.Config{Store: st.State(), StoreDir: cfg.DataDir, Clock: clk, PassdbPath: passdbPath})
	masterKey, kerr := authSvc.OpenMasterKey(ctx)
	if kerr != nil {
		say(stderr, "stowcloud %s: serve: the master key: %v\n", version, kerr)
		return exitConfig
	}

	// The evaluator is held rather than created inline, because the admin
	// screens edit the grants it answers from and it has to be reloaded when
	// they do. Created inline it could only ever be loaded once, at startup.
	// Everything from here can fail over a stored setting, and everything from
	// here degrades rather than exits: the store and the auth service are open,
	// which is all the repair door needs, and a process that exited would leave
	// an operator with a log line and no screen to act on it from.
	evaluator := acl.NewEvaluator()
	if lerr := evaluator.LoadFromState(ctx, st.State().SQL()); lerr != nil {
		return degrade(ctx, st, authSvc, dataDir, "the grants could not be loaded: "+lerr.Error(), log, stderr)
	}
	coreSvc, cerr := core.New(st, core.Options{ACL: evaluator, Clock: clk})
	if cerr != nil {
		return degrade(ctx, st, authSvc, dataDir, "the core domain could not be built: "+cerr.Error(), log, stderr)
	}
	// Share links need the master key to open their own tokens and the auth
	// service to hash and check their passwords. Nothing called this, so a
	// password-protected link could not be created and a link URL could not be
	// listed again: both failed closed, which is the right failure and still a
	// feature that did not work.
	// The active key, not the version: the version travels with each row and
	// the cipher reads it from there, which is what lets a rotation leave
	// older rows readable.
	activeKey, _ := masterKey.Active()
	coreSvc.AttachLinkCrypto(
		auth.NewLinkCipher(activeKey),
		func(c context.Context, plain string) (string, error) {
			return authSvc.Hash(c, secret.New([]byte(plain)))
		},
		func(c context.Context, enc, candidate string) (bool, error) {
			ok, _, verr := authSvc.Verify(c, enc, secret.New([]byte(candidate)))
			return ok, verr
		},
	)
	// Every share lives in the state database; a restart must re-open them
	// under the same ids the running process used.
	rejectedShares, rerr := coreSvc.ReloadPersistedShares(ctx)
	if rerr != nil {
		return degrade(ctx, st, authSvc, dataDir, "the shares could not be reopened: "+rerr.Error(), log, stderr)
	}

	setupGate, gerr := server.NewSetupGate(ctx, authSvc, clk, cfg.DataDir)
	if gerr != nil {
		say(stderr, "stowcloud %s: serve: the setup gate: %v\n", version, gerr)
		return exitConfig
	}
	// What makes a fresh deployment usable. The first administrator gets a
	// grant over every configured share, because a share is only reachable
	// through one and the first run has none: without this, setup produces an
	// account that signs in to an empty interface with no way to give itself
	// anything.
	setupGate.GrantsFirstAdmin(func(c context.Context, uid int64) error {
		return grantEveryShare(c, coreSvc, st, evaluator, uid, clk)
	})
	if setupGate.IsRequired(ctx) {
		if ierr := setupGate.Issue(os.Stdout); ierr != nil {
			say(stderr, "stowcloud %s: serve: issuing the setup token: %v\n", version, ierr)
			return exitConfig
		}
	}

	health := handler.NewHealthState()
	for _, rej := range rejectedShares {
		health.Degrade(handler.ReasonShareRejected, rej.Name+":"+rej.Kind)
	}
	// Every share root is re-probed on a schedule, in both directions: a disk
	// unmounted underneath a running server leaves a descriptor that fails one
	// request at a time with nothing saying which share is at fault, and one
	// that came back has to start working without anybody pressing anything.
	server.WatchShares(ctx, coreSvc, health, log)
	for _, step := range jailStatus.Steps {
		if !step.Applied {
			// The detail is a token naming the layer, not the errno's
			// sentence: a caller reads the kind and the layer, and the errno
			// is in the startup log for whoever is diagnosing it.
			health.Degrade(handler.ReasonHardening, step.Name+"_unavailable")
		}
	}

	// The settings, read again from the store this process holds open. The
	// pass above the sandbox was for the domain and ran in the image the
	// re-exec replaced.
	//
	// The compiled-in defaults are the floor, because there is no file: a key
	// nobody has saved runs as the default, and the stored document is the only
	// thing over it.
	defaults := runtimecfg.Defaults()
	watchDefaults := watch.DefaultConfig()
	defaults.WatchHotSetMax = watchDefaults.HotSetMax
	defaults.WatchFullThreshold = watchDefaults.FullThreshold
	// The root homes would take, named rather than left blank: a switch whose
	// effect is not visible until it is flipped is one an administrator has to
	// guess at.
	defaults.HomesRoot = filepath.Join(dataDir, "homes")
	rtcfg := runtimecfg.New(runtimecfg.Load(ctx, st.State(), defaults, log))

	// The typed configuration the listener and the guards are built from,
	// rebuilt now that the OIDC secret can be opened: the boot pass above had
	// no master key, so it had no secret either.
	clientSecret, cserr := server.OpenOIDCSecret(ctx, st.State(), authSvc)
	if cserr != nil {
		log.Error("the single sign-on secret could not be opened; single sign-on stays off", "error", cserr)
	}
	cfg = server.FromValues(dataDir, rtcfg.Get(), clientSecret)

	// Thumbnails. The pool, the cache, the decoders and the jailed worker were
	// all complete and tested, and nothing outside the package had ever
	// constructed a pool: no image in this product has ever had a thumbnail.
	//
	// A failure here is a degradation rather than a refusal to start. The grid
	// draws type icons without it, which is what it did for the whole life of
	// the port, and a server that will not boot because it cannot spawn a
	// decoder is worse than one that serves files without pictures.
	var previewSvc *preview.Service
	if pool, perr := preview.NewPool(preview.PoolOptions{Clock: clk}); perr != nil {
		log.Error("thumbnails are unavailable: the worker pool could not be built", "error", perr)
		health.Degrade(handler.ReasonPreviewPoolUnavailable, "pool")
	} else if cache, cerr := preview.NewCache(filepath.Join(cfg.DataDir, "thumbs")); cerr != nil {
		log.Error("thumbnails are unavailable: the cache could not be opened", "error", cerr)
		health.Degrade(handler.ReasonPreviewPoolUnavailable, "cache")
		_ = pool.Close() //nolint:errcheck // the pool is being abandoned.
	} else {
		previewSvc = preview.NewService(preview.ServiceOptions{
			Core: coreSvc, Pool: pool, Cache: cache, Clock: clk,
		})
		defer func() {
			if err := pool.Close(); err != nil {
				log.Warn("closing the preview pool", "error", err)
			}
		}()
	}

	uploads, uerr := upload.New(ctx, coreSvc, st.State(), upload.Options{
		Clock: clk, Logger: log,
		// Fixed under the data directory: it is already inside the sandbox's
		// domain, so the switch needs no restart, and there is no path setting
		// to get wrong. An operator wanting a faster or larger spool mounts a
		// volume there.
		CacheDir: filepath.Join(cfg.DataDir, "spool"),
	})
	if uerr != nil {
		return degrade(ctx, st, authSvc, dataDir, "the upload engine could not be built: "+uerr.Error(), log, stderr)
	}
	defer func() {
		if cerr := uploads.Close(); cerr != nil {
			log.Warn("closing the upload engine", "error", cerr)
		}
	}()
	// What the cache still holds is reconciled against what the sessions claim
	// before a single request is served. The recommended spool is a tmpfs and a
	// reboot empties one, so a resuming client must not be handed an offset
	// whose bytes are gone.
	if rerr := uploads.RecoverCache(ctx); rerr != nil {
		log.Error("the upload cache could not be reconciled after the restart", "error", rerr)
	}

	// The search index's updater, declared before the watcher so the fan-out
	// can reach it and assigned after the service exists. Nil until then, and
	// the observer reads it per event rather than capturing it.
	var indexUpdater *service.Updater
	rt := rtcfg.Get()

	// Homes, when an administrator has turned them on. The share is registered
	// under a reserved id and every account gets a directory under it on first
	// access, so this is the one switch that changes what a person sees
	// without anybody writing a grant.
	//
	// A failure is a degradation rather than a refusal to start: the other
	// shares are unaffected, and a server that will not boot because one
	// directory is unwritable is worse than one that says so.
	if rt.HomesEnabled {
		root := rt.HomesRoot
		if root == "" {
			root = filepath.Join(cfg.DataDir, "homes")
		}
		if herr := coreSvc.EnableHomes(ctx, root); herr != nil {
			log.Error("homes are turned on and could not be opened", "root", root, "error", herr)
			health.Degrade(handler.ReasonShareRejected, "homes")
		} else {
			log.Info("serving homes", "root", root)
		}
	}

	watcher, hub, werr := server.StartWatch(ctx, coreSvc, clk, log, watch.Config{
		HotSetMax:     rt.WatchHotSetMax,
		FullThreshold: rt.WatchFullThreshold,
	}, func(ev watch.InvalEvent) {
		if indexUpdater != nil {
			indexUpdater.Offer(ev)
		}
	})
	if werr != nil {
		return degrade(ctx, st, authSvc, dataDir, "the watcher could not be started: "+werr.Error(), log, stderr)
	}
	defer func() {
		_ = watcher.Close() //nolint:errcheck // shutdown is closing everything anyway.
	}()

	// Single sign-on. Nil when none is configured, which leaves the surfaces
	// answering that this deployment has no provider rather than pretending.
	var oidcClient *oidc.Client
	if cfg.OIDC != nil {
		c, oerr := oidc.New(*cfg.OIDC, clk)
		if oerr != nil {
			return degrade(ctx, st, authSvc, dataDir, "the identity provider could not be built: "+oerr.Error(), log, stderr)
		}
		oidcClient = c
	}

	// Search. The index is optional and off by default: a query answers from a
	// walk when there is none, so the escalation is taken deliberately rather
	// than assumed. What decides is the stored switch, not the config file,
	// which is what lets an administrator turn it on without a restart.
	searchSvc := service.New(service.Options{Clock: clk, Storage: service.StorageSSD, CPUs: runtime.NumCPU()})
	indexOn, ierr := st.State().IndexNameEnabled(ctx)
	if ierr != nil {
		say(stderr, "stowcloud %s: serve: the index setting: %v\n", version, ierr)
		return exitConfig
	}
	if indexOn {
		// A corrupt index is disabled rather than fatal: the walk still
		// answers, which is the whole reason the index is an escalation.
		searchSvc.SetIndex(service.OpenIndex(filepath.Join(cfg.DataDir, "index"), index.DefaultConfig(), log))
	}

	// What keeps the index current. Without it the index holds whatever the
	// last build found: a file created afterwards is missing from every result
	// the index answers, and nothing reports the result as short.
	//
	// Started whether or not the index is on right now, because the switch can
	// be turned on later in this same process and the updater reads the live
	// index per event.
	indexUpdater = service.NewUpdater(searchSvc, coreSvc.ScanSources, log)
	task.Go(ctx, "search index updater", func() { indexUpdater.Run(ctx) })

	// The size guard. dbfile has always been able to refuse a write that grows
	// a file, and nothing ever sampled the volume to decide it should: the flag
	// was set only by tests, so the guard could not trip and the health reason
	// for it could not be reported. Returns immediately when neither bound is
	// configured, which is the default.
	if cfg.DBGuard.Enabled() {
		task.Go(ctx, "store size guard", func() {
			st.RunGuard(ctx, cfg.DBGuard, func(g store.GuardState) {
				if g.Blocked {
					log.Warn("the store size guard tripped; writes are refused and reads continue",
						"reason", g.Reason,
						"available_bytes", g.AvailableBytes,
						"store_bytes", g.StoreBytes)
					health.Degrade(handler.ReasonDatabaseSizeGuard, g.Reason)
					return
				}
				log.Info("the store size guard cleared; writes are accepted again",
					"available_bytes", g.AvailableBytes)
				health.ResolveKind(handler.ReasonDatabaseSizeGuard)
			})
		})
	}

	// What an administrator saved for SMB, over what the config file said.
	//
	// Folded in before the publisher is built, so the first publish already
	// carries it: the render is whole, and a half-applied configuration is one
	// nobody wrote. The screen reports the enable switch as needing a restart
	// because that is what this is: the publisher is assembled once, here.
	smbFromConfig := cfg.SMB.Render
	if rt.SMBConfigured {
		cfg.SMB.Render.Enabled = rt.SMB.Enabled
		if rt.SMB.Workgroup != "" {
			cfg.SMB.Render.Workgroup = rt.SMB.Workgroup
		}
		cfg.SMB.Render.ServerName = rt.SMB.ServerName
		if rt.SMB.ServiceUser != "" {
			cfg.SMB.Render.ServiceUser = rt.SMB.ServiceUser
		}
		cfg.SMB.Render.AllowPublicBind = rt.SMB.AllowPublicBind
		if rt.SMB.ServiceGID != 0 {
			cfg.SMB.ServiceGID = rt.SMB.ServiceGID
		}
		// The policy decides what is published, never what is stored, so
		// moving it back restores access without anybody setting a password
		// again.
		if rt.SMB.TOTPPolicy == "block" {
			authSvc.SetSMBTOTPPolicy(auth.TOTPBlock)
		}
		// Rendered now rather than at the first publish, so a stored value the
		// renderer refuses is a line in the log at startup instead of a
		// settings screen failing later.
		if _, rerr := smb.Render(cfg.SMB.Render, nil); rerr != nil {
			log.Error("the stored SMB settings cannot be rendered; running with the config file's",
				"error", rerr)
			cfg.SMB.Render = smbFromConfig
		}
	}

	// SMB publishing. The whole render is rebuilt from state on every call
	// rather than diffed, so a change that stops at one surface is still
	// visible to the sidecar on the next apply.
	publishSMB := func(c context.Context) (smbagent.Report, error) {
		return smbpublish.Publish(c, smbpublish.Deps{
			Core:       coreSvc,
			Auth:       authSvc,
			ConfigDir:  cfg.SMB.ConfigDir,
			Socket:     cfg.SMB.AgentSocket,
			ServiceGID: cfg.SMB.ServiceGID,
			Grants: func(c context.Context) ([]acl.Grant, error) {
				return acl.ListGrants(c, st.State().SQL(), acl.GrantFilter{})
			},
			Names: func(c context.Context, id int64) (string, error) {
				return authSvc.NameOf(c, id)
			},
		}, cfg.SMB.Render)
	}
	if !cfg.SMB.Render.Enabled {
		// Nothing to publish and nothing to refuse with: the surface says SMB
		// is not configured rather than pretending an apply happened.
		publishSMB = nil
	}

	if publishSMB != nil {
		// The credential paths live in the auth package, which cannot reach the
		// publisher: the publisher asks it for the two credential files. So the
		// wire is made here, and every path that already republished the file
		// now also tells the sidecar to import it. Writing the file and telling
		// nobody is a revocation that lands whenever something else publishes.
		authSvc.SetSMBPublisher(func(c context.Context) {
			if _, perr := publishSMB(c); perr != nil {
				log.Warn("a credential change did not reach the SMB sidecar", "error", perr)
				health.Degrade(handler.ReasonSMBStale, "publish_failed")
				return
			}
			health.Resolve(handler.ReasonSMBStale, "publish_failed")
		})

		// Once at startup, because the state can have moved while this server
		// was not running: a migration, a hand-edited database, or a grant
		// changed by a build that had no sink. Without this the daemon serves
		// whatever it was left with until the next write.
		if _, perr := publishSMB(ctx); perr != nil {
			// Not fatal. SMB is one of several surfaces and the others work;
			// refusing to start would take the whole deployment down for it.
			log.Warn("the SMB configuration could not be published at startup", "error", perr)
			health.Degrade(handler.ReasonSMBStale, "publish_failed")
		}
	}

	// Turning the index on or off in the running process, which is what stops
	// the administrator's switch needing a restart it did not mention. The
	// stored switch stays the record; this is the process catching up to it.
	applyIndex := func(enabled bool) error {
		if !enabled {
			searchSvc.SetIndex(nil)
			return nil
		}
		ix := service.OpenIndex(filepath.Join(cfg.DataDir, "index"), index.DefaultConfig(), log)
		if ix == nil {
			// Corrupt or unopenable. It was reported where it was opened; the
			// switch is refused so the screen says the index is not running
			// rather than reporting a success that left every query walking.
			return errors.New("the index could not be opened")
		}
		searchSvc.SetIndex(ix)
		return nil
	}

	// What a settings save asks for when the change is one this process
	// cannot make in place: the sandbox, and everything built under it.
	//
	// The process exits and the supervisor starts it again. It is a request
	// rather than a restart: nothing here can bring the process back, and a
	// deployment with no supervisor stays stopped. The exit is scheduled
	// rather than immediate so the response the caller is reading finishes
	// leaving.
	restart := make(chan struct{}, 1)
	requestRestart := func() {
		select {
		case restart <- struct{}{}:
		default:
		}
	}

	serve, nerr := server.New(ctx, cfg, server.Options{Store: st, Auth: authSvc, Core: coreSvc, Log: log, Clk: clk, Watch: watcher, WS: hub, Health: health, Uploads: uploads, Preview: previewSvc,
		Search:            searchSvc,
		Runtime:           rtcfg,
		ApplyIndexEnabled: applyIndex,
		OIDC:              oidcClient,
		PublishSMB:        publishSMB,
		ReloadACL:         func(c context.Context) error { return evaluator.LoadFromState(c, st.State().SQL()) },
		StoreSecret: func(c context.Context, plain string) error {
			return server.StoreOIDCSecret(c, st.State(), authSvc, plain)
		},
		HasOIDCSecret:  clientSecret != "",
		RequestRestart: requestRestart,
		PathInJail: func(host string) bool {
			// A domain that was never applied constrains nothing, so every
			// path is reachable and no share needs a restart.
			if !jailStatus.LandlockApplied() {
				return true
			}
			return inJail(domain, host)
		},
		ActiveWork: func() handler.ActiveWork {
			w, aerr := st.State().CountActiveWork(ctx)
			if aerr != nil {
				// Unknown reads as busy: the refusal is what an administrator
				// can override, and guessing idle would take a restart
				// through an upload without asking.
				log.Warn("could not count the work a restart would interrupt", "error", aerr)
				return handler.ActiveWork{Uploads: 1}
			}
			return handler.ActiveWork{Uploads: w.Uploads, Jobs: w.Jobs}
		},
	}, setupGate)
	if nerr != nil {
		return degrade(ctx, st, authSvc, dataDir, "the listener could not be started: "+nerr.Error(), log, stderr)
	}
	// The engine is up, so whatever it failed on before is over. Forgetting
	// the history here rather than on exit is what makes the count mean
	// "consecutive failures": a deployment that has run for a month and then
	// fails once should get three fresh attempts, not the conclusion.
	clearEngineFailures(dataDir)
	log.Info("listening", "addr", serve.Addr(), "app_host", cfg.AppHost)

	// The shutdown path: a signal or a restart request starts the drain, and
	// the drain has a deadline of its own so a stuck upload cannot hold the
	// process forever.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info("shutting down", "signal", s.String())
	case <-restart:
		log.Info("restarting: a saved setting needs the process rebuilt")
	}
	serve.Stop(ctx)
	return exitOK
}
