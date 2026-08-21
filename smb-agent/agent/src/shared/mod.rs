//! The two things the agent shares with the server that renders for it.
//!
//! Vendored here at the cutover rather than imported from a crate, because the
//! server is a Go program now and there is no crate left to import from.
//!
//! Both halves have to agree on these or the sidecar answers a request the
//! server did not make and admits a network the server did not classify. The
//! Go side's own copies are `internal/smb/bind.go` and the control protocol in
//! this directory's README; a change to either has to move both, and the
//! agent's tests are what catch a drift on this side.

// Some of what these two carry is the server's half of the contract rather
// than this program's: the request builder, the client-side timeout, the
// address classifier the server uses to check a pinned interface. It is kept
// rather than trimmed, because the two halves have to describe one protocol
// and one classification, and deleting the side this binary does not call is
// how the two drift apart without either noticing.
#![allow(dead_code)]

/// The control protocol: what the server asks for and what the agent reports.
pub mod agent;

/// What counts as an internal network, and the blocks a private address
/// encloses to. The server renders the closed case and this is what expands
/// it, because only this process can see the host's own devices.
pub mod bind;
