//! The Unix socket `sc-core` pushes to.
//!
//! One request, one answer, one connection, handled serially: an apply takes
//! the agent's own lock anyway, so accepting concurrently would only queue in
//! a different place.
//!
//! This is the half of the channel that makes a change immediate. Without it
//! `sc-core` writes four files and waits for a poll to notice, and learns
//! nothing about what happened when it does.

use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::Path;
use std::sync::Arc;

use sc_smb::agent::{Report, Request};

use crate::sync::Agent;

/// Bind and serve until the process ends.
///
/// The socket is chowned to whoever owns the rendered config directory,
/// which is `sc-core` by construction: it is the process that writes there.
/// That keeps the channel at mode 0660 between exactly the two identities
/// that already exchange NT hashes through the same volume, instead of a
/// world-writable socket.
pub fn serve(socket: &Path, agent: Arc<Agent>, config_dir: &Path) -> std::io::Result<()> {
    if let Some(parent) = socket.parent() {
        std::fs::create_dir_all(parent)?;
    }
    // A socket file left behind by a killed agent would make `bind` fail with
    // EADDRINUSE forever. Removing it is safe: nothing else owns this path,
    // and a live agent holding it means this process should not have started.
    if socket.exists() {
        std::fs::remove_file(socket)?;
    }
    let listener = UnixListener::bind(socket)?;
    set_socket_owner(socket, config_dir);

    tracing::info!(socket = %socket.display(), "listening for apply requests from sc-core");
    for stream in listener.incoming() {
        match stream {
            Ok(s) => handle(s, &agent),
            Err(e) => tracing::warn!(error = %e, "accept failed"),
        }
    }
    Ok(())
}

fn handle(stream: UnixStream, agent: &Agent) {
    // A client that connects and then says nothing must not hold the loop.
    let _ = stream.set_read_timeout(Some(std::time::Duration::from_secs(5)));
    let _ = stream.set_write_timeout(Some(std::time::Duration::from_secs(5)));

    let mut line = String::new();
    if let Err(e) = BufReader::new(&stream).read_line(&mut line) {
        tracing::warn!(error = %e, "reading a request");
        return;
    }
    let report = match serde_json::from_str::<Request>(line.trim()) {
        Ok(Request::Apply) => agent.apply(),
        Ok(Request::Status) => agent.last(),
        Err(e) => Report::failed(format!("unintelligible request: {e}")),
    };
    if !report.ok {
        if let Some(err) = &report.error {
            tracing::warn!(error = %err, "apply finished with something to report");
        }
    }
    let mut body = match serde_json::to_string(&report) {
        Ok(b) => b,
        Err(e) => format!(r#"{{"ok":false,"error":"encoding the report: {e}"}}"#),
    };
    body.push('\n');
    let _ = (&stream).write_all(body.as_bytes());
    let _ = (&stream).flush();
}

/// Best effort by construction: a socket nobody can reach costs the push and
/// leaves the agent's own poll doing the work, which is the behaviour before
/// this channel existed.
fn set_socket_owner(socket: &Path, config_dir: &Path) {
    use std::os::unix::fs::{MetadataExt, PermissionsExt};

    let owner = std::fs::metadata(config_dir).map(|m| (m.uid(), m.gid()));
    match owner {
        Ok((uid, gid)) if uid != 0 => {
            let c = std::ffi::CString::new(socket.as_os_str().as_encoded_bytes()).unwrap_or_default();
            // SAFETY: a NUL-terminated path this process just created.
            let rc = unsafe { libc::chown(c.as_ptr(), uid, gid) };
            if rc != 0 {
                tracing::warn!(
                    error = %std::io::Error::last_os_error(),
                    "could not hand the control socket to the sc-core account; it may not be able to connect"
                );
            }
            let _ = std::fs::set_permissions(socket, std::fs::Permissions::from_mode(0o660));
        }
        _ => {
            // The config directory is root-owned (a bare-metal install), so
            // there is no unprivileged identity to hand it to and the
            // directory's own mode is the gate.
            let _ = std::fs::set_permissions(socket, std::fs::Permissions::from_mode(0o660));
        }
    }
}
