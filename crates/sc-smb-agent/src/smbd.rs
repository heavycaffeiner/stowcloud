//! Starting, reloading and replacing smbd, and the one distinction that
//! matters: **smbd binds its listening sockets once, at startup.**
//!
//! `smbcontrol all reload-config` rereads shares, users and permissions in
//! place, and does not revisit those sockets. A changed `interfaces` line
//! therefore needs the process replaced, not reloaded. Before this agent
//! existed the sidecar reloaded either way, so a container that came up
//! before its network did stayed bound to loopback for as long as it ran,
//! with a promoted config on disk that said otherwise.

use std::io;
use std::process::{Child, Command, Stdio};

/// Who owns the smbd process.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Mode {
    /// This agent does: it spawns smbd as its own child and replaces it. The
    /// container case, where there is no service manager to ask.
    Supervise,
    /// A service manager does, under this unit name. The bare-metal case.
    Service(String),
}

impl Mode {
    /// `systemctl` on Rocky/RHEL (`smb`) and Debian/Ubuntu (`smbd`),
    /// OpenRC's `samba` on Alpine, and supervision when there is neither.
    pub fn detect() -> Self {
        if which("systemctl") {
            for unit in ["smb", "smbd"] {
                let known = Command::new("systemctl")
                    .arg("cat")
                    .arg(format!("{unit}.service"))
                    .stdout(Stdio::null())
                    .stderr(Stdio::null())
                    .status()
                    .map(|s| s.success())
                    .unwrap_or(false);
                if known {
                    return Self::Service(unit.to_string());
                }
            }
            return Self::Service("smbd".to_string());
        }
        if which("rc-service") {
            return Self::Service("samba".to_string());
        }
        Self::Supervise
    }
}

fn which(bin: &str) -> bool {
    std::env::var_os("PATH")
        .map(|paths| {
            std::env::split_paths(&paths).any(|d| d.join(bin).is_file())
        })
        .unwrap_or(false)
}

pub struct Smbd {
    mode: Mode,
    /// Only ever `Some` under [`Mode::Supervise`].
    child: Option<Child>,
    nmbd: Option<Child>,
}

impl Smbd {
    pub fn new(mode: Mode) -> Self {
        Self {
            mode,
            child: None,
            nmbd: None,
        }
    }

    /// `true` if smbd is up as far as this agent can tell. Under supervision
    /// that is exact; under a service manager it is what the manager says.
    pub fn running(&mut self) -> bool {
        match &self.mode {
            Mode::Supervise => match self.child.as_mut() {
                // `try_wait` reaps, which is also what keeps a crashed smbd
                // from lingering as a zombie until this process exits.
                Some(c) => matches!(c.try_wait(), Ok(None)),
                None => false,
            },
            Mode::Service(unit) => service(unit, "is-active").unwrap_or(false),
        }
    }

    pub fn start(&mut self) -> io::Result<()> {
        match self.mode.clone() {
            Mode::Supervise => {
                // Inherited stdio on purpose: smbd's own diagnostics are what
                // `docker logs` shows for this container.
                let child = Command::new("smbd")
                    .args(["--foreground", "--no-process-group"])
                    .spawn()?;
                self.child = Some(child);
                Ok(())
            }
            Mode::Service(unit) => require(&unit, "start"),
        }
    }

    /// Config only. Cheap, and wrong for anything that moves a socket.
    pub fn reload(&mut self) -> io::Result<()> {
        let out = Command::new("smbcontrol")
            .args(["all", "reload-config"])
            .output()?;
        if out.status.success() {
            return Ok(());
        }
        // A service manager can reload a daemon whose control socket this
        // agent cannot reach; supervision has no second way to ask.
        match self.mode.clone() {
            Mode::Service(unit) => require(&unit, "reload"),
            Mode::Supervise => Err(io::Error::other(format!(
                "smbcontrol reload-config failed: {}",
                String::from_utf8_lossy(&out.stderr).trim()
            ))),
        }
    }

    /// The only thing that moves the listening sockets.
    pub fn restart(&mut self) -> io::Result<()> {
        match self.mode.clone() {
            Mode::Supervise => {
                self.stop()?;
                self.start()
            }
            Mode::Service(unit) => require(&unit, "restart"),
        }
    }

