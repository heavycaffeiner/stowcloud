//! The OCS envelope.
//!
//! Reference: `lib/private/AppFramework/OCS/{BaseResponse,V1Response,V2Response}.php`
//! and `ocs/v1.php` / `ocs/v2.php`.
//!
//! Getting the status codes wrong here does not produce an error a user can
//! see — clients treat a wrong `statuscode` as "call failed" and give up
//! silently, which is why v1/v2 divergence is spelled out so explicitly below.
//!
//! XML is written by hand rather than via serde. `DESIGN-COMPAT.md` §4 calls
//! for this and the reference explains why: `BaseResponse::toXML` renames every
//! numerically-keyed entry to `<element>`, writes booleans as `1`/empty, and
//! flattens `@`-prefixed keys into attributes. None of that is serde's default
//! mapping, and a "close enough" mapping fails in ways that are invisible until
//! a specific client chokes.

use axum::body::Body;
use axum::http::{header, HeaderMap, HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};

/// Which OCS entry point the request came in through.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum OcsVersion {
    /// `/ocs/v1.php` — success is **100**, HTTP is **always 200**.
    V1,
    /// `/ocs/v2.php` — success is **200**, and the OCS code is mirrored into
    /// the HTTP status.
    V2,
}

impl OcsVersion {
    /// The success `statuscode`. This is the single most commonly botched
    /// value in an OCS reimplementation.
    ///
    /// * v1: `100` (`\OCP\API::RESPOND_*` era constant `OCS_SUCCESS`)
    /// * v2: `200`
    #[inline]
    pub fn success_code(self) -> u16 {
        match self {
            OcsVersion::V1 => 100,
            OcsVersion::V2 => 200,
        }
    }

    /// HTTP status for a given *internal* status code.
    ///
    /// v1 (`V1Response::getStatus`) pins HTTP 200 for everything **except**
    /// `997`, which becomes 401 — the one status the v1 endpoint is allowed to
    /// leak into HTTP:
    ///
    /// ```php
    /// $status = parent::getStatus();
    /// if ($status === OCSController::RESPOND_UNAUTHORISED) {
    ///     return Http::STATUS_UNAUTHORIZED;
    /// }
    /// return Http::STATUS_OK;
    /// ```
    ///
    /// v2 (`V2Response::getStatus`) mirrors, with the sentinels remapped, in
    /// exactly this evaluation order:
    ///
    /// ```text
    ///   997 (unauthorised)  -> 401
    ///   998 (not found)     -> 404
    ///   996 (server error)  -> 500
    ///   999 (unknown)       -> 500
    ///   < 200 or > 600      -> 400     <-- note: 100 lands here
    ///   otherwise           -> the code itself
    /// ```
    pub fn http_status(self, ocs_code: u16) -> StatusCode {
        match self {
            OcsVersion::V1 => {
                if ocs_code == ocs_code::RESPOND_UNAUTHORISED {
                    StatusCode::UNAUTHORIZED
                } else {
                    StatusCode::OK
                }
            }
            OcsVersion::V2 => match ocs_code {
                997 => StatusCode::UNAUTHORIZED,
                998 => StatusCode::NOT_FOUND,
                996 | 999 => StatusCode::INTERNAL_SERVER_ERROR,
                c if !(200..=600).contains(&c) => StatusCode::BAD_REQUEST,
                c => StatusCode::from_u16(c).unwrap_or(StatusCode::BAD_REQUEST),
            },
        }
    }
}

/// Legacy OCS sentinel codes. Present so callers can express "unauthorised"
/// without hard-coding 997 at every site.
pub mod ocs_code {
    pub const RESPOND_UNAUTHORISED: u16 = 997;
    pub const RESPOND_NOT_FOUND: u16 = 998;
    pub const RESPOND_SERVER_ERROR: u16 = 996;
    pub const RESPOND_UNKNOWN_ERROR: u16 = 999;
}

/// Requested serialisation.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum OcsFormat {
    Xml,
    Json,
}

impl OcsFormat {
    /// `?format=` wins; otherwise fall back to `Accept`; otherwise XML, which
    /// is the OCS default.
    pub fn negotiate(query: Option<&str>, headers: &HeaderMap) -> Self {
        if let Some(q) = query {
            for pair in q.split('&') {
                let (k, v) = match pair.split_once('=') {
                    Some(kv) => kv,
                    None => continue,
                };
                if k == "format" {
                    return match v {
                        "json" => OcsFormat::Json,
                        _ => OcsFormat::Xml,
                    };
                }
            }
        }
        match headers.get(header::ACCEPT).and_then(|v| v.to_str().ok()) {
            Some(a) if a.contains("application/json") => OcsFormat::Json,
            _ => OcsFormat::Xml,
        }
    }
}

