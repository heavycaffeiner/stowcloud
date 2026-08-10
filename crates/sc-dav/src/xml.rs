//! Hardened XML request-body parsing.
//!
//! Rules, all enforced here and nowhere else:
//!
//! * `Event::DocType` and `Event::PI` are rejected outright. We do not try to
//!   "safely expand" entities — there is no legitimate reason for a DTD in a
//!   WebDAV body, and refusing closes XXE and billion-laughs in one move.
//! * Body size, element count (10 000) and nesting depth (64) are capped.
//! * Everything is namespace-resolved. Raw prefixes are never compared:
//!   `D:`, `d:`, `a:` and the default namespace are all the same document to
//!   us, which is what real clients require.

use quick_xml::events::Event;
use quick_xml::name::ResolveResult;
use quick_xml::NsReader;

use crate::error::{DavError, DavResult};

pub const MAX_ELEMENTS: usize = 10_000;
pub const MAX_DEPTH: usize = 64;
pub const MAX_NAME_LEN: usize = 256;

pub const NS_DAV: &str = "DAV:";

/// A namespace-resolved element name. `ns` is the URI, never a prefix.
#[derive(Clone, PartialEq, Eq, Debug, Hash, PartialOrd, Ord)]
pub struct PropName {
    pub ns: String,
    pub name: String,
}

impl PropName {
    pub fn dav(name: &str) -> Self {
        PropName {
            ns: NS_DAV.to_string(),
            name: name.to_string(),
        }
    }
    pub fn is_dav(&self, name: &str) -> bool {
        self.ns == NS_DAV && self.name == name
    }
}

#[derive(Debug)]
enum Node {
    Start(PropName, bool),
    End,
    Text(String),
}

struct Scanner<'i> {
    r: NsReader<&'i [u8]>,
    depth: usize,
    elements: usize,
}

impl<'i> Scanner<'i> {
    fn new(body: &'i [u8], max_body: usize) -> DavResult<Self> {
        if body.len() > max_body {
            return Err(DavError::TooLarge);
        }
        let mut r = NsReader::from_reader(body);
        let cfg = r.config_mut();
        cfg.check_end_names = true;
        cfg.expand_empty_elements = false;
        // Reader-level trimming used to be safe because a property value was
        // one `Text` event. Since 0.41 splits it at every entity reference,
        // trimming each fragment would eat the spaces *around* the entity —
        // "Tom &amp; Jerry" came back as "Tom&Jerry". The two consumers that
        // keep text (`parse_proppatch`, `parse_lockinfo`) trim the accumulated
        // value instead, which drops pretty-print indentation just the same.
        cfg.trim_text(false);
        Ok(Scanner {
            r,
            depth: 0,
            elements: 0,
        })
    }