    pub fn stop(&mut self) -> io::Result<()> {
        self.nmbd(false);
        match self.mode.clone() {
            Mode::Supervise => {
                if let Some(mut c) = self.child.take() {
                    let _ = c.kill();
                    let _ = c.wait();
                }
                Ok(())
            }
            Mode::Service(unit) => require(&unit, "stop"),
        }
    }

    /// nmbd, but only while the promoted config asks for a name.
    ///
    /// Name service is broadcast UDP 137/138 and does not cross a Docker
    /// bridge, so on a bridged network nmbd starts and nobody hears it.
    /// Failure is a log line, never fatal: the address still mounts.
    pub fn nmbd(&mut self, want: bool) {
        let up = matches!(self.nmbd.as_mut().map(|c| c.try_wait()), Some(Ok(None)));
        if want == up {
            return;
        }
        if want {
            match Command::new("nmbd").arg("--foreground").spawn() {
                Ok(c) => self.nmbd = Some(c),
                Err(e) => tracing::warn!(
                    error = %e,
                    "nmbd did not start, so \\\\NAME will not resolve; the address still works"
                ),
            }
        } else if let Some(mut c) = self.nmbd.take() {
            let _ = c.kill();
            let _ = c.wait();
        }
    }

    /// fail2ban's ban action writes iptables rules, which needs
    /// `CAP_NET_ADMIN`. The reference deployment does not grant it once this
    /// runs on the host's network, because there those rules are the *host's*
    /// firewall handed to the process that parses SMB off the wire. Checking
    /// first turns that into one line saying what is off, instead of a jail
    /// failing to start with an iptables error that reads like a bug.
    pub fn start_fail2ban(&self) {
        if !which("fail2ban-server") {
            return;
        }
        if !has_net_admin() {
            tracing::info!(
                "no CAP_NET_ADMIN, so fail2ban is not started and repeated bad passwords are \
                 never banned; what limits an attacker is 'hosts allow' plus SMB3-required auth"
            );
            return;
        }
        // No syslog daemon in a container, so a plain file: with
        // `--logtarget=syslog` fail2ban-server logs "Failed to change log
        // target" and comes up with zero jails loaded.
        let started = Command::new("fail2ban-server")
            .args(["-b", "--logtarget=/var/log/fail2ban.log"])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .map(|s| s.success())
            .unwrap_or(false);
        if !started {
            tracing::warn!("fail2ban did not start; continuing without it");
        }
    }
}

impl Drop for Smbd {
    fn drop(&mut self) {
        if self.mode == Mode::Supervise {
            let _ = self.stop();
        }
    }
}

fn service(unit: &str, action: &str) -> io::Result<bool> {
    let status = if which("systemctl") {
        Command::new("systemctl")
            .args([action, unit])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()?
    } else {
        Command::new("rc-service")
            .args([unit, action])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()?
    };
    Ok(status.success())
}

fn require(unit: &str, action: &str) -> io::Result<()> {
    if service(unit, action)? {
        Ok(())
    } else {
        Err(io::Error::other(format!("could not {action} the {unit} service")))
    }
}

/// CAP_NET_ADMIN is bit 12 of the effective set.
fn has_net_admin() -> bool {
    let Ok(status) = std::fs::read_to_string("/proc/self/status") else {
        return false;
    };
    status
        .lines()
        .find_map(|l| l.strip_prefix("CapEff:"))
        .and_then(|v| u64::from_str_radix(v.trim(), 16).ok())
        .map(|caps| caps & (1 << 12) != 0)
        .unwrap_or(false)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The distinction the module exists for, asserted where it is decided
    /// rather than left to the caller to remember.
    #[test]
    fn a_scope_change_is_a_restart_not_a_reload() {
        assert!(crate::sync::needs_restart("lo", "lo eth0"));
        assert!(crate::sync::needs_restart("lo eth0", "lo"));
        assert!(!crate::sync::needs_restart("lo eth0", "lo eth0"));
    }

    #[test]
    fn supervision_is_the_fallback_when_there_is_no_service_manager() {
        // Not an assertion about the test host: only that detection answers
        // something usable rather than panicking on a machine with neither.
        let m = Mode::detect();
        assert!(matches!(m, Mode::Supervise | Mode::Service(_)));
    }
}
