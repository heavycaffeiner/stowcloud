//! Master key load and generate — the "always ready" property SMB depends
//! on, and the checklist item that keeps the key out of the data directory.
//!
//! Rules, straight from the docs:
//!
//! * Loaded from the file `SC_MASTER_KEY_FILE` points at (or
//!   `<data_dir>/master.key` if unset). If absent, generated and written
//!   with mode `0600` (best-effort outside Unix).
//! * **Never** accepted from an environment variable directly — only a
//!   *path* to a file may come from the environment. `docker inspect`/
//!   `/proc/*/environ` expose env vars to anyone who can inspect the
//!   container, which defeats the whole point of a key file.
//! * A warning (not a refusal) if the key file resolves to somewhere inside
//!   the data directory — backing up the DB and the key together defeats
//!   encryption-at-rest.

use std::path::Path;

use crate::config::Config;

/// The env var that historically tempts people to shortcut key management.
/// Presence alone is a hard error, regardless of value.
const FORBIDDEN_ENV_KEY: &str = "SC_MASTER_KEY";

#[derive(Debug, thiserror::Error)]
pub enum MasterKeyError {
    #[error(
        "{FORBIDDEN_ENV_KEY} is set: the master key must never be provided via an environment \
         variable (it would be visible via `docker inspect` / `/proc/*/environ`). Point \
         SC_MASTER_KEY_FILE at a secret file instead."
    )]
    EnvVarRefused,
    #[error("master key file {path} has {len} bytes, expected 32")]
    BadLength {
        path: std::path::PathBuf,
        len: usize,
    },
    #[error("no master key file at {path} — nothing to rotate; start the server once first to generate one")]
    NotFound { path: std::path::PathBuf },
    #[error("io error at {path}: {source}")]
    Io {
        path: std::path::PathBuf,
        #[source]
        source: std::io::Error,
    },
    #[error("getrandom failed: {0}")]
    Rng(String),
}

impl From<getrandom::Error> for MasterKeyError {
    fn from(e: getrandom::Error) -> Self {
        MasterKeyError::Rng(e.to_string())
    }
}

#[derive(Debug)]
pub struct MasterKeyResult {
    pub key: [u8; 32],
    /// `true` if the key file lives inside `data_dir` — startup diagnostics
    /// prints a warning, but this is not fatal.
    pub inside_data_dir: bool,
    /// `true` if this call generated a fresh key (first run).
    pub generated: bool,
}

/// Load the master key, generating and persisting one if missing. Refuses
/// outright if `SC_MASTER_KEY` (the key material itself) is set in the
/// environment.
pub fn load_or_generate(cfg: &Config) -> Result<MasterKeyResult, MasterKeyError> {
    if std::env::var_os(FORBIDDEN_ENV_KEY).is_some() {
        return Err(MasterKeyError::EnvVarRefused);
    }

    let path = cfg.master_key_path();
    let inside_data_dir = path_is_inside(&path, &cfg.data_dir);

    if path.exists() {
        let bytes = std::fs::read(&path).map_err(|e| MasterKeyError::Io {
            path: path.clone(),
            source: e,
        })?;
        if bytes.len() != 32 {
            return Err(MasterKeyError::BadLength {
                path,
                len: bytes.len(),
            });
        }
        let mut key = [0u8; 32];
        key.copy_from_slice(&bytes);
        return Ok(MasterKeyResult {
            key,
            inside_data_dir,
            generated: false,
        });
    }

    let mut key = [0u8; 32];
    getrandom::getrandom(&mut key)?;
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| MasterKeyError::Io {
            path: parent.to_path_buf(),
            source: e,
        })?;
    }
    write_secret_file(&path, &key)?;

    Ok(MasterKeyResult {
        key,
        inside_data_dir,
        generated: true,
    })
}

/// Loads the master key for `rotate` — unlike [`load_or_generate`], a
/// missing file is an error, not something to paper over: rotating "into" a
/// key that never existed would just be generation with extra steps, and
/// silently doing that would rotate no data at all.
pub fn load_existing(cfg: &Config) -> Result<[u8; 32], MasterKeyError> {
    if std::env::var_os(FORBIDDEN_ENV_KEY).is_some() {
        return Err(MasterKeyError::EnvVarRefused);
    }
    let path = cfg.master_key_path();
    if !path.exists() {
        return Err(MasterKeyError::NotFound { path });
    }
    let bytes = std::fs::read(&path).map_err(|e| MasterKeyError::Io {
        path: path.clone(),
        source: e,
    })?;
    if bytes.len() != 32 {
        return Err(MasterKeyError::BadLength { path, len: bytes.len() });
    }
    let mut key = [0u8; 32];
    key.copy_from_slice(&bytes);
    Ok(key)
}

/// Writes `new_key` to a `.new` sibling of the master key file, then renames
/// it into place. `rename` replacing an existing destination is atomic on
/// POSIX as long as source and destination share a filesystem — true here,
/// since the sibling lives in the same directory — so a crash mid-swap
/// leaves either the old key file or the new one fully intact, never a
/// half-written one. This runs *after* `AuthService::rotate_master_key` has
/// already committed the database under `new_key` (see that function's doc
/// for why a crash between the two is a loud refusal to start, not silent
/// corruption).
pub fn swap_key_file(cfg: &Config, new_key: &[u8; 32]) -> Result<(), MasterKeyError> {
    let path = cfg.master_key_path();
    let mut tmp = path.clone().into_os_string();
    tmp.push(".new");
    let tmp = std::path::PathBuf::from(tmp);
    write_secret_file(&tmp, new_key)?;
    std::fs::rename(&tmp, &path).map_err(|e| MasterKeyError::Io {
        path: path.clone(),
        source: e,
    })?;
    Ok(())
}

