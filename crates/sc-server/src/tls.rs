//! The LAN HTTPS listener and the self-signed certificate behind it.
//!
//! # Why the server terminates TLS at all
//!
//! The session cookie is `__Host-sc_sid` (`sc_http::SESSION_COOKIE`), and the
//! `__Host-` prefix is defined to require `Secure`, so a browser discards it
//! outright over plaintext HTTP. `http://localhost` is the one exception every
//! browser makes. The practical consequence was that reaching the server at its
//! LAN address served the page and then silently refused to stay logged in: the
//! login POST answered `200`, the cookie was dropped, and the next request was
//! anonymous. No error said so anywhere.
//!
//! A reverse proxy fixes that for the name it terminates, which is the right
//! answer for the outside world and an awkward one for a NAS on a home LAN,
//! where "type the box's address" is the ordinary way in. So the server puts up
//! its own HTTPS listener with a certificate it generates for itself.
//!
//! # Why self-signed is enough here, and where it is not
//!
//! Nothing trusts this certificate: the first visit from each browser shows an
//! interstitial the user has to click through. That is a real cost and it is
//! accepted deliberately, because the alternative on a private address is not
//! "a trusted certificate" but "no HTTPS and therefore no login". After the
//! click-through the origin *is* `https://`, which is all `__Host-` needs.
//!
//! This is not a substitute for the reverse proxy on the public name. It exists
//! so `https://192.168.0.50:8443` works without one.

use std::net::{IpAddr, SocketAddr, UdpSocket};
use std::path::{Path, PathBuf};
use std::sync::Arc;

use anyhow::Context;
use rustls::pki_types::pem::PemObject;
use rustls::pki_types::{CertificateDer, PrivateKeyDer};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::mpsc;
use tokio_rustls::TlsAcceptor;

/// Where the generated material lives, under the data directory.
///
/// Not beside the master key: this is a certificate for a private address that
/// the server regenerates whenever it stops matching, not a secret whose loss
/// costs anything. Backing up `data/` and getting it is harmless.
fn tls_dir(data_dir: &Path) -> PathBuf {
    data_dir.join("tls")
}

/// The address this host uses to reach the rest of its LAN.
///
/// No interface enumeration crate: a UDP socket "connected" to a documentation
/// address sends nothing, but the kernel still has to pick a source address for
/// it, and that is exactly the answer wanted. Multi-homed hosts get whichever
/// interface the routing table prefers, which is the one a LAN client reaches
/// anyway; anything else can be named in `app_hosts` and lands in the SAN list
/// through [`san_entries`].
///
/// **In a container this is the bridge address, not the host's.** A bridged
/// namespace cannot see `192.168.0.50` at all, so a compose deployment has to
/// name the host's LAN address in `app_hosts` for the certificate to cover it.
/// Getting that wrong is not fatal: the browser reports a name mismatch instead
/// of an unknown issuer, and both are interstitials the user clicks through.
fn primary_lan_address() -> Option<IpAddr> {
    // TEST-NET-1 (RFC 5737): reserved for documentation and never routed.
    let sock = UdpSocket::bind(("0.0.0.0", 0)).ok()?;
    sock.connect(("192.0.2.1", 1)).ok()?;
    let ip = sock.local_addr().ok()?.ip();
    (!ip.is_unspecified() && !ip.is_loopback()).then_some(ip)
}

/// Every name and address the generated certificate should answer to, sorted
/// and deduplicated so the result is stable across restarts.
///
/// Stability matters: [`load_or_generate`] compares this list against the one
/// recorded beside the certificate to decide whether to regenerate, and an
/// unstable ordering would regenerate on every start and re-prompt every
/// browser that had accepted the old one.
fn san_entries(app_hosts: &[String], lan: Option<IpAddr>) -> Vec<String> {
    let mut v: Vec<String> = vec!["localhost".into(), "127.0.0.1".into(), "::1".into()];
    v.extend(app_hosts.iter().map(|h| h.trim().to_ascii_lowercase()));
    v.extend(lan.map(|ip| ip.to_string()));
    v.retain(|s| !s.is_empty());
    v.sort();
    v.dedup();
    v
}

/// The recorded SAN list from the previous generation, if the files are all
/// present. Any read failure reads as "regenerate", which is always safe.
fn recorded_sans(dir: &Path) -> Option<Vec<String>> {
    let txt = std::fs::read_to_string(dir.join("self-signed.sans")).ok()?;
    Some(txt.lines().map(|l| l.trim().to_string()).filter(|l| !l.is_empty()).collect())
}

