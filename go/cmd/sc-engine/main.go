// Linux only, because the engine it serves is Linux only.
//go:build linux

// A listener in front of the rebuilt engine.
//
// The shipped command still serves the old stack, and until it is rewired
// there is no way to reach engine/lifecycle over a socket. That makes the v1
// surface unreachable from a browser, so the frontend cannot be driven against
// it and nothing about the rewiring can be checked. This is that socket.
//
// It serves the API and nothing else. The SPA is embedded in a package this
// tier may not import, so the frontend runs under its own dev server and
// proxies here, which is how web/vite.config.ts is already set up.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/sys/unix"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/jail"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/mountinfo"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vault"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview/worker"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/fsatomic"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

func main() {
	// The subcommands run without a listener. The decoder re-exec is one of
	// them: it arrives with no argv to parse, which is why the dispatch
	// precedes the flags rather than following them.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "settings":
			os.Exit(runSettings(os.Args[2:]))
		case "serve":
			os.Exit(runServeCmd(os.Args[2:]))
		case "healthcheck":
			os.Exit(runHealthcheck(os.Args[2:]))
		case "preview-worker":
			os.Exit(runPreviewWorker())
		}
	}

	var (
		addr    = flag.String("addr", "", "listen address; overrides the stored one")
		dataDir = flag.String("data", ".dev/data", "data directory")
		plain   = flag.Bool("plain", false, "serve HTTP instead of HTTPS")
	)
	flag.Parse()

	if err := run(*addr, *dataDir, *plain); err != nil {
		slog.Error("sc-engine failed", "error", err)
		os.Exit(1)
	}
}

// defaultListen is where the server binds when neither the flag nor the
// stored setting names an address.
const defaultListen = "127.0.0.1:8081"

func run(addr, dataDir string, plain bool) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolving the data directory: %w", err)
	}
	if mkErr := os.MkdirAll(abs, 0o700); mkErr != nil {
		return fmt.Errorf("creating the data directory: %w", mkErr)
	}

	// The atomic path resolver is checked before anything is opened: under a
	// hardening policy that refuses a racy resolver, saying so now beats
	// failing later at every write.
	if rerr := vfs.RequireResolver(vfs.Probe()); rerr != nil {
		return rerr
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The settings and the share hosts are read before anything else opens,
	// because the sandbox is built from them: its domain has to name the data
	// directory and every share's parent before the first file is opened, and
	// a Landlock domain cannot be widened once it is installed.
	values, shareHosts, exactPaths := bootSettings(abs, logger)

	// Read on the first pass only. The sandbox re-executes this binary to
	// spread its domain across every thread, and the image it produces
	// cannot read /proc/self/mountinfo: the domain built on the first pass
	// is already installed and governs this one. Reading it here would warn
	// that folders cannot be added on every start of a server where they
	// can.
	var roots []string
	if !jail.Reexeced(jail.ReexecMarker()) {
		mounts, merr := mountinfo.Self()
		if merr != nil {
			// A warning rather than a failure: the server still starts with
			// the data directory granted, and this is the one thing that
			// explains a folder being refused with no share root to blame.
			logger.Warn("reading the mount table; new folders cannot be added until this is fixed", "error", merr)
		}
		roots = shareRoots(mounts)
		logger.Info("discovered share roots", "roots", roots)
	}

	domain := jailSpec(values, abs, roots, shareHosts, exactPaths)
	jailStatus, jerr := jail.Apply(values.Hardening, domain)
	if jerr != nil {
		code := jail.Refuse(os.Stderr, jailStatus)
		slog.Error("the sandbox could not be applied; a required policy this kernel cannot satisfy is a stored setting, and it is the one most likely to put a deployment in a restart loop",
			"status", jailStatus.String())
		os.Exit(code)
	}
	logger.Info("hardening", "status", jailStatus.String())

	// The flag wins, then the stored setting, then the compiled default. The
	// stored one exists so an operator can move a deployment that is bound
	// somewhere unreachable, which only works if starting it without an
	// address actually uses what they stored.
	if addr == "" {
		addr = values.Listen
	}
	if addr == "" {
		addr = defaultListen
	}

	// No worker is named, which leaves the pool re-executing this binary with
	// the subcommand handled at the top of main.
	eng, err := lifecycle.Open(ctx, lifecycle.Options{
		DataDir: abs,
		Logger:  logger,
		// What was actually installed above, not what the document says: a
		// restart compares the two to tell a change it can apply from one the
		// kernel will not let it.
		Hardening: values.Hardening,
	})
	if err != nil {
		return fmt.Errorf("opening the engine: %w", err)
	}
	defer func() {
		if cerr := eng.Close(); cerr != nil {
			logger.Error("closing the engine", "error", cerr)
		}
	}()

	// One builder per address, so a rebind constructs its own app and listener
	// rather than moving a live one. The engine is mounted per generation for
	// the same reason the supervisor gives: draining the old app must not stop
	// the new one.
	scheme := "https"
	if plain {
		scheme = "http"
	}
	build := func(a string) (*fiber.App, net.Listener, error) {
		app, merr := eng.Mount()
		if merr != nil {
			return nil, nil, fmt.Errorf("mounting: %w", merr)
		}
		ln, lerr := net.Listen("tcp", a)
		if lerr != nil {
			return nil, nil, fmt.Errorf("listening on %s: %w", a, lerr)
		}
		if !plain {
			cert, terr := devCertificate(abs, a)
			if terr != nil {
				//nolint:errcheck // the bind failed; the listener is going away.
				_ = ln.Close()
				return nil, nil, terr
			}
			ln = tls.NewListener(ln, &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			})
		}
		return app, ln, nil
	}

	srv := server.NewServe(build, logger)
	if serr := srv.Swap(addr); serr != nil {
		return fmt.Errorf("serving on %s: %w", addr, serr)
	}
	logger.Info("serving the engine",
		"url", scheme+"://"+srv.Current().Ln.Addr().String(), "data", abs)

	// A saved bind address moves the socket. The old generation keeps
	// answering until the new one is confirmed serving, so an address that
	// cannot be bound leaves the server exactly where it was.
	eng.OnBindChange(addr, func(next string) {
		if serr := srv.Swap(next); serr != nil {
			logger.Error("the bind address could not be moved",
				"address", next, "error", serr)
			return
		}
		logger.Info("the listener moved", "url", scheme+"://"+next)
	})

	// A restart replaces this process image rather than exiting: the engine is
	// its container's only process, so an exit takes the container down and
	// whether it comes back is a policy the operator may not have set.
	//
	// Drained first, so an upload mid-write is not cut off, and the databases
	// are closed so the new image opens files nobody is still writing.
	eng.OnRestart(func() {
		logger.Info("restarting")
		if serr := srv.Shutdown(); serr != nil {
			logger.Error("the listener did not drain; restarting anyway", "error", serr)
		}
		if cerr := eng.Close(); cerr != nil {
			logger.Error("the engine did not close cleanly; restarting anyway", "error", cerr)
		}
		// Only a failure returns. The old image has already drained, so there
		// is nothing to keep serving with: say why, and leave the exit code to
		// whatever supervises this.
		if xerr := lifecycle.ExecSelf(); xerr != nil {
			logger.Error("the process could not replace itself", "error", xerr)
			os.Exit(1)
		}
	})

	<-ctx.Done()
	logger.Info("shutting down")
	if serr := srv.Shutdown(); serr != nil {
		return fmt.Errorf("shutting down: %w", serr)
	}
	return nil
}

