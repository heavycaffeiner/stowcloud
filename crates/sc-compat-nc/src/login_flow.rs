//! Login Flow v2 — the standard login path for current desktop and mobile
//! clients.
//!
//! Reference: `core/Controller/ClientFlowLoginV2Controller.php`.
//!
//! ```text
//! (1) client  POST /index.php/login/v2
//!             -> { "poll": { "token": .., "endpoint": .. }, "login": .. }
//! (2) client opens `login` in the system browser
//!             GET /index.php/login/v2/flow/<flow token>
//!             -> unauthenticated: redirect to our web login (returnTo kept)
//!             -> authenticated:   consent screen  [POST-only Approve button]
//! (3) client  POST /index.php/login/v2/poll   token=<poll token>
//!             -> 404 while pending
//!             -> 200 { "server", "loginName", "appPassword" }, once only
//! ```
//!
//! # Security properties this module is responsible for
//!
//! 1. **Two independent 256-bit tokens.** Knowing the flow token (which travels
//!    through a browser address bar, gets into history, referrers and shoulder
//!    surfers) must not let anyone poll for the resulting password.
//! 2. **Only SHA-256 digests are stored.** A database leak must not be
//!    replayable against a live flow.
//! 3. **20-minute expiry** plus a sweep.
//! 4. **Rate-limited polling**, one poll per token per second. Unbounded
//!    polling is a database-scan DoS.
//! 5. **Approval is a CSRF-protected POST performed by a logged-in human.**
//!    This is the whole ballgame: if a `GET` could approve, then merely
//!    visiting an attacker's page while logged in would mint an app password
//!    and hand it to the attacker's waiting poller. That is a full account
//!    takeover from a drive-by image tag.
//! 6. **Approval is single-use** — `flow_approve` refuses a second approval,
//!    so a flow can never mint more than one app password. The *result* of an
//!    approval, however, is retrievable by the same poll token as many times
//!    as asked for, until the flow's TTL expires or it is swept. Deleting it
//!    on first read (what the reference server does) means a client that fails
//!    to consume that one response — a dropped connection, the process
//!    getting backgrounded mid-parse — loses a valid credential forever with
//!    no way to retry. Android 34.1.0 makes this worse: `poolLogin()`'s
//!    `scheduleWithFixedDelay` permanently stops polling after any single
//!    exception, so there may be no second attempt at all. Bounding this by
//!    TTL + sweep keeps the exposure window the same shape as everything else
//!    a poll token can already do.
//! 7. **URLs are built from the configured canonical URL, never `Host`.**

use std::sync::Arc;

use hmac::{Hmac, Mac};
use sha2::{Digest, Sha256};
use subtle::ConstantTimeEq;

use crate::config::NcConfig;
use crate::ports::{AuthPort, PortError, PortResult, Principal, Scope};
use crate::store::{FlowResult, NcStore, PollOutcome};

type HmacSha256 = Hmac<Sha256>;

/// Injectable clock. Expiry and rate-limiting are the two things most likely
/// to be wrong, and neither is testable against the wall clock.
pub trait Clock: Send + Sync {
    fn now_ns(&self) -> i64;
}

pub struct SystemClock;

impl Clock for SystemClock {
    fn now_ns(&self) -> i64 {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos() as i64)
            .unwrap_or(0)
    }
}

/// 32 raw bytes rendered as 64 lowercase hex characters — the same length as
/// the reference's 64-character alphanumeric tokens, and URL-safe without
/// escaping.
fn new_token() -> String {
    let mut buf = [0u8; 32];
    getrandom::getrandom(&mut buf).expect("OS entropy unavailable");
    data_encoding::HEXLOWER.encode(&buf)
}

