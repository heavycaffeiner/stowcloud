//! Compat-layer configuration and the advertised version matrix.

use serde::Deserialize;

/// `compat_matrix.toml`, baked in as the default. Deployments may override it
/// from disk via [`CompatMatrix::parse`]; the admin UI writes that file.
pub const DEFAULT_COMPAT_MATRIX: &str = include_str!("../compat_matrix.toml");

#[derive(Clone, Debug, Deserialize)]
pub struct CompatMatrix {
    pub claim: Claim,
    /// Client network layers we read the source of. Documentation, not
    /// evidence — see [`CompatMatrix::device_verified`].
    #[serde(default)]
    pub source_audited: SourceAudited,
    /// Clients a real build of which actually talked to this server.
    ///
    /// Kept separate from [`Self::source_audited`] because the two have been
    /// confused once already, expensively: reading the desktop client's source
    /// produced `<D:multistatus>`, which is valid XML and correct for every
    /// namespace-aware parser, and which made iOS see every directory as empty
    /// while still reporting success. Source-reading found that bug and
    /// source-reading is also what shipped it.
    #[serde(default)]
    pub device_verified: Verified,
}

/// `client -> the reference we read`, e.g. `"iOS SDK @ master"`.
#[derive(Clone, Debug, Default, Deserialize)]
pub struct SourceAudited {
    #[serde(default)]
    pub desktop: String,
    #[serde(default)]
    pub android: String,
    #[serde(default)]
    pub ios: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Claim {
    /// Four-element dotted version, e.g. `31.0.4.1`. This is what `status.php`
    /// reports as `version` and what the capabilities `version` object is
    /// decomposed from.
    pub version: String,
    /// Three-element human string, e.g. `31.0.4`.
    pub versionstring: String,
    #[serde(default)]
    pub edition: String,
    /// Clients branch on this. Do not change it.
    pub productname: String,
    #[serde(default)]
    pub extended_support: bool,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Verified {
    #[serde(default)]
    pub desktop: Vec<String>,
    #[serde(default)]
    pub android: Vec<String>,
    #[serde(default)]
    pub ios: Vec<String>,
    #[serde(default)]
    pub rclone: Vec<String>,
}

impl Default for CompatMatrix {
    fn default() -> Self {
        Self::parse(DEFAULT_COMPAT_MATRIX).expect("bundled compat_matrix.toml must parse")
    }
}

impl CompatMatrix {
    pub fn parse(s: &str) -> Result<Self, toml::de::Error> {
        toml::from_str(s)
    }

    /// `(major, minor, micro)` for the capabilities `version` object. Missing
    /// components read as 0 rather than failing: a malformed matrix must not
    /// take the server down.
    pub fn version_triple(&self) -> (u32, u32, u32) {
        let mut it = self.version_parts();
        (
            it.next().unwrap_or(0),
            it.next().unwrap_or(0),
            it.next().unwrap_or(0),
        )
    }

    fn version_parts(&self) -> impl Iterator<Item = u32> + '_ {
        self.claim
            .version
            .split('.')
            .map(|p| p.parse::<u32>().unwrap_or(0))
    }
}

/// How far `sharees` autocomplete is allowed to reach.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ShareeLookup {
    /// Only principals that share a group with the caller. **Default**:
    /// unrestricted partial-match search is an account enumeration oracle.
    #[default]
    SameGroup,
    All,
    Off,
}

#[derive(Clone, Debug)]
pub struct NcConfig {
    pub matrix: CompatMatrix,

    /// Canonical, externally reachable base URL, **without** a trailing slash,
    /// e.g. `https://cloud.example.com`.
    ///
    /// # Never derive this from the `Host` header
    ///
    /// Login Flow v2 hands this URL to a browser that a human is about to
    /// authenticate against, and hands it back to the client as the `server`
    /// to bind to. Trusting `Host` lets anyone who can reach the server mint a
    /// login page pointing at a host they control — a phishing redirect with
    /// our name on it, and a client permanently bound to the attacker's
    /// hostname.
    pub canonical_url: String,

    /// Name of the browser session cookie, handed in rather than spelled here.
    ///
    /// This crate depends on no HTTP crate by design (§1), so it cannot import
    /// the constant from the layer that sets the cookie — and when the name was
    /// simply written out twice, the two copies drifted: this side looked for
    /// `sc_session` while the server set `__Host-sc_sid`. The cookie was
    /// therefore never found, so the login-flow consent screen judged every
    /// visitor unauthenticated and redirected to `/login`, which redirected
    /// back to the flow. On a phone that reads as the login form resetting
    /// itself after correct credentials, with nothing in any log to say why.
    ///
    /// `sc-server` sets this from `sc_http::SESSION_COOKIE`, which keeps one
    /// authority for the name — the same reason `reserved_path_prefixes` is
    /// handed to `sc-http` from outside instead of being duplicated.
    pub session_cookie: String,

    /// The instance id, read once from `nc_instance` at startup.
    ///
    /// # !!! CHANGING THIS FORCES A FULL RESYNC ON EVERY CLIENT !!!
    ///
    /// It is a suffix of every `oc:id` this server has ever emitted. Clients
    /// key their local sync journal on those ids. A new instance id means
    /// every file looks brand new: every client discards its journal and
    /// re-downloads everything. For a large deployment that is terabytes of
    /// traffic and hours of unavailability, and it happens silently.
    ///
    /// It MUST be in your backups and MUST be restored verbatim. Restoring
    /// file data without `nc_instance` is not a restore. See `DEPLOYMENT.md`.
    pub instance_id: String,

