//! The control channel between `sc-core` and `sc-smb-agent`.
//!
//! One request, one response, one connection, over a Unix socket on a
//! directory both sides mount. Line-delimited JSON, because the whole
//! vocabulary is "apply what you just read, and tell me what happened" and a
//! framed binary format would buy nothing an operator cannot already get from
//! `socat - UNIX-CONNECT:...`.
//!
//! A socket rather than a second watched file: the point of the channel is
//! that the answer comes back. Before it existed, `sc-core` wrote four files
//! and learned nothing — a rejected `testparm`, a share path that does not
//! exist where smbd runs, or an import that produced no passdb entry all
//! looked identical to success from this side, and only turned up as a client
//! failing to connect.
//!
//! Not authenticated, deliberately. The socket lives on a volume shared by
//! exactly the two containers that already exchange NT hashes through
//! `smbpasswd` in the same directory; anything that can open it can already
//! read those. Filesystem permissions are the whole gate.

use std::path::{Path, PathBuf};
use std::time::Duration;

use serde::{Deserialize, Serialize};

/// How long `sc-core` waits for an answer. The agent's work is a `testparm`,
/// a file copy and a `pdbedit` import, so a slow answer means something is
/// stuck rather than something is large.
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(10);

/// Where the socket lives unless `smb.agent_socket` says otherwise. Not under
/// `smb.config_dir`: the agent mounts that read-only (it holds NT hashes and
/// only `sc-core` writes it), and a listener has to create its own socket.
pub const DEFAULT_SOCKET: &str = "/run/sc-smb/agent.sock";

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(tag = "op", rename_all = "snake_case")]
pub enum Request {
    /// Re-read the rendered files and apply them now.
    Apply,
    /// Repeat the last apply's report without doing another one.
    Status,
}

/// What the agent did to smbd on this pass.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SmbdAction {
    /// Nothing changed that smbd had to be told about.
    #[default]
    Unchanged,
    /// Config reread in place. Enough for shares, users and permissions.
    Reloaded,
    /// Process replaced. The only thing that moves the listening sockets:
    /// smbd binds them once at startup and a reload does not revisit them,
    /// so a changed `interfaces` line needs this and not a reload.
    Restarted,
    Started,
    /// The rendered config went away, which is how `smb.enabled = false`
    /// reaches this side.
    Stopped,
    /// Wanted to act and could not. `Report::error` says why.
    Failed,
}

/// One apply, in the agent's own words. Everything here is something
/// `sc-core` could not otherwise know, because it is true of the *agent's*
/// namespace and filesystem rather than of `sc-core`'s.
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Report {
    /// `false` if anything below is a problem an operator has to act on. The
    /// config may still have been promoted: a share whose path is missing
    /// does not stop the other shares from working.
    pub ok: bool,
    /// The `[section]` names smbd is now serving.
    pub shares: Vec<String>,
    /// The expanded scope lines, after detection. `sc-core` renders these
    /// closed and cannot see what they became, which is exactly the pair of
    /// values a "why can nothing connect" question needs.
    pub interfaces: String,
    pub hosts_allow: String,
    pub smbd: SmbdAction,
    /// Share paths named in `smb.conf` that do not exist in the agent's own
    /// filesystem. `testparm` does not check this, so before it was reported
    /// here the symptom was a client being told the network name is invalid
    /// while every file on the `sc-core` side looked right.
    pub missing_paths: Vec<String>,
    /// Accounts listed in `passwd` that the passdb import did not produce an
    /// entry for. They cannot authenticate; every downstream symptom points
    /// at credentials instead of at the import.
    pub missing_passdb: Vec<String>,
    /// Why this apply failed, or a warning when `ok` is false for a reason
    /// that did not stop the promotion.
    pub error: Option<String>,
}

impl Report {
    pub fn failed(error: impl Into<String>) -> Self {
        Self {
            ok: false,
            smbd: SmbdAction::Failed,
            error: Some(error.into()),
            ..Self::default()
        }
    }
}

#[derive(Debug)]
pub enum AgentError {
    /// Nothing is listening. Not a fault by itself: a bare-metal deployment
    /// may run the agent on a poll with no socket, and a deployment with no
    /// SMB sidecar at all is a legitimate configuration.
    NotListening(PathBuf),
    Io(std::io::Error),
    /// The agent answered something this version does not understand.
    Protocol(String),
}

