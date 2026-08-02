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