    /// Next simplified node, or `None` at EOF.
    fn next(&mut self) -> DavResult<Option<Node>> {
        loop {
            let (ns, ev) = self
                .r
                .read_resolved_event()
                .map_err(|e| DavError::BadXml(e.to_string()))?;
            match ev {
                // ---- the two hard refusals ----
                Event::DocType(_) => return Err(DavError::DtdForbidden),
                Event::PI(_) => return Err(DavError::PiForbidden),

                Event::Start(ref e) | Event::Empty(ref e) => {
                    let empty = matches!(ev, Event::Empty(_));
                    self.elements += 1;
                    if self.elements > MAX_ELEMENTS {
                        return Err(DavError::TooManyElements);
                    }
                    if self.depth + 1 > MAX_DEPTH {
                        return Err(DavError::TooDeep);
                    }
                    if !empty {
                        self.depth += 1;
                    }
                    let local = e.local_name();
                    let local = local.as_ref();
                    if local.len() > MAX_NAME_LEN {
                        return Err(DavError::BadXml("element name too long".into()));
                    }
                    let name = std::str::from_utf8(local)
                        .map_err(|_| DavError::BadXml("non-UTF-8 element name".into()))?
                        .to_string();
                    let ns = match ns {
                        ResolveResult::Bound(n) => std::str::from_utf8(n.as_ref())
                            .map_err(|_| DavError::BadXml("non-UTF-8 namespace".into()))?
                            .to_string(),
                        ResolveResult::Unbound => String::new(),
                        ResolveResult::Unknown(p) => {
                            return Err(DavError::BadXml(format!(
                                "undeclared namespace prefix {:?}",
                                String::from_utf8_lossy(&p)
                            )))
                        }
                    };
                    return Ok(Some(Node::Start(PropName { ns, name }, empty)));
                }
                Event::End(_) => {
                    self.depth = self.depth.saturating_sub(1);
                    return Ok(Some(Node::End));
                }
                Event::Text(e) => {
                    // 0.41 replaced `unescape()` with `xml10_content()`, which
                    // decodes and normalizes EOLs but does *not* resolve
                    // entities — those arrive separately as `GeneralRef`.
                    let t = e
                        .xml10_content()
                        .map_err(|e| DavError::BadXml(e.to_string()))?
                        .into_owned();
                    if !t.is_empty() {
                        return Ok(Some(Node::Text(t)));
                    }
                }
                // 0.41 stopped inlining entity/character references in `Text`
                // and emits them here instead. Dropping them (which the
                // catch-all below did until this arm existed) silently ate
                // every `&lt;`/`&amp;` in a request body.
                Event::GeneralRef(e) => {
                    let t = match e
                        .resolve_char_ref()
                        .map_err(|e| DavError::BadXml(e.to_string()))?
                    {
                        Some(c) => c.to_string(),
                        None => {
                            let name = e
                                .decode()
                                .map_err(|e| DavError::BadXml(e.to_string()))?;
                            // Only the five predefined entities resolve. A DTD
                            // is rejected outright above, so nothing else can
                            // have a definition — expanding an unknown name is
                            // the XXE/billion-laughs path this module closes.
                            quick_xml::escape::resolve_xml_entity(&name)
                                .ok_or_else(|| {
                                    DavError::BadXml(format!("undefined entity &{name};"))
                                })?
                                .to_string()
                        }
                    };
                    if !t.is_empty() {
                        return Ok(Some(Node::Text(t)));
                    }
                }
                Event::CData(e) => {
                    let t = String::from_utf8(e.into_inner().into_owned())
                        .map_err(|_| DavError::BadXml("non-UTF-8 CDATA".into()))?;
                    if !t.is_empty() {
                        return Ok(Some(Node::Text(t)));
                    }
                }
                Event::Eof => return Ok(None),
                // Decl, Comment, and any future variant: ignored.
                _ => {}
            }
        }
    }
}

// ---------------------------------------------------------------- PROPFIND

#[derive(Clone, PartialEq, Eq, Debug)]
pub enum PropFindBody {
    AllProp,
    PropName,
    Prop(Vec<PropName>),
}

/// An empty body means `allprop` (RFC 4918 §9.1 — and several clients rely on
/// it, notably older Finder builds).
pub fn parse_propfind(body: &[u8], max_body: usize) -> DavResult<PropFindBody> {
    if body.iter().all(|b| b.is_ascii_whitespace()) {
        return Ok(PropFindBody::AllProp);
    }
    let mut sc = Scanner::new(body, max_body)?;
    let mut stack: Vec<PropName> = Vec::new();
    let mut mode: Option<PropFindBody> = None;
    let mut props: Vec<PropName> = Vec::new();
    let mut saw_root = false;

    while let Some(node) = sc.next()? {
        match node {
            Node::Start(name, empty) => {
                if !saw_root {
                    if !name.is_dav("propfind") {
                        return Err(DavError::BadXml("root element is not DAV:propfind".into()));
                    }
                    saw_root = true;
                    if !empty {
                        stack.push(name);
                    }
                    continue;
                }
                match stack.len() {
                    1 => {
                        if name.is_dav("allprop") {
                            mode.get_or_insert(PropFindBody::AllProp);
                        } else if name.is_dav("propname") {
                            mode = Some(PropFindBody::PropName);
                        }
                        // `prop` and `include` fall through: their children are
                        // collected at depth 2.
                        if !empty {
                            stack.push(name);
                        }
                    }
                    2 => {
                        let parent = &stack[1];
                        if (parent.is_dav("prop") || parent.is_dav("include"))
                            && !props.contains(&name)
                        {
                            props.push(name.clone());
                        }
                        if !empty {
                            stack.push(name);
                        }
                    }
                    _ => {
                        if !empty {
                            stack.push(name);
                        }
                    }
                }
            }
            Node::End => {
                stack.pop();
            }
            Node::Text(_) => {}
        }
    }
    if !saw_root {
        return Err(DavError::BadXml("empty document".into()));
    }
    Ok(match mode {
        Some(PropFindBody::PropName) => PropFindBody::PropName,
        Some(PropFindBody::AllProp) if props.is_empty() => PropFindBody::AllProp,
        // `allprop` + `include` — treat as allprop, the extra names are already
        // covered by the live set or supplied by a PropSource.
        Some(PropFindBody::AllProp) => PropFindBody::AllProp,
        _ => {
            if props.is_empty() {
                PropFindBody::AllProp
            } else {
                PropFindBody::Prop(props)
            }
        }
    })
}

