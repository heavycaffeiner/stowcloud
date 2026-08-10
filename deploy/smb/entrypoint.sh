#!/bin/bash
# `sc:smb` sidecar entrypoint.
#
# Runs as PID 1's child under tini. Responsibilities, in order:
#   1. Wait for sc-core to have written a config (`sc-server smb-sync` is an
#      operator/cron-triggered CLI command, not something `serve` calls on
#      its own — crates/sc-server/src/lib.rs — so on a fresh deployment this
#      directory can be empty for a while).
#   2. Validate every candidate with `testparm -s` before it ever reaches
#      smbd; a rejected candidate leaves the previous good config running.
#   3. Import the passdb and rebuild /etc/passwd so `getpwnam` succeeds for
#      every SMB user.
#   4. Watch the shared config volume and re-run 1-3 on every change,
#      reloading smbd without restarting it.
set -euo pipefail

CONFIG_DIR="${SC_SMB_CONFIG_DIR:-/config/smb}"
STATE_DIR=/var/lib/sc-smb-sidecar
SMB_CONF_ACTIVE=/etc/samba/smb.conf
PASSDB_PATH=/var/lib/samba/private/passdb.tdb

mkdir -p "$STATE_DIR" /var/lib/samba/private /var/run/samba /var/log/samba

# `crates/sc-smb/src/conf.rs::render_global` always emits `[global]` as the
# literal first line, so inserting our two logging directives right after
# it keeps them inside the `[global]` section without needing to know
# anything else about the file's shape. Logging is a sidecar/operational
# concern (fail2ban needs somewhere to tail), deliberately not added to the
# `sc-smb` crate itself, which only renders the security-hardening
# directives (its own module doc draws that line).
# Single file, not the per-client `log.%m` some Samba examples use:
# `fail2ban-samba.local`/`fail2ban-samba-filter.conf` hardcode
# `logpath = /var/log/samba/log.smbd`, and a `%m`-split log would scatter
# auth failures across files fail2ban never looks at.
#
# `auth_audit:3`: neither Debian's nor Alpine's fail2ban package ships a
# `samba` filter at all (checked both directly — there is no upstream
# "stock" one, a previous version of this comment was wrong about that), so
# `fail2ban-samba-filter.conf` is a filter written for this project against
# real captured smbd output. That output only exists at this debug level —
# plain `log level = 1` (what this line used to be) never logs an auth
# failure at any level, confirmed by an actual bad-password connection
# producing nothing to match. `auth_audit:3` raises just the auth-event
# class high enough to log `Auth: [...] status [NT_STATUS_...]` lines
# without raising the global level (still 1) and turning on smbd's normal
# per-request chatter.
inject_logging() {
    { head -n1 "$1"; printf '  log file = /var/log/samba/log.smbd\n  log level = 1 auth_audit:3\n'; tail -n +2 "$1"; }
}

# Validate and, only on success, promote a candidate config + passdb +
# passwd file. Returns non-zero (and changes nothing live) on any failure.
sync_once() {
    local src="$CONFIG_DIR/smb.conf"
    if [ ! -f "$src" ]; then
        echo "sc-smb: waiting for $src (sc-core has not run smb-sync yet)" >&2
        return 1
    fi

    local candidate="$STATE_DIR/smb.conf.candidate"
    inject_logging "$src" > "$candidate"

    if ! testparm -s "$candidate" >"$STATE_DIR/testparm.out" 2>&1; then
        echo "sc-smb: rejected candidate smb.conf, keeping the previous config:" >&2
        cat "$STATE_DIR/testparm.out" >&2
        return 1
    fi
    cp "$candidate" "$SMB_CONF_ACTIVE"

    # Rebuild from the pristine image baseline every time (not appended
    # incrementally) so a user removed from sc-core's registry disappears
    # here too, instead of accumulating stale getpwnam entries forever.
    cp /etc/passwd.base /etc/passwd
    if [ -f "$CONFIG_DIR/passwd" ]; then
        cat "$CONFIG_DIR/passwd" >> /etc/passwd
    fi

    # `pdbedit -i` imports by username, overwriting existing entries —
    # idempotent to re-run on every sync.
    if [ -f "$CONFIG_DIR/smbpasswd" ]; then
        pdbedit -i "smbpasswd:$CONFIG_DIR/smbpasswd" -e "tdbsam:$PASSDB_PATH" >/dev/null
        verify_passdb
    fi
    return 0
}