/// A serialisation-format-agnostic value tree.
///
/// This exists because OCS JSON and OCS XML are not two renderings of the same
/// document — an empty collection is `[]` in JSON and an empty element in XML,
/// booleans are `true` in JSON and `1`/`` in XML, and list items lose their
/// index and become `<element>` in XML. One tree, two writers.
#[derive(Clone, Debug, PartialEq)]
pub enum Val {
    Null,
    Bool(bool),
    Int(i64),
    /// Needed for `quota.relative`, which the reference emits as a real JSON
    /// number with two decimals.
    Float(f64),
    Str(String),
    /// Ordered list. Renders as `[...]` in JSON, as repeated `<element>` in XML.
    List(Vec<Val>),
    /// Ordered map. Insertion order is preserved because several clients are
    /// order-sensitive when parsing XML.
    Map(Vec<(String, Val)>),
}

impl Val {
    pub fn str(s: impl Into<String>) -> Val {
        Val::Str(s.into())
    }

    pub fn map<I, K>(it: I) -> Val
    where
        I: IntoIterator<Item = (K, Val)>,
        K: Into<String>,
    {
        Val::Map(it.into_iter().map(|(k, v)| (k.into(), v)).collect())
    }

    pub fn list<I: IntoIterator<Item = Val>>(it: I) -> Val {
        Val::List(it.into_iter().collect())
    }

    pub fn empty_list() -> Val {
        Val::List(Vec::new())
    }

    pub fn empty_map() -> Val {
        Val::Map(Vec::new())
    }

    pub fn get(&self, key: &str) -> Option<&Val> {
        match self {
            Val::Map(m) => m.iter().find(|(k, _)| k == key).map(|(_, v)| v),
            _ => None,
        }
    }

    /// Convenience for tests: dotted path lookup.
    pub fn path(&self, dotted: &str) -> Option<&Val> {
        let mut cur = self;
        for seg in dotted.split('.') {
            cur = cur.get(seg)?;
        }
        Some(cur)
    }

    pub fn to_json(&self) -> serde_json::Value {
        match self {
            Val::Null => serde_json::Value::Null,
            Val::Bool(b) => serde_json::Value::Bool(*b),
            Val::Int(i) => serde_json::Value::Number((*i).into()),
            Val::Float(f) => serde_json::Number::from_f64(*f)
                .map(serde_json::Value::Number)
                .unwrap_or(serde_json::Value::Null),
            Val::Str(s) => serde_json::Value::String(s.clone()),
            Val::List(v) => serde_json::Value::Array(v.iter().map(Val::to_json).collect()),
            Val::Map(m) => serde_json::Value::Object(
                m.iter()
                    .map(|(k, v)| (k.clone(), v.to_json()))
                    .collect::<serde_json::Map<_, _>>(),
            ),
        }
    }
}

impl From<bool> for Val {
    fn from(v: bool) -> Self {
        Val::Bool(v)
    }
}
impl From<i64> for Val {
    fn from(v: i64) -> Self {
        Val::Int(v)
    }
}
impl From<u64> for Val {
    fn from(v: u64) -> Self {
        Val::Int(v as i64)
    }
}
impl From<u32> for Val {
    fn from(v: u32) -> Self {
        Val::Int(v as i64)
    }
}
impl From<usize> for Val {
    fn from(v: usize) -> Self {
        Val::Int(v as i64)
    }
}
impl From<&str> for Val {
    fn from(v: &str) -> Self {
        Val::Str(v.to_owned())
    }
}
impl From<String> for Val {
    fn from(v: String) -> Self {
        Val::Str(v)
    }
}
impl<T: Into<Val>> From<Option<T>> for Val {
    fn from(v: Option<T>) -> Self {
        match v {
            Some(x) => x.into(),
            None => Val::Null,
        }
    }
}

pub fn xml_escape_text(s: &str, out: &mut String) {
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            // Not strictly required in character data, but Sabre/libxml
            // consumers on the client side are happier with them escaped and
            // it costs nothing.
            '"' => out.push_str("&quot;"),
            '\'' => out.push_str("&apos;"),
            // XML 1.0 forbids most C0 controls outright. Drop rather than emit
            // a document the client's parser will reject wholesale.
            c if (c as u32) < 0x20 && c != '\t' && c != '\n' && c != '\r' => {}
            c => out.push(c),
        }
    }
}

