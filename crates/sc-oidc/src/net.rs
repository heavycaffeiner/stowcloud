//! The rustls-backed [`HttpFetch`], and the only code in this crate that
//! knows hyper exists. Compiled under `feature = "net"`, which is on by
//! default (see this crate's `Cargo.toml` for why an off-by-default TLS
//! connector would be a connector nobody compiles).
//!
//! Four things this implementation enforces, all of them from proposal
//! §4.3.4 and §4.1.2:
//!
//!  1. HTTPS only, checked on the URL and again by the connector.
//!  2. A response body cap ([`MAX_RESPONSE_BYTES`]), enforced while the body
//!     streams in rather than after it has all been buffered, because the
//!     point is to bound allocation and a cap you check at the end does not.
//!  3. A whole-request timeout ([`REQUEST_TIMEOUT_SECS`]).
//!  4. The private, loopback, and link-local address rule, with the
//!     `allow_private_endpoints` escape hatch.
//!
//! What it does not do is prove itself in CI. Proposal §4.1.3 is explicit
//! that this path is exercised only by hand against a real IdP; the test
//! suite drives the trait, not this.

use crate::endpoint::{check_endpoint_url, is_blocked_socket, EndpointError};
use crate::fetch::{FetchError, HttpFetch, MAX_RESPONSE_BYTES, REQUEST_TIMEOUT_SECS};
use async_trait::async_trait;
use base64::Engine;
use bytes::Bytes;
use http_body_util::{BodyExt, Full};
use hyper_util::client::legacy::connect::dns::{GaiResolver, Name};
use hyper_util::client::legacy::connect::HttpConnector;
use hyper_util::client::legacy::Client;
use hyper_util::rt::TokioExecutor;
use std::future::Future;
use std::net::SocketAddr;
use std::pin::Pin;
use std::task::{Context, Poll};
use std::time::Duration;
use tower_service::Service;

type GuardedConnector = hyper_rustls::HttpsConnector<HttpConnector<GuardedResolver>>;

/// An outbound HTTPS client with the guards above wired in. One per process
/// is enough; cloning is cheap and shares the connection pool.
pub struct RustlsFetch {
    client: Client<GuardedConnector, Full<Bytes>>,
    allow_private: bool,
}

/// Why the client could not be constructed. Separate from [`FetchError`]
/// because none of these can happen at request time: they are all decided
/// once, at startup, and a server that cannot build a TLS config should say
/// so then rather than at the first login attempt.
#[derive(Debug, thiserror::Error)]
pub enum ClientError {
    #[error("rustls rejected the client configuration: {0}")]
    Tls(String),
}

impl RustlsFetch {
    /// `allow_private` is `oidc.allow_private_endpoints`.
    pub fn new(allow_private: bool) -> Result<Self, ClientError> {
        // Root store: the compiled-in Mozilla set. See the workspace
        // manifest for why this rather than reading the image's CA bundle.
        let roots = rustls::RootCertStore {
            roots: webpki_roots::TLS_SERVER_ROOTS.to_vec(),
        };
        // The provider is named, not defaulted. rustls picks `aws-lc-rs`
        // when both are compiled in, and this workspace deliberately builds
        // neither cmake nor a C toolchain into the musl cross-check.
        let provider = std::sync::Arc::new(rustls::crypto::ring::default_provider());
        let tls = rustls::ClientConfig::builder_with_provider(provider)
            .with_safe_default_protocol_versions()
            .map_err(|e| ClientError::Tls(e.to_string()))?
            .with_root_certificates(roots)
            .with_no_client_auth();

        let mut http = HttpConnector::new_with_resolver(GuardedResolver {
            inner: GaiResolver::new(),
            allow_private,
        });
        // The connector must not be allowed to fall back to plaintext. The
        // URL check below already refuses non-HTTPS, and `https_only()`
        // below refuses it again at the connector; this line is the third
        // leg, covering a hypothetical redirect handler.
        http.enforce_http(false);
        http.set_connect_timeout(Some(Duration::from_secs(REQUEST_TIMEOUT_SECS)));

        let connector = hyper_rustls::HttpsConnectorBuilder::new()
            .with_tls_config(tls)
            .https_only()
            .enable_http1()
            .wrap_connector(http);

        Ok(Self {
            // No redirect following, on purpose: hyper's legacy client does
            // not do it, and an IdP that answers discovery with a 302 is one
            // whose final URL nobody has checked.
            client: Client::builder(TokioExecutor::new()).build(connector),
            allow_private,
        })
    }

