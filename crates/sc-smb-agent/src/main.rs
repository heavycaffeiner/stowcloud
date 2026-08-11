//! `sc-smb-agent` — the privileged half of SMB publishing.
//!
//! `sc-server` renders `smb.conf`, `smbpasswd`, `passwd` and `network.policy`
//! into a shared directory as an unprivileged user. This picks them up as
//! root, decides the network scope from the interfaces it can actually see,
//! validates the result, promotes it, imports the passdb and tells smbd. The
//! split is the point: `sc-server` gains no capability, and this program never
//! parses anything off the network.
//!
//! Two ways in, and they are the same code path:
//!
//!   * `sc-core` connects to the control socket and asks. Immediate, and the
//!     answer goes back as a report it can put on the settings screen.
//!   * A poll notices the rendered files changed, or that the machine's own
//!     interfaces moved, which happens when a VPN or a VLAN comes up and
//!     writes nothing anywhere.
//!
//! It replaces two shell scripts that did this, one per deployment shape.
//! They had already drifted apart once, and the container copy silently lost
//! SMB entirely on an image without `ip` installed.
//!
//! Linux only, and the `cfg` below is not a portability aspiration: Samba,
//! `/etc/passwd` and Unix sockets are the whole job. It exists so the
//! workspace still builds on the Windows development host.

// Which addresses SMB answers on, and what reaches smbd, are decided by two
// pure functions over data. They stay compiled everywhere, and their tests
// with them: gating them behind `unix` once meant a wrong assertion in each
// was invisible until CI ran, which is the slowest possible place to find
// out that the config renderer disagrees with its own test.
//
// Nothing calls them on a non-unix host, hence the `allow`.
#[cfg_attr(not(unix), allow(dead_code))]
mod conf;
#[cfg_attr(not(unix), allow(dead_code))]
mod scope;

#[cfg(unix)]
mod accounts;
#[cfg(unix)]
mod control;
#[cfg(unix)]
mod run;
#[cfg(unix)]
mod smbd;
#[cfg(unix)]
mod sync;

fn main() -> std::process::ExitCode {
    #[cfg(unix)]
    {
        run::main()
    }
    #[cfg(not(unix))]
    {
        eprintln!("sc-smb-agent runs beside smbd on Linux; there is nothing for it to do here");
        std::process::ExitCode::FAILURE
    }
}
