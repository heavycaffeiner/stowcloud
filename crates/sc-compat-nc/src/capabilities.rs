//! `GET /ocs/{v1,v2}.php/cloud/capabilities`
//!
//! Reference: `core/Controller/OCSController.php::getCapabilities`, plus the
//! per-app `getCapabilities()` providers in `apps/{dav,files,files_sharing,
//! theming,user_status,notifications}/lib/Capabilities.php`.
//!
//! # The rule that governs this whole file
//!
//! **A missing key is not "unsupported" — it is "assume the default", and the
//! default is usually `true`.** A client that does not see
//! `files_sharing.federation.outgoing` does not conclude we lack federation; it
//! concludes nothing and probes the endpoint. So every feature we do not have
//! is written out explicitly as `false`, `[]`, `{}` or `""`. That is why this
//! file is longer than it looks like it should be.
//!
//! The one exception is `dav.bulkupload`: there, *presence* is the signal. If
//! the key exists at all the desktop client switches small files onto a
//! separate bundled-upload path we do not implement, so the key is omitted
//! rather than set false (note 3).

use crate::config::NcConfig;
use crate::ocs::Val;

pub fn capabilities(cfg: &NcConfig) -> Val {
    Val::map([
        ("version", version_block(cfg)),
        ("capabilities", capability_block(cfg)),
    ])
}

fn version_block(cfg: &NcConfig) -> Val {
    let (major, minor, micro) = cfg.matrix.version_triple();
    Val::map([
        ("major", Val::from(major)),
        ("minor", Val::from(minor)),
        ("micro", Val::from(micro)),
        ("string", Val::str(cfg.matrix.claim.versionstring.clone())),
        ("edition", Val::str(cfg.matrix.claim.edition.clone())),
        (
            "extendedSupport",
            Val::Bool(cfg.matrix.claim.extended_support),
        ),
    ])
}

fn sharee_caps(cfg: &NcConfig) -> Val {
    Val::map([
        // Upstream's default is 0 — i.e. a single-character query enumerates
        // every account on the server. We publish our real floor so clients
        // stop typing-ahead below it instead of hammering a rate limiter.
        ("minSearchStringLength", Val::from(cfg.sharee_min_search)),
        // Never consult a global lookup server: it would leak our account
        // names off-box.
        ("query_lookup_default", Val::Bool(false)),
        ("always_show_unique", Val::Bool(true)),
    ])
}

fn capability_block(cfg: &NcConfig) -> Val {
    Val::map([
        ("core", core_caps(cfg)),
        ("bruteforce", bruteforce_caps()),
        ("dav", dav_caps()),
        ("files", files_caps(cfg)),
        ("files_sharing", files_sharing_caps(cfg)),
        ("theming", theming_caps(cfg)),
        ("user_status", user_status_caps()),
        ("notifications", notifications_caps()),
        // NOTE: `activity` is deliberately ABSENT. See `activity_caps`.
        // Explicit denials for whole apps clients probe for. Without these the
        // client attempts the app's endpoints on every sync.
        ("password_policy", Val::empty_map()),
        ("end-to-end-encryption", e2ee_caps()),
        ("systemtags", Val::map([("enabled", Val::Bool(false))])),
        ("comments", Val::Bool(false)),
        ("undelete", Val::Bool(true)),
    ])
}

fn core_caps(cfg: &NcConfig) -> Val {
    Val::map([
        // Seconds between polls for clients without a push channel. We do not
        // implement notify_push (non-goal), so this is the only freshness
        // mechanism the client has.
        ("pollinterval", Val::from(cfg.poll_interval_s)),
        // No leading slash — that is how upstream writes it and some clients
        // concatenate naively.
        ("webdav-root", Val::str("remote.php/webdav")),
        // The reference API (link previews for pasted URLs) needs an endpoint
        // we do not serve.
        ("reference-api", Val::Bool(false)),
        ("reference-regex", Val::str("")),
        // We do not require index.php in URLs.
        ("mod-rewrite-working", Val::Bool(true)),
    ])
}