/// Generate a self-signed certificate covering `sans` and write it out.
fn generate(dir: &Path, sans: &[String]) -> anyhow::Result<()> {
    // `rcgen` classifies each entry itself: an IP-parseable string becomes an
    // iPAddress SAN and everything else a dNSName. Browsers match a literal
    // address against the former only, so getting this wrong would produce a
    // certificate that fails on exactly the URL it was generated for.
    let mut params = rcgen::CertificateParams::new(sans.to_vec())
        .context("building certificate parameters")?;
    params
        .distinguished_name
        .push(rcgen::DnType::CommonName, "Stowcloud LAN (self-signed)");
    // Self-signed leaf, no CA bit: nothing else is ever issued from this, and a
    // certificate that could sign others is a worse thing to leave on disk.
    params.is_ca = rcgen::IsCa::NoCa;

    let key = rcgen::KeyPair::generate().context("generating a key pair")?;
    let cert = params.self_signed(&key).context("self-signing")?;

    std::fs::create_dir_all(dir).with_context(|| format!("creating {}", dir.display()))?;
    write_private(&dir.join("self-signed.key"), key.serialize_pem().as_bytes())?;
    std::fs::write(dir.join("self-signed.crt"), cert.pem()).context("writing the certificate")?;
    // Written last, so a crash between the two leaves no `.sans` file and the
    // next start regenerates rather than trusting a half-written pair.
    std::fs::write(dir.join("self-signed.sans"), sans.join("\n")).context("writing the SAN list")?;
    Ok(())
}

/// Write a private key with an owner-only mode from the moment it exists.
///
/// `std::fs::write` then `set_permissions` would leave the key world-readable
/// for the width of that gap. `OpenOptions::mode` applies it at `open`.
fn write_private(path: &Path, bytes: &[u8]) -> anyhow::Result<()> {
    use std::io::Write;
    let mut opts = std::fs::OpenOptions::new();
    opts.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut f = opts.open(path).with_context(|| format!("creating {}", path.display()))?;
    f.write_all(bytes).with_context(|| format!("writing {}", path.display()))?;
    f.sync_all().with_context(|| format!("flushing {}", path.display()))?;
    Ok(())
}

/// Load the stored certificate, regenerating it first when it is missing or
/// does not yet cover an address this host answers on.
///
/// Regenerating at all is what makes a DHCP lease change survivable: a
/// certificate issued for `192.168.0.50` presents a name mismatch once the box
/// comes back as `192.168.0.51`.
///
/// The check is **additive**: regenerate when something is missing, never
/// because something went away, and carry the old entries into the new
/// certificate. Comparing for equality instead looks tidier and churns: a
/// container's bridge address is not stable across recreation, so every
/// `docker compose up` would have issued a fresh certificate and invalidated the
/// exception each browser had stored. Keeping a stale private address costs
/// nothing, since nothing trusts this certificate to begin with.
pub fn load_or_generate(
    data_dir: &Path,
    app_hosts: &[String],
) -> anyhow::Result<Arc<rustls::ServerConfig>> {
    let dir = tls_dir(data_dir);
    let want = san_entries(app_hosts, primary_lan_address());

    let usable = dir.join("self-signed.crt").exists() && dir.join("self-signed.key").exists();
    // A recorded list with no certificate beside it describes nothing, so it is
    // discarded rather than carried forward.
    let have = if usable { recorded_sans(&dir).unwrap_or_default() } else { Vec::new() };
    let missing: Vec<&String> = want.iter().filter(|w| !have.contains(w)).collect();
    if !usable || !missing.is_empty() {
        let mut all = have.clone();
        all.extend(want.iter().cloned());
        all.sort();
        all.dedup();
        tracing::info!(
            sans = ?all,
            added = ?missing,
            regenerated = usable,
            "tls: writing a self-signed certificate for the LAN listener"
        );
        generate(&dir, &all)?;
    }

    // `rustls`'s own PEM reader, reached through the `pki-types` re-export.
    // `rustls-pemfile` is the better-known crate for this and is unmaintained
    // (RUSTSEC-2025-0134); these APIs are where its functionality moved, and
    // they cost no dependency at all because `rustls` already enables the
    // feature that carries them.
    let certs = CertificateDer::pem_file_iter(dir.join("self-signed.crt"))
        .context("reading the certificate")?
        .collect::<Result<Vec<_>, _>>()
        .context("parsing the certificate")?;
    let key = PrivateKeyDer::from_pem_file(dir.join("self-signed.key"))
        .context("reading the private key")?;

    let mut cfg = rustls::ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(certs, key)
        .context("assembling the TLS configuration")?;
    // Browsers negotiate h2 when offered it; this server is wired for HTTP/1.1
    // only (`axum::serve` over a byte stream), so advertising just that keeps a
    // client from selecting a protocol nothing here speaks.
    cfg.alpn_protocols = vec![b"http/1.1".to_vec()];
    Ok(Arc::new(cfg))
}

