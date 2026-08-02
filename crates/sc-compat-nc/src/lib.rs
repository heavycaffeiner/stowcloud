//! # `sc-compat-nc` — the legacy-client compatibility layer
//!
//! Everything in this crate exists for one reason: to let the unmodified
//! Nextcloud desktop and mobile clients sync against this server. It is
//! gated behind `feature = "compat-nc"` in `sc-server` and is designed to be
//! deleted wholesale without leaving a trace anywhere else.
//!
//! ## The isolation contract (`docs/DESIGN-COMPAT.md` §1)
//!
//! The test applied to every line here is:
//!
//! > *Would this feature need to exist without the compat layer?*
//!
//! If yes, it belongs in a core crate. If no, it belongs here. That is why
//! stable file ids, directory-aggregate ETags, the chunked upload engine and
//! share links live in `sc-meta`/`sc-core`/`sc-upload`, while the
//! `"%08d{fileid}{instanceid}"` serialisation, the `SRGDNVCK` permission
//! string, the OCS envelope and the `MKCOL`/`PUT {n}`/`MOVE .file` protocol
//! mapping live here.
//!
//! Concretely:
//!
//! * This crate consumes only public APIs of the core crates. It required no
//!   change to any of them, and introduced no compat vocabulary into any of
//!   them. The CI gate
//!   `rg -i "\boc[:_-]|\bocs\b|remote\.php" crates/sc-{vfs,meta,core,acl,auth,dav,upload,http,watch,search}/src`
//!   must return nothing.
//! * All persistent compat-specific state lives in this crate's own tables:
//!   `nc_instance`, `nc_favorite`, `nc_upload_alias`, `nc_login_flow` (see
//!   [`store::NC_SCHEMA_SQL`]).
//! * There is no reverse dependency: no core crate mentions this one.
//!
//! ## !!! Back up `nc_instance` !!!
//!
//! The `instance_id` generated on first boot is a suffix of every `oc:id` this
//! server has ever emitted, and clients key their entire local sync journal on
//! those ids. **If it changes, every connected client discards its journal and
//! performs a full resync** — potentially terabytes of re-download, silently.
//! It must be in your backups and restored verbatim. See [`config::NcConfig`]
//! and `DEPLOYMENT.md`.
//!
//! ## Module map
//!
//! | module | responsibility |
//! |---|---|
//! | [`ports`] | the seam against `sc-core`/`sc-dav`/`sc-auth`/`sc-upload` |
//! | [`config`] | advertised version matrix, canonical URL, advisory limits |
//! | [`store`] | the `nc_*` tables |
//! | [`ocs`] | the OCS envelope, XML and JSON |
//! | [`status`] | `GET /status.php` |
//! | [`capabilities`] | `GET /ocs/*/cloud/capabilities` |
//! | [`user`] | `GET /ocs/*/cloud/user`, quota sentinels |
//! | [`login_flow`] | Login Flow v2 |
//! | [`props`] | `oc:`/`nc:` PROPFIND decoration, `oc_permissions` |
//! | [`dav_paths`] | `/remote.php/...` URL mapping |
//! | [`chunking`] | chunked upload v2 mapped onto `sc-upload` |
//! | [`shares`] | shares + sharees OCS API |
//! | [`preview`] | `/index.php/core/preview` -> 302 to the content origin |
//! | [`stubs`] | empty-success endpoints for apps we do not implement |
//! | [`router`] | assembles all of the above into one `axum::Router` |
//!
//! ## Non-goals (`DESIGN-COMPAT.md` §13)
//!
//! App store, server-side/E2E encryption, versioning, comments, tags, Talk,
//! groupware (CalDAV/CardDAV), federation, office integrations, notify_push,
//! activity streams, external storage mounts, workflows. Each is declared
//! `false`/empty in [`capabilities`] rather than omitted, because a *missing*
//! capability key makes clients assume the feature is present.

pub mod capabilities;
pub mod chunking;
pub mod config;
pub mod dav_paths;
pub mod login_flow;
pub mod ocs;
pub mod ports;
pub mod preview;
pub mod props;
pub mod router;
pub mod shares;
pub mod status;
pub mod store;
pub mod stubs;
pub mod user;

pub use config::{CompatMatrix, NcConfig, ShareeLookup};
pub use ocs::{Ocs, OcsError, OcsFormat, OcsVersion, Val};
pub use props::{nc_id, oc_permissions, NcPropSource, NS_NC, NS_OC};
pub use router::{router, NcState};
pub use store::{MemStore, NcStore, NC_SCHEMA_SQL};

#[cfg(feature = "sqlite")]
pub use store::SqliteStore;
