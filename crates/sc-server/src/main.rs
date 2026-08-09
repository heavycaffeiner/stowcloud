use clap::Parser;

// "musl's default allocator is much slower than glibc's
// for workloads with heavy multithreaded allocation, so `mimalloc` is set as
// the global allocator." Gated to
// `target_env = "musl"` rather than applied unconditionally: the doc's own
// argument is specifically about musl's allocator (no per-thread arenas,
// effectively a single global lock under contention), not about glibc's,
// and `crates/sc-server/Cargo.toml`'s matching
// `[target.'cfg(target_env = "musl")'.dependencies]` table means the
// `mimalloc` crate — and the C toolchain its build script needs — is only
// ever pulled in for the target that actually ships (musl,
// x86_64/aarch64-unknown-linux-musl). Native Windows dev builds and the
// `ubuntu-latest` glibc CI job never touch it.
//
// This lives in `main.rs`, not `lib.rs`, because `main.rs` is the crate root
// of the `sc-server` *binary* target and `#[global_allocator]` is a
// whole-program setting resolved at link time — declaring it here covers
// every allocation the linked `sc_server` library (and everything it calls
// into) makes too.
//
// Measured, not asserted: a standalone multi-threaded small-allocation microbenchmark
// (many 16B-1KiB `Vec`/`String`/`HashMap` allocations across 4-8 threads,
// matching "many small allocations across a thread pool") was cross-built
// for x86_64-unknown-linux-musl and, separately, built and run natively on a
// real Linux box (the project's Rocky 9 test VM) with and without mimalloc
// as the global allocator. On that VM — glibc, not musl, and virtualized —
// mimalloc was consistently ~3-4x *slower* than the default allocator for
// this workload, almost certainly because mimalloc's segment/page
// management leans on `mmap`/`madvise` more heavily than glibc's
// arena-based malloc, and syscalls are relatively more expensive inside a
// VM. That result does not contradict: musl's allocator
// (dlmalloc-derived, no per-thread arenas) is a different comparison than
// glibc's, and no environment reachable from this box could actually
// execute a linked musl binary to test *that* comparison directly — Windows
// cannot run ELF at all, and cross-linking a full musl executable here (as
// opposed to `cargo check`/`clippy`, which only compile-and-archive, never
// link, and which is all `scripts/verify.sh` ever does for the musl
// target) hits a duplicate `_start` conflict between rustc's self-contained
// musl CRT objects and zig's own bundled musl libc. The real answer for
// musl specifically can only come from the Docker image this repo's own
// `docker` CI workflow builds and smoke-tests in a genuine Linux container
// — re-measure there before leaning on this doc's throughput claim.
#[cfg(target_env = "musl")]
#[global_allocator]
static GLOBAL_ALLOCATOR: mimalloc::MiMalloc = mimalloc::MiMalloc;

fn main() -> anyhow::Result<()> {
    // `healthcheck` is deliberately not a clap subcommand of `sc_server::Cli`
    // (`crates/sc-server/src/lib.rs`) — it short-circuits here, before
    // `Cli::parse()` ever runs, so it never touches that enum. See the
    // Dockerfile's `HEALTHCHECK` instruction and
    // for why this exists: the runtime image
    // (distroless/static, no shell, no curl/wget) has nothing else capable
    // of running an exec-form probe, so the probe has to be this same
    // binary, re-invoked with a different argv[1].
    if std::env::args().nth(1).as_deref() == Some("healthcheck") {
        std::process::exit(run_healthcheck());
    }

    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    let cli = sc_server::Cli::parse();
    sc_server::dispatch(cli)
}

