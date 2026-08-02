#!/bin/sh
# sc-smb-agent -- the bare-metal twin of deploy/smb/entrypoint.sh
# (DEPLOYMENT.md §7.5).
#
# Same contract as the sidecar: sc-server writes smb.conf/smbpasswd/passwd
# into a shared directory, something privileged picks them up, validates them
# and reloads smbd. In a container that "something" is the entrypoint; here it
# is this script under systemd/OpenRC as root. sc-server itself stays
# unprivileged and gains no new capability -- that is the whole point of doing
# it this way rather than handing it sudo rules for useradd/pdbedit.
#
# POSIX sh, and no dependency beyond the samba package plus coreutils/busybox:
#   - no inotify-tools (the sidecar's inotifywait): this polls instead, so
#     Alpine and a minimal Rocky both work without an extra package.
#   - no shadow-utils: /etc/passwd is reconciled directly rather than through
#     useradd, because busybox adduser cannot create the several accounts
#     that must share one uid (§7.2 passdb sync point 3), having no -o.
set -eu

CONFIG_DIR="${SC_SMB_CONFIG_DIR:-/var/lib/sc/smbcfg}"
STATE_DIR="${SC_SMB_STATE_DIR:-/var/lib/sc-smb-agent}"
SMB_CONF="${SC_SMB_CONF:-/etc/samba/smb.conf}"
PASSDB="${SC_SMB_PASSDB:-/var/lib/samba/private/passdb.tdb}"
POLL="${SC_SMB_POLL_SECONDS:-5}"

# Written into the GECOS field of every account this script creates, and the
# only thing that makes one eligible for removal. An account without it is
# somebody else's and is never touched.
MARKER=sc-managed-smb

log() { echo "sc-smb-agent: $*" >&2; }
die() { log "$*"; exit 1; }

# --- platform -------------------------------------------------------------

# Rocky/RHEL name the unit smb.service, Debian/Ubuntu smbd.service, Alpine
# runs OpenRC's samba. Resolved once; SC_SMB_SERVICE overrides.
detect_service() {
    if [ -n "${SC_SMB_SERVICE:-}" ]; then
        echo "$SC_SMB_SERVICE"
        return
    fi
    if command -v systemctl >/dev/null 2>&1; then
        for s in smb smbd; do
            if systemctl cat "$s.service" >/dev/null 2>&1; then
                echo "$s"
                return
            fi
        done
        echo smbd
        return
    fi
    echo samba
}

svc() {
    action=$1
    if command -v systemctl >/dev/null 2>&1; then
        systemctl "$action" "$SERVICE" || return 1
    elif command -v rc-service >/dev/null 2>&1; then
        rc-service "$SERVICE" "$action" || return 1
    else
        return 1
    fi
}

# --- validation -----------------------------------------------------------

# sc-core account names reach useradd-shaped territory here, so they are
# checked against the portable-username rule before anything writes them into
# /etc/passwd. A name starting with '-' would also be argument injection into
# pdbedit below.
valid_name() {
    echo "$1" | grep -qE '^[a-z_][a-z0-9_-]{0,31}$'
}

# --- /etc/passwd reconciliation -------------------------------------------

desired_names() { [ -f "$CONFIG_DIR/passwd" ] && cut -d: -f1 "$CONFIG_DIR/passwd" || true; }
managed_names() { awk -F: -v m="$MARKER" '$5 == m { print $1 }' /etc/passwd; }

# Refuse to sync at all if an SMB account name collides with a pre-existing
# system account we did not create. Adopting it silently would give that
# account's name an NT hash in the passdb, and removing it later would delete
# a system user -- neither is a decision this script gets to make.
check_collisions() {
    rc=0
    for n in $(desired_names); do
        if ! valid_name "$n"; then
            log "refusing to sync: '$n' is not a valid system account name"
            rc=1
            continue
        fi
        existing=$(awk -F: -v n="$n" -v m="$MARKER" '$1 == n && $5 != m { print $1 }' /etc/passwd)
        if [ -n "$existing" ]; then
            log "refusing to sync: '$n' already exists as a non-managed system account"
            rc=1
        fi
    done
    return $rc
}