// bootSettings reads the stored settings and every path a registered share
// will make this process open, in one brief open that closes before the
// engine is constructed. The sandbox is built from what it returns, which is
// why this runs first: a Landlock domain cannot be widened after it is
// installed, so every such path has to be known before the databases below
// are opened for the engine proper.
//
// The two path lists are granted differently, which is why they are two
// lists. A share host is a directory somebody may add a sibling folder
// beside, so its parent is granted. A VeraCrypt container is one file, and
// its parent is a directory holding the operator's other containers, which
// this process has no reason to read.
func bootSettings(dataDir string, log *slog.Logger) (
	values runtimecfg.Values, shareHosts, exactPaths []string,
) {
	ctx := context.Background()

	stateFile, err := dbfile.Open(ctx, state.Spec(filepath.Join(dataDir, "state.db")))
	if err != nil {
		return runtimecfg.Defaults(), nil, nil
	}
	defer func() {
		if cerr := stateFile.Close(); cerr != nil {
			log.Warn("closing the settings probe", "error", cerr)
		}
	}()
	st := state.New(stateFile)

	values = runtimecfg.Load(ctx, st, runtimecfg.Defaults(), log)

	rows, lerr := st.ListShares(ctx)
	if lerr != nil {
		return values, nil, nil
	}
	for _, row := range rows {
		switch row.Backend {
		case string(core.BackendVeracrypt):
			cfg, perr := vault.ParseConfig([]byte(row.BackendConfig))
			if perr != nil {
				// A warning rather than a failure: the same configuration is
				// read again by the registration below, and that is where an
				// operator is told the share is broken.
				log.Warn("a veracrypt share's configuration is unreadable, so its container is not granted",
					"share", row.Name, "error", perr)
				continue
			}
			exactPaths = append(exactPaths, cfg.Container)
		case string(core.BackendS3):
			// Everything an S3 share touches is scratch space under the data
			// directory, which is granted already.
		default:
			shareHosts = append(shareHosts, row.Host)
		}
	}
	return values, shareHosts, exactPaths
}

