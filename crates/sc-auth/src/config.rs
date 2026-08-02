use std::time::Duration;

/// Tunable auth parameters. See `docs/DESIGN-AUTH.md`.
#[derive(Clone, Debug)]
pub struct AuthConfig {
    /// Max concurrent Argon2 hash operations (memory-peak bound). Default 4.
    pub argon2_parallelism: usize,
    /// Argon2id memory cost in KiB. Default 48 * 1024 (48 MiB).
    pub argon2_m_cost_kib: u32,
    /// Argon2id time cost. Default 3.
    pub argon2_t_cost: u32,
    /// Argon2id parallelism (lanes). Default 1.
    pub argon2_p_cost: u32,

    /// Minimum account password length. Default 10.
    pub min_password_len: usize,

    /// Session sliding idle expiry. Default 7 days.
    pub session_idle: Duration,
    /// Session absolute expiry. Default 30 days.
    pub session_absolute: Duration,

    /// Credential verification cache (DESIGN-AUTH §4.2) capacity. Default 4096.
    pub cred_cache_cap: usize,
    /// Positive entry absolute TTL. Default 15 minutes.
    pub cred_cache_pos_ttl: Duration,
    /// Positive entry idle TTL. Default 5 minutes.
    pub cred_cache_pos_idle: Duration,
    /// Negative entry TTL. Default 30 seconds.
    pub cred_cache_neg_ttl: Duration,

    /// App-password token cache (DESIGN-AUTH §5.3) capacity. Default 4096.
    pub token_cache_cap: usize,
    /// App-password token cache TTL. Default 60 seconds.
    pub token_cache_ttl: Duration,
    /// Coalescing interval for `last_used_ns` writes. Default 60 seconds.
    pub last_used_coalesce: Duration,

    /// Connection-memo (DESIGN-AUTH §4.2 ①) capacity.
    pub conn_memo_cap: usize,

    /// Per-IP hard token bucket: capacity. Default 20.
    pub rate_ip_capacity: u32,
    /// Per-IP hard token bucket: refill period per token. Default 10s.
    pub rate_ip_refill: Duration,

    /// Per-account soft-delay bucket: refill period per token (decay rate
    /// used to age out the failure counter). Default 30s.
    pub rate_account_refill: Duration,

    /// `[auth] dav_account_password` policy.
    pub dav_account_password: DavAccountPassword,
    /// `[smb] totp_policy`.
    pub smb_totp_policy: SmbTotpPolicy,
    /// `oidc.local_password_login`. Default [`OidcLocalPasswordLogin::Allow`];
    /// see that variant for why the less strict value is the default.
    pub oidc_local_password_login: OidcLocalPasswordLogin,
    /// `auth.rotate_password_on_totp_disable`. Default false.
    pub rotate_password_on_totp_disable: bool,

    /// Current SMB NT-hash / TOTP-secret encryption key version.
    pub key_ver: u32,
}

impl Default for AuthConfig {
    fn default() -> Self {
        Self {
            argon2_parallelism: 4,
            argon2_m_cost_kib: 48 * 1024,
            argon2_t_cost: 3,
            argon2_p_cost: 1,

            min_password_len: 10,

            session_idle: Duration::from_secs(7 * 24 * 3600),
            session_absolute: Duration::from_secs(30 * 24 * 3600),

            cred_cache_cap: 4096,
            cred_cache_pos_ttl: Duration::from_secs(15 * 60),
            cred_cache_pos_idle: Duration::from_secs(5 * 60),
            cred_cache_neg_ttl: Duration::from_secs(30),

            token_cache_cap: 4096,
            token_cache_ttl: Duration::from_secs(60),
            last_used_coalesce: Duration::from_secs(60),

            conn_memo_cap: 8192,

            rate_ip_capacity: 20,
            rate_ip_refill: Duration::from_secs(10),

            rate_account_refill: Duration::from_secs(30),

            dav_account_password: DavAccountPassword::Allow,
            smb_totp_policy: SmbTotpPolicy::RequireSeparate,
            oidc_local_password_login: OidcLocalPasswordLogin::Allow,
            rotate_password_on_totp_disable: false,

            key_ver: 1,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DavAccountPassword {
    Allow,
    Deny,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SmbTotpPolicy {
    RequireSeparate,
    Block,
}

/// Whether an OIDC-linked account may still log into the web UI with its
/// local account password (proposal §4.3.5).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OidcLocalPasswordLogin {
    /// The default, and the *less* strict of the two: someone who knows the
    /// local password can log in without meeting whatever the IdP would have
    /// demanded of them.
    ///
    /// It is the default anyway because `Deny` has no recovery path. If the
    /// IdP is down, or the client registration is wrong, or the redirect URI
    /// stopped matching, a `Deny` deployment has locked out every linked
    /// account including the administrator who would have to fix it. That is
    /// the same class of unrecoverable state `DESIGN-AUTH.md` refuses
    /// elsewhere: §7.1 declines account lockout, and §11 declines any
    /// operation that would leave zero active administrators.
    Allow,
    /// Local passwords do not open the web UI for a linked account. Set in
    /// `config.toml` only, never through the settings screen, because
    /// `Config::apply_settings_overrides` lets a stored override win over the
    /// file on every boot, so an operator who set this from the screen and
    /// then lost their IdP could not undo it by editing the file (§4.3.5).
    Deny,
}