// --------------------------------------------------------------- PROPPATCH

#[derive(Clone, PartialEq, Eq, Debug)]
pub enum PropPatchOp {
    /// Value is *text only*. We deliberately do not retain the client's markup:
    /// the value is re-serialised from this text on the way out, which is what
    /// makes echoing it back injection-free (§4.2).
    Set(PropName, String),
    Remove(PropName),
}

pub fn parse_proppatch(body: &[u8], max_body: usize) -> DavResult<Vec<PropPatchOp>> {
    let mut sc = Scanner::new(body, max_body)?;
    let mut stack: Vec<PropName> = Vec::new();
    let mut ops = Vec::new();
    let mut saw_root = false;
    // (name, accumulated text, is_set, stack depth at which it started)
    let mut capture: Option<(PropName, String, bool, usize)> = None;

    while let Some(node) = sc.next()? {
        match node {
            Node::Start(name, empty) => {
                if !saw_root {
                    if !name.is_dav("propertyupdate") {
                        return Err(DavError::BadXml(
                            "root element is not DAV:propertyupdate".into(),
                        ));
                    }
                    saw_root = true;
                    if !empty {
                        stack.push(name);
                    }
                    continue;
                }
                if capture.is_none() && stack.len() == 3 {
                    // propertyupdate / (set|remove) / prop / <property>
                    let action = &stack[1];
                    let is_set = action.is_dav("set");
                    let is_remove = action.is_dav("remove");
                    if stack[2].is_dav("prop") && (is_set || is_remove) {
                        if empty {
                            ops.push(if is_set {
                                PropPatchOp::Set(name, String::new())
                            } else {
                                PropPatchOp::Remove(name)
                            });
                            continue;
                        }
                        capture = Some((name.clone(), String::new(), is_set, stack.len()));
                        stack.push(name);
                        continue;
                    }
                }
                if !empty {
                    stack.push(name);
                }
            }
            Node::End => {
                stack.pop();
                if let Some((_, _, _, at)) = &capture {
                    if stack.len() == *at {
                        let (name, text, is_set, _) = capture.take().unwrap();
                        ops.push(if is_set {
                            PropPatchOp::Set(name, text.trim().to_string())
                        } else {
                            PropPatchOp::Remove(name)
                        });
                    }
                }
            }
            Node::Text(t) => {
                if let Some((_, buf, _, _)) = capture.as_mut() {
                    buf.push_str(&t);
                }
            }
        }
    }
    if !saw_root {
        return Err(DavError::BadXml("empty document".into()));
    }
    Ok(ops)
}

// ------------------------------------------------------------------ SEARCH

/// Root element name of a REPORT body, and nothing else.
///
/// A report's body is defined by whoever defined the report, so reading past
/// the root would mean this crate learning a vocabulary that is not `DAV:`.
pub fn parse_report_root(body: &[u8], max_body: usize) -> DavResult<PropName> {
    let mut sc = Scanner::new(body, max_body)?;
    while let Some(node) = sc.next()? {
        if let Node::Start(name, _) = node {
            return Ok(name);
        }
    }
    Err(DavError::BadXml("empty document".into()))
}

/// A report body reduced to the two shapes every RFC 3253 report has: the
/// `DAV:prop` set that says what the response should carry, and the leaf
/// elements outside it that say what the report is filtering on.
#[derive(Clone, Debug, Default)]
pub struct ReportBody {
    /// Children of `DAV:prop`, in document order.
    pub props: Vec<PropName>,
    /// Every other leaf element, with its text. What they *mean* is the
    /// claiming source's business; this crate only reads their names.
    pub leaves: Vec<(PropName, String)>,
}