pub fn sha256(s: &str) -> [u8; 32] {
    let mut h = Sha256::new();
    h.update(s.as_bytes());
    h.finalize().into()
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum FlowError {
    /// Unknown token, expired token, or a result that has already been taken.
    /// All three collapse to one variant so a caller cannot use the response
    /// to distinguish them.
    NotFound,
    /// Polled again too soon.
    RateLimited,
    /// Still waiting for a human to approve.
    Pending,
    /// The CSRF state token did not verify.
    BadState,
    Backend(String),
}

impl From<PortError> for FlowError {
    fn from(e: PortError) -> Self {
        FlowError::Backend(e.to_string())
    }
}

#[derive(Clone, Debug)]
pub struct InitResult {
    pub poll_token: String,
    pub poll_endpoint: String,
    pub login_url: String,
}

impl InitResult {
    pub fn to_json(&self) -> serde_json::Value {
        serde_json::json!({
            "poll": {
                "token": self.poll_token,
                "endpoint": self.poll_endpoint,
            },
            "login": self.login_url,
        })
    }
}

/// What the consent screen needs to render.
#[derive(Clone, Debug)]
pub struct ConsentInfo {
    /// Already HTML-escaped.
    pub client_name: String,
    /// Already HTML-escaped.
    pub client_ip: String,
    pub state_token: String,
    pub flow_token: String,
    pub user_display: String,
}

pub struct LoginFlowService {
    store: Arc<dyn NcStore>,
    auth: Arc<dyn AuthPort>,
    cfg: Arc<NcConfig>,
    clock: Arc<dyn Clock>,
    /// Per-process key for the CSRF state token MAC. Rotating it on restart is
    /// fine: it only invalidates consent screens that are currently open.
    csrf_key: [u8; 32],
}

impl LoginFlowService {
    pub fn new(
        store: Arc<dyn NcStore>,
        auth: Arc<dyn AuthPort>,
        cfg: Arc<NcConfig>,
        clock: Arc<dyn Clock>,
    ) -> Self {
        let mut csrf_key = [0u8; 32];
        getrandom::getrandom(&mut csrf_key).expect("OS entropy unavailable");
        Self { store, auth, cfg, clock, csrf_key }
    }

    /// Step 1. `POST /index.php/login/v2`.
    pub fn init(&self, user_agent: &str, client_ip: &str) -> Result<InitResult, FlowError> {
        let poll_token = new_token();
        let flow_token = new_token();
        let now = self.clock.now_ns();

        self.store.flow_create(
            sha256(&poll_token),
            sha256(&flow_token),
            &parse_client_name(user_agent),
            client_ip,
            now,
            now + self.cfg.login_flow_ttl_ns,
        )?;

        Ok(InitResult {
            poll_token,
            // Built from the CONFIGURED canonical URL. Using the request Host
            // here would let anyone who can reach us hand a client a poll
            // endpoint on a host they control.
            poll_endpoint: self.cfg.url("/index.php/login/v2/poll"),
            login_url: self
                .cfg
                .url(&format!("/index.php/login/v2/flow/{flow_token}")),
        })
    }

    /// Step 3. `POST /index.php/login/v2/poll`, body `token=<poll token>`.
    ///
    /// Returns the credentials once granted, and keeps returning them to the
    /// same poll token until the flow expires or is swept — see
    /// `NcStore::flow_poll` for why this deliberately does not delete on
    /// first read. Every failure mode maps onto HTTP 404 with an empty body
    /// at the route layer, matching the reference, which documents 404 as
    /// "not found **or completed**".
    pub fn poll(&self, poll_token: &str) -> Result<serde_json::Value, FlowError> {
        let now = self.clock.now_ns();
        let outcome = self.store.flow_poll(
            &sha256(poll_token),
            now,
            self.cfg.login_flow_poll_interval_ns,
        )?;
        match outcome {
            Ok(res) => Ok(serde_json::json!({
                "server": self.cfg.canonical_url.trim_end_matches('/'),
                "loginName": res.login_name,
                "appPassword": res.app_password,
            })),
            Err(PollOutcome::Pending) => Err(FlowError::Pending),
            Err(PollOutcome::RateLimited) => Err(FlowError::RateLimited),
            Err(PollOutcome::Unknown) => Err(FlowError::NotFound),
        }
    }

    /// Step 2a. `GET /index.php/login/v2/flow/<flow token>` for an
    /// authenticated user.
    ///
    /// This **only renders**. It has no side effects at all — no password is
    /// issued, no flow state changes. That is the entire point.
    pub fn consent(
        &self,
        flow_token: &str,
        principal: &Principal,
        session_token: &str,
    ) -> Result<ConsentInfo, FlowError> {
        let flow_hash = sha256(flow_token);
        let now = self.clock.now_ns();
        let rec = self
            .store
            .flow_peek(&flow_hash, now)?
            .ok_or(FlowError::NotFound)?;
        if rec.approved {
            // Already granted; do not offer to grant again.
            return Err(FlowError::NotFound);
        }
        Ok(ConsentInfo {
            client_name: html_escape(&rec.client_name),
            client_ip: html_escape(&rec.client_ip),
            state_token: self.state_token(&flow_hash, principal, session_token),
            flow_token: flow_token.to_owned(),
            user_display: html_escape(&principal.display_name),
        })
    }

    /// Step 2b. `POST /index.php/login/v2/grant`.
    ///
    /// MUST be reachable by POST only, from an authenticated session, with a
    /// matching state token.
    pub fn grant(
        &self,
        flow_token: &str,
        state_token: &str,
        principal: &Principal,
        session_token: &str,
        scope: Scope,
    ) -> Result<(), FlowError> {
        let flow_hash = sha256(flow_token);

        // Verify CSRF before touching anything. The state token binds this
        // POST to (this flow, this user, this browser session), so a form
        // submitted from another origin cannot produce a valid one even if the
        // attacker knows the flow token.
        let expected = self.state_token(&flow_hash, principal, session_token);
        if expected.as_bytes().ct_eq(state_token.as_bytes()).unwrap_u8() != 1 {
            return Err(FlowError::BadState);
        }

        let now = self.clock.now_ns();
        let rec = self
            .store
            .flow_peek(&flow_hash, now)?
            .ok_or(FlowError::NotFound)?;
        if rec.approved {
            return Err(FlowError::NotFound);
        }

        let name = format!("{} ({})", rec.client_name, rec.client_ip);
        let (_cred_id, secret) = self
            .auth
            .issue_app_password(principal.user, &name, scope)?;

        let stored = self.store.flow_approve(
            &flow_hash,
            now,
            &FlowResult {
                login_name: principal.login_name.clone(),
                app_password: secret,
            },
        )?;
        if !stored {
            // Lost a race, or the flow expired between peek and approve. The
            // credential we just minted is now orphaned; surfacing this as an
            // error is correct — silently succeeding would leave a password
            // nobody can see but which is live.
            return Err(FlowError::NotFound);
        }

        // Audit trail.
        tracing::info!(
            event = "apppw.created",
            user = %principal.login_name,
            via = "login_flow_v2",
            client = %rec.client_name,
            ip = %rec.client_ip,
            "app password issued via login flow v2"
        );
        Ok(())
    }

    pub fn sweep(&self) -> PortResult<usize> {
        self.store.flow_sweep(self.clock.now_ns())
    }

    /// HMAC over (flow hash, user id, session token hash). Stateless, so no
    /// extra table, and unforgeable without the process key.
    fn state_token(
        &self,
        flow_hash: &[u8; 32],
        principal: &Principal,
        session_token: &str,
    ) -> String {
        let mut mac =
            HmacSha256::new_from_slice(&self.csrf_key).expect("HMAC accepts any key length");
        mac.update(flow_hash);
        mac.update(&principal.user.0.to_le_bytes());
        // Hash the session token rather than MACing it directly, so the state
        // token cannot be used as an oracle for session token bytes.
        mac.update(&sha256(session_token));
        data_encoding::HEXLOWER.encode(&mac.finalize().into_bytes())
    }

    /// The URL an unauthenticated visitor to the flow endpoint is sent to.
    /// `returnTo` is preserved so the browser lands back on consent.
    pub fn login_redirect(&self, flow_token: &str) -> String {
        let return_to = format!("/index.php/login/v2/flow/{flow_token}");
        let encoded = percent_encoding::utf8_percent_encode(
            &return_to,
            percent_encoding::NON_ALPHANUMERIC,
        )
        .to_string();
        self.cfg.url(&format!("/login?returnTo={encoded}"))
    }
}

/// Turn a User-Agent into something short enough to show a human.
///
/// Compat clients send e.g.
/// `Mozilla/5.0 (Linux) mirall/3.13.0 (Nextcloud, ...)` or
/// `Mozilla/5.0 (Android) Nextcloud-android/3.30.0`. We pick the recognisable
/// product token when we can find one and otherwise fall back to a truncated
/// raw agent — never to an empty string, because a blank client name on a
/// consent screen trains users to approve blindly.
pub fn parse_client_name(ua: &str) -> String {
    let ua = ua.trim();
    if ua.is_empty() {
        return "Unknown client".to_string();
    }
    for tok in ua.split_whitespace() {
        let t = tok.trim_matches(|c: char| !c.is_ascii_alphanumeric() && c != '/' && c != '-');
        let lower = t.to_ascii_lowercase();
        if lower.starts_with("nextcloud-")
            || lower.starts_with("mirall/")
            || lower.starts_with("nextcloud/")
        {
            return t.chars().take(64).collect();
        }
    }
    ua.chars().take(64).collect()
}

/// Escape for interpolation into HTML text or a double-quoted attribute.
///
/// The client name is fully attacker-controlled (it is a request header), and
/// it is rendered on a page where the user is about to authorise access to
/// their account. Unescaped, it is a stored-XSS-into-consent-screen, which is
/// about the worst place to have one.
pub fn html_escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 8);
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            '\'' => out.push_str("&#x27;"),
            '/' => out.push_str("&#x2F;"),
            c => out.push(c),
        }
    }
    out
}