# Drop every marked account, then append the freshly rendered ones. Rebuilding
# rather than diffing is what makes a user removed from sc-core's registry
# disappear here too, instead of leaving a stale getpwnam entry forever -- the
# sidecar gets the same property from `cp /etc/passwd.base /etc/passwd`.
#
# Written to a temp file and renamed, so a reader never sees a partial
# /etc/passwd. That still leaves the ordinary lost-update race against a
# concurrent useradd; shadow's fcntl lock is not reachable from POSIX sh, and
# a file server's SMB roster is not a multi-writer passwd.
rebuild_passwd() {
    tmp="$STATE_DIR/passwd.new"
    awk -F: -v m="$MARKER" '$5 != m' /etc/passwd > "$tmp"
    if [ -f "$CONFIG_DIR/passwd" ]; then
        # sc-smb renders an empty GECOS; stamp the marker into field 5 so the
        # accounts are recognisable as ours on the next pass.
        awk -F: -v m="$MARKER" 'BEGIN { OFS=":" } NF >= 7 { $5 = m; print }' \
            "$CONFIG_DIR/passwd" >> "$tmp"
    fi
    chmod 644 "$tmp"
    cat "$tmp" > "$STATE_DIR/passwd.staged" && mv "$tmp" /etc/passwd
    rm -f "$STATE_DIR/passwd.staged"
}

# Every group id the rendered accounts reference must already exist: this
# script does not invent groups, and an account whose gid resolves to nothing
# breaks getpwnam just as surely as a missing account.
check_groups() {
    [ -f "$CONFIG_DIR/passwd" ] || return 0
    rc=0
    for g in $(cut -d: -f4 "$CONFIG_DIR/passwd" | sort -u); do
        if ! awk -F: -v g="$g" '$3 == g { found = 1 } END { exit !found }' /etc/group; then
            log "refusing to sync: gid $g (smb_service_gid) has no group in /etc/group"
            rc=1
        fi
    done
    return $rc
}

# --- passdb ---------------------------------------------------------------

# Drop NT hashes for accounts that are no longer ours. Without this, disabling
# a user in sc-core would leave them able to authenticate over SMB.
prune_passdb() {
    keep=$(desired_names)
    pdbedit -L 2>/dev/null | cut -d: -f1 | while read -r u; do
        [ -n "$u" ] || continue
        if ! echo "$keep" | grep -qx "$u"; then
            pdbedit -x -u "$u" >/dev/null 2>&1 || true
            log "removed $u from the passdb"
        fi
    done
}

# `pdbedit -i` reports success and imports nothing when the smbpasswd file's
# uid field does not match the account's real uid -- no error, no output, an
# empty passdb. Measured on Samba 4.20. Every other symptom (auth fails, the
# share is invisible) looks like a config problem, so check it here where the
# cause is still obvious. A warning, not a failure: one bad entry must not
# take SMB down for everyone else.
verify_passdb() {
    known=$(pdbedit -L 2>/dev/null | cut -d: -f1)
    missing=''
    for n in $(desired_names); do
        echo "$known" | grep -qx "$n" || missing="$missing $n"
    done
    [ -z "$missing" ] || log "WARNING: no passdb entry for:$missing -- they cannot authenticate over SMB (check the uid field in smbpasswd)"
}

# --- sync -----------------------------------------------------------------

# The sidecar's inject_logging, verbatim in intent: render_global always emits
# [global] as the literal first line, so the two directives go in right after
# it. fail2ban-samba-filter.conf needs auth_audit:3 to have anything to match.
inject_logging() {
    { head -n1 "$1"
      printf '  log file = /var/log/samba/log.smbd\n  log level = 1 auth_audit:3\n'
      tail -n +2 "$1"
    }
}