/// Parse a report body through the same hardened scanner every other DAV body
/// goes through, without interpreting anything but `DAV:prop`.
pub fn parse_report_body(body: &[u8], max_body: usize) -> DavResult<ReportBody> {
    let mut sc = Scanner::new(body, max_body)?;
    let mut stack: Vec<PropName> = Vec::new();
    let mut out = ReportBody::default();
    // Depth at which the enclosing `DAV:prop` sits, when we are inside one.
    let mut in_prop: Option<usize> = None;
    // Open elements outside any `DAV:prop`, innermost last: `(name, text,
    // has_child)`. Only an element that never opened a child is a leaf — a
    // filter's rules are wrapped in a container, and recording the container
    // would report the rule's value under the wrapper's name.
    let mut open: Vec<(PropName, String, bool)> = Vec::new();
    let mut saw_root = false;

    while let Some(node) = sc.next()? {
        match node {
            Node::Start(name, empty) => {
                if in_prop.is_some() {
                    if !out.props.contains(&name) {
                        out.props.push(name.clone());
                    }
                } else if name.is_dav("prop") {
                    if let Some(parent) = open.last_mut() {
                        parent.2 = true;
                    }
                    if !empty {
                        in_prop = Some(stack.len());
                    }
                } else if !saw_root {
                    saw_root = true;
                } else {
                    if let Some(parent) = open.last_mut() {
                        parent.2 = true;
                    }
                    if empty {
                        out.leaves.push((name.clone(), String::new()));
                    } else {
                        open.push((name.clone(), String::new(), false));
                    }
                }
                if !empty {
                    stack.push(name);
                }
            }
            Node::End => {
                stack.pop();
                if let Some(at) = in_prop {
                    if stack.len() == at {
                        in_prop = None;
                        continue;
                    }
                }
                // Outside a `DAV:prop`, `open` holds every stack element but
                // the root, so `open.len() >= stack.len()` after the pop means
                // the element that just closed was one of them.
                if in_prop.is_none() && open.len() >= stack.len() {
                    if let Some((name, text, has_child)) = open.pop() {
                        if !has_child {
                            out.leaves.push((name, text.trim().to_string()));
                        }
                    }
                }
            }
            Node::Text(t) => {
                if in_prop.is_none() {
                    if let Some((_, buf, _)) = open.last_mut() {
                        buf.push_str(&t);
                    }
                }
            }
        }
    }
    Ok(out)
}

