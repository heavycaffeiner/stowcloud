//! RFC 4918 §10.4 `If` header — recursive-descent parser and evaluator.
//!
//! ```text
//! If           = 1*( No-tag-list | Tagged-list )
//! No-tag-list  = List
//! Tagged-list  = Resource-Tag 1*List
//! List         = "(" 1*Condition ")"
//! Condition    = ["Not"] ( State-token | "[" entity-tag "]" )
//! State-token  = Coded-URL
//! Coded-URL    = "<" absolute-URI ">"
//! Resource-Tag = "<" Simple-ref ">"
//! ```
//!
//! The grammar is only locally ambiguous: at the top level a `<` starts a
//! Resource-Tag, inside parentheses it starts a State-token. That single fact
//! is what the parser is built around.
//!
//! Parse failure is a 400, an unsatisfied header is a 412, and a write to a
//! locked resource without the matching token is a 423 — those three outcomes
//! are decided by the caller from [`IfHeader::evaluate`] plus
//! [`IfHeader::tokens`].

use crate::error::{DavError, DavResult};

#[derive(Clone, PartialEq, Eq, Debug)]
pub enum CondKind {
    StateToken(String),
    /// Entity tag, *without* surrounding quotes; `weak` records `W/`.
    ETag { value: String, weak: bool },
}

#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Condition {
    pub not: bool,
    pub kind: CondKind,
}

/// One parenthesised list, together with the resource it was tagged for
/// (`None` for a No-tag-list, meaning "the request URI").
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct IfList {
    pub tag: Option<String>,
    pub conditions: Vec<Condition>,
}

#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct IfHeader {
    pub lists: Vec<IfList>,
}

/// What the server knows about one resource, for evaluating conditions.
#[derive(Clone, Debug, Default)]
pub struct ResourceState {
    /// Active lock tokens on the resource, in `urn:uuid:…` form.
    pub tokens: Vec<String>,
    /// Current entity tag, unquoted.
    pub etag: Option<String>,
    /// False when the resource does not exist; no condition can match then
    /// except a negated one.
    pub exists: bool,
}

struct Cursor<'a> {
    b: &'a [u8],
    i: usize,
}

impl<'a> Cursor<'a> {
    fn peek(&self) -> Option<u8> {
        self.b.get(self.i).copied()
    }
    fn bump(&mut self) -> Option<u8> {
        let c = self.peek();
        if c.is_some() {
            self.i += 1;
        }
        c
    }
    fn skip_ws(&mut self) {
        while matches!(self.peek(), Some(c) if c == b' ' || c == b'\t' || c == b'\r' || c == b'\n')
        {
            self.i += 1;
        }
    }
    fn eof(&self) -> bool {
        self.i >= self.b.len()
    }
    /// Case-insensitive literal match that also refuses to swallow a longer
    /// token (`Nothing` must not parse as `Not` + `hing`).
    fn eat_keyword(&mut self, kw: &str) -> bool {
        let n = kw.len();
        if self.i + n > self.b.len() {
            return false;
        }
        if !self.b[self.i..self.i + n].eq_ignore_ascii_case(kw.as_bytes()) {
            return false;
        }
        match self.b.get(self.i + n) {
            None => {}
            Some(c) if c.is_ascii_whitespace() || *c == b'<' || *c == b'[' || *c == b'(' => {}
            Some(_) => return false,
        }
        self.i += n;
        true
    }
}

const MAX_LISTS: usize = 256;
const MAX_CONDS: usize = 256;
const MAX_TOKEN_LEN: usize = 2048;