fn bruteforce_caps() -> Val {
    // Clients read `delay` to decide whether to back off after a failed login.
    // Reporting 0 with no allow-list is honest: our throttling lives in
    // sc-auth and is not expressed through this channel.
    Val::map([
        ("delay", Val::Int(0)),
        ("allow-listed", Val::Bool(false)),
    ])
}

fn dav_caps() -> Val {
    Val::map([
        // "1.0" is the chunking-v2 (`/remote.php/dav/uploads/...`) protocol
        // marker, and it is a STRING in the reference, not a float. The desktop
        // client does a bytewise `>= "1.0"` comparison
        // (`Capabilities::chunkingNg`), so the type matters.
        ("chunking", Val::str("1.0")),
        // Chunked upload into a public file-drop link: not wired up.
        ("public_shares_chunking", Val::Bool(false)),
        // DAV SEARCH report extensions. We serve no REPORT, so advertising
        // these would make clients issue searches that 405.
        ("search_supports_creation_time", Val::Bool(false)),
        ("search_supports_upload_time", Val::Bool(false)),
        ("search_supports_last_activity", Val::Bool(false)),
        ("absence-supported", Val::Bool(false)),
        // NOTE: `bulkupload` is deliberately ABSENT, not false. Its presence —
        // whatever the value — makes the client bundle small files into a
        // multipart POST to /remote.php/dav/bulk. Omitting the key is the only
        // way to say no.
    ])
}

fn files_caps(cfg: &NcConfig) -> Val {
    Val::map([
        ("bigfilechunking", Val::Bool(true)),
        (
            "chunked_upload",
            Val::map([
                // ADVISORY ONLY. Despite the name, we do not enforce a chunk
                // size ceiling — there is none server-side, and an oversized chunk is accepted normally. This
                // number just tells the client what is unlikely to be rejected
                // by an intermediary. If a proxy does return 413 it never
                // reaches us and the client's own auto-adjust handles it.
                ("max_size", Val::from(cfg.chunk_size_advisory)),
                (
                    "max_parallel_count",
                    Val::from(cfg.chunk_parallel_advisory),
                ),
            ]),
        ),
        (
            "blacklisted_files",
            Val::list(
                cfg.blacklisted_files
                    .iter()
                    .map(|s| Val::str(s.clone()))
                    .collect::<Vec<_>>(),
            ),
        ),
        (
            "forbidden_filename_characters",
            // MUST match SafePath's rejection table. If we advertise a name as
            // legal and then reject the PUT, the client retries the same file
            // forever and the sync never converges.
            Val::list(
                cfg.forbidden_filename_characters
                    .iter()
                    .map(|s| Val::str(s.clone()))
                    .collect::<Vec<_>>(),
            ),
        ),
        ("undelete", Val::Bool(true)),
        // ---- explicitly unsupported ----
        ("versioning", Val::Bool(false)),
        ("version_labeling", Val::Bool(false)),
        ("version_deletion", Val::Bool(false)),
        ("comments", Val::Bool(false)),
        ("locking", Val::Bool(false)),
        (
            "directEditing",
            Val::map([
                ("url", Val::str("")),
                ("etag", Val::str("")),
                ("supportsFileId", Val::Bool(false)),
            ]),
        ),
    ])
}