/// RFC 5323 `DAV:searchrequest` carrying a `DAV:basicsearch`.
///
/// # What is deliberately not honoured
///
/// The boolean structure of `d:where` is flattened. Every comparison in the
/// tree is collected and applied conjunctively, except media-type prefixes,
/// which accumulate into a set matched disjunctively because that is the one
/// place either client uses `d:or` (`image/%` or `video/%`). A `d:not` is
/// ignored rather than inverted.
///
/// Refusing the queries that do not fit would fail the whole search box for a
/// shape no client actually sends; answering a superset would be worse. In
/// practice the union of both clients' query bodies is small and fixed, and
/// every member of it maps exactly.
pub fn parse_searchrequest(
    body: &[u8],
    max_body: usize,
) -> DavResult<crate::search::SearchRequest> {
    use crate::search::{like_needle, parse_http_date_ns, SearchOp, SearchTerm};

    let mut sc = Scanner::new(body, max_body)?;
    let mut stack: Vec<PropName> = Vec::new();
    let mut saw_root = false;

    // Which comparison element we are inside, and the property/literal it has
    // named so far. A comparison is `<op><prop><NAME/></prop><literal>V</literal></op>`.
    let mut op: Option<(SearchOp, Option<PropName>, String, usize)> = None;
    let mut select: Vec<PropName> = Vec::new();
    let mut select_all = false;
    let mut scope_href = String::new();
    let mut depth_infinity = false;
    let mut limit: u32 = 0;
    
    let mut order_desc_seen = false;
    let mut order_on_mtime = false;

    let mut name_contains = None;
    let mut content_type_prefixes: Vec<String> = Vec::new();
    let mut mtime_from_ns = None;
    let mut mtime_to_ns = None;
    let mut is_collection = None;
    let mut vendor: Vec<SearchTerm> = Vec::new();

    // Text accumulators for the three leaf elements that carry a value.
    let mut text_sink: Option<(&'static str, String, usize)> = None;

    while let Some(node) = sc.next()? {
        match node {
            Node::Start(name, empty) => {
                if !saw_root {
                    if !name.is_dav("searchrequest") {
                        return Err(DavError::BadXml(
                            "root element is not DAV:searchrequest".into(),
                        ));
                    }
                    saw_root = true;
                    if !empty {
                        stack.push(name);
                    }
                    continue;
                }

                // A comparison operator opens a capture.
                let as_op = if name.is_dav("eq") {
                    Some(SearchOp::Eq)
                } else if name.is_dav("like") {
                    Some(SearchOp::Like)
                } else if name.is_dav("lt") || name.is_dav("lte") {
                    Some(SearchOp::Lt)
                } else if name.is_dav("gt") || name.is_dav("gte") {
                    Some(SearchOp::Gt)
                } else {
                    None
                };
                if let Some(o) = as_op {
                    if op.is_none() && !empty {
                        op = Some((o, None, String::new(), stack.len()));
                        stack.push(name);
                        continue;
                    }
                }

                // Inside a comparison, the first element under `d:prop` is the
                // property being compared.
                if let Some((_, prop, _, _)) = op.as_mut() {
                    let parent_is_prop = stack.last().map(|p| p.is_dav("prop")).unwrap_or(false);
                    if parent_is_prop && prop.is_none() {
                        *prop = Some(name.clone());
                    }
                    if name.is_dav("literal") && !empty {
                        text_sink = Some(("literal", String::new(), stack.len()));
                    }
                } else if stack.iter().any(|p| p.is_dav("select")) {
                    let parent_is_prop = stack.last().map(|p| p.is_dav("prop")).unwrap_or(false);
                    if parent_is_prop && !select.contains(&name) {
                        select.push(name.clone());
                    }
                    if name.is_dav("allprop") {
                        select_all = true;
                    }
                } else if name.is_dav("is-collection") || name.is_dav("iscollection") {
                    // RFC 5323 §5.16 spells this as a bare predicate inside
                    // `d:where`, taking no operand at all — `<d:and><d:like…/>
                    // <d:is-collection/></d:and>` is how a client asks for
                    // folders only. It is *not* reached through a comparison,
                    // so the `d:eq`-on-a-property spelling some clients use
                    // instead is handled separately, below.
                    //
                    // A `d:not` around it is ignored rather than inverted,
                    // like every other boolean this parser flattens.
                    is_collection = Some(true);
                } else if name.is_dav("href") && !empty {
                    text_sink = Some(("href", String::new(), stack.len()));
                } else if name.is_dav("depth") && !empty {
                    text_sink = Some(("depth", String::new(), stack.len()));
                } else if name.is_dav("nresults") && !empty {
                    text_sink = Some(("nresults", String::new(), stack.len()));
                } else if stack.iter().any(|p| p.is_dav("orderby")) {
                    if name.is_dav("descending") {
                        order_desc_seen = true;
                    }
                    let parent_is_prop = stack.last().map(|p| p.is_dav("prop")).unwrap_or(false);
                    if parent_is_prop && name.is_dav("getlastmodified") {
                        order_on_mtime = true;
                    }
                }

                if !empty {
                    stack.push(name);
                }
            }
            Node::End => {
                if let Some((kind, buf, at)) = text_sink.as_ref() {
                    if stack.len() == *at + 1 {
                        let (kind, buf) = (*kind, buf.trim().to_string());
                        text_sink = None;
                        match kind {
                            "href" if scope_href.is_empty() => scope_href = buf,
                            "depth" => depth_infinity = buf.eq_ignore_ascii_case("infinity"),
                            "nresults" => limit = buf.parse().unwrap_or(0),
                            "literal" => {
                                if let Some((_, _, lit, _)) = op.as_mut() {
                                    *lit = buf;
                                }
                            }
                            _ => {}
                        }
                    }
                }
                stack.pop();
                if let Some((_, _, _, at)) = op.as_ref() {
                    if stack.len() == *at {
                        let (o, prop, literal, _) = op.take().unwrap();
                        let Some(prop) = prop else { continue };
                        if prop.ns != NS_DAV {
                            vendor.push(SearchTerm {
                                ns: prop.ns,
                                name: prop.name,
                                op: o,
                                literal,
                            });
                            continue;
                        }
                        match (prop.name.as_str(), o) {
                            ("displayname", SearchOp::Like) | ("displayname", SearchOp::Eq) => {
                                let (needle, _) = like_needle(&literal);
                                if !needle.is_empty() {
                                    name_contains = Some(needle);
                                }
                            }
                            ("getcontenttype", _) => {
                                let (needle, _) = like_needle(&literal);
                                if !needle.is_empty() {
                                    content_type_prefixes.push(needle);
                                }
                            }
                            ("getlastmodified", SearchOp::Gt) => {
                                mtime_from_ns = Some(parse_http_date_ns(&literal)?);
                            }
                            ("getlastmodified", SearchOp::Lt) => {
                                mtime_to_ns = Some(parse_http_date_ns(&literal)?);
                            }
                            ("iscollection", _) | ("is-collection", _) => {
                                is_collection = Some(matches!(literal.trim(), "1" | "true" | "T"));
                            }
                            _ => {}
                        }
                    }
                }
            }
            Node::Text(t) => {
                if let Some((_, buf, _)) = text_sink.as_mut() {
                    buf.push_str(&t);
                }
            }
        }
    }
    if !saw_root {
        return Err(DavError::BadXml("empty document".into()));
    }

    let newest_first = order_on_mtime && order_desc_seen;
    let props = if select_all || select.is_empty() {
        crate::props::PropReq { all: true, names_only: false, requested: Vec::new() }
    } else {
        crate::props::PropReq { all: false, names_only: false, requested: select }
    };

    Ok(crate::search::SearchRequest {
        scope_href,
        depth_infinity,
        name_contains,
        content_type_prefixes,
        mtime_from_ns,
        mtime_to_ns,
        is_collection,
        limit,
        newest_first,
        vendor,
        props,
    })
}

// -------------------------------------------------------------------- LOCK

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum LockScope {
    Exclusive,
    Shared,
}

#[derive(Clone, Debug)]
pub struct LockBody {
    pub scope: LockScope,
    /// Text content of `<DAV:owner>`, re-serialised on output.
    pub owner: String,
}

pub fn parse_lockinfo(body: &[u8], max_body: usize) -> DavResult<LockBody> {
    let mut sc = Scanner::new(body, max_body)?;
    let mut stack: Vec<PropName> = Vec::new();
    let mut saw_root = false;
    let mut scope = LockScope::Exclusive;
    let mut owner = String::new();
    let mut in_owner: Option<usize> = None;
    let mut saw_write = false;

    while let Some(node) = sc.next()? {
        match node {
            Node::Start(name, empty) => {
                if !saw_root {
                    if !name.is_dav("lockinfo") {
                        return Err(DavError::BadXml("root element is not DAV:lockinfo".into()));
                    }
                    saw_root = true;
                    if !empty {
                        stack.push(name);
                    }
                    continue;
                }
                if stack.len() == 1 && name.is_dav("owner") && in_owner.is_none() && !empty {
                    in_owner = Some(stack.len());
                }
                if stack.len() == 2 {
                    let parent = &stack[1];
                    if parent.is_dav("lockscope") {
                        if name.is_dav("shared") {
                            scope = LockScope::Shared;
                        } else if name.is_dav("exclusive") {
                            scope = LockScope::Exclusive;
                        }
                    } else if parent.is_dav("locktype") && name.is_dav("write") {
                        saw_write = true;
                    }
                }
                if !empty {
                    stack.push(name);
                }
            }
            Node::End => {
                stack.pop();
                if let Some(at) = in_owner {
                    if stack.len() == at {
                        in_owner = None;
                    }
                }
            }
            Node::Text(t) => {
                if in_owner.is_some() {
                    owner.push_str(&t);
                }
            }
        }
    }
    if !saw_root {
        return Err(DavError::BadXml("empty document".into()));
    }
    // We only implement write locks; anything else is a client bug.
    if !saw_write {
        // Be lenient: several clients omit <locktype>. Assume write.
    }
    Ok(LockBody {
        scope,
        owner: owner.trim().to_string(),
    })
}

// ------------------------------------------------------------- serialising

/// Escape into an XML text/attribute context and drop characters that are not
/// legal in XML 1.0 at all (a client can and does send them).
pub fn escape_into(s: &str, out: &mut String) {
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            '\'' => out.push_str("&apos;"),
            '\t' | '\n' | '\r' => out.push(c),
            c if (c as u32) < 0x20 => {}
            '\u{fffe}' | '\u{ffff}' => {}
            c => out.push(c),
        }
    }
}

