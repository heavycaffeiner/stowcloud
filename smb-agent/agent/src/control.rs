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

use crate::shared::agent::{Report, Request};

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
        Ok(Request::Apply) => {
            let r = agent.apply();
            crate::sync::log_report(&r, "sc-core");
            r
        }
        // Repeating the last answer is not an event.
        Ok(Request::Status) => agent.last(),
        Err(e) => {
            let r = Report::failed(format!("unintelligible request: {e}"));
            crate::sync::log_report(&r, "sc-core");
            r
        }
    };
    let mut body = match serde_json::to_string(&report) {
        Ok(b) => b,
        Err(e) => format!(r#"{{"ok":false,"error":"encoding the report: {e}"}}"#),
    };
    body.push('\n');
    let _ = (&stream).write_all(body.as_bytes());
    let _ = (&stream).flush();
}

/// Make the socket reachable by `sc-core` and by nothing else that can be
/// helped.
///
/// The identity to hand it to is whoever owns the rendered config directory:
/// that is the process which writes there, which is `sc-core` by
/// construction. On bare metal both sides are root and there is nothing to
/// do.
///
/// The container case cannot chown at all. `cap_drop: ALL` leaves the sidecar
/// without `CAP_CHOWN`, so root there may not give a file away, and a socket
/// left at 0660 root:root is one the `sc` container (uid 1000) gets EACCES
/// on. Adding the capability back to a container that parses SMB off the wire
/// buys less than it costs, so the fallback is 0666 — which is not the wide
/// grant it looks like, because the socket lives on a Docker volume under
/// `/var/lib/docker/volumes`. Only the containers that mount it and host root
/// can reach the path at all, and the vocabulary behind it is "apply" and
/// "status".
fn set_socket_owner(socket: &Path, config_dir: &Path) {
    use std::os::unix::fs::{MetadataExt, PermissionsExt};

    let mode = match std::fs::metadata(config_dir).map(|m| (m.uid(), m.gid())) {
        Ok((uid, gid)) if uid != unsafe { libc::geteuid() } => {
            let c = std::ffi::CString::new(socket.as_os_str().as_encoded_bytes()).unwrap_or_default();
            // SAFETY: a NUL-terminated path this process just created.
            if unsafe { libc::chown(c.as_ptr(), uid, gid) } == 0 {
                0o660
            } else {
                tracing::info!(
                    uid,
                    error = %std::io::Error::last_os_error(),
                    "cannot hand the control socket to the sc-core account (no CAP_CHOWN), \
                     so it is opened to anything that can already reach this directory"
                );
                0o666
            }
        }
        // Same identity on both ends, or the directory is unreadable. Either
        // way there is nobody to hand it to.
        _ => 0o660,
    };
    if let Err(e) = std::fs::set_permissions(socket, std::fs::Permissions::from_mode(mode)) {
        // A socket nobody can reach costs the push and leaves the poll doing
        // the work, which is what this deployment had before the channel
        // existed. `sc-core` reports it as unreachable rather than silently
        // continuing.
        tracing::warn!(error = %e, "could not set the control socket's mode");
    }
}
