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

    let certs = {
        let pem = std::fs::read(dir.join("self-signed.crt")).context("reading the certificate")?;
        rustls_pemfile::certs(&mut pem.as_slice())
            .collect::<Result<Vec<_>, _>>()
            .context("parsing the certificate")?
    };
    let key = {
        let pem = std::fs::read(dir.join("self-signed.key")).context("reading the private key")?;
        rustls_pemfile::private_key(&mut pem.as_slice())
            .context("parsing the private key")?
            .context("the private key file contained no key")?
    };

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
                    match acceptor.accept(tcp).await {
                        Ok(stream) => {
                            let _ = tx.send((stream, peer)).await;
                        }
                        // The overwhelmingly common cause is a browser that has
                        // not been given the certificate exception yet, so this
                        // is `debug`, not `warn`: at `warn` every first visit
                        // would look like a fault.
                        Err(e) => tracing::debug!(peer = %peer, error = %e, "tls: handshake failed"),
                    }
                });
            }
        });
        Ok(Self { local, rx })
    }
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
