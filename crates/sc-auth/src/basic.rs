use crate::config::DavAccountPassword;
use crate::cred_cache::Outcome;
use crate::{AuthService, AuthVia, BasicResult, Principal, Scope};
use secrecy::{ExposeSecret, SecretString};
use std::net::IpAddr;

impl AuthService {
    /// The DAV Basic-auth 3-tier verification path,
    /// minus tier ① (connection memo), which the HTTP layer drives directly
    /// via [`AuthService::conn_memo_check`]/[`AuthService::conn_memo_store`]
    /// since it needs the raw `Authorization` header bytes this method
    /// never sees.
    ///
    /// Accepts either an app password (fast path, sha256, no Argon2) or the
    /// account password (cached Argon2 verification). TOTP users are
    /// rejected on the account-password path (§4.3), with no exception, and
    /// so are OIDC-linked accounts (proposal §4.3.5). The two refusals sit at
    /// deliberately different points in this function; see the comments at
    /// each.
    pub async fn verify_basic(&self, user: &str, pw: &SecretString, ip: IpAddr) -> BasicResult {
        let secret = pw.expose_secret();

        // App passwords are self-identifying by prefix, so they can be
        // routed straight to the fast (non-Argon2) path without touching
        // the account-password rate gate/cache at all.
        if secret.starts_with("stow_") {
            return match self.verify_app_password(secret, ip) {
                Ok(Some(p)) => BasicResult::Ok(p),
                Ok(None) => BasicResult::Invalid,
                Err(_) => BasicResult::Invalid,
            };
        }

        if matches!(self.cfg.dav_account_password, DavAccountPassword::Deny) {
            return BasicResult::AppPasswordRequired;
        }

        let user_row = self.find_user(user).unwrap_or_default();
        if let Some(u) = &user_row {
            // Returns before the rate gate and before Argon2, which makes a
            // wrong password against a TOTP account measurably cheaper than
            // a wrong password against an ordinary one, which is an oracle
            // for which accounts have 2FA. That is a pre-existing flaw and it stays
            // here on purpose: moving the check changes observable behaviour
            // for every TOTP account, which is a separate change from adding
            // a provider, and it is filed as its own issue.
            //
            // The OIDC refusal below deliberately does *not* copy this
            // placement. See it for the argument.
            if u.totp_enabled {
                return BasicResult::AppPasswordRequired;
            }
        }

        let gen = self.generation();
        let cache_key = self.cred_cache.key(user, secret);
        if let Some(outcome) = self.cred_cache.get(&cache_key, gen) {
            return match outcome {
                Outcome::Accepted(p) => {
                    // §2.4: cache hit still carries the plaintext (it's
                    // right here in `secret`) — MD4 backfill is cheap and
                    // independent of the Argon2 result being cached.
                    if let (Some(u), Ok(conn)) = (&user_row, self.pool.get()) {
                        let _ = self.maybe_backfill_nt(&conn, u.id, secret);
                    }
                    BasicResult::Ok(p)
                }
                Outcome::Rejected => BasicResult::Invalid,
            };
        }

        if let Some(retry_after_s) = self.ip_gate.check(ip) {
            return BasicResult::RateLimited { retry_after_s };
        }

        let account_key = user.to_lowercase();
        let delay = self.account_gate.record_failure(&account_key);
        if !delay.is_zero() {
            tokio::time::sleep(delay).await;
        }

        let hash = user_row
            .as_ref()
            .map(|u| self.pw_hash_of(u.id).unwrap_or_else(|_| self.dummy_hash.clone()))
            .unwrap_or_else(|| self.dummy_hash.clone());
        let verified = self.verify_password_async(&hash, SecretString::from(secret.to_string())).await;
        let ok = verified && user_row.as_ref().is_some_and(|u| !u.disabled);

        if !ok {
            self.cred_cache.put(cache_key, Outcome::Rejected, gen);
            self.audit(None, "auth.login_failed", Some(user), Some(ip), false, Some("dav_basic"));
            return BasicResult::Invalid;
        }
        self.account_gate.reset(&account_key);

        let u = user_row.expect("ok implies user_row is Some");

        // §4.3.5: an OIDC-linked account cannot reach DAV with its account
        // password; it needs an app password.
        //
        // Here, and not up beside the `totp_enabled` check, because the
        // password has to be *verified* first. Refusing before the rate gate
        // and Argon2 would make a wrong password against a linked account
        // finish visibly sooner than a wrong password against any other,
        // which tells an unauthenticated caller which accounts use SSO. That
        // is exactly the enumeration oracle flattens
        // timing to prevent, and it would be a new one, introduced by this
        // change, rather than an inherited one.
        //
        // The gate is reset first: the credential was correct, and letting a
        // linked account accumulate the per-account delay on correct
        // passwords would just be the same oracle wearing a different hat.
        // Nothing is cached either: a decision that ends in a refusal is not
        // worth a cache entry, and `link_oidc_identity` bumps `generation`
        // so entries made before the link are already dead.
        match self.oidc_linked(u.id) {
            Ok(true) => {
                self.audit(Some(u.id), "auth.login_failed", Some(user), Some(ip), false, Some("dav_oidc_linked"));
                return BasicResult::AppPasswordRequired;
            }
            Ok(false) => {}
            // Storage failure. Refuse rather than fall through: "we could not
            // tell whether this account is linked" must not resolve to "let
            // it in with the credential linking was supposed to close".
            Err(e) => {
                tracing::warn!(error = %e, user = u.id.get(), "oidc link lookup failed during dav basic auth");
                return BasicResult::AppPasswordRequired;
            }
        }

        let _ = self.maybe_rehash(u.id, secret, &hash);
        if let Ok(conn) = self.pool.get() {
            let _ = self.maybe_backfill_nt(&conn, u.id, secret);
        }

        let principal = Principal {
            user: u.id,
            scope: Scope::default(),
            via: AuthVia::AccountPassword,
        };
        self.cred_cache.put(cache_key, Outcome::Accepted(principal.clone()), gen);
        self.audit(Some(u.id), "auth.login", Some(user), Some(ip), true, Some("dav_basic"));
        BasicResult::Ok(principal)
    }

    /// Connection-memo tier ① lookup (tier ①). `auth_header_hash`
    /// is `sha256` of the raw `Authorization` header bytes, computed by the
    /// HTTP layer.
    pub fn conn_memo_check(&self, auth_header_hash: &[u8; 32]) -> Option<Principal> {
        self.conn_memo.check(auth_header_hash, self.generation())
    }

    pub fn conn_memo_store(&self, auth_header_hash: [u8; 32], p: Principal) {
        self.conn_memo.store(auth_header_hash, p, self.generation());
    }
}