fn files_sharing_caps(cfg: &NcConfig) -> Val {
    Val::map([
        ("api_enabled", Val::Bool(true)),
        ("default_permissions", Val::from(31u32)),
        ("exclude_reshare_from_edit", Val::Bool(false)),
        // We do not implement reshare chains.
        ("resharing", Val::Bool(false)),
        ("group_sharing", Val::Bool(true)),
        ("sharebymail", Val::map([("enabled", Val::Bool(false))])),
        (
            "user",
            Val::map([
                ("send_mail", Val::Bool(false)),
                ("expire_date", Val::map([("enabled", Val::Bool(true))])),
            ]),
        ),
        (
            "group",
            Val::map([
                ("enabled", Val::Bool(true)),
                ("expire_date", Val::map([("enabled", Val::Bool(true))])),
            ]),
        ),
        (
            "public",
            Val::map([
                ("enabled", Val::Bool(true)),
                ("upload", Val::Bool(true)),
                ("upload_files_drop", Val::Bool(true)),
                ("multiple_links", Val::Bool(true)),
                (
                    "password",
                    Val::map([
                        ("enforced", Val::Bool(false)),
                        ("askForOptionalPassword", Val::Bool(true)),
                    ]),
                ),
                ("expire_date", Val::map([("enabled", Val::Bool(true))])),
                (
                    "expire_date_internal",
                    Val::map([("enabled", Val::Bool(false))]),
                ),
                ("send_mail", Val::Bool(false)),
                ("custom_tokens", Val::Bool(false)),
            ]),
        ),
        (
            "federation",
            Val::map([
                ("outgoing", Val::Bool(false)),
                ("incoming", Val::Bool(false)),
                ("expire_date", Val::map([("enabled", Val::Bool(false))])),
                (
                    "expire_date_supported",
                    Val::map([("enabled", Val::Bool(false))]),
                ),
            ]),
        ),
        ("sharee", sharee_caps(cfg)),
        // Talk is a non-goal, so password-over-Talk cannot work.
        (
            "public_password_by_talk",
            Val::map([("enabled", Val::Bool(false))]),
        ),
    ])
}

fn theming_caps(cfg: &NcConfig) -> Val {
    Val::map([
        ("name", Val::str(cfg.theming_name.clone())),
        ("productName", Val::str(cfg.theming_name.clone())),
        ("url", Val::str(cfg.canonical_url.clone())),
        ("imprintUrl", Val::str("")),
        ("privacyUrl", Val::str("")),
        ("slogan", Val::str("")),
        ("color", Val::str(cfg.theming_color.clone())),
        ("color-text", Val::str("#ffffff")),
        ("color-element", Val::str(cfg.theming_color.clone())),
        ("color-element-bright", Val::str(cfg.theming_color.clone())),
        ("color-element-dark", Val::str(cfg.theming_color.clone())),
        ("logo", Val::str("")),
        ("background", Val::str("")),
        ("background-plain", Val::Bool(true)),
        ("background-default", Val::Bool(true)),
        ("logoheader", Val::str("")),
        ("favicon", Val::str("")),
    ])
}

fn user_status_caps() -> Val {
    // Advertised off, and the endpoint itself 404s (`stubs::NOT_FOUND_PATHS`).
    // Both are needed: some clients render a status UI purely from this flag,
    // without checking it before every fetch.
    Val::map([
        ("enabled", Val::Bool(false)),
        ("restore", Val::Bool(false)),
        ("supports_emoji", Val::Bool(false)),
    ])
}

fn notifications_caps() -> Val {
    // We keep the endpoint list because clients that see the `notifications`
    // key at all will call `list`; the stub answers with an empty array. The
    // push/admin surfaces are advertised empty so nothing else is attempted.
    Val::map([
        (
            "ocs-endpoints",
            Val::list([Val::str("list"), Val::str("get"), Val::str("delete")]),
        ),
        ("push", Val::empty_list()),
        ("admin-notifications", Val::empty_list()),
    ])
}

/// Not called. Kept as the record of a fix, because the reasoning it replaces
/// was plausible and would otherwise be re-derived.
///
/// The old value was `{"apiv2": []}`, on the theory that "the app exists but
/// exposes no v2 filters" makes the 404 on `/apps/activity/api/v2/activity`
/// an expected outcome rather than an error. That is true of the *desktop*
/// client. It is wrong for both mobile clients, which gate the whole activity
/// feature on **presence of the key**, never on its contents:
///
/// ```text
/// GetCapabilitiesRemoteOperation.java:643-648
///     if (respCapabilities.has(NODE_ACTIVITY)) {
///         capability.setActivity(CapabilityBooleanType.TRUE);
///     } else {
///         capability.setActivity(CapabilityBooleanType.FALSE);
///     }
///
/// the iOS SDK's +Capabilities.swift:413
///     capabilities.activityEnabled = json.activity != nil
/// ```
///
/// So `{"apiv2": []}` — or even `{}` — turns the activity UI *on* in both
/// apps, which then poll an endpoint we answer with 404. Omitting the key
/// entirely is the only way to say "no".
///
/// This same presence-is-truth shape applies to `external` and `governance`
/// (`the iOS SDK's +Capabilities.swift:429, 416`), which is why neither of those
/// keys is emitted either. `governance` is the sharpest case: its Swift model
/// is `struct Governance: Codable {}`, a type with no fields at all, so there
/// is no value that could mean "off".
#[allow(dead_code)]
fn activity_caps() -> Val {
    Val::map([("apiv2", Val::empty_list())])
}