    async fn send(&self, req: http::Request<Full<Bytes>>) -> Result<Vec<u8>, FetchError> {
        let fut = self.client.request(req);
        let res = tokio::time::timeout(Duration::from_secs(REQUEST_TIMEOUT_SECS), fut)
            .await
            .map_err(|_| FetchError::Transport(format!("timed out after {REQUEST_TIMEOUT_SECS}s")))?
            .map_err(|e| FetchError::Transport(e.to_string()))?;

        let status = res.status();
        let mut body = res.into_body();
        let mut buf: Vec<u8> = Vec::new();
        // Streamed, not `collect()`ed: the cap has to be able to abandon a
        // body that is still arriving, which is the case it exists for.
        while let Some(frame) = body.frame().await {
            let frame = frame.map_err(|e| FetchError::Transport(e.to_string()))?;
            if let Some(chunk) = frame.data_ref() {
                if buf.len() + chunk.len() > MAX_RESPONSE_BYTES {
                    return Err(FetchError::TooLarge);
                }
                buf.extend_from_slice(chunk);
            }
        }
        if !status.is_success() {
            return Err(FetchError::Status(status.as_u16()));
        }
        Ok(buf)
    }

    fn guard_url(&self, url: &str) -> Result<(), FetchError> {
        match check_endpoint_url(url, self.allow_private) {
            Ok(_) => Ok(()),
            Err(EndpointError::InsecureScheme) => {
                Err(FetchError::Transport("refusing a non-https url".into()))
            }
            Err(EndpointError::PrivateAddress) => Err(FetchError::Transport(
                "refusing a private, loopback, or link-local address".into(),
            )),
            Err(EndpointError::Malformed(e)) => {
                Err(FetchError::Transport(format!("unparseable url: {e}")))
            }
        }
    }
}

#[async_trait]
impl HttpFetch for RustlsFetch {
    async fn get(&self, url: &str) -> Result<Vec<u8>, FetchError> {
        self.guard_url(url)?;
        let req = http::Request::builder()
            .method(http::Method::GET)
            .uri(url)
            .header(http::header::ACCEPT, "application/json")
            .body(Full::new(Bytes::new()))
            .map_err(|e| FetchError::Transport(e.to_string()))?;
        self.send(req).await
    }

    async fn post_form(
        &self,
        url: &str,
        form: &[(&str, &str)],
        basic: Option<(&str, &str)>,
    ) -> Result<Vec<u8>, FetchError> {
        self.guard_url(url)?;
        let mut body = String::new();
        for (k, v) in form {
            if !body.is_empty() {
                body.push('&');
            }
            body.push_str(&form_encode(k));
            body.push('=');
            body.push_str(&form_encode(v));
        }

        let mut req = http::Request::builder()
            .method(http::Method::POST)
            .uri(url)
            .header(
                http::header::CONTENT_TYPE,
                "application/x-www-form-urlencoded",
            )
            .header(http::header::ACCEPT, "application/json");
        if let Some((user, pass)) = basic {
            // RFC 6749 §2.3.1: the two halves are form-urlencoded *before*
            // base64, not after. An IdP secret with a `+` or a space in it
            // authenticates only if both ends agree on that.
            let raw = format!("{}:{}", form_encode(user), form_encode(pass));
            let encoded = base64::engine::general_purpose::STANDARD.encode(raw);
            req = req.header(http::header::AUTHORIZATION, format!("Basic {encoded}"));
        }
        let req = req
            .body(Full::new(Bytes::from(body)))
            .map_err(|e| FetchError::Transport(e.to_string()))?;
        self.send(req).await
    }
}

/// `application/x-www-form-urlencoded` percent-encoding, which is not the
/// same thing as URL path encoding: space becomes `+`, and everything
/// outside the unreserved set is escaped.
fn form_encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.as_bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(*b as char)
            }
            b' ' => out.push('+'),
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

/// hyper's `GaiResolver` with the address rule applied to what it returns.
///
/// This is a resolver rather than a lookup before the request because those
/// are not the same check. A separate lookup answers "what did this name
/// resolve to a moment ago"; this answers "what is the connector about to
/// dial", and a hostile IdP that flips its DNS between the two only defeats
/// the first.
///
/// It does not cover an IP literal: `HttpConnector` parses those itself and
/// never calls the resolver (`hyper-util`'s
/// `client/legacy/connect/http.rs`, `SocketAddrs::try_parse`). That case is
/// [`check_endpoint_url`]'s, which every path here runs first.
#[derive(Clone)]
struct GuardedResolver {
    inner: GaiResolver,
    allow_private: bool,
}

impl Service<Name> for GuardedResolver {
    type Response = std::vec::IntoIter<SocketAddr>;
    type Error = Box<dyn std::error::Error + Send + Sync>;
    type Future = Pin<Box<dyn Future<Output = Result<Self::Response, Self::Error>> + Send>>;

    fn poll_ready(&mut self, cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx).map_err(Into::into)
    }

    fn call(&mut self, name: Name) -> Self::Future {
        let allow_private = self.allow_private;
        let fut = self.inner.call(name);
        Box::pin(async move {
            let addrs = fut.await?;
            let kept: Vec<SocketAddr> = addrs
                .filter(|a| allow_private || !is_blocked_socket(a))
                .collect();
            if kept.is_empty() {
                // Empty and "the name has no A record" are reported the same
                // way on purpose. The operator-facing distinction lives in
                // the config error for `allow_private_endpoints`, not here.
                return Err("name resolved only to private, loopback, or link-local addresses"
                    .to_string()
                    .into());
            }
            Ok(kept.into_iter())
        })
    }
}