fn open_close(name: &str, body: &str, out: &mut String) {
    // PHP's XMLWriter collapses an element with no content to `<k/>`. That
    // applies to null, to the empty string, to `false` (which casts to ""), and
    // to empty arrays. Emitting `<k></k>` instead is not equivalent for every
    // client-side XML parser, so reproduce the collapse.
    out.push('<');
    out.push_str(name);
    if body.is_empty() {
        out.push_str("/>");
        return;
    }
    out.push('>');
    out.push_str(body);
    out.push_str("</");
    out.push_str(name);
    out.push('>');
}

fn write_val_xml(name: &str, v: &Val, out: &mut String) {
    match v {
        // writeElement($k) with no content.
        Val::Null => open_close(name, "", out),
        // (string) casting a PHP bool gives "1" / "". Not "true"/"false".
        Val::Bool(b) => open_close(name, if *b { "1" } else { "" }, out),
        Val::Int(i) => open_close(name, &i.to_string(), out),
        Val::Float(f) => open_close(name, &format_php_float(*f), out),
        Val::Str(s) => {
            let mut esc = String::with_capacity(s.len());
            xml_escape_text(s, &mut esc);
            open_close(name, &esc, out);
        }
        Val::List(items) => {
            let mut inner = String::new();
            for it in items {
                // Numeric keys become <element>. BaseResponse::toXML:
                //   if (\is_numeric($k)) { $k = 'element'; }
                write_val_xml("element", it, &mut inner);
            }
            open_close(name, &inner, out);
        }
        Val::Map(entries) => {
            let mut inner = String::new();
            for (k, v) in entries {
                write_val_xml(k, v, &mut inner);
            }
            open_close(name, &inner, out);
        }
    }
}

/// PHP renders a float that happens to be integral without a trailing `.0`
/// (`(string)25.0 === "25"`). Match that, because `relative` in the quota block
/// is exactly this case.
fn format_php_float(f: f64) -> String {
    if f.fract() == 0.0 && f.abs() < 1e15 {
        format!("{}", f as i64)
    } else {
        let s = format!("{f}");
        s
    }
}

/// A failed OCS call.
#[derive(Clone, Debug)]
pub struct OcsError {
    pub code: u16,
    pub message: String,
}

impl OcsError {
    pub fn new(code: u16, message: impl Into<String>) -> Self {
        Self { code, message: message.into() }
    }
    pub fn bad_request(m: impl Into<String>) -> Self {
        Self::new(400, m)
    }
    pub fn unauthorized(m: impl Into<String>) -> Self {
        Self::new(401, m)
    }
    pub fn forbidden(m: impl Into<String>) -> Self {
        Self::new(403, m)
    }
    pub fn not_found(m: impl Into<String>) -> Self {
        Self::new(404, m)
    }
    pub fn server_error(m: impl Into<String>) -> Self {
        Self::new(500, m)
    }
}

/// The envelope. Build one of these and return it from a handler.
pub struct Ocs {
    pub version: OcsVersion,
    pub format: OcsFormat,
    pub result: Result<Val, OcsError>,
}

impl Ocs {
    pub fn ok(version: OcsVersion, format: OcsFormat, data: Val) -> Self {
        Self { version, format, result: Ok(data) }
    }

    pub fn err(version: OcsVersion, format: OcsFormat, e: OcsError) -> Self {
        Self { version, format, result: Err(e) }
    }

    /// `(ocs statuscode, status word, message, data)`.
    ///
    /// The internal code is 200 on success for both versions; v1 then *maps*
    /// it to 100 in `getOCSStatus()`. Keeping that distinction matters because
    /// v1's HTTP status and `status` word are derived from different sides of
    /// the mapping.
    fn parts(&self) -> (u16, &'static str, String, Val) {
        let internal = match &self.result {
            Ok(_) => 200u16,
            Err(e) => e.code,
        };
        let (ocs_code, status_word) = match self.version {
            // V1Response::getOCSStatus: 200 -> 100, everything else passes
            // through. status is 'ok' iff the mapped code is exactly 100.
            OcsVersion::V1 => {
                let c = if internal == 200 { 100 } else { internal };
                (c, if c == 100 { "ok" } else { "failure" })
            }
            // V2 does NOT override getOCSStatus, so meta.statuscode is the raw
            // internal code (which is why a v2 404 shows statuscode 404 while a
            // legacy 998 shows statuscode 998 with HTTP 404).
            OcsVersion::V2 => (
                internal,
                if (200..300).contains(&internal) { "ok" } else { "failure" },
            ),
        };
        let message = match &self.result {
            Ok(_) => "OK".to_string(),
            Err(e) => e.message.clone(),
        };
        let data = match &self.result {
            Ok(d) => d.clone(),
            // Errors carry an empty data node, not a missing one: clients
            // dereference `ocs.data` unconditionally. Empty renders as `[]` in
            // JSON and a self-closing `<data/>` in XML, matching PHP.
            Err(_) => Val::empty_list(),
        };
        (ocs_code, status_word, message, data)
    }

