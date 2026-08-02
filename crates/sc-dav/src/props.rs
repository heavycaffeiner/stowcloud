//! Property emission.
//!
//! The core live-property set lives here. Anything beyond RFC 4918 / RFC 4331
//! is *not* this crate's business: a decorator registers a [`PropSource`] with
//! [`crate::DavService::add_prop_source`] and gets called for every entry, with
//! its own namespaces declared on the multistatus root.

use std::sync::Arc;

use sc_vfs::{SafePath, ShareId, UserId};

use crate::backend::{DavProp, Entry, Quota};
use crate::xml::{escape_into, PropName, NS_DAV};

/// Which properties the client asked for.
#[derive(Clone, Debug)]
pub struct PropReq {
    pub all: bool,
    pub names_only: bool,
    pub requested: Vec<PropName>,
}

impl PropReq {
    pub fn wants(&self, ns: &str, name: &str) -> bool {
        if self.all || self.names_only {
            return true;
        }
        self.requested
            .iter()
            .any(|p| p.ns == ns && p.name == name)
    }
    pub fn wants_dav(&self, name: &str) -> bool {
        self.wants(NS_DAV, name)
    }
    /// True when the client enumerated names explicitly, so unknown ones must
    /// be reported as 404 rather than silently dropped.
    pub fn is_explicit(&self) -> bool {
        !self.all && !self.names_only
    }
}

/// Everything a [`PropSource`] may need that is not on the [`Entry`].
pub struct PropCtx {
    pub user: UserId,
    pub share: ShareId,
    pub path: SafePath,
    pub is_root: bool,
}

/// Accumulates one `<d:response>`'s worth of properties.
///
/// Sources call [`PropWriter::text`] / [`PropWriter::raw`] / [`PropWriter::empty`]
/// with their own prefix (the one they returned from
/// [`PropSource::namespaces`]); the prefix has already been declared on the
/// multistatus root.
pub struct PropWriter {
    ok: String,
    missing: Vec<PropName>,
    emitted: Vec<PropName>,
    names_only: bool,
    /// prefix -> namespace URI, so the writer can record what was emitted in
    /// namespace terms even though sources write with their own prefix.
    prefix_ns: std::collections::HashMap<String, String>,
}

impl PropWriter {
    pub fn new(names_only: bool, prefix_ns: std::collections::HashMap<String, String>) -> Self {
        PropWriter {
            ok: String::new(),
            missing: Vec::new(),
            emitted: Vec::new(),
            names_only,
            prefix_ns,
        }
    }

    fn mark(&mut self, prefix: &str, name: &str) {
        let ns = self
            .prefix_ns
            .get(prefix)
            .cloned()
            .unwrap_or_else(|| if prefix == "d" { NS_DAV.into() } else { String::new() });
        self.emitted.push(PropName {
            ns,
            name: name.to_string(),
        });
    }

    /// Element with escaped text content.
    pub fn text(&mut self, prefix: &str, name: &str, value: &str) {
        if self.names_only {
            return self.empty(prefix, name);
        }
        self.mark(prefix, name);
        self.ok.push('<');
        self.ok.push_str(prefix);
        self.ok.push(':');
        self.ok.push_str(name);
        self.ok.push('>');
        escape_into(value, &mut self.ok);
        self.ok.push_str("</");
        self.ok.push_str(prefix);
        self.ok.push(':');
        self.ok.push_str(name);
        self.ok.push('>');
    }

    /// Element whose content is already well-formed XML we generated ourselves.
    /// Never pass client input here.
    pub fn raw(&mut self, prefix: &str, name: &str, inner_xml: &str) {
        if self.names_only {
            return self.empty(prefix, name);
        }
        self.mark(prefix, name);
        self.ok.push('<');
        self.ok.push_str(prefix);
        self.ok.push(':');
        self.ok.push_str(name);
        self.ok.push('>');
        self.ok.push_str(inner_xml);
        self.ok.push_str("</");
        self.ok.push_str(prefix);
        self.ok.push(':');
        self.ok.push_str(name);
        self.ok.push('>');
    }

