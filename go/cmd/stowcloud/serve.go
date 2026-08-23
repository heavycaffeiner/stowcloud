package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/oidc"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/index"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/service"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/server"
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
func jailSpec(cfg *server.Config, configPath string, shareHosts []string) jail.Spec {
	spec := jail.Spec{ExceptExec: true}
	spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: cfg.DataDir})
	// The config file is read again by the image the sandbox re-executes into,
	// because that image starts from the beginning. A domain that did not grant
	// it starts, replaces itself, and then cannot read the file it was told to
	// read, which is a failure that only appears once the sandbox is real.
	if dir := filepath.Dir(configPath); dir != "" {
		spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: dir, Access: jail.ReadOnly})
	}
	// Every share is granted by its host path. A domain built from the data
	// directory alone would deny every share the server exists to serve, which
	// is a sandbox that only works on a deployment with no shares.
	for _, host := range shareHosts {
		spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: host})
	}
	return spec
}

// applyJail builds the domain and applies it.
//
// The share paths are read straight from the database rather than from the
// core, because the core cannot be built before the sandbox: doing so is what
// made everything above the sandbox run twice.
func applyJail(cfg *server.Config, configPath string, clk clock.Clock) (jail.Status, error) {
	var hosts []string
	if st, err := store.Open(cfg.DataDir, store.Options{Clock: clk}); err == nil {
		rows, lerr := st.State().ListShares(context.Background())
		if lerr == nil {
			for _, row := range rows {
				hosts = append(hosts, row.Host)
			}
		}
		// Closed again immediately: this is only to learn which paths the
		// domain has to grant, and holding it open across the re-exec would
		// leave the descriptor in the replaced image.
		_ = st.Close() //nolint:errcheck // nothing was written, and the open below is the one that matters.
	}
	return jail.Apply(cfg.Hardening, jailSpec(cfg, configPath, hosts))
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

// registerConfigShares opens every folder the config file names.
//
// A folder that cannot be served is reported and skipped rather than stopping
// the server: one bad entry is not a reason for the other folders to be
// unreachable, and the health surface names which one is missing and why.
func registerConfigShares(
	ctx context.Context, c *core.Core, cfg *server.Config, log *slog.Logger,
) []core.RejectedShare {
	var rejected []core.RejectedShare
	for i, sh := range cfg.Shares {
		def := core.ShareDef{
			ID:               core.ShareID(i + 1),
			Name:             sh.Name,
			Host:             sh.Host,
			Policy:           vfs.DefaultSharePolicy(),
			SharedExternally: sh.SharedExternally,
			TrashEnabled:     sh.TrashEnabled,
		}
		if err := c.RegisterShare(ctx, def); err != nil {
			log.Error("a configured share was refused and is not being served",
				"share", sh.Name, "path", sh.Host, "error", err)
			rejected = append(rejected, core.RejectedShare{
				Name: sh.Name, Kind: core.RejectionKind(err), Err: err,
			})
			continue
		}
		log.Info("serving share", "name", sh.Name, "path", sh.Host)
	}
	return rejected
}

// runServe starts the server: config, store, master key, core domain, the
// setup gate, the listener, and a graceful shutdown on SIGINT or SIGTERM.
func runServe(args []string, stderr io.Writer) int {
	if len(args) > 1 {
		say(stderr, "usage: stowcloud serve [sc.toml]\n\n")
		say(stderr, "  Starts the server. The one config file names the data directory,\n")
		say(stderr, "  the hosts this server answers for, the trusted-proxy ranges, and\n")
		say(stderr, "  the rate bounds. With no argument, sc.toml in the working\n")
		say(stderr, "  directory is read; a missing file is a refused startup.\n")
		return exitUsage
	}
	configPath := "sc.toml"
	if len(args) == 1 {
		configPath = args[0]
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	clk := clock.System()
	ctx := context.Background()

	// The atomic path resolver is checked before anything is opened. It is a
	// refusal under every hardening policy, the one that turns hardening off
	// included, because that policy means a weaker sandbox is accepted and not
	// that a path resolver which can be raced is.
	if rerr := vfs.RequireResolver(vfs.Probe()); rerr != nil {
		say(stderr, "stowcloud %s: serve: %v\n", version, rerr)
		return exitConfig
	}

	cfg, err := server.Load(configPath)
	if err != nil {
		say(stderr, "stowcloud %s: serve: %v\n", version, err)
		return exitConfig
	}

	// The sandbox goes on before anything else is opened.
	//
	// It replaces the process image, so everything above this point runs
	// twice and everything below it runs once. That is why it sits here
	// rather than just before the listener: with it later, the store was
	// opened twice, the master key was opened twice, and a first run minted
	// and printed two setup tokens, only the second of which was the live one.
	jailStatus, jerr := applyJail(cfg, configPath, clk)
	if jerr != nil {
		return jail.Refuse(stderr, jailStatus)
	}
	log.Info("hardening", "status", jailStatus.String())

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
	evaluator := acl.NewEvaluator()
	if lerr := evaluator.LoadFromState(ctx, st.State().SQL()); lerr != nil {
		say(stderr, "stowcloud %s: serve: loading the grants: %v\n", version, lerr)
		return exitConfig
	}
	coreSvc, cerr := core.New(st, core.Options{ACL: evaluator, Clock: clk})
	if cerr != nil {
		say(stderr, "stowcloud %s: serve: the core domain: %v\n", version, cerr)
		return exitConfig
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
	// Admin-created shares live in the state database; a restart must re-open
	// them under the same ids the running process used.
	rejectedShares, rerr := coreSvc.ReloadPersistedShares(ctx)
	if rerr != nil {
		say(stderr, "stowcloud %s: serve: reloading persisted shares: %v\n", version, rerr)
		return exitConfig
	}
	// The shares the operator named in the config file. Their ids are low and
	// fixed by position, below the range an admin-created share takes, so the
	// two cannot collide and a config share keeps its id across a restart.
	rejectedShares = append(rejectedShares, registerConfigShares(ctx, coreSvc, cfg, log)...)

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
	for _, step := range jailStatus.Steps {
		if !step.Applied {
			// The detail is a token naming the layer, not the errno's
			// sentence: a caller reads the kind and the layer, and the errno
			// is in the startup log for whoever is diagnosing it.
			health.Degrade(handler.ReasonHardening, step.Name+"_unavailable")
		}
	}

	uploads, uerr := upload.New(ctx, coreSvc, st.State(), upload.Options{Clock: clk, Logger: log})
	if uerr != nil {
		say(stderr, "stowcloud %s: serve: the upload engine: %v\n", version, uerr)
		return exitConfig
	}

	// The search index's updater, declared before the watcher so the fan-out
	// can reach it and assigned after the service exists. Nil until then, and
	// the observer reads it per event rather than capturing it.
	var indexUpdater *service.Updater
	watcher, hub, werr := server.StartWatch(ctx, coreSvc, clk, log, func(ev watch.InvalEvent) {
		if indexUpdater != nil {
			indexUpdater.Offer(ev)
		}
	})
	if werr != nil {
		say(stderr, "stowcloud %s: serve: the watcher: %v\n", version, werr)
		return exitConfig
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
			say(stderr, "stowcloud %s: serve: the identity provider: %v\n", version, oerr)
			return exitConfig
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

	srv, nerr := server.New(cfg, server.Options{Store: st, Auth: authSvc, Core: coreSvc, Log: log, Clk: clk, Watch: watcher, WS: hub, Health: health, Uploads: uploads,
		Search:            searchSvc,
		ApplyIndexEnabled: applyIndex,
		OIDC:              oidcClient,
		PublishSMB:        publishSMB,
		ReloadACL:         func(c context.Context) error { return evaluator.LoadFromState(c, st.State().SQL()) },
	}, setupGate)
	if nerr != nil {
		say(stderr, "stowcloud %s: serve: %v\n", version, nerr)
		return exitConfig
	}

	ln, lerr := net.Listen("tcp", cfg.Listen)
	if lerr != nil {
		say(stderr, "stowcloud %s: serve: binding %s: %v\n", version, cfg.Listen, lerr)
		return exitNoAnswer
	}

	// The shutdown path: a signal starts the drain, and the drain has a
	// deadline of its own so a stuck upload cannot hold the process forever.
	// The watcher is a task, which is the one spawn this tree allows and the
	// one that installs a recover.
	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	task.Go(ctx, "shutdown watcher", func() {
		s := <-sig
		log.Info("shutting down", "signal", s.String())
		shCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if serr := srv.Shutdown(shCtx); serr != nil {
			log.Error("shutdown did not drain cleanly", "error", serr)
			_ = srv.Close() //nolint:errcheck // the drain already failed; closing is the fallback.
		}
		close(done)
	})

	log.Info("listening", "addr", cfg.Listen, "app_host", cfg.AppHost)
	if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		say(stderr, "stowcloud %s: serve: %v\n", version, err)
		return exitNoAnswer
	}
	<-done
	return exitOK
}