    /// The `meta` object, as an ordered key list.
    ///
    /// **v1 always emits five keys.** `totalitems` and `itemsperpage` are
    /// `(string)($this->itemsCount ?? '')`, i.e. present-but-empty-string when
    /// unset. v2 emits three and omits the pagination pair entirely unless it
    /// has values (and emits them as integers, not strings). Clients that
    /// pattern-match the v1 envelope notice the difference.
    fn meta(&self) -> Vec<(&'static str, Val)> {
        let (code, status, message, _) = self.parts();
        let mut m: Vec<(&'static str, Val)> = vec![
            ("status", Val::str(status)),
            ("statuscode", Val::Int(code as i64)),
            ("message", Val::str(message)),
        ];
        if self.version == OcsVersion::V1 {
            m.push(("totalitems", Val::str("")));
            m.push(("itemsperpage", Val::str("")));
        }
        m
    }

    pub fn to_json_value(&self) -> serde_json::Value {
        let (_, _, _, data) = self.parts();
        let meta = self
            .meta()
            .into_iter()
            .map(|(k, v)| (k.to_string(), v.to_json()))
            .collect::<serde_json::Map<_, _>>();
        serde_json::json!({
            "ocs": { "meta": meta, "data": data.to_json() }
        })
    }

    pub fn to_xml_string(&self) -> String {
        let (_, _, _, data) = self.parts();
        let mut out = String::with_capacity(512);
        // PHP: $writer->startDocument() with setIndent(true).
        out.push_str("<?xml version=\"1.0\"?>\n<ocs>\n <meta>\n");
        for (k, v) in self.meta() {
            out.push_str("  ");
            write_val_xml(k, &v, &mut out);
            out.push('\n');
        }
        out.push_str(" </meta>\n ");
        write_val_xml("data", &data, &mut out);
        out.push_str("\n</ocs>\n");
        out
    }

    pub fn http_status(&self) -> StatusCode {
        let internal = match &self.result {
            Ok(_) => 200u16,
            Err(e) => e.code,
        };
        self.version.http_status(internal)
    }
}

impl IntoResponse for Ocs {
    fn into_response(self) -> Response {
        let status = self.http_status();
        let (ct, body) = match self.format {
            OcsFormat::Json => (
                "application/json; charset=utf-8",
                serde_json::to_string(&self.to_json_value())
                    .unwrap_or_else(|_| "{}".to_string()),
            ),
            OcsFormat::Xml => ("application/xml; charset=utf-8", self.to_xml_string()),
        };
        let mut resp = Response::builder()
            .status(status)
            .header(header::CONTENT_TYPE, HeaderValue::from_static(ct))
            .body(Body::from(body))
            .expect("static header values are valid");
        // Mirrors ocs/v*.php, which sets this unconditionally.
        resp.headers_mut().insert(
            header::HeaderName::from_static("access-control-allow-origin"),
            HeaderValue::from_static("*"),
        );
        resp
    }
}