/// Loopback liveness probe for `HEALTHCHECK CMD ["/sc-server", "healthcheck"]`.
///
/// Reads `SC_BIND` the same way `Config::apply_env` does
/// (`crates/sc-server/src/config.rs`: `v.parse::<SocketAddr>()`) to find the
/// port the running server was told to bind, then always dials `127.0.0.1`
/// on that port over TLS, because that is the only thing the server speaks.
/// The probe runs as a second process inside the *same* container, hence the
/// same network namespace, so loopback reaches a socket bound to `0.0.0.0` or
/// to any specific interface either way.
/// Defaults to port 8443 (the image's own `ENV SC_BIND` default) if the
/// variable is absent or unparseable.
///
/// Exit code is liveness, not health. `GET /api/health`
/// (`crates/sc-server/src/routes.rs`) answers `"ok"` or `"degraded"`
/// (`crates/sc-server/src/diagnostics.rs`), and both mean the process is up
/// and answering requests — a rejected share, a failed SMB bind, or a
/// tripped DB-size guard is a configuration state, not something a
/// container restart fixes, so mapping "degraded" to "unhealthy" would just
/// make Docker restart-loop a problem forever without ever resolving it.
/// This exits `0` for *either* JSON status, and `1` only when the server
/// doesn't answer a well-formed health response at all (refused connection,
/// timeout, garbled response) — the one condition a restart can plausibly
/// help.
fn run_healthcheck() -> i32 {
    use std::io::{Read, Write};
    use std::net::{IpAddr, Ipv4Addr, SocketAddr, TcpStream};
    use std::path::PathBuf;
    use std::sync::Arc;
    use std::time::Duration;

    use rustls::pki_types::pem::PemObject;
    use rustls::pki_types::{CertificateDer, ServerName};

    let port = std::env::var("SC_BIND")
        .ok()
        .and_then(|v| v.parse::<SocketAddr>().ok())
        .map(|a| a.port())
        .unwrap_or(8443);
    let addr = format!("127.0.0.1:{port}");

    // The server's own certificate, as the only trust anchor. Skipping
    // verification would have been shorter and is the wrong habit to encode in
    // a file that ships in the image: the probe runs in the same container as
    // the server, so it can read the very certificate that server presents, and
    // real verification costs nothing here. `127.0.0.1` is always in the SAN
    // list (`tls::san_entries`), so the name check passes on its own terms.
    let data_dir = match std::env::var("SC_DATA_DIR") {
        Ok(v) if !v.is_empty() => PathBuf::from(v),
        _ => PathBuf::from("/var/lib/sc"),
    };
    let mut roots = rustls::RootCertStore::empty();
    let certs = match CertificateDer::pem_file_iter(data_dir.join("tls/self-signed.crt")) {
        Ok(it) => it,
        Err(_) => return 1,
    };
    for cert in certs.flatten() {
        if roots.add(cert).is_err() {
            return 1;
        }
    }
    if roots.is_empty() {
        return 1;
    }
    let tls_config = rustls::ClientConfig::builder()
        .with_root_certificates(roots)
        .with_no_client_auth();
    let name = ServerName::IpAddress(IpAddr::V4(Ipv4Addr::LOCALHOST).into());
    let Ok(conn) = rustls::ClientConnection::new(Arc::new(tls_config), name) else {
        return 1;
    };

    let timeout = Duration::from_secs(3);
    let Ok(stream) = TcpStream::connect(&addr) else {
        return 1;
    };
    if stream.set_read_timeout(Some(timeout)).is_err()
        || stream.set_write_timeout(Some(timeout)).is_err()
    {
        return 1;
    }
    let mut tls = rustls::StreamOwned::new(conn, stream);

    let request = b"GET /api/health HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n";
    if tls.write_all(request).is_err() {
        return 1;
    }

    // A server that closes without `close_notify` surfaces as an error here
    // even though the response arrived intact, so what was read matters and the
    // error on its own does not. An empty buffer still fails, below.
    let mut body = Vec::new();
    let _ = tls.read_to_end(&mut body);
    let text = String::from_utf8_lossy(&body);

    let is_200 = text
        .lines()
        .next()
        .is_some_and(|line| line.contains(" 200 "));
    // Tolerate either compact or spaced JSON formatting; this is a liveness
    // probe reading a two-key object, not a general JSON parser.
    let has_known_status = text.contains("\"status\":\"ok\"")
        || text.contains("\"status\": \"ok\"")
        || text.contains("\"status\":\"degraded\"")
        || text.contains("\"status\": \"degraded\"");

    if is_200 && has_known_status {
        0
    } else {
        1
    }
}