impl std::fmt::Display for AgentError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::NotListening(p) => write!(f, "no SMB agent listening at {}", p.display()),
            Self::Io(e) => write!(f, "SMB agent I/O: {e}"),
            Self::Protocol(m) => write!(f, "SMB agent protocol: {m}"),
        }
    }
}

impl std::error::Error for AgentError {}

/// Send one request and read one answer. Blocking, with a timeout on every
/// stage: this runs inside a settings-screen request on `sc-core`'s side, and
/// an agent that accepts a connection and then stops talking must not hold
/// that request open.
#[cfg(unix)]
pub fn request(socket: &Path, req: &Request, timeout: Duration) -> Result<Report, AgentError> {
    use std::io::{BufRead, BufReader, Write};
    use std::os::unix::net::UnixStream;

    let stream = UnixStream::connect(socket).map_err(|e| match e.kind() {
        std::io::ErrorKind::NotFound | std::io::ErrorKind::ConnectionRefused => {
            AgentError::NotListening(socket.to_path_buf())
        }
        _ => AgentError::Io(e),
    })?;
    stream.set_read_timeout(Some(timeout)).map_err(AgentError::Io)?;
    stream.set_write_timeout(Some(timeout)).map_err(AgentError::Io)?;

    let mut line = serde_json::to_string(req)
        .map_err(|e| AgentError::Protocol(format!("encoding the request: {e}")))?;
    line.push('\n');
    (&stream).write_all(line.as_bytes()).map_err(AgentError::Io)?;
    (&stream).flush().map_err(AgentError::Io)?;

    let mut answer = String::new();
    BufReader::new(&stream)
        .read_line(&mut answer)
        .map_err(AgentError::Io)?;
    if answer.trim().is_empty() {
        return Err(AgentError::Protocol("the agent closed without answering".into()));
    }
    serde_json::from_str(&answer)
        .map_err(|e| AgentError::Protocol(format!("decoding the answer: {e}")))
}

/// Windows has no Unix sockets and no Samba sidecar; this exists so the
/// workspace still type-checks on a development host.
#[cfg(not(unix))]
pub fn request(socket: &Path, _req: &Request, _timeout: Duration) -> Result<Report, AgentError> {
    Err(AgentError::NotListening(socket.to_path_buf()))
}

/// "Apply now", the only call `sc-core` makes in production.
pub fn apply(socket: &Path) -> Result<Report, AgentError> {
    request(socket, &Request::Apply, DEFAULT_TIMEOUT)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_report_round_trips() {
        let r = Report {
            ok: false,
            shares: vec!["Share".into()],
            interfaces: "lo eth0".into(),
            hosts_allow: "10.0.0.0/8".into(),
            smbd: SmbdAction::Restarted,
            missing_paths: vec!["/Tokyo/Share".into()],
            missing_passdb: vec![],
            error: Some("one share path does not exist here".into()),
        };
        let wire = serde_json::to_string(&r).unwrap();
        let back: Report = serde_json::from_str(&wire).unwrap();
        assert_eq!(back.shares, r.shares);
        assert_eq!(back.smbd, SmbdAction::Restarted);
        assert_eq!(back.missing_paths, r.missing_paths);
        assert!(!back.ok);
    }

    /// An older agent answering a newer `sc-core` must not be a hard error:
    /// `#[serde(default)]` is what keeps a missing field from failing the
    /// whole decode.
    #[test]
    fn a_sparse_report_decodes() {
        let back: Report = serde_json::from_str(r#"{"ok":true}"#).unwrap();
        assert!(back.ok);
        assert_eq!(back.smbd, SmbdAction::Unchanged);
        assert!(back.shares.is_empty());
    }

    #[test]
    fn requests_are_tagged_by_op() {
        assert_eq!(serde_json::to_string(&Request::Apply).unwrap(), r#"{"op":"apply"}"#);
        assert_eq!(serde_json::to_string(&Request::Status).unwrap(), r#"{"op":"status"}"#);
    }

    #[test]
    fn a_missing_socket_is_not_listening_rather_than_io() {
        let dir = tempfile::tempdir().unwrap();
        let err = apply(&dir.path().join("nope.sock")).unwrap_err();
        assert!(matches!(err, AgentError::NotListening(_)), "got {err:?}");
    }
}