/// A [`axum::serve::Listener`] yielding finished TLS connections.
///
/// Handshakes run in their own tasks and completed connections arrive over a
/// channel, rather than being awaited inline in `accept`. Inline is the obvious
/// shape and it is a denial of service: one client that opens a socket and then
/// sends nothing holds the accept loop, and no further connection is served
/// until it times out.
pub struct TlsListener {
    local: SocketAddr,
    rx: mpsc::Receiver<(tokio_rustls::server::TlsStream<TcpStream>, SocketAddr)>,
}

impl TlsListener {
    pub fn spawn(listener: TcpListener, acceptor: TlsAcceptor) -> anyhow::Result<Self> {
        let local = listener.local_addr().context("reading the TLS listener address")?;
        // Bounded, so a flood of handshakes cannot grow this without limit; the
        // accept loop simply stops taking new sockets while it is full.
        let (tx, rx) = mpsc::channel(128);
        tokio::spawn(async move {
            loop {
                let (tcp, peer) = match listener.accept().await {
                    Ok(v) => v,
                    // Per-connection errors (a client gone between the SYN and
                    // the accept) are normal and must not end the loop.
                    Err(e) => {
                        tracing::debug!(error = %e, "tls: accept failed");
                        continue;
                    }
                };
                let acceptor = acceptor.clone();
                let tx = tx.clone();
                tokio::spawn(async move {
                    match first_byte(&tcp).await {
                        // A TLS record layer opens with ContentType::Handshake.
                        Some(TLS_HANDSHAKE) => match acceptor.accept(tcp).await {
                            Ok(stream) => {
                                let _ = tx.send((stream, peer)).await;
                            }
                            // The overwhelmingly common cause is a browser that
                            // has not been given the certificate exception yet,
                            // so this is `debug`, not `warn`: at `warn` every
                            // first visit would look like a fault.
                            Err(e) => tracing::debug!(peer = %peer, error = %e, "tls: handshake failed"),
                        },
                        // Anything else on this port is somebody who typed
                        // `http://`. Answer the redirect and close; the socket
                        // never reaches the router.
                        Some(_) => redirect_to_https(tcp, peer).await,
                        None => tracing::debug!(peer = %peer, "tls: peer sent nothing before the deadline"),
                    }
                });
            }
        });
        Ok(Self { local, rx })
    }
}

/// `ContentType::Handshake`, the first byte of every TLS `ClientHello`.
const TLS_HANDSHAKE: u8 = 0x16;

/// How long a caller gets to reveal which protocol it speaks, and then to
/// finish its request line and headers if the answer was plaintext.
const PLAINTEXT_DEADLINE: std::time::Duration = std::time::Duration::from_secs(5);

/// Bounded so a plaintext caller cannot buy memory with headers it never ends.
const PLAINTEXT_MAX_HEAD: usize = 8 * 1024;

/// Peeks the first byte without consuming it, so the TLS acceptor still sees a
/// whole `ClientHello`.
///
/// Bounded, or a client that connects and then says nothing keeps a task and a
/// socket for as long as it cares to.
async fn first_byte(tcp: &TcpStream) -> Option<u8> {
    tokio::time::timeout(PLAINTEXT_DEADLINE, async {
        let mut b = [0u8; 1];
        loop {
            if tcp.readable().await.is_err() {
                return None;
            }
            match tcp.peek(&mut b).await {
                Ok(0) => return None,
                Ok(_) => return Some(b[0]),
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => continue,
                Err(_) => return None,
            }
        }
    })
    .await
    .ok()
    .flatten()
}

