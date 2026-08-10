//! DAV error type and its HTTP projection.

use axum::response::{IntoResponse, Response};
use http::{header, StatusCode};

use crate::backend::CoreError;

#[derive(Debug, thiserror::Error)]
pub enum DavError {
    // ---- XML hardening: no DTD, no PI, bounded depth/size ----
    #[error("request body too large")]
    TooLarge,
    #[error("DTD is forbidden in DAV request bodies")]
    DtdForbidden,
    #[error("processing instructions are forbidden in DAV request bodies")]
    PiForbidden,
    #[error("XML nesting too deep")]
    TooDeep,
    #[error("too many XML elements")]
    TooManyElements,
    #[error("malformed XML: {0}")]
    BadXml(String),

    // ---- protocol ----
    #[error("bad request: {0}")]
    BadRequest(String),
    #[error("unsupported media type")]
    UnsupportedMediaType,
    #[error("unauthenticated")]
    Unauthorized,
    #[error("forbidden")]
    Forbidden,
    #[error("not found")]
    NotFound,
    #[error("conflict")]
    Conflict,
    #[error("precondition failed")]
    PreconditionFailed,
    #[error("locked")]
    Locked,
    #[error("depth infinity is refused")]
    FiniteDepthRequired,
    /// No source claimed the report's root element. RFC 3253 §3.1.5 makes this
    /// a 403 with a `DAV:supported-report` precondition, not a 404: the
    /// resource exists, the report does not.
    #[error("unsupported report")]
    UnsupportedReport,
    #[error("bad gateway")]
    BadGateway,
    #[error("insufficient storage")]
    InsufficientStorage,
    #[error("method not allowed")]
    MethodNotAllowed,
    #[error("range not satisfiable")]
    RangeNotSatisfiable,
    #[error("internal error: {0}")]
    Internal(String),
}

impl DavError {
    pub fn status(&self) -> StatusCode {
        use DavError::*;
        match self {
            TooLarge => StatusCode::PAYLOAD_TOO_LARGE,
            DtdForbidden | PiForbidden | TooDeep | TooManyElements | BadXml(_)
            | BadRequest(_) => StatusCode::BAD_REQUEST,
            UnsupportedMediaType => StatusCode::UNSUPPORTED_MEDIA_TYPE,
            Unauthorized => StatusCode::UNAUTHORIZED,
            // `FiniteDepthRequired` is a 403 carrying a DAV:error body — RFC
            // 4918 explicitly sanctions refusing depth infinity this way.
            Forbidden | FiniteDepthRequired | UnsupportedReport => StatusCode::FORBIDDEN,
            NotFound => StatusCode::NOT_FOUND,
            Conflict => StatusCode::CONFLICT,
            PreconditionFailed => StatusCode::PRECONDITION_FAILED,
            Locked => StatusCode::LOCKED,
            BadGateway => StatusCode::BAD_GATEWAY,
            InsufficientStorage => StatusCode::INSUFFICIENT_STORAGE,
            MethodNotAllowed => StatusCode::METHOD_NOT_ALLOWED,
            RangeNotSatisfiable => StatusCode::RANGE_NOT_SATISFIABLE,
            Internal(_) => StatusCode::INTERNAL_SERVER_ERROR,
        }
    }

    /// Precondition element for the `DAV:error` body, when one applies.
    fn error_element(&self) -> Option<&'static str> {
        match self {
            DavError::FiniteDepthRequired => Some("<d:propfind-finite-depth/>"),
            DavError::UnsupportedReport => Some("<d:supported-report/>"),
            DavError::Locked => Some("<d:lock-token-submitted/>"),
            DavError::Conflict => None,
            _ => None,
        }
    }
}

/// The `Denied` / `NotListable` split is what keeps existence from leaking:
/// only a path the caller *can* list may answer 403, because 403 confirms the
/// path is there. Everything else is 404.
impl From<CoreError> for DavError {
    fn from(e: CoreError) -> Self {
        match e {
            CoreError::NotFound => DavError::NotFound,
            // No listing right => the path must look identical to a missing one.
            CoreError::NotListable => DavError::NotFound,
            CoreError::Denied => DavError::Forbidden,
            CoreError::Exists => DavError::PreconditionFailed,
            CoreError::NotEmpty => DavError::Conflict,
            CoreError::IsDir | CoreError::NotDir => DavError::Conflict,
            CoreError::NoSpace => DavError::InsufficientStorage,
            CoreError::NameTooLong => DavError::BadRequest("name too long".into()),
            CoreError::SymlinkDenied => DavError::Forbidden,
            CoreError::ReadOnly => DavError::Forbidden,
            CoreError::Invalid(m) => DavError::BadRequest(m),
            CoreError::Io(m) => DavError::Internal(m),
        }
    }
}

pub fn security_headers(resp: &mut Response) {
    // A DAV endpoint never renders anything, so it gets
    // the strictest possible CSP rather than the web-UI one.
    let h = resp.headers_mut();
    h.insert(
        header::CONTENT_SECURITY_POLICY,
        http::HeaderValue::from_static("default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; object-src 'none'; sandbox"),
    );
    h.insert(
        header::X_CONTENT_TYPE_OPTIONS,
        http::HeaderValue::from_static("nosniff"),
    );
    h.insert(
        header::REFERRER_POLICY,
        http::HeaderValue::from_static("no-referrer"),
    );
    h.insert(
        http::HeaderName::from_static("cross-origin-opener-policy"),
        http::HeaderValue::from_static("same-origin"),
    );
    h.insert(
        http::HeaderName::from_static("cross-origin-resource-policy"),
        http::HeaderValue::from_static("same-site"),
    );
    h.insert(
        http::HeaderName::from_static("permissions-policy"),
        http::HeaderValue::from_static(
            "geolocation=(), camera=(), microphone=(), interest-cohort=()",
        ),
    );
}

impl IntoResponse for DavError {
    fn into_response(self) -> Response {
        let status = self.status();
        let mut resp = match self.error_element() {
            Some(el) => {
                let body = format!(
                    "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<d:error xmlns:d=\"DAV:\">{el}</d:error>"
                );
                let mut r = Response::new(axum::body::Body::from(body));
                *r.status_mut() = status;
                r.headers_mut().insert(
                    header::CONTENT_TYPE,
                    http::HeaderValue::from_static("application/xml; charset=utf-8"),
                );
                r
            }
            None => {
                let mut r = Response::new(axum::body::Body::empty());
                *r.status_mut() = status;
                r
            }
        };
        if status == StatusCode::UNAUTHORIZED {
            resp.headers_mut().insert(
                header::WWW_AUTHENTICATE,
                http::HeaderValue::from_static("Basic realm=\"WebDAV\", charset=\"UTF-8\""),
            );
        }
        security_headers(&mut resp);
        resp
    }
}

pub type DavResult<T> = Result<T, DavError>;
