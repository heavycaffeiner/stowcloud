//! `smb.conf` rendering.
//!
//! Every directive listed as "non-negotiable" by the spec this crate
//! implements is emitted unconditionally in `[global]`; nothing here is
//! configurable by the admin (that's the point — coexistence and NTLM
//! hardening are not options, they're facts about how this server runs).

use crate::bind::{PRIVATE_CIDRS_V4, PRIVATE_CIDRS_V6};
use crate::error::SmbError;
use crate::{SmbConfig, SmbShareDef, SmbUser};

/// Veto list shared by every share — our own reserved names:
/// our own control files must never be visible/writable over SMB.
pub const VETO_FILES: &str = "/.sctrash/.scpart-*/.scmeta/.scindex/";

pub(crate) fn render(
    cfg: &SmbConfig,
    shares: &[SmbShareDef],
    _users: &[SmbUser],
) -> Result<String, SmbError> {
    for s in shares {
        if s.name.trim().is_empty() {
            return Err(SmbError::InvalidShare {
                name: s.name.clone(),
                reason: "share name must not be empty".to_string(),
            });
        }
        if s.name.contains(['[', ']', '\n']) {
            return Err(SmbError::InvalidShare {
                name: s.name.clone(),
                reason: "share name contains characters illegal in smb.conf".to_string(),
            });
        }
    }

    let mut out = String::new();
    out.push_str(&render_global(cfg));
    out.push('\n');
    for s in shares {
        out.push_str(&render_share(s));
        out.push('\n');
    }
    Ok(out)
}

fn render_global(cfg: &SmbConfig) -> String {
    let mut ifaces = String::from("lo");
    for c in PRIVATE_CIDRS_V4 {
        ifaces.push(' ');
        ifaces.push_str(c);
    }
    for c in PRIVATE_CIDRS_V6 {
        ifaces.push(' ');
        ifaces.push_str(c);
    }

    // Every private CIDR, deduplicated. An earlier `skip(1)` over the V4 list
    // assumed loopback came first; it is last, so the rendered list dropped
    // 10.0.0.0/8 and repeated 127.0.0.0/8. Samba then answered every client on
    // a 10.x LAN with an NBSS negative session response before SMB2 NEGOTIATE
    // -- reproduced 2026-07-31 against the production sidecar.
    let mut hosts_allow = String::new();
    for c in PRIVATE_CIDRS_V4.iter().chain(PRIVATE_CIDRS_V6) {
        if !hosts_allow.is_empty() {
            hosts_allow.push(' ');
        }
        hosts_allow.push_str(c);
    }

    format!(
        "[global]\n\
         \u{20}\u{20}workgroup = {workgroup}\n\
         \u{20}\u{20}server min protocol = SMB3_11\n\
         \u{20}\u{20}server signing = required\n\
         \u{20}\u{20}smb encrypt = required\n\
         \u{20}\u{20}client ipc signing = required\n\
         \u{20}\u{20}restrict anonymous = 2\n\
         \u{20}\u{20}null passwords = no\n\
         \u{20}\u{20}guest ok = no\n\
         \u{20}\u{20}map to guest = never\n\
         \u{20}\u{20}unix extensions = no\n\
         \u{20}\u{20}ntlm auth = ntlmv2-only\n\
         \u{20}\u{20}lanman auth = no\n\
         \u{20}\u{20}raw NTLMv2 auth = no\n\
         \u{20}\u{20}disable netbios = yes\n\
         \u{20}\u{20}smb ports = 445\n\
         \u{20}\u{20}load printers = no\n\
         \u{20}\u{20}printing = bsd\n\
         \u{20}\u{20}printcap name = /dev/null\n\
         \u{20}\u{20}disable spoolss = yes\n\
         \u{20}\u{20}passdb backend = tdbsam\n\
         \u{20}\u{20}force user = {service_user}\n\
         \u{20}\u{20}force group = {service_user}\n\
         \n\
         \u{20}\u{20}# ── shared-folder coexistence (never let Samba mutate perms/xattrs \
         other services rely on) ──\n\
         \u{20}\u{20}store dos attributes = no\n\
         \u{20}\u{20}map archive = no\n\
         \u{20}\u{20}map hidden = no\n\
         \u{20}\u{20}map system = no\n\
         \u{20}\u{20}map readonly = no\n\
         \u{20}\u{20}ea support = no\n\
         \n\
         \u{20}\u{20}# ── LAN-only enforcement ──\n\
         \u{20}\u{20}bind interfaces only = yes\n\
         \u{20}\u{20}interfaces = {ifaces}\n\
         \u{20}\u{20}hosts allow = {hosts_allow}\n\
         \u{20}\u{20}hosts deny = 0.0.0.0/0\n",
        workgroup = cfg.workgroup,
        service_user = cfg.service_user,
        ifaces = ifaces,
        hosts_allow = hosts_allow,
    )
}

fn render_share(s: &SmbShareDef) -> String {
    let valid = s.valid_users.join(" ");
    let read = s.read_list.join(" ");
    let write = s.write_list.join(" ");
    let mut out = format!(
        "[{name}]\n\
         \u{20}\u{20}path = {path}\n\
         \u{20}\u{20}valid users = {valid}\n\
         \u{20}\u{20}read list = {read}\n\
         \u{20}\u{20}write list = {write}\n\
         \u{20}\u{20}create mask = 0664\n\
         \u{20}\u{20}directory mask = 0775\n\
         \u{20}\u{20}veto files = {veto}\n\
         \u{20}\u{20}delete veto files = no\n",
        name = s.name,
        path = s.path,
        valid = valid,
        read = read,
        write = write,
        veto = VETO_FILES,
    );
    if s.shared_externally {
        // Other services can write the same files (Jellyfin, *arr, rsync);
        // oplocks/leases would let an SMB client cache a stale view.
        out.push_str("  oplocks = no\n");
    }
    out
}