fn write_secret_file(path: &Path, bytes: &[u8]) -> Result<(), MasterKeyError> {
    std::fs::write(path, bytes).map_err(|e| MasterKeyError::Io {
        path: path.to_path_buf(),
        source: e,
    })?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600));
    }
    Ok(())
}

/// Best-effort "is `path` underneath `dir`" check. Uses `canonicalize` when
/// possible (handles `..`/symlinks) but falls back to a prefix check on a
/// non-existent path (e.g. the key file hasn't been written yet on a dry
/// run) — permissive by design, since this only feeds a warning, not a
/// security decision.
fn path_is_inside(path: &Path, dir: &Path) -> bool {
    let (p, d) = match (path.canonicalize(), dir.canonicalize()) {
        (Ok(p), Ok(d)) => (p, d),
        _ => (path.to_path_buf(), dir.to_path_buf()),
    };
    p.starts_with(&d)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex;

    // Environment variables are process-global; serialize tests that touch
    // SC_MASTER_KEY so they don't race each other.
    static ENV_LOCK: Mutex<()> = Mutex::new(());

    #[test]
    fn generates_key_when_missing() {
        let _g = ENV_LOCK.lock().unwrap();
        std::env::remove_var(FORBIDDEN_ENV_KEY);
        let dir = tempfile::tempdir().unwrap();
        let cfg = Config {
            data_dir: dir.path().to_path_buf(),
            master_key_file: Some(dir.path().join("master.key")),
            ..Config::default()
        };

        let r1 = load_or_generate(&cfg).unwrap();
        assert!(r1.generated);
        assert!(dir.path().join("master.key").exists());

        let r2 = load_or_generate(&cfg).unwrap();
        assert!(!r2.generated);
        assert_eq!(r1.key, r2.key);
    }

    #[test]
    fn refuses_env_var_key_material() {
        let _g = ENV_LOCK.lock().unwrap();
        std::env::set_var(FORBIDDEN_ENV_KEY, "deadbeef");
        let dir = tempfile::tempdir().unwrap();
        let cfg = Config {
            data_dir: dir.path().to_path_buf(),
            master_key_file: Some(dir.path().join("master.key")),
            ..Config::default()
        };

        let err = load_or_generate(&cfg).unwrap_err();
        assert!(matches!(err, MasterKeyError::EnvVarRefused));
        std::env::remove_var(FORBIDDEN_ENV_KEY);
    }

    #[test]
    fn flags_key_inside_data_dir() {
        let _g = ENV_LOCK.lock().unwrap();
        std::env::remove_var(FORBIDDEN_ENV_KEY);
        let dir = tempfile::tempdir().unwrap();
        let cfg = Config {
            data_dir: dir.path().to_path_buf(),
            master_key_file: Some(dir.path().join("master.key")),
            ..Config::default()
        };

        let r = load_or_generate(&cfg).unwrap();
        assert!(r.inside_data_dir);
    }

    #[test]
    fn does_not_flag_key_outside_data_dir() {
        let _g = ENV_LOCK.lock().unwrap();
        std::env::remove_var(FORBIDDEN_ENV_KEY);
        let data_dir = tempfile::tempdir().unwrap();
        let key_dir = tempfile::tempdir().unwrap();
        let cfg = Config {
            data_dir: data_dir.path().to_path_buf(),
            master_key_file: Some(key_dir.path().join("master.key")),
            ..Config::default()
        };

        let r = load_or_generate(&cfg).unwrap();
        assert!(!r.inside_data_dir);
    }

    #[test]
    fn load_existing_refuses_a_missing_file() {
        let _g = ENV_LOCK.lock().unwrap();
        std::env::remove_var(FORBIDDEN_ENV_KEY);
        let dir = tempfile::tempdir().unwrap();
        let cfg = Config {
            data_dir: dir.path().to_path_buf(),
            master_key_file: Some(dir.path().join("master.key")),
            ..Config::default()
        };
        assert!(matches!(load_existing(&cfg), Err(MasterKeyError::NotFound { .. })));
    }

    #[test]
    fn load_existing_reads_what_load_or_generate_wrote() {
        let _g = ENV_LOCK.lock().unwrap();
        std::env::remove_var(FORBIDDEN_ENV_KEY);
        let dir = tempfile::tempdir().unwrap();
        let cfg = Config {
            data_dir: dir.path().to_path_buf(),
            master_key_file: Some(dir.path().join("master.key")),
            ..Config::default()
        };
        let generated = load_or_generate(&cfg).unwrap();
        assert_eq!(load_existing(&cfg).unwrap(), generated.key);
    }

    #[test]
    fn swap_key_file_replaces_the_key_atomically() {
        let _g = ENV_LOCK.lock().unwrap();
        std::env::remove_var(FORBIDDEN_ENV_KEY);
        let dir = tempfile::tempdir().unwrap();
        let cfg = Config {
            data_dir: dir.path().to_path_buf(),
            master_key_file: Some(dir.path().join("master.key")),
            ..Config::default()
        };
        let old = load_or_generate(&cfg).unwrap().key;
        let new_key = [42u8; 32];
        swap_key_file(&cfg, &new_key).unwrap();

        assert_eq!(load_existing(&cfg).unwrap(), new_key);
        assert_ne!(load_existing(&cfg).unwrap(), old);
        // The `.new` staging file must not be left behind.
        assert!(!dir.path().join("master.key.new").exists());
    }
}
