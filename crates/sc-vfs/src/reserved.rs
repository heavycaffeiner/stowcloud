//! Names reserved for our own control files. A single shared list so the
//! tree walker, directory listing, and SMB `veto files` config all agree.

pub const RESERVED_PREFIXES: &[&str] = &[".sctrash", ".scpart-", ".scmeta", ".scindex"];

pub fn is_reserved_name(name: &str) -> bool {
    RESERVED_PREFIXES.iter().any(|p| name.starts_with(p))
}
