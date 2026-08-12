//! The advisory lock every process that owns a data directory holds for its
//! lifetime.
//!
//! It exists for the cutover. The state in a data directory spans several
//! independent WAL databases and SQLite offers no snapshot across them, so a
//! migration that read them while this server was writing would produce a
//! destination holding a user from one instant and their grants from another.
//! A `busy` probe cannot answer the question either: an idle server holds no
//! lock on any of the files and is still very much running.
//!
//! It is not a PID file. Nothing is written into it and nothing reads it. The
//! only state is the kernel lock on the open descriptor, which the kernel drops
//! however the process exits, so a file left behind by a crash says nothing and
//! blocks nothing.

use std::fs::{File, OpenOptions};
use std::path::Path;

/// The name both this server and the Go build's importer open.
pub const LOCK_FILE: &str = ".stowcloud-instance.lock";

/// A held lock. Dropping it closes the descriptor, which releases it.
pub struct InstanceLock {
    _file: File,
}

/// Takes the directory's lock without waiting.
///
/// Without waiting, because what it excludes is another running process rather
/// than a moment of contention, and a server that blocked here would look like
/// a server that hung.
pub fn acquire(data_dir: &Path) -> anyhow::Result<InstanceLock> {
    let path = data_dir.join(LOCK_FILE);
    let file = OpenOptions::new()
        .create(true)
        .read(true)
        .write(true)
        .truncate(false)
        .open(&path)
        .map_err(|e| anyhow::anyhow!("opening {}: {e}", path.display()))?;
    lock_exclusive(&file).map_err(|e| {
        anyhow::anyhow!(
            "data directory in use: {}: {e}. Another server or a migration holds it",
            data_dir.display()
        )
    })?;
    Ok(InstanceLock { _file: file })
}

#[cfg(unix)]
fn lock_exclusive(file: &File) -> std::io::Result<()> {
    use std::os::fd::AsRawFd;

    // The lock belongs to the open file description rather than to the process,
    // so a second acquire in this process contends with the first exactly as
    // another process would.
    let rc = unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) };
    if rc != 0 {
        return Err(std::io::Error::last_os_error());
    }
    Ok(())
}

/// The development host's copy. The shipping target is Linux and the branch
/// above is what runs there; this one exists so a native build still compiles.
#[cfg(not(unix))]
fn lock_exclusive(_file: &File) -> std::io::Result<()> {
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(unix)]
    #[test]
    fn a_second_acquire_is_refused_while_the_first_is_held() {
        let dir = tempfile::tempdir().unwrap();
        let held = acquire(dir.path()).unwrap();
        assert!(acquire(dir.path()).is_err());
        drop(held);
        assert!(acquire(dir.path()).is_ok());
    }

    #[test]
    fn the_lock_file_is_created_in_the_data_directory() {
        let dir = tempfile::tempdir().unwrap();
        let _held = acquire(dir.path()).unwrap();
        assert!(dir.path().join(LOCK_FILE).exists());
    }
}