    pub fn empty(&mut self, prefix: &str, name: &str) {
        self.mark(prefix, name);
        self.ok.push('<');
        self.ok.push_str(prefix);
        self.ok.push(':');
        self.ok.push_str(name);
        self.ok.push_str("/>");
    }

    /// Property the client asked for that we do not have. Emitted in a second
    /// `propstat` with status 404.
    pub fn not_found(&mut self, ns: &str, name: &str) {
        self.missing.push(PropName {
            ns: ns.to_string(),
            name: name.to_string(),
        });
    }

    pub(crate) fn finish(self) -> (String, Vec<PropName>, Vec<PropName>) {
        (self.ok, self.missing, self.emitted)
    }
}

/// Decorator hook. Implementors add properties in their own namespace without
/// this crate ever learning their vocabulary.
pub trait PropSource: Send + Sync {
    /// `(prefix, namespace-uri)` pairs to declare on `<d:multistatus>`.
    fn namespaces(&self) -> &[(&'static str, &'static str)];
    fn emit(&self, e: &Entry, ctx: &PropCtx, req: &PropReq, out: &mut PropWriter);
}

pub type PropSourceRef = Arc<dyn PropSource>;

/// The live properties every PROPFIND answers.
pub(crate) const LIVE_PROPS: &[&str] = &[
    "resourcetype",
    "getetag",
    "getcontentlength",
    "getlastmodified",
    "getcontenttype",
    "creationdate",
    "displayname",
    "supportedlock",
    "lockdiscovery",
    "quota-available-bytes",
    "quota-used-bytes",
];

pub(crate) fn rfc1123(mtime_ns: i128) -> String {
    let secs = (mtime_ns / 1_000_000_000) as i64;
    let t = if secs < 0 {
        std::time::UNIX_EPOCH - std::time::Duration::from_secs(secs.unsigned_abs())
    } else {
        std::time::UNIX_EPOCH + std::time::Duration::from_secs(secs as u64)
    };
    httpdate::fmt_http_date(t)
}

/// ISO 8601 / RFC 3339 in UTC, which is what `creationdate` wants.
pub(crate) fn iso8601(ns: i128) -> String {
    let secs = (ns / 1_000_000_000) as i64;
    let days = secs.div_euclid(86_400);
    let rem = secs.rem_euclid(86_400);
    let (y, m, d) = civil_from_days(days);
    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}Z",
        y,
        m,
        d,
        rem / 3600,
        (rem % 3600) / 60,
        rem % 60
    )
}

/// Howard Hinnant's `civil_from_days`.
fn civil_from_days(z: i64) -> (i64, u32, u32) {
    let z = z + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = (z - era * 146_097) as u64;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe as i64 + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    (if m <= 2 { y + 1 } else { y }, m, d)
}

pub(crate) fn guess_content_type(name: &str) -> String {
    mime_guess::from_path(name)
        .first_raw()
        .unwrap_or("application/octet-stream")
        .to_string()
}