/// Answers a plaintext request with `308` to the same URL over `https`.
///
/// This port serves TLS and nothing else, so a plaintext request here is a
/// person who typed the scheme rather than a client with a plan. Refusing the
/// connection told them nothing; a browser shows "the connection was reset" and
/// no part of that names the problem or its answer.
///
/// `308`, not `301`: a `301` invites a client to replay a POST as GET, and the
/// upload endpoints are the ones most likely to be hit at the wrong scheme by a
/// script somebody wrote against the old two-listener shape.
///
/// The `Location` is built from the request target and this connection's own
/// `Host`, and it only ever changes the scheme. That is not an open redirect:
/// the host it names is the one the caller already asked for. It is still
/// sanitised, because a `Host` is caller-controlled and lands in a header.
async fn redirect_to_https(mut tcp: TcpStream, peer: SocketAddr) {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    let head = match tokio::time::timeout(PLAINTEXT_DEADLINE, async {
        let mut buf = Vec::with_capacity(1024);
        let mut chunk = [0u8; 1024];
        loop {
            let n = tcp.read(&mut chunk).await.ok()?;
            if n == 0 {
                return None;
            }
            buf.extend_from_slice(&chunk[..n]);
            if buf.windows(4).any(|w| w == b"\r\n\r\n") {
                return Some(buf);
            }
            if buf.len() > PLAINTEXT_MAX_HEAD {
                return None;
            }
        }
    })
    .await
    {
        Ok(Some(h)) => h,
        _ => return,
    };

    let reply = match plaintext_redirect(&head) {
        Some(location) => format!(
            "HTTP/1.1 308 Permanent Redirect\r\nLocation: {location}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
        ),
        None => {
            tracing::debug!(peer = %peer, "tls: plaintext request without a usable Host");
            "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n".to_string()
        }
    };
    let _ = tcp.write_all(reply.as_bytes()).await;
    let _ = tcp.shutdown().await;
}

/// The `Location` for a plaintext request head, or `None` when it cannot be
/// trusted to build one.
fn plaintext_redirect(head: &[u8]) -> Option<String> {
    let text = std::str::from_utf8(head).ok()?;
    let mut lines = text.split("\r\n");

    // "GET /path?q HTTP/1.1"
    let mut request_line = lines.next()?.split(' ');
    let _method = request_line.next()?;
    let target = request_line.next()?;
    // Only an origin-form target. An absolute-form or `CONNECT` authority is
    // not a browser typing the wrong scheme, and rewriting one is guesswork.
    if !target.starts_with('/') {
        return None;
    }
    if !target.bytes().all(is_ok_target_byte) {
        return None;
    }

    let host = lines
        .take_while(|l| !l.is_empty())
        .find_map(|l| l.split_once(':').filter(|(k, _)| k.eq_ignore_ascii_case("host")))
        .map(|(_, v)| v.trim())?;
    if host.is_empty() || host.len() > 253 || !host.bytes().all(is_ok_host_byte) {
        return None;
    }

    Some(format!("https://{host}{target}"))
}

/// Unreserved, sub-delims, and the few others a path or query may carry. No
/// control characters, no space, and in particular no CR or LF, which is what
/// keeps this out of the response's own header framing.
fn is_ok_target_byte(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b"-._~:/?#[]@!$&'()*+,;=%".contains(&b)
}

/// Host names, IPv4 literals, and the brackets and colon an IPv6 literal with a
/// port needs.
fn is_ok_host_byte(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b"-._:[]".contains(&b)
}

impl axum::serve::Listener for TlsListener {
    type Io = tokio_rustls::server::TlsStream<TcpStream>;
    type Addr = SocketAddr;

    async fn accept(&mut self) -> (Self::Io, Self::Addr) {
        match self.rx.recv().await {
            Some(v) => v,
            // Only reachable if the accept task above is gone, which it never
            // is: it loops forever and holds the sender. Returning would make
            // `axum::serve` treat a dead listener as a clean shutdown of the
            // whole server, so this parks instead and leaves shutdown to the
            // signal handling in `cmd_serve`.
            None => std::future::pending().await,
        }
    }