    /// Advisory chunk size published as `files.chunked_upload.max_size`.
    ///
    /// The NC field name says "max". **We do not enforce it.** There is no
    /// server-side chunk size ceiling; a larger chunk
    /// is accepted normally. This value exists purely to tell clients what size
    /// is unlikely to trip an intermediary proxy. If one does return 413 the
    /// client's own auto-adjust handles it, and the 413 never reaches us.
    pub chunk_size_advisory: u64,

    /// Published as `files.chunked_upload.max_parallel_count`. Also advisory.
    pub chunk_parallel_advisory: u32,

    pub sharee_lookup: ShareeLookup,

    /// Minimum `sharees?search=` length. Upstream's default is 0 (unrestricted
    /// prefix enumeration of every account on the server). We require 3.
    pub sharee_min_search: usize,

    /// Max `sharees` queries per user per minute.
    pub sharee_rate_per_min: u32,

    /// Login flow lifetime.
    pub login_flow_ttl_ns: i64,

    /// Minimum spacing between two polls of the same token, in nanoseconds.
    /// Unbounded polling is a DB-scan DoS.
    pub login_flow_poll_interval_ns: i64,

    /// `core.pollinterval` in capabilities: how often a client without push
    /// should re-scan. Seconds.
    pub poll_interval_s: u32,

    /// Display name for `theming.name`.
    pub theming_name: String,
    pub theming_color: String,

    /// Published as `files.forbidden_filename_characters` — a **client-side**
    /// creation hint, not a mirror of `SafePath`'s server-side rejection
    /// rules. Its job is to stop a Windows client creating a name its own
    /// filesystem cannot store, so it must be a **superset** of what
    /// `sc_vfs::safe_path::validate_component` actually rejects, never equal
    /// to it: shared folders are co-accessed by Jellyfin, rsync and Samba,
    /// which write into the same directories on a Linux filesystem that
    /// permits `* ? " < > |` in names. Rejecting those eight server-side to
    /// match this list would make files that already exist on disk
    /// inaccessible through us — worse than the mismatch this list exists to
    /// avoid.
    ///
    /// The one direction that *is* required: never advertise a name as legal
    /// that `validate_component` then rejects — that puts a client's sync
    /// into a permanent retry loop ( note 4).
    /// Over-advertising has a real but survivable cost instead: a file
    /// already on disk whose name contains one of these characters (created
    /// by SMB, NFS, or another service sharing the directory) is listed to an
    /// NC client that considers the name invalid, and that client declines to
    /// sync just that one file rather than retrying forever.
    pub forbidden_filename_characters: Vec<String>,

    /// Published as `files.blacklisted_files`.
    pub blacklisted_files: Vec<String>,
}

impl Default for NcConfig {
    fn default() -> Self {
        Self {
            matrix: CompatMatrix::default(),
            canonical_url: "https://localhost".into(),
            session_cookie: "__Host-sc_sid".into(),
            instance_id: String::new(),
            // 10 MiB. Matches the desktop client's own default target and sits
            // under the common 100 MB proxy body limits with a wide margin.
            chunk_size_advisory: 10 * 1024 * 1024,
            chunk_parallel_advisory: 4,
            sharee_lookup: ShareeLookup::SameGroup,
            sharee_min_search: 3,
            sharee_rate_per_min: 30,
            login_flow_ttl_ns: 20 * 60 * 1_000_000_000,
            login_flow_poll_interval_ns: 1_000_000_000,
            poll_interval_s: 60,
            theming_name: "Nextcloud".into(),
            theming_color: "#0082c9".into(),
            forbidden_filename_characters: [
                "\\", "/", ":", "*", "?", "\"", "<", ">", "|",
            ]
            .iter()
            .map(|s| (*s).to_string())
            .collect(),
            blacklisted_files: vec![".htaccess".into()],
        }
    }
}

impl NcConfig {
    /// Join a path onto the canonical URL. Always use this instead of the
    /// request's `Host`.
    pub fn url(&self, path: &str) -> String {
        let base = self.canonical_url.trim_end_matches('/');
        if path.starts_with('/') {
            format!("{base}{path}")
        } else {
            format!("{base}/{path}")
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bundled_matrix_parses_and_is_a_release_version() {
        let m = CompatMatrix::default();
        assert_eq!(m.claim.productname, "Nextcloud");
        // A " dev" suffix would put clients on unreleased-server code paths.
        assert!(!m.claim.versionstring.contains("dev"));
        assert_eq!(m.version_triple(), (31, 0, 4));
        // status.php's `version` is the 4-element form.
        assert_eq!(m.claim.version.split('.').count(), 4);
        assert_eq!(m.claim.versionstring.split('.').count(), 3);
    }

    #[test]
    fn urls_are_built_from_canonical_not_host() {
        let c = NcConfig {
            canonical_url: "https://cloud.example.com/".into(),
            ..NcConfig::default()
        };
        assert_eq!(
            c.url("/index.php/login/v2/poll"),
            "https://cloud.example.com/index.php/login/v2/poll"
        );
        assert_eq!(c.url("s/tok"), "https://cloud.example.com/s/tok");
    }
}