fn e2ee_caps() -> Val {
    Val::map([
        ("enabled", Val::Bool(false)),
        ("api-version", Val::str("")),
    ])
}

#[cfg(test)]
mod tests {
    use super::*;

    fn caps_json() -> serde_json::Value {
        capabilities(&NcConfig::default()).to_json()
    }

    #[test]
    fn version_block_is_decomposed_from_the_matrix() {
        let j = caps_json();
        assert_eq!(j["version"]["major"], 31);
        assert_eq!(j["version"]["minor"], 0);
        assert_eq!(j["version"]["micro"], 4);
        assert_eq!(j["version"]["string"], "31.0.4");
        assert_eq!(j["version"]["edition"], "");
        assert_eq!(j["version"]["extendedSupport"], false);
    }

    /// The single most important property of this file: unsupported features
    /// are *present and false*, never absent.
    #[test]
    fn unsupported_features_are_explicitly_false() {
        let c = &caps_json()["capabilities"];
        for path in [
            "files.versioning",
            "files.version_labeling",
            "files.version_deletion",
            "files.comments",
            "files.locking",
            "files.directEditing.supportsFileId",
            "files_sharing.resharing",
            "files_sharing.federation.outgoing",
            "files_sharing.federation.incoming",
            "files_sharing.sharee.query_lookup_default",
            "files_sharing.user.send_mail",
            "files_sharing.public.send_mail",
            "files_sharing.public.password.enforced",
            "files_sharing.public_password_by_talk.enabled",
            "files_sharing.sharebymail.enabled",
            "user_status.enabled",
            "core.reference-api",
            "systemtags.enabled",
            "end-to-end-encryption.enabled",
        ] {
            let mut cur = c;
            for seg in path.split('.') {
                cur = cur
                    .get(seg)
                    .unwrap_or_else(|| panic!("capability key {path} is MISSING — clients will assume it is enabled"));
            }
            assert_eq!(
                cur,
                &serde_json::Value::Bool(false),
                "capability {path} must be explicitly false"
            );
        }
        assert_eq!(c["comments"], serde_json::Value::Bool(false));
        assert_eq!(c["files"]["directEditing"]["url"], "");
    }

    /// The mirror image of the rule above: for a handful of keys the mobile
    /// clients read *presence*, not value, so "explicitly false" is expressed
    /// by omitting the key. Emitting `{}` would enable the feature.
    #[test]
    fn presence_is_truth_keys_are_absent() {
        let c = &caps_json()["capabilities"];
        for key in [
            // Android: GetCapabilitiesRemoteOperation.java:643-648
            // iOS:     the iOS SDK's +Capabilities.swift:413
            "activity",
            // iOS: the iOS SDK's +Capabilities.swift:429
            "external",
            // iOS: the iOS SDK's +Capabilities.swift:416 — `struct Governance {}`
            // has no fields, so no value can mean "disabled".
            "governance",
            // iOS: the iOS SDK's +Capabilities.swift:407 gates on the object.
            "richdocuments",
        ] {
            assert!(
                c.get(key).is_none(),
                "capability `{key}` is gated on presence by a mobile client; \
                 emitting it at all — even empty — turns the feature on"
            );
        }
    }