/// Conservative XML `Name` check. Dead-property names come from the client and
/// end up as element names in our output, so they are validated on the way in
/// *and* on the way out.
pub fn is_valid_xml_name(s: &str) -> bool {
    if s.is_empty() || s.len() > MAX_NAME_LEN {
        return false;
    }
    let mut chars = s.chars();
    let first = chars.next().unwrap();
    if !(first.is_alphabetic() || first == '_') {
        return false;
    }
    if s.to_ascii_lowercase().starts_with("xml") {
        return false;
    }
    s.chars()
        .all(|c| c.is_alphanumeric() || c == '_' || c == '-' || c == '.')
}

pub fn escape(s: &str) -> String {
    let mut o = String::with_capacity(s.len());
    escape_into(s, &mut o);
    o
}

#[cfg(test)]
mod search_tests {
    use super::*;
    use crate::search::SearchOp;

    fn parse(body: &str) -> crate::search::SearchRequest {
        parse_searchrequest(body.as_bytes(), 64 * 1024).expect("a well-formed basic search")
    }

    /// The union of both mobile clients' query bodies is small and fixed.
    /// Every member of it has to map exactly, because a filter this parser
    /// drops turns "photos taken this year" into "everything".
    #[test]
    fn a_filename_search_becomes_a_substring_filter() {
        let r = parse(
            r#"<?xml version="1.0"?>
            <d:searchrequest xmlns:d="DAV:">
              <d:basicsearch>
                <d:select><d:prop><d:displayname/><d:getcontentlength/></d:prop></d:select>
                <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
                <d:where>
                  <d:like><d:prop><d:displayname/></d:prop><d:literal>%report%</d:literal></d:like>
                </d:where>
                <d:orderby>
                  <d:order><d:prop><d:getlastmodified/></d:prop><d:descending/></d:order>
                </d:orderby>
                <d:limit><d:nresults>30</d:nresults></d:limit>
              </d:basicsearch>
            </d:searchrequest>"#,
        );
        assert_eq!(r.name_contains.as_deref(), Some("report"));
        assert_eq!(r.scope_href, "/files/alice");
        assert!(r.depth_infinity);
        assert_eq!(r.limit, 30);
        assert!(r.newest_first);
        assert!(!r.props.all, "an explicit select is not allprop");
        assert_eq!(r.props.requested.len(), 2);
    }