    fn local_addr(&self) -> std::io::Result<Self::Addr> {
        Ok(self.local)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn san_list_is_stable_and_deduplicated() {
        let hosts = vec!["NAS.local".to_string(), "localhost".to_string()];
        let lan: IpAddr = "192.168.0.50".parse().unwrap();
        let a = san_entries(&hosts, Some(lan));
        let b = san_entries(&hosts, Some(lan));
        assert_eq!(a, b, "same inputs must not produce a different list");
        assert_eq!(a, vec!["127.0.0.1", "192.168.0.50", "::1", "localhost", "nas.local"]);
    }

    #[test]
    fn generated_material_round_trips_into_a_server_config() {
        let dir = tempfile::tempdir().unwrap();
        let hosts = vec!["nas.local".to_string()];
        let cfg = load_or_generate(dir.path(), &hosts).unwrap();
        assert_eq!(cfg.alpn_protocols, vec![b"http/1.1".to_vec()]);

        // A second call with the same inputs must reuse what is on disk, or
        // every restart would invalidate the exception each browser stored.
        let before = std::fs::read(dir.path().join("tls/self-signed.crt")).unwrap();
        load_or_generate(dir.path(), &hosts).unwrap();
        let after = std::fs::read(dir.path().join("tls/self-signed.crt")).unwrap();
        assert_eq!(before, after, "an unchanged SAN list must not regenerate");
    }

    #[test]
    fn a_new_name_regenerates() {
        let dir = tempfile::tempdir().unwrap();
        load_or_generate(dir.path(), &["nas.local".to_string()]).unwrap();
        let before = std::fs::read(dir.path().join("tls/self-signed.crt")).unwrap();
        load_or_generate(dir.path(), &["nas.local".to_string(), "cloud.example.com".to_string()]).unwrap();
        let after = std::fs::read(dir.path().join("tls/self-signed.crt")).unwrap();
        assert_ne!(before, after, "a new name must be covered by a new certificate");
    }

    /// The container case: the bridge address changes on recreation, and
    /// treating a *disappeared* entry as a reason to reissue would have made
    /// every `docker compose up` re-prompt every browser.
    #[test]
    fn a_name_that_goes_away_does_not_regenerate() {
        let dir = tempfile::tempdir().unwrap();
        load_or_generate(dir.path(), &["nas.local".to_string(), "172.17.0.2".to_string()]).unwrap();
        let before = std::fs::read(dir.path().join("tls/self-signed.crt")).unwrap();
        load_or_generate(dir.path(), &["nas.local".to_string()]).unwrap();
        let after = std::fs::read(dir.path().join("tls/self-signed.crt")).unwrap();
        assert_eq!(before, after, "a dropped entry is not a reason to reissue");
    }

    /// ...and the entry that replaces it is added without losing the old one,
    /// so a browser that accepted the previous certificate is not re-prompted
    /// for an address that still works.
    #[test]
    fn a_replaced_address_is_added_to_what_was_already_covered() {
        let dir = tempfile::tempdir().unwrap();
        load_or_generate(dir.path(), &["172.17.0.2".to_string()]).unwrap();
        load_or_generate(dir.path(), &["172.17.0.3".to_string()]).unwrap();
        let sans = std::fs::read_to_string(dir.path().join("tls/self-signed.sans")).unwrap();
        assert!(sans.contains("172.17.0.2"), "{sans}");
        assert!(sans.contains("172.17.0.3"), "{sans}");
    }

    #[cfg(unix)]
    #[test]
    fn the_private_key_is_not_world_readable() {
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        load_or_generate(dir.path(), &[]).unwrap();
        let mode = std::fs::metadata(dir.path().join("tls/self-signed.key")).unwrap().permissions().mode();
        assert_eq!(mode & 0o077, 0, "mode was {mode:o}");
    }
}

#[cfg(test)]
mod plaintext_tests {
    use super::plaintext_redirect;

    fn head(lines: &[&str]) -> Vec<u8> {
        let mut s = lines.join("\r\n");
        s.push_str("\r\n\r\n");
        s.into_bytes()
    }

    #[test]
    fn rewrites_only_the_scheme() {
        let h = head(&["GET /b/home?sort=name HTTP/1.1", "Host: 192.168.0.50:8443"]);
        assert_eq!(
            plaintext_redirect(&h).as_deref(),
            Some("https://192.168.0.50:8443/b/home?sort=name")
        );
    }

    #[test]
    fn keeps_the_port_the_caller_reached_us_on() {
        let h = head(&["GET / HTTP/1.1", "Host: nas.example:8443"]);
        assert_eq!(plaintext_redirect(&h).as_deref(), Some("https://nas.example:8443/"));
    }

    #[test]
    fn accepts_a_bracketed_ipv6_authority() {
        let h = head(&["GET /trash HTTP/1.1", "Host: [fd00::5]:8443"]);
        assert_eq!(plaintext_redirect(&h).as_deref(), Some("https://[fd00::5]:8443/trash"));
    }