impl IfHeader {
    pub fn parse(s: &str) -> DavResult<IfHeader> {
        let mut c = Cursor {
            b: s.as_bytes(),
            i: 0,
        };
        let mut lists = Vec::new();
        c.skip_ws();
        if c.eof() {
            return Err(DavError::BadRequest("empty If header".into()));
        }
        while !c.eof() {
            if lists.len() > MAX_LISTS {
                return Err(DavError::BadRequest("If header has too many lists".into()));
            }
            match c.peek() {
                Some(b'(') => {
                    let conds = parse_list(&mut c)?;
                    lists.push(IfList {
                        tag: None,
                        conditions: conds,
                    });
                }
                Some(b'<') => {
                    let tag = parse_coded_url(&mut c)?;
                    c.skip_ws();
                    // Tagged-list requires at least one List.
                    if c.peek() != Some(b'(') {
                        return Err(DavError::BadRequest(
                            "resource tag not followed by a list".into(),
                        ));
                    }
                    while c.peek() == Some(b'(') {
                        if lists.len() > MAX_LISTS {
                            return Err(DavError::BadRequest(
                                "If header has too many lists".into(),
                            ));
                        }
                        let conds = parse_list(&mut c)?;
                        lists.push(IfList {
                            tag: Some(tag.clone()),
                            conditions: conds,
                        });
                        c.skip_ws();
                    }
                }
                Some(other) => {
                    return Err(DavError::BadRequest(format!(
                        "unexpected byte {:?} in If header",
                        other as char
                    )))
                }
                None => break,
            }
            c.skip_ws();
        }
        if lists.is_empty() {
            return Err(DavError::BadRequest("If header has no lists".into()));
        }
        Ok(IfHeader { lists })
    }

    /// Every state token mentioned anywhere in the header, negated or not.
    /// The lock layer uses this to decide 423 vs. proceed.
    pub fn tokens(&self) -> Vec<&str> {
        let mut out = Vec::new();
        for l in &self.lists {
            for c in &l.conditions {
                if let CondKind::StateToken(t) = &c.kind {
                    if !c.not {
                        out.push(t.as_str());
                    }
                }
            }
        }
        out
    }

    /// RFC 4918 §10.4.3: lists are OR-ed, conditions inside a list are AND-ed.
    ///
    /// `state_of` is asked for the state of a tagged resource (`Some(url)`) or
    /// of the request URI (`None`).
    pub fn evaluate<F>(&self, mut state_of: F) -> bool
    where
        F: FnMut(Option<&str>) -> ResourceState,
    {
        for list in &self.lists {
            let st = state_of(list.tag.as_deref());
            let mut all = true;
            for cond in &list.conditions {
                let hit = match &cond.kind {
                    CondKind::StateToken(t) => st.tokens.iter().any(|x| x == t),
                    CondKind::ETag { value, weak: _ } => {
                        st.exists && st.etag.as_deref() == Some(value.as_str())
                    }
                };
                if hit == cond.not {
                    all = false;
                    break;
                }
            }
            if all {
                return true;
            }
        }
        false
    }
}

fn parse_list(c: &mut Cursor<'_>) -> DavResult<Vec<Condition>> {
    debug_assert_eq!(c.peek(), Some(b'('));
    c.bump();
    let mut conds = Vec::new();
    loop {
        c.skip_ws();
        match c.peek() {
            Some(b')') => {
                c.bump();
                break;
            }
            None => return Err(DavError::BadRequest("unterminated If list".into())),
            _ => {}
        }
        if conds.len() > MAX_CONDS {
            return Err(DavError::BadRequest("too many conditions".into()));
        }
        let not = c.eat_keyword("Not");
        c.skip_ws();
        let kind = match c.peek() {
            Some(b'<') => CondKind::StateToken(parse_coded_url(c)?),
            Some(b'[') => parse_etag(c)?,
            Some(other) => {
                return Err(DavError::BadRequest(format!(
                    "unexpected byte {:?} in If condition",
                    other as char
                )))
            }
            None => return Err(DavError::BadRequest("truncated If condition".into())),
        };
        conds.push(Condition { not, kind });
    }
    if conds.is_empty() {
        // `List = "(" 1*Condition ")"` — an empty list is a syntax error.
        return Err(DavError::BadRequest("empty If list".into()));
    }
    Ok(conds)
}

fn parse_coded_url(c: &mut Cursor<'_>) -> DavResult<String> {
    debug_assert_eq!(c.peek(), Some(b'<'));
    c.bump();
    let start = c.i;
    loop {
        match c.bump() {
            Some(b'>') => break,
            Some(b'<') => return Err(DavError::BadRequest("nested '<' in If header".into())),
            Some(_) => {
                if c.i - start > MAX_TOKEN_LEN {
                    return Err(DavError::BadRequest("If token too long".into()));
                }
            }
            None => return Err(DavError::BadRequest("unterminated '<' in If header".into())),
        }
    }
    let raw = &c.b[start..c.i - 1];
    if raw.is_empty() {
        return Err(DavError::BadRequest("empty coded-url".into()));
    }
    std::str::from_utf8(raw)
        .map(|s| s.to_string())
        .map_err(|_| DavError::BadRequest("non-UTF-8 coded-url".into()))
}