// jailSpec builds the sandbox domain from what this process touches: the data
// directory, the SMB sidecar's config directory and control socket when one
// is configured, every discovered share root, the parent directory of every
// registered share, and every exact file a share names.
//
// A discovered share root is granted at the mount point itself, never its
// parent: granting the parent of /mnt/photos would hand over all of /mnt,
// which is everything else mounted alongside it. A registered share's own
// parent is still granted, because that share already exists and was
// resolved from the process, so the path is known reachable; the discovered
// roots exist to admit a folder that has not been registered yet.
//
// An exact path is granted as itself and nothing around it. It is what a
// VeraCrypt share needs: one container file, whose directory holds the
// operator's other containers and is none of this process's business.
//
// "/" is never granted, which is the one thing this sandbox exists to
// prevent.
func jailSpec(values runtimecfg.Values, dataDir string, roots, shareHosts, exactPaths []string) jail.Spec {
	spec := jail.Spec{ExceptExec: true}
	spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: dataDir})

	if values.SMB.Enabled && values.SMBConfigDir != "" {
		spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: values.SMBConfigDir})
	}
	if sock := values.SMBSocket; values.SMB.Enabled && sock != "" {
		if dir := filepath.Dir(sock); dir != "" && dir != "/" {
			spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: dir})
		}
	}
	if values.ThumbnailDir != "" {
		spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: filepath.Clean(values.ThumbnailDir)})
	}
	spec.GrantBeneath = append(spec.GrantBeneath, outboundGrants()...)

	seen := map[string]bool{}
	grant := func(dir string) {
		if dir == "" || dir == "/" || dir == "." || seen[dir] {
			return
		}
		seen[dir] = true
		spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: dir})
	}

	for _, root := range roots {
		if root != "" {
			grant(filepath.Clean(root))
		}
	}
	for _, host := range shareHosts {
		if host == "" {
			continue
		}
		clean := filepath.Clean(host)
		parent := filepath.Dir(clean)
		if parent == "/" || parent == "." || parent == clean {
			parent = clean
		}
		grant(parent)
	}
	for _, path := range exactPaths {
		if path == "" || seen[path] {
			continue
		}
		clean := filepath.Clean(path)
		if clean == "/" || clean == "." || seen[clean] {
			continue
		}
		seen[clean] = true
		// A rule on a regular file may only carry rights that apply to one.
		// Landlock answers EINVAL for a directory-only right such as
		// read-dir, and the whole domain then fails to install, which under
		// the required policy is a server that will not start.
		spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{
			Path: clean,
			Access: uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE |
				unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
				unix.LANDLOCK_ACCESS_FS_TRUNCATE),
		})
	}
	return spec
}

// outboundGrants are the read-only files this process must reach to dial a
// host by name over TLS: the resolver's configuration and the system trust
// store.
//
// Without them a domain that grants only the served data leaves the Go
// resolver unable to read /etc/resolv.conf, so it falls back to a nameserver
// on localhost that nothing answers, and every outbound connection fails
// with a DNS error naming a server the operator never configured. That is
// what registering an S3-compatible share did, and what single sign-on
// against an external provider would do.
//
// Read-only, and never the whole of /etc: these are world-readable files
// holding no secret, which this process could already read before the domain
// existed. A missing one is skipped rather than fatal, since Landlock
// refuses a rule whose path does not resolve and an image laid out
// differently must still start.
func outboundGrants() []jail.Grant {
	const readFile = uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)
	const readDir = readFile | uint64(unix.LANDLOCK_ACCESS_FS_READ_DIR)

	candidates := []jail.Grant{
		{Path: "/etc/resolv.conf", Access: readFile},
		{Path: "/etc/hosts", Access: readFile},
		{Path: "/etc/nsswitch.conf", Access: readFile},
		{Path: "/etc/ssl/certs", Access: readDir},
		{Path: "/etc/pki/tls/certs", Access: readDir},
	}
	out := make([]jail.Grant, 0, len(candidates))
	for _, g := range candidates {
		if _, err := os.Stat(g.Path); err != nil {
			continue
		}
		out = append(out, g)
	}
	return out
}

// namedShareDirs are the directories a deployment puts served data in when
// it has no bind mounts to discover: bare metal, a VM, or any runtime whose
// root is a single filesystem. There the mount table has no row for any of
// these, because they are all part of "/", so without this list nothing
// outside the data directory could ever be granted on such a host.
//
// The obvious alternative, granting every child of "/", was rejected: on a
// normal host that set is /etc, /usr, /var and /root among others, which is
// every path this sandbox exists to keep out.
func namedShareDirs() []string {
	return []string{"/srv", "/mnt", "/media", "/data", "/home", "/opt"}
}