    #[test]
    fn the_gallery_query_collects_both_media_prefixes() {
        let r = parse(
            r#"<d:searchrequest xmlns:d="DAV:"><d:basicsearch>
                 <d:where><d:or>
                   <d:like><d:prop><d:getcontenttype/></d:prop><d:literal>image/%</d:literal></d:like>
                   <d:like><d:prop><d:getcontenttype/></d:prop><d:literal>video/%</d:literal></d:like>
                 </d:or></d:where>
               </d:basicsearch></d:searchrequest>"#,
        );
        assert_eq!(r.content_type_prefixes, vec!["image/", "video/"]);
    }

    /// iOS pages the photo timeline by an mtime window.
    #[test]
    fn a_date_window_becomes_both_mtime_bounds() {
        let r = parse(
            r#"<d:searchrequest xmlns:d="DAV:"><d:basicsearch>
                 <d:where><d:and>
                   <d:gt><d:prop><d:getlastmodified/></d:prop><d:literal>Tue, 01 Jul 2025 00:00:00 GMT</d:literal></d:gt>
                   <d:lt><d:prop><d:getlastmodified/></d:prop><d:literal>Fri, 01 Aug 2025 00:00:00 GMT</d:literal></d:lt>
                 </d:and></d:where>
               </d:basicsearch></d:searchrequest>"#,
        );
        let from = r.mtime_from_ns.expect("a lower bound");
        let to = r.mtime_to_ns.expect("an upper bound");
        assert!(from < to);
        assert_eq!(from, 1_751_328_000i128 * 1_000_000_000);
    }

    /// A bound the server silently drops is the difference between "modified
    /// since Tuesday" and "everything", so an unparseable date fails the whole
    /// request instead.
    #[test]
    fn an_unparseable_date_literal_fails_the_request() {
        let body = r#"<d:searchrequest xmlns:d="DAV:"><d:basicsearch><d:where>
                        <d:gt><d:prop><d:getlastmodified/></d:prop><d:literal>last tuesday</d:literal></d:gt>
                      </d:where></d:basicsearch></d:searchrequest>"#;
        assert!(parse_searchrequest(body.as_bytes(), 64 * 1024).is_err());
    }