/// Emit the RFC 4918 / RFC 4331 live set for one entry.
#[allow(clippy::too_many_arguments)]
pub(crate) fn emit_live(
    e: &Entry,
    req: &PropReq,
    quota: Option<&Quota>,
    lockdiscovery_xml: &str,
    dead: &[DavProp],
    out: &mut PropWriter,
) {
    if req.wants_dav("resourcetype") {
        if e.is_dir() {
            out.raw("d", "resourcetype", "<d:collection/>");
        } else {
            out.empty("d", "resourcetype");
        }
    }
    if req.wants_dav("displayname") {
        out.text("d", "displayname", &e.name);
    }
    if req.wants_dav("getetag") {
        // MUST be quoted. Several clients refuse to sync without the quotes.
        out.text("d", "getetag", &format!("\"{}\"", e.etag));
    }
    if req.wants_dav("getlastmodified") {
        out.text("d", "getlastmodified", &rfc1123(e.mtime_ns));
    }
    if req.wants_dav("creationdate") {
        match e.btime_ns {
            Some(b) => out.text("d", "creationdate", &iso8601(b)),
            None if req.is_explicit() => out.not_found(NS_DAV, "creationdate"),
            None => {}
        }
    }
    if !e.is_dir() {
        if req.wants_dav("getcontentlength") {
            out.text("d", "getcontentlength", &e.size.to_string());
        }
        if req.wants_dav("getcontenttype") {
            out.text("d", "getcontenttype", &guess_content_type(&e.name));
        }
    } else if req.is_explicit() {
        if req.wants_dav("getcontentlength") {
            out.not_found(NS_DAV, "getcontentlength");
        }
        if req.wants_dav("getcontenttype") {
            out.not_found(NS_DAV, "getcontenttype");
        }
    }
    if req.wants_dav("supportedlock") {
        out.raw(
            "d",
            "supportedlock",
            "<d:lockentry><d:lockscope><d:exclusive/></d:lockscope><d:locktype><d:write/></d:locktype></d:lockentry>\
             <d:lockentry><d:lockscope><d:shared/></d:lockscope><d:locktype><d:write/></d:locktype></d:lockentry>",
        );
    }
    if req.wants_dav("lockdiscovery") {
        out.raw("d", "lockdiscovery", lockdiscovery_xml);
    }

    // RFC 4331. Without these Finder reports 0 bytes free and refuses copies.
    match quota {
        Some(q) => {
            if req.wants_dav("quota-used-bytes") {
                out.text("d", "quota-used-bytes", &q.used.to_string());
            }
            if req.wants_dav("quota-available-bytes") {
                match q.available {
                    Some(a) => out.text("d", "quota-available-bytes", &a.to_string()),
                    None if req.is_explicit() => {
                        out.not_found(NS_DAV, "quota-available-bytes")
                    }
                    None => {}
                }
            }
        }
        None if req.is_explicit() => {
            if req.wants_dav("quota-used-bytes") {
                out.not_found(NS_DAV, "quota-used-bytes");
            }
            if req.wants_dav("quota-available-bytes") {
                out.not_found(NS_DAV, "quota-available-bytes");
            }
        }
        None => {}
    }

    // Dead properties. `allprop` deliberately does not dump every dead property
    // (RFC 4918 §9.1 permits this); they are returned when named, or as bare
    // names under `propname`.
    for p in dead {
        if req.names_only || (req.is_explicit() && req.wants(&p.ns, &p.name)) {
            emit_dead(p, out, req.names_only);
        }
    }

    // Anything explicitly requested that nobody produced becomes a 404;
    // propfind.rs does that after the PropSources have had their turn.
}

/// Dead properties carry their namespace inline: they can be in any namespace
/// at all and we will not have declared a prefix on the root for it.
fn emit_dead(p: &DavProp, out: &mut PropWriter, names_only: bool) {
    if !crate::xml::is_valid_xml_name(&p.name) {
        // Stored by an older/looser version, or a hostile store. Refuse rather
        // than emit something that would break the document.
        return;
    }
    let mut s = String::new();
    s.push_str("<x:");
    s.push_str(&p.name);
    s.push_str(" xmlns:x=\"");
    escape_into(&p.ns, &mut s);
    s.push('"');
    if names_only {
        s.push_str("/>");
    } else {
        s.push('>');
        // Re-serialised from stored text — never the client's original markup.
        escape_into(&p.value, &mut s);
        s.push_str("</x:");
        s.push_str(&p.name);
        s.push('>');
    }
    out.push_prebuilt(&s, &p.ns, &p.name);
}

impl PropWriter {
    pub(crate) fn push_prebuilt(&mut self, xml: &str, ns: &str, name: &str) {
        self.ok.push_str(xml);
        self.emitted.push(PropName {
            ns: ns.to_string(),
            name: name.to_string(),
        });
    }
}