    /// Android parses capabilities with `org.json`, whose `getX()` accessors
    /// **throw** on a missing key, and the whole parse is wrapped in a single
    /// `catch (JSONException | IOException)`
    /// (`GetCapabilitiesRemoteOperation.java:252-254`). So one absent key does
    /// not degrade one feature — it discards the entire capabilities response.
    ///
    /// Every entry below is a `getX()` call with no `has()` guard around it,
    /// i.e. a key that is mandatory *given that its parent object is present*.
    #[test]
    fn keys_android_reads_without_a_has_guard_are_all_present() {
        let c = &caps_json()["capabilities"];
        // (parent path, required child, line in GetCapabilitiesRemoteOperation)
        for (parent, child) in [
            ("core", "pollinterval"),                  // :371
            ("files_sharing", "resharing"),            // :442
            ("files_sharing.public", "enabled"),       // :396
            ("files_sharing.user", "send_mail"),       // :438
            ("files_sharing.federation", "outgoing"),  // :446
            ("files_sharing.federation", "incoming"),  // :448
            ("files", "bigfilechunking"),              // :458
            ("files.directEditing", "etag"),           // :477
            ("theming", "name"),                       // :513
            ("theming", "slogan"),                     // :514
            ("theming", "color"),                      // :515
            ("end-to-end-encryption", "enabled"),      // :619
            ("end-to-end-encryption", "api-version"),  // :636
        ] {
            let mut cur = c;
            for seg in parent.split('.') {
                cur = cur.get(seg).unwrap_or_else(|| {
                    panic!("capability parent `{parent}` vanished")
                });
            }
            assert!(
                cur.get(child).is_some(),
                "`{parent}.{child}` is read by Android without a has() guard; \
                 omitting it throws JSONException and discards ALL capabilities"
            );
        }
    }

    /// iOS decodes capabilities with a `Codable` struct in which these fields
    /// are non-optional (`the iOS SDK's +Capabilities.swift:109-116, 177`). A
    /// missing one throws out of `JSONDecoder.decode` and the client stores no
    /// capabilities at all (`:57`, `.invalidData`).
    ///
    /// `major`/`minor`/`micro` are typed `Int` and `string`/`edition` `String`,
    /// so the JSON *types* matter too: `"major": "31"` fails the decode.
    #[test]
    fn keys_ios_requires_to_decode_at_all_are_present_and_correctly_typed() {
        let j = caps_json();
        let v = &j["version"];
        for k in ["major", "minor", "micro"] {
            assert!(
                v[k].is_i64() || v[k].is_u64(),
                "version.{k} must be a JSON number; iOS decodes it as Int"
            );
        }
        assert!(v["string"].is_string(), "version.string must be a JSON string");

        // Required only when the enclosing `public` object exists — and it does.
        let pubs = &j["capabilities"]["files_sharing"]["public"];
        assert!(
            pubs["enabled"].is_boolean(),
            "files_sharing.public.enabled is a non-optional Bool in iOS's model"
        );
    }

    /// Presence, not value, is the signal for bulkupload.
    #[test]
    fn bulkupload_key_is_absent() {
        let c = &caps_json()["capabilities"];
        assert_eq!(c["dav"]["chunking"], "1.0");
        assert!(
            c["dav"].get("bulkupload").is_none(),
            "advertising bulkupload at all routes small files to an endpoint we do not serve"
        );
    }

    #[test]
    fn advisory_chunk_size_is_published() {
        let cfg = NcConfig::default();
        let c = capabilities(&cfg).to_json();
        assert_eq!(
            c["capabilities"]["files"]["chunked_upload"]["max_size"],
            10 * 1024 * 1024
        );
        assert_eq!(
            c["capabilities"]["files"]["chunked_upload"]["max_parallel_count"],
            4
        );
    }

    #[test]
    fn forbidden_characters_are_advertised() {
        let c = caps_json();
        let f = c["capabilities"]["files"]["forbidden_filename_characters"]
            .as_array()
            .unwrap();
        assert!(f.contains(&serde_json::Value::String("/".into())));
        assert!(f.contains(&serde_json::Value::String("\\".into())));
    }
}