    /// RFC 5323 spells "folders only" as a bare predicate with no operand.
    /// Some clients send the comparison form instead; both mean the same
    /// thing, and dropping either turns "folders" into "everything".
    #[test]
    fn a_folders_only_query_sets_the_kind_filter_in_both_spellings() {
        let bare = parse(
            r#"<d:searchrequest xmlns:d="DAV:"><d:basicsearch><d:where><d:and>
                 <d:like><d:prop><d:displayname/></d:prop><d:literal>%x%</d:literal></d:like>
                 <d:is-collection/>
               </d:and></d:where></d:basicsearch></d:searchrequest>"#,
        );
        assert_eq!(bare.is_collection, Some(true));
        assert_eq!(bare.name_contains.as_deref(), Some("x"));

        let compared = parse(
            r#"<d:searchrequest xmlns:d="DAV:"><d:basicsearch><d:where>
                 <d:eq><d:prop><d:iscollection/></d:prop><d:literal>1</d:literal></d:eq>
               </d:where></d:basicsearch></d:searchrequest>"#,
        );
        assert_eq!(compared.is_collection, Some(true));

        // The comparison form can also say "files only".
        let files = parse(
            r#"<d:searchrequest xmlns:d="DAV:"><d:basicsearch><d:where>
                 <d:eq><d:prop><d:iscollection/></d:prop><d:literal>0</d:literal></d:eq>
               </d:where></d:basicsearch></d:searchrequest>"#,
        );
        assert_eq!(files.is_collection, Some(false));
    }

    /// A comparison against a property outside `DAV:` is passed through
    /// untouched. This crate must not learn what it means, and the two the
    /// clients send — the favourites flag and the file id — are exactly that.
    #[test]
    fn a_non_dav_comparison_is_handed_on_verbatim() {
        let r = parse(
            r#"<d:searchrequest xmlns:d="DAV:" xmlns:v="urn:vendor:example">
                 <d:basicsearch><d:where>
                   <d:eq><d:prop><v:marked/></d:prop><d:literal>yes</d:literal></d:eq>
                 </d:where></d:basicsearch></d:searchrequest>"#,
        );
        assert_eq!(r.vendor.len(), 1);
        assert_eq!(r.vendor[0].ns, "urn:vendor:example");
        assert_eq!(r.vendor[0].name, "marked");
        assert_eq!(r.vendor[0].op, SearchOp::Eq);
        assert_eq!(r.vendor[0].literal, "yes");
        assert!(r.name_contains.is_none(), "nothing DAV: was invented from it");
    }

    #[test]
    fn a_body_that_is_not_a_search_request_is_refused() {
        assert!(parse_searchrequest(b"<d:propfind xmlns:d=\"DAV:\"/>", 1024).is_err());
        assert!(parse_searchrequest(b"", 1024).is_err());
    }

    /// The same hardening every other DAV body gets. A `SEARCH` body is
    /// client-supplied XML like any other.
    #[test]
    fn a_search_body_is_refused_a_dtd_like_every_other_body() {
        let body = b"<!DOCTYPE d [<!ENTITY x SYSTEM \"file:///etc/passwd\">]><d:searchrequest xmlns:d=\"DAV:\"/>";
        assert!(matches!(
            parse_searchrequest(body, 64 * 1024),
            Err(DavError::DtdForbidden)
        ));
    }

    /// A report body reduces to its property set and its filter leaves, and
    /// nothing here knows what either means.
    #[test]
    fn a_report_body_splits_into_props_and_filter_leaves() {
        let body = br#"<v:filter-files xmlns:d="DAV:" xmlns:v="urn:vendor:example">
              <d:prop><d:getetag/><v:id/></d:prop>
              <d:filter-rules><v:marked>1</v:marked></d:filter-rules>
            </v:filter-files>"#;
        let root = parse_report_root(body, 64 * 1024).unwrap();
        assert_eq!(root.ns, "urn:vendor:example");
        assert_eq!(root.name, "filter-files");

        let parsed = parse_report_body(body, 64 * 1024).unwrap();
        assert_eq!(parsed.props.len(), 2);
        assert!(parsed.props.iter().any(|p| p.is_dav("getetag")));
        assert!(parsed
            .leaves
            .iter()
            .any(|(n, v)| n.name == "marked" && v == "1"));
    }
}