/// Minimal consent page. The Approve control is a **form POST**, never a link.
pub fn consent_html(info: &ConsentInfo) -> String {
    format!(
        r#"<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Grant access</title></head>
<body>
<main>
  <h1>Grant access</h1>
  <p><strong>{client}</strong> is requesting access to your account
     (signed in as <strong>{user}</strong>).</p>
  <p>Request origin: <code>{ip}</code></p>
  <form method="POST" action="/index.php/login/v2/grant">
    <input type="hidden" name="flowToken" value="{flow}">
    <input type="hidden" name="stateToken" value="{state}">
    <fieldset>
      <legend>Access level</legend>
      <label><input type="radio" name="scope" value="full" checked> Full access</label>
      <label><input type="radio" name="scope" value="readonly"> Read only</label>
    </fieldset>
    <button type="submit">Grant access</button>
  </form>
</main>
</body></html>"#,
        client = info.client_name,
        user = info.user_display,
        ip = info.client_ip,
        flow = html_escape(&info.flow_token),
        state = html_escape(&info.state_token),
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ports::{Perms, UserId};
    use crate::store::MemStore;
    use parking_lot::Mutex;

    struct TestClock(Mutex<i64>);
    impl TestClock {
        fn new() -> Arc<Self> {
            Arc::new(Self(Mutex::new(1_000_000_000_000)))
        }
        fn advance(&self, ns: i64) {
            *self.0.lock() += ns;
        }
    }
    impl Clock for TestClock {
        fn now_ns(&self) -> i64 {
            *self.0.lock()
        }
    }

    struct TestAuth {
        issued: Mutex<Vec<(UserId, String)>>,
    }
    impl TestAuth {
        fn new() -> Arc<Self> {
            Arc::new(Self { issued: Mutex::new(Vec::new()) })
        }
        fn count(&self) -> usize {
            self.issued.lock().len()
        }
    }
    impl AuthPort for TestAuth {
        fn issue_app_password(
            &self,
            user: UserId,
            name: &str,
            _scope: Scope,
        ) -> PortResult<(u32, String)> {
            let mut g = self.issued.lock();
            g.push((user, name.to_owned()));
            Ok((g.len() as u32, format!("stow_secret_{}", g.len())))
        }
        fn verify_basic(
            &self,
            _l: &str,
            _s: &str,
            _from: crate::ports::ClientAddr,
        ) -> PortResult<Option<Principal>> {
            Ok(None)
        }
        fn validate_session(&self, _t: &str) -> PortResult<Option<Principal>> {
            Ok(None)
        }
    }

    fn principal() -> Principal {
        Principal {
            user: UserId(7),
            login_name: "alice".into(),
            display_name: "Alice".into(),
        }
    }

    fn svc() -> (Arc<LoginFlowService>, Arc<TestClock>, Arc<TestAuth>) {
        let clock = TestClock::new();
        let auth = TestAuth::new();
        let cfg = NcConfig {
            canonical_url: "https://cloud.example.com".into(),
            ..NcConfig::default()
        };
        let s = Arc::new(LoginFlowService::new(
            Arc::new(MemStore::new()),
            auth.clone(),
            Arc::new(cfg),
            clock.clone(),
        ));
        (s, clock, auth)
    }

    /// Extract the flow token out of the login URL the client is told to open.
    fn flow_token_of(init: &InitResult) -> String {
        init.login_url.rsplit('/').next().unwrap().to_string()
    }

    #[test]
    fn happy_path() {
        let (s, clock, auth) = svc();
        let init = s.init("Mozilla/5.0 (Android) Nextcloud-android/3.30.0", "10.0.0.5").unwrap();

        assert_eq!(
            init.poll_endpoint,
            "https://cloud.example.com/index.php/login/v2/poll"
        );
        assert!(init
            .login_url
            .starts_with("https://cloud.example.com/index.php/login/v2/flow/"));

        // Poll before approval -> Pending (route layer renders 404).
        assert_eq!(s.poll(&init.poll_token), Err(FlowError::Pending));

        let flow = flow_token_of(&init);
        clock.advance(2_000_000_000);
        let consent = s.consent(&flow, &principal(), "sess-abc").unwrap();
        // Already HTML-escaped for rendering, so `/` appears as `&#x2F;`.
        assert_eq!(consent.client_name, "Nextcloud-android&#x2F;3.30.0");
        assert_eq!(consent.client_ip, "10.0.0.5");

        // Merely rendering the consent screen must not issue anything.
        assert_eq!(auth.count(), 0);

        s.grant(
            &flow,
            &consent.state_token,
            &principal(),
            "sess-abc",
            Scope::full(),
        )
        .unwrap();
        assert_eq!(auth.count(), 1);

        clock.advance(2_000_000_000);
        let creds = s.poll(&init.poll_token).unwrap();
        assert_eq!(creds["server"], "https://cloud.example.com");
        assert_eq!(creds["loginName"], "alice");
        assert_eq!(creds["appPassword"], "stow_secret_1");
    }

    /// The critical one. A GET on the flow endpoint is a render, not an
    /// approval; without a POST carrying a valid state token, no credential is
    /// ever created and the poll stays pending forever.
    #[test]
    fn get_only_approval_does_not_issue_a_password() {
        let (s, clock, auth) = svc();
        let init = s.init("mirall/3.13.0", "10.0.0.5").unwrap();
        let flow = flow_token_of(&init);

        // Simulate an attacker-triggered navigation: the victim's browser hits
        // the flow URL while logged in, repeatedly.
        for _ in 0..5 {
            let _ = s.consent(&flow, &principal(), "sess-abc").unwrap();
            clock.advance(2_000_000_000);
        }

        assert_eq!(auth.count(), 0, "GET must never mint an app password");
        assert_eq!(s.poll(&init.poll_token), Err(FlowError::Pending));
    }

    #[test]
    fn grant_without_valid_state_token_is_rejected() {
        let (s, _clock, auth) = svc();
        let init = s.init("mirall/3.13.0", "10.0.0.5").unwrap();
        let flow = flow_token_of(&init);

        assert_eq!(
            s.grant(&flow, "not-the-state-token", &principal(), "sess-abc", Scope::full()),
            Err(FlowError::BadState)
        );
        assert_eq!(auth.count(), 0);

        // A state token minted for a *different* browser session must not work
        // either — that is what stops a cross-session replay.
        let consent = s.consent(&flow, &principal(), "sess-abc").unwrap();
        assert_eq!(
            s.grant(
                &flow,
                &consent.state_token,
                &principal(),
                "sess-OTHER",
                Scope::full()
            ),
            Err(FlowError::BadState)
        );
        assert_eq!(auth.count(), 0);

        // ...nor one minted for a different user.
        let other = Principal {
            user: UserId(8),
            login_name: "mallory".into(),
            display_name: "Mallory".into(),
        };
        assert_eq!(
            s.grant(&flow, &consent.state_token, &other, "sess-abc", Scope::full()),
            Err(FlowError::BadState)
        );
        assert_eq!(auth.count(), 0);
    }

    /// The strand from the field report: login OK, grant, "close this
    /// window" — then a real handset can fail to finish processing that one
    /// successful poll response (dropped connection, the app backgrounded
    /// mid-parse, or `poolLogin`'s `scheduleWithFixedDelay` never getting a
    /// second chance after an unrelated exception). If the server deleted the
    /// credential the instant it served it — which is what upstream
    /// the reference server's `LoginFlowV2Service::poll()` does, deleting the row before
    /// it even attempts to decrypt the password — every later poll of the
    /// same token is a 404 forever, with a perfectly valid app password
    /// destroyed and no way to recover it. This is why we diverge: the same
    /// poll token must keep returning the same credential.
    #[test]
    fn result_survives_a_client_that_drops_the_first_response() {
        let (s, clock, auth) = svc();
        let init = s.init("Mozilla/5.0 (Android) Nextcloud-android/34.1.0", "10.0.0.5").unwrap();
        let flow = flow_token_of(&init);
        let consent = s.consent(&flow, &principal(), "sess-abc").unwrap();
        s.grant(&flow, &consent.state_token, &principal(), "sess-abc", Scope::full())
            .unwrap();
        assert_eq!(auth.count(), 1, "grant mints exactly one app password");

        clock.advance(2_000_000_000);
        // First poll: the server hands back the credential (this is the
        // response the real client failed to consume).
        let first = s.poll(&init.poll_token).unwrap();

        clock.advance(2_000_000_000);
        // Simulated retry with the *same* poll token: must return the exact
        // same credential, not `NotFound` and not a freshly minted one.
        let second = s.poll(&init.poll_token).unwrap();
        assert_eq!(first, second);
        assert_eq!(auth.count(), 1, "no second app password was minted to serve the retry");

        // It is still bounded: once the flow's 20-minute TTL is gone, so is
        // the credential.
        clock.advance(21 * 60 * 1_000_000_000);
        assert_eq!(s.poll(&init.poll_token), Err(FlowError::NotFound));
    }

    #[test]
    fn poll_is_rate_limited() {
        let (s, clock, _auth) = svc();
        let init = s.init("mirall/3.13.0", "10.0.0.5").unwrap();
        assert_eq!(s.poll(&init.poll_token), Err(FlowError::Pending));
        // Immediately again -> throttled.
        assert_eq!(s.poll(&init.poll_token), Err(FlowError::RateLimited));
        clock.advance(1_100_000_000);
        assert_eq!(s.poll(&init.poll_token), Err(FlowError::Pending));
    }

    #[test]
    fn expired_tokens_are_rejected() {
        let (s, clock, _auth) = svc();
        let init = s.init("mirall/3.13.0", "10.0.0.5").unwrap();
        let flow = flow_token_of(&init);

        // 20 minutes + a bit.
        clock.advance(21 * 60 * 1_000_000_000);

        assert_eq!(s.poll(&init.poll_token), Err(FlowError::NotFound));
        assert!(matches!(
            s.consent(&flow, &principal(), "sess-abc"),
            Err(FlowError::NotFound)
        ));
        assert_eq!(s.sweep().unwrap(), 1);
    }

    #[test]
    fn flow_token_cannot_be_used_to_poll() {
        let (s, _clock, _auth) = svc();
        let init = s.init("mirall/3.13.0", "10.0.0.5").unwrap();
        let flow = flow_token_of(&init);
        assert_ne!(flow, init.poll_token);
        assert_eq!(flow.len(), 64);
        assert_eq!(init.poll_token.len(), 64);
        // Knowing the flow token (it travels through a browser URL bar) gets
        // you nothing on the poll endpoint.
        assert_eq!(s.poll(&flow), Err(FlowError::NotFound));
    }

    #[test]
    fn double_grant_issues_only_one_password() {
        let (s, _clock, auth) = svc();
        let init = s.init("mirall/3.13.0", "10.0.0.5").unwrap();
        let flow = flow_token_of(&init);
        let consent = s.consent(&flow, &principal(), "sess-abc").unwrap();
        s.grant(&flow, &consent.state_token, &principal(), "sess-abc", Scope::full())
            .unwrap();
        // The consent screen is single-use; a resubmitted form must not mint a
        // second credential that nobody can ever collect.
        assert!(s
            .grant(&flow, &consent.state_token, &principal(), "sess-abc", Scope::full())
            .is_err());
        assert_eq!(auth.count(), 1);
    }

    #[test]
    fn client_name_is_escaped_on_the_consent_page() {
        let (s, _clock, _auth) = svc();
        let evil = "<img src=x onerror=alert(1)>";
        let init = s.init(evil, "1.2.3.4").unwrap();
        let flow = flow_token_of(&init);
        let consent = s.consent(&flow, &principal(), "sess").unwrap();
        let html = consent_html(&consent);
        assert!(!html.contains("<img src=x"));
        assert!(html.contains("&lt;img"));
        // And the approve control is a POST form, not a link.
        assert!(html.contains(r#"<form method="POST""#));
        assert!(!html.to_ascii_lowercase().contains("<a href"));
    }

    #[test]
    fn scope_can_be_narrowed() {
        let (s, _clock, auth) = svc();
        let init = s.init("mirall/3.13.0", "10.0.0.5").unwrap();
        let flow = flow_token_of(&init);
        let consent = s.consent(&flow, &principal(), "sess").unwrap();
        s.grant(
            &flow,
            &consent.state_token,
            &principal(),
            "sess",
            Scope { perms: Perms::READ | Perms::DOWNLOAD, share: None },
        )
        .unwrap();
        assert_eq!(auth.count(), 1);
    }

    #[test]
    fn user_agent_parsing() {
        assert_eq!(
            parse_client_name("Mozilla/5.0 (Android) Nextcloud-android/3.30.0"),
            "Nextcloud-android/3.30.0"
        );
        assert_eq!(
            parse_client_name("Mozilla/5.0 (Linux) mirall/3.13.1 (Nextcloud)"),
            "mirall/3.13.1"
        );
        assert_eq!(parse_client_name("   "), "Unknown client");
        assert_eq!(parse_client_name("").len(), "Unknown client".len());
        assert!(parse_client_name(&"x".repeat(500)).chars().count() <= 64);
    }

    #[test]
    fn login_redirect_preserves_return_to_and_uses_canonical_host() {
        let (s, _c, _a) = svc();
        let r = s.login_redirect("abc123");
        assert!(r.starts_with("https://cloud.example.com/login?returnTo="));
        assert!(r.contains("abc123"));
        assert!(!r.contains("/index.php/login/v2/flow/abc123"), "must be encoded");
    }
}