// shareRoots picks the mounts a folder may be registered under, plus
// whichever of namedShareDirs exist on this host.
//
// A mount qualifies when all four hold: its filesystem type is on vfs's
// allow-list, its point is not "/" itself, it does not sit under /proc,
// /sys, /dev or /run, and its point is a real directory. The result is
// sorted and deduplicated, so the domain built from it is stable even when
// the kernel reports the same point more than once.
func shareRoots(mounts []mountinfo.Mount) []string {
	var out []string
	for _, m := range mounts {
		point := filepath.Clean(m.Point)

		// Rule 1: only a filesystem this server can actually hold a share
		// on. One allow-list, read here rather than reimplemented: vfs
		// already refuses overlay, proc, sysfs, fuse, nfs and cifs.
		if adm, _ := vfs.AdmitFsType(vfs.ParseFsType(m.FsType)); !adm.OK {
			continue
		}

		// Rule 2: "/" is refused explicitly, and this check cannot be
		// folded into rule 1. In a container the root is overlay and rule
		// 1 already drops it, but on bare metal or a VM the root is ext4
		// or xfs and would otherwise pass, which would grant the whole
		// domain and turn this sandbox into a no-op.
		if point == "/" {
			continue
		}

		// Rule 3: kernel and runtime bookkeeping. tmpfs is on the allow
		// list, so without this /dev/shm, /proc/acpi and the many
		// /sys/devices/system/cpu/*/thermal_throttle mounts would pass
		// rule 1.
		if underAny(point, "/proc", "/sys", "/dev", "/run") {
			continue
		}

		// Rule 4: a container runtime binds single files over
		// /etc/hostname, /etc/resolv.conf and /etc/hosts. Those pass rules
		// 1-3 and are not a place a folder can be registered.
		info, err := os.Stat(point)
		if err != nil || !info.IsDir() {
			continue
		}

		out = append(out, point)
	}

	// The named-directory source: granted only when the directory is
	// actually present, using the same stat the mount rule applies above.
	// A deployment without /srv does not get a grant for a path that
	// is not there.
	for _, dir := range namedShareDirs() {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		out = append(out, filepath.Clean(dir))
	}

	slices.Sort(out)
	return slices.Compact(out)
}

// underAny reports whether point is one of bases or lies beneath one.
func underAny(point string, bases ...string) bool {
	for _, base := range bases {
		if point == base || strings.HasPrefix(point, base+"/") {
			return true
		}
	}
	return false
}

// runPreviewWorker is the jailed decoder, and it is never run by hand.
//
// Confinement is required rather than preferred here. This is the one process
// whose whole job is parsing untrusted image data, so a kernel that cannot
// confine it is a refusal: decoding unconfined would be worse than not
// producing thumbnails at all.
func runPreviewWorker() int {
	status, err := worker.Run(jail.Required)
	if err != nil {
		if errors.Is(err, jail.ErrHardeningRefused) {
			// The step, the errno and the kernel, because "hardening failed"
			// is not something anybody can act on.
			return jail.Refuse(os.Stderr, status)
		}
		slog.Error("the preview worker stopped", "error", err)
		return 1
	}
	return 0
}

// devCertificate reuses the engine's own material so a browser that has
// accepted this deployment's certificate once keeps trusting it.
func devCertificate(dataDir, addr string) (tls.Certificate, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		host = "127.0.0.1"
	}
	hosts := []string{host}
	if host != "localhost" {
		hosts = append(hosts, "localhost")
	}

	paths := server.TLSPaths{
		Cert: filepath.Join(dataDir, "tls", "cert.pem"),
		Key:  filepath.Join(dataDir, "tls", "key.pem"),
	}
	if mkErr := os.MkdirAll(filepath.Dir(paths.Cert), 0o700); mkErr != nil {
		return tls.Certificate{}, fmt.Errorf("creating the TLS directory: %w", mkErr)
	}

	_, statErr := os.Stat(paths.Cert)
	firstBoot := errors.Is(statErr, os.ErrNotExist)

	return server.EnsureTLS(paths, hosts, clock.System(), firstBoot,
		func(names []string, modes []uint32, write func(i int, f *os.File) error) error {
			units := make([]fsatomic.Unit, len(names))
			for i, n := range names {
				units[i] = fsatomic.Unit{Path: n, Mode: modes[i]}
			}
			return fsatomic.ReplaceFilesDurable(units, write)
		})
}
