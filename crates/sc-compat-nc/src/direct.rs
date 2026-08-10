//! `POST /ocs/v2.php/apps/dav/api/v1/direct` — a URL a media player can open.
//!
//! Both apps hand the result to an external player process so a video can be
//! streamed instead of downloaded whole first. That process carries no
//! credentials, which is what makes the four rules below the whole design:
//!
//! 1. the URL names one file id, resolved and ACL-checked here, at issue time,
//!    under the requesting principal;
//! 2. it expires in minutes, not the eight hours a share link gets, because
//!    the client uses it immediately;
//! 3. it is `GET`-only and grants read alone;
//! 4. it is served from the content origin, so a signed URL can never reach an
//!    app-origin route.
//!
//! Rules 2 to 4 are the signing mechanism's, not this module's: it mints the
//! same kind of claim a preview URL carries. Rule 1 is here.

use crate::ocs::{OcsError, Val};
use crate::ports::{Deps, FileId, UserId};

pub fn mint(deps: &Deps, user: UserId, file_id: i64) -> Result<Val, OcsError> {
    // A file id the caller cannot read is not-found, never forbidden: 403
    // would confirm the id names something.
    let not_found = || OcsError::not_found("File not found");

    let (share, path) = deps.core.locate(user, FileId(file_id)).map_err(|_| not_found())?;
    let vpath = deps
        .core
        .vpath_for(user, share, &path)
        .ok_or_else(not_found)?;
    match deps.preview.signed_download_url(user, &vpath) {
        Ok(Some(url)) => Ok(Val::map([("url", Val::str(url))])),
        // No content origin configured, or the file has no id to sign over.
        // Either way there is no URL to hand out, and saying so is better than
        // handing back one that cannot work.
        Ok(None) => Err(not_found()),
        Err(_) => Err(not_found()),
    }
}