sync_once() {
    src="$CONFIG_DIR/smb.conf"
    [ -f "$src" ] || return 1

    candidate="$STATE_DIR/smb.conf.candidate"
    inject_logging "$src" > "$candidate"

    # Validate before it can reach smbd; a rejected candidate leaves the
    # running config alone (DEPLOYMENT.md §7.3, last line).
    if ! testparm -s "$candidate" > "$STATE_DIR/testparm.out" 2>&1; then
        log "rejected candidate smb.conf, keeping the previous config:"
        cat "$STATE_DIR/testparm.out" >&2
        return 1
    fi

    check_collisions || return 1
    check_groups || return 1

    # Back up whatever the distro's samba package shipped, once, so turning
    # this off is reversible.
    if [ -f "$SMB_CONF" ] && [ ! -f "$STATE_DIR/smb.conf.orig" ]; then
        cp "$SMB_CONF" "$STATE_DIR/smb.conf.orig"
    fi
    cp "$candidate" "$SMB_CONF"

    rebuild_passwd

    if [ -f "$CONFIG_DIR/smbpasswd" ]; then
        pdbedit -i "smbpasswd:$CONFIG_DIR/smbpasswd" -e "tdbsam:$PASSDB" >/dev/null
    fi
    prune_passdb
    verify_passdb
    return 0
}

# sc-server removes the rendered files when smb.enabled goes false, so their
# absence after a good sync is the off switch -- not merely "not synced yet".
teardown() {
    log "config removed: stopping $SERVICE and dropping managed accounts"
    svc stop || true
    prune_passdb
    rm -f "$CONFIG_DIR/passwd"
    rebuild_passwd
    if [ -f "$STATE_DIR/smb.conf.orig" ]; then
        cp "$STATE_DIR/smb.conf.orig" "$SMB_CONF"
    fi
}

fingerprint() {
    for f in smb.conf smbpasswd passwd; do
        if [ -f "$CONFIG_DIR/$f" ]; then
            printf '%s ' "$(cksum < "$CONFIG_DIR/$f")"
        else
            printf 'absent '
        fi
    done
}

# --- main -----------------------------------------------------------------

# --once applies the current config and exits without touching the samba
# service: the operator's "apply now", and the only way to exercise this on a
# host that has no service manager (a container, in the test harness).
ONCE=0
[ "${1:-}" = "--once" ] && ONCE=1

[ "$(id -u)" = 0 ] || die "must run as root (it edits /etc/passwd and the samba passdb)"
command -v testparm >/dev/null 2>&1 || die "samba is not installed (no testparm on PATH)"
command -v pdbedit  >/dev/null 2>&1 || die "samba is not installed (no pdbedit on PATH)"

SERVICE=$(detect_service)
mkdir -p "$STATE_DIR" "$(dirname "$PASSDB")" /var/log/samba
chmod 700 "$STATE_DIR"
if [ "$ONCE" = 1 ]; then
    if [ -f "$CONFIG_DIR/smb.conf" ]; then
        sync_once || die "sync failed"
        log "applied $CONFIG_DIR (single pass)"
    else
        teardown
    fi
    exit 0
fi

log "watching $CONFIG_DIR, samba service '$SERVICE', poll ${POLL}s"

last=''
started=0
while :; do
    now=$(fingerprint)
    if [ "$now" != "$last" ]; then
        if [ -f "$CONFIG_DIR/smb.conf" ]; then
            if sync_once; then
                if [ "$started" = 0 ]; then
                    svc start && started=1 || log "could not start $SERVICE"
                else
                    smbcontrol all reload-config >/dev/null 2>&1 || svc reload || true
                fi
                log "applied config change"
                last="$now"
            fi
        elif [ "$started" = 1 ] || [ -n "$last" ]; then
            teardown
            started=0
            last="$now"
        else
            last="$now"
        fi
    fi
    sleep "$POLL"
done