    #[test]
    fn finds_host_whatever_case_it_arrives_in() {
        let h = head(&["GET / HTTP/1.1", "Accept: */*", "HOST: nas.example"]);
        assert_eq!(plaintext_redirect(&h).as_deref(), Some("https://nas.example/"));
    }

    #[test]
    fn refuses_a_request_with_no_host() {
        let h = head(&["GET / HTTP/1.1", "Accept: */*"]);
        assert_eq!(plaintext_redirect(&h), None);
    }

    #[test]
    fn refuses_a_host_that_would_break_the_header_framing() {
        // A `Host` is caller-controlled and lands in a `Location`. Splitting on
        // CRLF already separates lines, so the danger is anything else a header
        // parser might rejoin: reject on the character class, not on the split.
        for bad in ["nas.example\u{0}", "nas example", "nas.example\u{7f}", "na<s>.example"] {
            let h = head(&["GET / HTTP/1.1", &format!("Host: {bad}")]);
            assert_eq!(plaintext_redirect(&h), None, "accepted {bad:?}");
        }
    }

    #[test]
    fn refuses_a_target_that_is_not_origin_form() {
        // Absolute-form and CONNECT authority-form are not a browser typing the
        // wrong scheme, and rewriting either one is guesswork.
        for target in ["http://elsewhere.example/", "nas.example:8443", "*"] {
            let h = head(&[&format!("GET {target} HTTP/1.1"), "Host: nas.example"]);
            assert_eq!(plaintext_redirect(&h), None, "accepted {target:?}");
        }
    }

    #[test]
    fn refuses_a_target_carrying_control_characters() {
        let h = head(&["GET /a\u{0}b HTTP/1.1", "Host: nas.example"]);
        assert_eq!(plaintext_redirect(&h), None);
    }

    #[test]
    fn refuses_a_truncated_request_line() {
        assert_eq!(plaintext_redirect(b"GET\r\n\r\n"), None);
        assert_eq!(plaintext_redirect(b""), None);
    }
}

/// The dispatch itself, over a real socket. The parsing above says what a
/// request head turns into; these say the right head reaches the right side.
#[cfg(test)]
mod dispatch_tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    /// A listener on an ephemeral loopback port, and the port it took.
    async fn listening() -> (tempfile::TempDir, SocketAddr) {
        let dir = tempfile::tempdir().unwrap();
        let cfg = load_or_generate(dir.path(), &[]).unwrap();
        let tcp = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
        let addr = tcp.local_addr().unwrap();
        // The receiver is dropped, so nothing is ever taken off the channel.
        // That is fine for both cases here: the plaintext path never reaches
        // it, and the TLS path only has to get as far as answering.
        TlsListener::spawn(tcp, TlsAcceptor::from(cfg)).unwrap();
        (dir, addr)
    }

    #[tokio::test]
    async fn a_plaintext_request_is_answered_with_a_redirect() {
        let (_dir, addr) = listening().await;
        let mut c = TcpStream::connect(addr).await.unwrap();
        c.write_all(format!("GET /b/home HTTP/1.1\r\nHost: {addr}\r\n\r\n").as_bytes()).await.unwrap();

        let mut reply = String::new();
        c.read_to_string(&mut reply).await.unwrap();
        assert!(reply.starts_with("HTTP/1.1 308 "), "{reply:?}");
        assert!(reply.contains(&format!("Location: https://{addr}/b/home\r\n")), "{reply:?}");
    }

    /// The peek must not eat the `ClientHello`. This one is deliberately
    /// malformed, so rustls answers with an alert rather than a handshake -- but
    /// an alert is a TLS record, and that is the whole assertion: the socket
    /// went to the acceptor and not to the redirect.
    #[tokio::test]
    async fn a_tls_record_still_reaches_the_acceptor() {
        let (_dir, addr) = listening().await;
        let mut c = TcpStream::connect(addr).await.unwrap();
        c.write_all(&[0x16, 0x03, 0x01, 0x00, 0x04, 0x01, 0x00, 0x00, 0x00]).await.unwrap();

        let mut reply = Vec::new();
        c.read_to_end(&mut reply).await.unwrap();
        assert_eq!(reply.first(), Some(&0x15), "expected a TLS alert, got {reply:?}");
    }
}