fn parse_etag(c: &mut Cursor<'_>) -> DavResult<CondKind> {
    debug_assert_eq!(c.peek(), Some(b'['));
    c.bump();
    c.skip_ws();
    let mut weak = false;
    if c.i + 2 <= c.b.len() && c.b[c.i..c.i + 2].eq_ignore_ascii_case(b"W/") {
        weak = true;
        c.i += 2;
    }
    if c.peek() != Some(b'"') {
        return Err(DavError::BadRequest("entity-tag must be quoted".into()));
    }
    c.bump();
    let start = c.i;
    loop {
        match c.bump() {
            Some(b'"') => break,
            Some(_) => {
                if c.i - start > MAX_TOKEN_LEN {
                    return Err(DavError::BadRequest("entity-tag too long".into()));
                }
            }
            None => return Err(DavError::BadRequest("unterminated entity-tag".into())),
        }
    }
    let value = std::str::from_utf8(&c.b[start..c.i - 1])
        .map_err(|_| DavError::BadRequest("non-UTF-8 entity-tag".into()))?
        .to_string();
    c.skip_ws();
    if c.bump() != Some(b']') {
        return Err(DavError::BadRequest("entity-tag missing ']'".into()));
    }
    Ok(CondKind::ETag { value, weak })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn documented_grammar_cases() {
        // RFC 4918 §10.4.6 examples plus what clients actually send.
        let cases = [
            "(<urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2>)",
            "(<urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2> [\"I am an ETag\"])",
            "([\"I am an ETag\"])",
            "(Not <urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2>)",
            "(Not <urn:uuid:a> <urn:uuid:b>)",
            "</resource1> (<urn:uuid:181d4fae-7d8c-11d0-a765-00a0c91e6bf2> [W/\"A weak ETag\"]) ([\"strong\"])",
            "(<urn:uuid:a>) (Not <urn:uuid:b>)",
            "</a/b> (<urn:uuid:x>) </c/d> (<urn:uuid:y>)",
        ];
        for c in cases {
            IfHeader::parse(c).unwrap_or_else(|e| panic!("{c:?} failed: {e}"));
        }
    }

    #[test]
    fn tagged_list_binds_tag_to_each_list() {
        let h = IfHeader::parse("</a> (<urn:uuid:x>) (<urn:uuid:y>)").unwrap();
        assert_eq!(h.lists.len(), 2);
        assert_eq!(h.lists[0].tag.as_deref(), Some("/a"));
        assert_eq!(h.lists[1].tag.as_deref(), Some("/a"));
    }

    #[test]
    fn not_is_case_insensitive_and_not_greedy() {
        let h = IfHeader::parse("(NOT <urn:uuid:x>)").unwrap();
        assert!(h.lists[0].conditions[0].not);
        // "Notmuch" is not a keyword; it is also not a valid condition start.
        assert!(IfHeader::parse("(Notmuch <urn:uuid:x>)").is_err());
    }

    #[test]
    fn malformed_is_rejected() {
        for bad in [
            "",
            "   ",
            "()",
            "(",
            "(<urn:uuid:x>",
            "<urn:uuid:x>",
            "</a>",
            "[\"etag\"]",
            "(<>)",
            "(<urn:uuid:x)",
            "([etag])",
            "([\"etag\")",
            "garbage",
            "(Not)",
        ] {
            assert!(
                IfHeader::parse(bad).is_err(),
                "{bad:?} should not have parsed"
            );
        }
    }

    #[test]
    fn evaluation_or_of_lists_and_of_conditions() {
        let h = IfHeader::parse("(<urn:uuid:x> [\"e1\"]) (<urn:uuid:z>)").unwrap();
        let st = |_: Option<&str>| ResourceState {
            tokens: vec!["urn:uuid:z".into()],
            etag: Some("e9".into()),
            exists: true,
        };
        assert!(h.evaluate(st));

        let st2 = |_: Option<&str>| ResourceState {
            tokens: vec!["urn:uuid:x".into()],
            etag: Some("e9".into()),
            exists: true,
        };
        assert!(!h.evaluate(st2)); // first list fails on etag, second on token
    }

    #[test]
    fn negated_condition_matches_when_absent() {
        let h = IfHeader::parse("(Not <urn:uuid:nope>)").unwrap();
        assert!(h.evaluate(|_| ResourceState {
            tokens: vec![],
            etag: None,
            exists: true
        }));
    }
}