# `pdbedit -i` reports success and imports nothing when the smbpasswd file's
# uid field names no passwd entry: no error, no output, exit 0, an empty
# passdb. Every downstream symptom (NT_STATUS_LOGON_FAILURE at the client,
# NT_STATUS_NO_SUCH_USER in the smbd log) points at credentials or config
# rather than at the import, so say it here where the cause is still visible.
#
# `deploy/smb/native/sc-smb-agent.sh` has had this check since it was written;
# this sidecar did not, and that asymmetry is what made a real occurrence
# expensive to diagnose on 2026-08-03 — the container path failed exactly this
# way and said nothing at all. A warning, not a failure: one bad entry must
# not take SMB down for everyone else.
verify_passdb() {
    local known missing=''
    # `set -e` plus a redirect from a missing file would abort the whole
    # entrypoint; nothing to check without it anyway.
    [ -f "$CONFIG_DIR/passwd" ] || return 0
    known=$(pdbedit -L 2>/dev/null | cut -d: -f1)
    while IFS=: read -r name _; do
        [ -n "$name" ] || continue
        echo "$known" | grep -qx "$name" || missing="$missing $name"
    done < "$CONFIG_DIR/passwd"
    [ -z "$missing" ] || echo "sc-smb: WARNING: no passdb entry for:$missing — they cannot authenticate over SMB (check the uid field in smbpasswd against $CONFIG_DIR/passwd)" >&2
}

# nmbd, but only while the active config asks for a name. `disable netbios` is
# what `sc-smb`'s renderer flips when `smb.server_name` is set, and reading it
# back from the promoted file rather than from an env var is what lets a
# settings change take effect on the next reload instead of the next restart.
#
# Name service is broadcast UDP 137/138 and does not cross a Docker bridge, so
# on a bridged network nmbd starts and nobody hears it. `network_mode: host`
# or macvlan is what makes `\\NAME` resolve.
#
# smbd stays on 445 either way (`smb ports = 445`): nmbd publishes the name,
# it does not bring back the 139 transport, which predates SMB3 signing and
# encryption.
sync_nmbd() {
    local disabled
    disabled=$(testparm -s --parameter-name='disable netbios' "$SMB_CONF_ACTIVE" 2>/dev/null \
        | tr -d '[:space:]' | tr '[:upper:]' '[:lower:]')
    if [ "$disabled" = 'no' ]; then
        if pgrep -x nmbd >/dev/null 2>&1; then
            return 0
        fi
        nmbd --daemon >/dev/null 2>&1 || \
            echo "sc-smb: nmbd did not start, so \\\\NAME will not resolve; the address still works" >&2
    else
        pkill -x nmbd >/dev/null 2>&1 || true
    fi
    return 0
}

# smbd cannot start without at least one accepted config.
until sync_once; do
    sleep 2
done
sync_nmbd

# fail2ban watches the log path `inject_logging` just pointed smbd at
# (jail.d/samba.conf, baked into the image). smbd itself doesn't start until
# the `exec` below, so without this touch the file doesn't exist yet at the
# moment fail2ban-server loads its jails — confirmed by an actual run: it
# failed with "Have not found any log file for samba jail" / "Async
# configuration of server failed" (a hard failure for the whole daemon, not
# just this jail), even though the exact same path exists moments later once
# smbd has opened it. Not fatal if fail2ban still fails to start for some
# other reason (e.g. NET_ADMIN not granted to the container) — SMB itself
# still has to work; brute-force mitigation is defense in depth on top of it.
touch /var/log/samba/log.smbd
# `--logtarget=syslog` was wrong for this container: there is no syslog
# daemon here (no `/dev/log`), confirmed by an actual run — fail2ban-server
# logged "Failed to change log target" and came up with 0 jails loaded
# instead of the [samba] jail. A plain file needs no such dependency.
fail2ban-server -b --logtarget=/var/log/fail2ban.log >"$STATE_DIR/fail2ban.boot.log" 2>&1 || \
    echo "sc-smb: fail2ban did not start (see $STATE_DIR/fail2ban.boot.log); continuing without it" >&2

# Re-sync on every change to the shared config volume, for the life of the
# container. Reparented to tini (real PID 1) on exec below, so it keeps
# running and gets reaped correctly.
(
    while inotifywait -e close_write,create,move -q "$CONFIG_DIR" >/dev/null 2>&1; do
        if sync_once; then
            smbcontrol all reload-config 2>/dev/null || true
            # After the reload, because turning a name on or off starts or
            # stops a daemon rather than changing one smbd already reread.
            sync_nmbd
            echo "sc-smb: reloaded after a config change"
        fi
    done
) &

exec smbd --foreground --no-process-group