/// Enforce `OCS-APIRequest: true`.
///
/// The reference server treats the presence of this header as the CSRF defence for the
/// entire OCS surface: it is a header a browser cannot set cross-origin
/// without a preflight, so its presence proves the request was not a
/// drive-by form post from another site. Accepting requests without it would
/// expose every state-changing OCS endpoint (share creation, above all) to
/// CSRF.
///
/// Case-insensitive on the value, because clients are inconsistent about
/// `true` vs `True`.
pub fn require_ocs_api_request(headers: &HeaderMap) -> Result<(), OcsError> {
    let ok = headers
        .get("ocs-apirequest")
        .and_then(|v| v.to_str().ok())
        .map(|v| v.eq_ignore_ascii_case("true"))
        .unwrap_or(false);
    if ok {
        Ok(())
    } else {
        // Use the legacy sentinel 997 rather than a bare 401. It is what
        // `ocs/v1.php` passes to `ApiHelper::respond` for an unauthorised call,
        // and it is the *only* code `V1Response::getStatus` promotes to an HTTP
        // 401 — a plain `401` on v1 would come back as HTTP 200 with
        // `statuscode: 401`, which some clients read as a soft failure and
        // retry forever.
        Err(OcsError::new(
            ocs_code::RESPOND_UNAUTHORISED,
            "OCS-APIRequest header is required",
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn v1_success_is_100_and_http_is_200_except_997() {
        assert_eq!(OcsVersion::V1.success_code(), 100);
        assert_eq!(OcsVersion::V1.http_status(200), StatusCode::OK);
        // A failure is still HTTP 200 on v1 ...
        assert_eq!(OcsVersion::V1.http_status(404), StatusCode::OK);
        assert_eq!(OcsVersion::V1.http_status(999), StatusCode::OK);
        // ... except RESPOND_UNAUTHORISED, the one code v1 leaks into HTTP.
        assert_eq!(OcsVersion::V1.http_status(997), StatusCode::UNAUTHORIZED);
    }

    #[test]
    fn v1_meta_always_carries_the_pagination_pair_as_empty_strings() {
        let j = Ocs::ok(OcsVersion::V1, OcsFormat::Json, Val::empty_map()).to_json_value();
        let meta = j["ocs"]["meta"].as_object().unwrap();
        assert_eq!(meta.len(), 5);
        assert_eq!(meta["totalitems"], "");
        assert_eq!(meta["itemsperpage"], "");

        // v2 omits them entirely.
        let j2 = Ocs::ok(OcsVersion::V2, OcsFormat::Json, Val::empty_map()).to_json_value();
        let meta2 = j2["ocs"]["meta"].as_object().unwrap();
        assert_eq!(meta2.len(), 3);
        assert!(!meta2.contains_key("totalitems"));
    }

    #[test]
    fn v1_error_code_passes_through_but_http_stays_200() {
        let e = Ocs::err(OcsVersion::V1, OcsFormat::Json, OcsError::not_found("gone"));
        assert_eq!(e.http_status(), StatusCode::OK);
        let j = e.to_json_value();
        assert_eq!(j["ocs"]["meta"]["statuscode"], 404);
        assert_eq!(j["ocs"]["meta"]["status"], "failure");
    }

    #[test]
    fn v2_success_is_200_and_mirrors_into_http() {
        assert_eq!(OcsVersion::V2.success_code(), 200);
        assert_eq!(OcsVersion::V2.http_status(200), StatusCode::OK);
        assert_eq!(OcsVersion::V2.http_status(404), StatusCode::NOT_FOUND);
        assert_eq!(OcsVersion::V2.http_status(400), StatusCode::BAD_REQUEST);
        // Legacy sentinels.
        assert_eq!(OcsVersion::V2.http_status(997), StatusCode::UNAUTHORIZED);
        assert_eq!(OcsVersion::V2.http_status(998), StatusCode::NOT_FOUND);
        assert_eq!(
            OcsVersion::V2.http_status(996),
            StatusCode::INTERNAL_SERVER_ERROR
        );
        assert_eq!(
            OcsVersion::V2.http_status(999),
            StatusCode::INTERNAL_SERVER_ERROR
        );
        // Out of range.
        assert_eq!(OcsVersion::V2.http_status(100), StatusCode::BAD_REQUEST);
        assert_eq!(OcsVersion::V2.http_status(42), StatusCode::BAD_REQUEST);
    }

    #[test]
    fn xml_shape() {
        let o = Ocs::ok(
            OcsVersion::V2,
            OcsFormat::Xml,
            Val::map([("greeting", Val::str("hi"))]),
        );
        let x = o.to_xml_string();
        assert!(x.starts_with("<?xml version=\"1.0\"?>"));
        assert!(x.contains("<status>ok</status>"));
        assert!(x.contains("<statuscode>200</statuscode>"));
        assert!(x.contains("<message>OK</message>"));
        assert!(x.contains("<data><greeting>hi</greeting></data>"));
    }

    #[test]
    fn xml_lists_become_element_children() {
        let o = Ocs::ok(
            OcsVersion::V2,
            OcsFormat::Xml,
            Val::map([("xs", Val::list([Val::Int(1), Val::Int(2)]))]),
        );
        assert!(o
            .to_xml_string()
            .contains("<xs><element>1</element><element>2</element></xs>"));
    }

    #[test]
    fn xml_bools_are_one_and_self_closing() {
        let o = Ocs::ok(
            OcsVersion::V2,
            OcsFormat::Xml,
            Val::map([("t", Val::Bool(true)), ("f", Val::Bool(false))]),
        );
        let x = o.to_xml_string();
        assert!(x.contains("<t>1</t>"));
        // (string)false === "" and XMLWriter collapses empty content.
        assert!(x.contains("<f/>"));
    }

    #[test]
    fn xml_empty_collections_and_nulls_self_close() {
        let o = Ocs::ok(
            OcsVersion::V2,
            OcsFormat::Xml,
            Val::map([
                ("l", Val::empty_list()),
                ("m", Val::empty_map()),
                ("n", Val::Null),
                ("s", Val::str("")),
            ]),
        );
        let x = o.to_xml_string();
        for frag in ["<l/>", "<m/>", "<n/>", "<s/>"] {
            assert!(x.contains(frag), "missing {frag} in {x}");
        }
        // An error envelope's empty data node.
        let e = Ocs::err(OcsVersion::V2, OcsFormat::Xml, OcsError::bad_request("x"));
        assert!(e.to_xml_string().contains("<data/>"));
    }

    #[test]
    fn json_shape_v1_vs_v2() {
        let v1 = Ocs::ok(OcsVersion::V1, OcsFormat::Json, Val::empty_map()).to_json_value();
        assert_eq!(v1["ocs"]["meta"]["statuscode"], 100);
        assert_eq!(v1["ocs"]["meta"]["status"], "ok");
        let v2 = Ocs::ok(OcsVersion::V2, OcsFormat::Json, Val::empty_map()).to_json_value();
        assert_eq!(v2["ocs"]["meta"]["statuscode"], 200);
    }

    #[test]
    fn error_envelope_has_empty_data_array() {
        let e = Ocs::err(
            OcsVersion::V2,
            OcsFormat::Json,
            OcsError::bad_request("nope"),
        );
        let j = e.to_json_value();
        assert_eq!(j["ocs"]["meta"]["status"], "failure");
        assert_eq!(j["ocs"]["meta"]["statuscode"], 400);
        assert_eq!(j["ocs"]["meta"]["message"], "nope");
        assert!(j["ocs"]["data"].as_array().unwrap().is_empty());
    }

    #[test]
    fn xml_escapes_are_applied() {
        let mut s = String::new();
        xml_escape_text("a<b>&\"c\'", &mut s);
        assert_eq!(s, "a&lt;b&gt;&amp;&quot;c&apos;");
        let mut s = String::new();
        xml_escape_text("ok\u{0007}here", &mut s);
        assert_eq!(s, "okhere", "C0 controls must be dropped, not emitted");
    }

    #[test]
    fn format_negotiation() {
        let h = HeaderMap::new();
        assert_eq!(OcsFormat::negotiate(None, &h), OcsFormat::Xml);
        assert_eq!(
            OcsFormat::negotiate(Some("format=json"), &h),
            OcsFormat::Json
        );
        assert_eq!(
            OcsFormat::negotiate(Some("a=1&format=json&b=2"), &h),
            OcsFormat::Json
        );
        assert_eq!(OcsFormat::negotiate(Some("format=xml"), &h), OcsFormat::Xml);
        let mut h2 = HeaderMap::new();
        h2.insert(header::ACCEPT, HeaderValue::from_static("application/json"));
        assert_eq!(OcsFormat::negotiate(None, &h2), OcsFormat::Json);
        // Explicit query beats Accept.
        assert_eq!(OcsFormat::negotiate(Some("format=xml"), &h2), OcsFormat::Xml);
    }

    #[test]
    fn ocs_api_request_header_is_mandatory() {
        let mut h = HeaderMap::new();
        assert!(require_ocs_api_request(&h).is_err());
        h.insert("OCS-APIRequest", HeaderValue::from_static("false"));
        assert!(require_ocs_api_request(&h).is_err());
        h.insert("OCS-APIRequest", HeaderValue::from_static("true"));
        assert!(require_ocs_api_request(&h).is_ok());
        h.insert("OCS-APIRequest", HeaderValue::from_static("TRUE"));
        assert!(require_ocs_api_request(&h).is_ok());
    }
}
