//! Trait boundary for signed content-URL byte serving (`DESIGN-PREVIEW.md`
//! §2). Kept separate from [`crate::core_api::CoreApi`] for two reasons:
//!
//! * a signed URL's [`crate::content::Claim`] addresses its target by
//!   `FileId`, not virtual path — there is no `user`/`vpath` to resolve, by
//!   design (`DESIGN-PREVIEW.md` §2.1: verification is stateless capability,
//!   not an ACL re-check);
//! * `InlineThumb` needs `sc-preview`, which this crate does not (and should
//!   not) depend on — `sc-server` is the only place that binds this trait to
//!   a real `sc_preview::PreviewService`.

use std::future::Future;
use std::io::Read;
use std::pin::Pin;

use sc_vfs::ids::FileId;

use crate::core_api::CoreError;

pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;

/// Enough about the file to build response headers and to check the
/// etag-mismatch `410 Gone` condition (`DESIGN-PREVIEW.md` §2.4).
#[derive(Clone, Debug)]
pub struct ContentStat {
    pub name: String,
    pub size: u64,
    pub etag: String,
}

pub trait ContentApi: Send + Sync {
    /// Metadata only — no file descriptor opened. Used both to build
    /// headers/decide `Range` satisfiability before streaming, and by
    /// `POST /api/fs/link` to derive the claim's etag8 from the *current*
    /// server-side state rather than trusting a client-supplied value.
    fn stat_by_fid(&self, fid: FileId) -> Result<ContentStat, CoreError>;

    /// Whether `user` currently holds `READ` on `fid`. `POST /api/fs/link`
    /// must refuse to mint a capability for a file the caller cannot read —
    /// `fid`s are small sequential integers, not secrets, so "the client
    /// already knows a legitimate one" is not an access-control argument.
    fn check_read(&self, user: sc_vfs::ids::UserId, fid: FileId) -> Result<(), CoreError>;

    /// Open a bounded-memory reader over `fid`, restricted to the inclusive
    /// `(start, end)` byte range already clamped by the caller against
    /// [`ContentStat::size`] (or the whole file when `None`). The
    /// implementation keeps the underlying fd open for the reader's entire
    /// lifetime (`ARCHITECTURE.md` §5.2).
    fn open_stream(&self, fid: FileId, range: Option<(u64, u64)>) -> Result<Box<dyn Read + Send>, CoreError>;

    /// Generate (or fetch from cache) the inline re-encoded thumbnail for
    /// `fid` at preset size `(w, h)`. **Never** returns original bytes —
    /// `InlineThumb` is only ever our own re-encode (`DESIGN-PREVIEW.md`
    /// §2.4), which is what makes it safe to serve `inline`.
    fn thumbnail(&self, fid: FileId, w: u16, h: u16, etag8: [u8; 8]) -> BoxFuture<'static, Result<Vec<u8>, CoreError>>;
}

/// Reports every operation as not-yet-wired — the default for `AppState`
/// until a real preview/streaming backend is plugged in, and for HTTP-layer
/// tests that don't exercise content serving.
pub struct UnimplementedContent;

impl ContentApi for UnimplementedContent {
    fn stat_by_fid(&self, _fid: FileId) -> Result<ContentStat, CoreError> {
        Err(CoreError::NotFound)
    }
    fn check_read(&self, _user: sc_vfs::ids::UserId, _fid: FileId) -> Result<(), CoreError> {
        Err(CoreError::Denied { by: None })
    }
    fn open_stream(&self, _fid: FileId, _range: Option<(u64, u64)>) -> Result<Box<dyn Read + Send>, CoreError> {
        Err(CoreError::NotFound)
    }
    fn thumbnail(&self, _fid: FileId, _w: u16, _h: u16, _etag8: [u8; 8]) -> BoxFuture<'static, Result<Vec<u8>, CoreError>> {
        Box::pin(async { Err(CoreError::NotSupported) })
    }
}
