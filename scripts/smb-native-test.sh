#!/usr/bin/env bash
# Exercise sc-smb-agent on each distro the bare-metal path claims to support.
#
# Run on a Docker host. Each distro gets a throwaway container with nothing
# but its own samba package, which is the premise of that path -- no
# inotify-tools, no shadow-utils on Alpine.
#
# What it covers: config install, /etc/passwd reconciliation via the
# sc-managed-smb marker, smbpasswd import, a real authenticated round-trip
# over SMB3, teardown, the two refusals (name collision, missing group), and
# the service glue -- install.sh, the init file it lays down, and the watch
# loop that replaces --once in production (section 6).
# What it does not: systemd actually executing the unit. Booting systemd in a
# container needs --privileged and cgroup mounts; section 6 substitutes
# `systemd-analyze verify` there and runs the real thing under OpenRC.
#
# The [global] block comes from real `stowcloud smb-sync` output rather than
# being hand-written, so a renderer change that breaks the agent's
# first-line-is-[global] assumption fails this test instead of production.
#
#   scripts/smb-native-test.sh [--template /opt/sc/prod/smbcfg/smb.conf] [--keep]
set -euo pipefail

TEMPLATE=/opt/sc/prod/smbcfg/smb.conf
KEEP=0
IMAGES=${SC_SMB_TEST_IMAGES:-"rockylinux:9 debian:12 alpine:3.20"}
PASSWORD='native-agent-test-2026'

while [ $# -gt 0 ]; do
    case "$1" in
        --template) TEMPLATE=$2; shift 2 ;;
        --keep)     KEEP=1; shift ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done

REPO=$(cd "$(dirname "$0")/.." && pwd)
[ -f "$TEMPLATE" ] || { echo "no rendered smb.conf at $TEMPLATE (run 'stowcloud smb-sync' first, or pass --template)" >&2; exit 1; }

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# One statically linked binary for all three distros. glibc and musl images
# share the mount, so a dynamically linked build would fail on Alpine only,
# which is the least useful place to find out. CGO_ENABLED=0 is what makes it
# static, and it is written out rather than left to the default: go turns cgo
# on whenever a C compiler is on PATH.
mkdir -p "$WORK/bin"
echo "building sc-smb-agent"
( cd "$REPO/go" && CGO_ENABLED=0 GOOS=linux go build -o "$WORK/bin/sc-smb-agent" ./cmd/sc-smb-agent ) || {
    echo "could not build the agent" >&2
    exit 1
}
chmod 755 "$WORK/bin/sc-smb-agent"
if ldd "$WORK/bin/sc-smb-agent" 2>/dev/null | grep -q '=>'; then
    echo "the agent came out dynamically linked; Alpine would fail on it" >&2
    exit 1
fi

# Keep the real [global] verbatim; render_global emits it as the literal
# first line and stops at the first share section.
awk 'NR==1 && $0 !~ /^\[global\]/ { exit 1 } /^\[/ && NR>1 { exit } { print }' \
    "$TEMPLATE" > "$WORK/global.conf" || {
        echo "$TEMPLATE does not start with [global]; not renderer output?" >&2; exit 1; }

mkdir -p "$WORK/cfg"
{
    cat "$WORK/global.conf"
    cat <<'SHARE'

[test]
  path = /shares/test
  valid users = alice
  read list =
  write list = alice
  create mask = 0664
  directory mask = 0775
  veto files = /.sctrash/.scpart-*/.scmeta/.scindex/
  delete veto files = no
SHARE
} > "$WORK/cfg/smb.conf"

# Exactly what the server's passwd renderer produces: the account's own uid
# (smb.service_uid + its row id, so 1000 + 1 here) on the shared service gid.
# Not the service uid itself -- that is scsvc's, and the agent now refuses a
# rendered uid that belongs to an account it does not manage.
echo 'alice:x:1001:1000::/nonexistent:/usr/sbin/nologin' > "$WORK/cfg/passwd"

cat > "$WORK/run.sh" <<'INNER'
#!/bin/sh
set -eu
fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }

# Poll a shell predicate for up to N seconds. The agent's watch loop reacts on
# its own clock (SC_SMB_POLL_SECONDS), so section 6 has nothing to synchronise
# against except the effect it is waiting for.
wait_for() {
    i=0
    while [ "$i" -lt "$2" ]; do
        eval "$1" && return 0
        i=$((i + 1))
        sleep 1
    done
    return 1
}

. /etc/os-release
case "$ID" in
  rocky|rhel|centos)
    dnf -y -q install samba samba-client samba-common-tools >/dev/null
    groupadd -g 1000 scsvc
    useradd -u 1000 -g 1000 -M -s /sbin/nologin scsvc
    ;;
  debian|ubuntu)
    export DEBIAN_FRONTEND=noninteractive
    apt-get -qq update >/dev/null
    apt-get -qq -y install samba smbclient >/dev/null
    groupadd -g 1000 scsvc
    useradd -u 1000 -g 1000 -M -s /usr/sbin/nologin scsvc
    ;;
  alpine)
    apk add --quiet samba samba-client samba-common-tools >/dev/null
    addgroup -g 1000 scsvc
    adduser -u 1000 -G scsvc -H -D -s /sbin/nologin scsvc
    ;;
  *) fail "unhandled distro $ID" ;;
esac
echo "  distro: $ID $VERSION_ID"

# 0775, not 0755 -- under `force user` Samba checks the NT ACL with the
# authenticated user's SID, which is not the owner, so 0755 makes the share
# silently read-only.
mkdir -p /shares/test
chown scsvc:scsvc /shares/test
chmod 0775 /shares/test

# install.sh is invoked through `sh` rather than executed: the repo is mounted
# from whatever tree the Docker host happens to hold, and every
# sftp/zip/CI-artefact route into that host drops the executable bit --
# `vm.mjs push` lands the whole tree 0644, which failed the first apply here
# with nothing louder than a suppressed "Permission denied".
#
# The agent cannot go through `sh`: it is a binary now. The caller builds it
# statically against musl and mounts it at /agent/bin with the mode it needs,
# so it runs on all three distros regardless of their libc.
AGENT=/agent/bin/sc-smb-agent
INSTALL="sh /agent/install.sh"
export SC_SMB_CONFIG_DIR=/config/smb

# Section 5 empties the config dir on its way out; section 6 needs something
# for the daemon to apply, so keep the rendered files as they arrived.
CFG_BACKUP=/tmp/cfg.orig
mkdir -p "$CFG_BACKUP"
cp "$SC_SMB_CONFIG_DIR"/* "$CFG_BACKUP/"

# --- 1. first apply: installs smb.conf, creates alice ---------------------
# Output kept for this one: it is the first time the agent runs at all, so a
# problem with the mount, the image or the config lands here rather than on
# any of the assertions below, and swallowing it cost a debugging round.
$AGENT --once > /tmp/apply.out 2>&1 || { cat /tmp/apply.out >&2; fail "first apply"; }
grep -q '^alice:' /etc/passwd || fail "alice not created in /etc/passwd"
grep -q '^alice:.*:sc-managed-smb:' /etc/passwd || fail "alice missing the sc-managed-smb marker"
grep -q 'log level = 1 auth_audit:3' /etc/samba/smb.conf || fail "logging directives not injected"
head -n1 /etc/samba/smb.conf | grep -q '^\[global\]' || fail "installed smb.conf lost its [global] first line"
ok "config installed, alice created and marked"

# --- 2. produce a genuine smbpasswd file, then re-import it ---------------
# Uses samba's own exporter rather than a hand-written NT hash, so the format
# under test is the one sc-auth::export_smbpasswd targets.
printf '%s\n%s\n' "$SC_PASSWORD" "$SC_PASSWORD" | smbpasswd -s -a alice >/dev/null
# Samba refuses to read an smbpasswd file that is not 0600, and pdbedit -e
# creates it 0644. The renderer already writes 0600, so this only
# reproduces production's mode rather than working around anything.
touch "$SC_SMB_CONFIG_DIR/smbpasswd"
chmod 600 "$SC_SMB_CONFIG_DIR/smbpasswd"
pdbedit -e "smbpasswd:$SC_SMB_CONFIG_DIR/smbpasswd" >/dev/null

# pdbedit -e writes uid 0 into field 2, where the credential export writes
# the smb.service_uid (§7.2 shows alice:1000:...). Importing the uid-0 form is
# a silent no-op -- so check the agent notices, then rewrite the field to the
# shape production actually renders.
#
# Note what this rewrite can and cannot prove. It fixes up *pdbedit's* export,
# not sc-auth's, so it says nothing about which uid sc-auth actually writes --
# and until 2026-08-03 sc-auth wrote a bare row id, which meant this line was
# quietly repairing a real bug on every run instead of failing on it. The
# renderer's own output is pinned by
# the smbpasswd uid rule in `go/internal/auth` (base plus row id, all distinct)
# and by the first-run integration test; keep this script's scope to the agent.
rm -f /var/lib/samba/private/passdb.tdb
$AGENT --once 2>&1 | grep -q 'WARNING: no passdb entry for: alice' \
    || fail "a silently-empty passdb import went unreported"
ok "silently-empty passdb import reported"

awk -F: 'BEGIN { OFS=":" } { $2 = 1001; print }' \
    "$SC_SMB_CONFIG_DIR/smbpasswd" > "$SC_SMB_CONFIG_DIR/.smbpasswd.tmp"
mv "$SC_SMB_CONFIG_DIR/.smbpasswd.tmp" "$SC_SMB_CONFIG_DIR/smbpasswd"
chmod 600 "$SC_SMB_CONFIG_DIR/smbpasswd"

rm -f /var/lib/samba/private/passdb.tdb
$AGENT --once >/dev/null 2>&1 || fail "re-apply with smbpasswd"
pdbedit -L 2>/dev/null | grep -q '^alice:' || fail "alice not imported into the passdb"
ok "smbpasswd imported into tdbsam"

# --- 3. real authenticated round-trip -------------------------------------
smbd -D
sleep 2
head -c 65536 /dev/urandom > /tmp/rt.bin
smbclient //127.0.0.1/test -U "alice%$SC_PASSWORD" \
    -c 'put /tmp/rt.bin rt.bin' >/dev/null 2>&1 || fail "SMB write denied"
smbclient //127.0.0.1/test -U "alice%$SC_PASSWORD" \
    -c 'get rt.bin /tmp/rt.back' >/dev/null 2>&1 || fail "SMB read denied"
[ "$(sha256sum < /tmp/rt.bin)" = "$(sha256sum < /tmp/rt.back)" ] || fail "round-trip not byte-identical"
ok "SMB3 round-trip byte-identical"

smbclient //127.0.0.1/test -U 'alice%wrong-password' -c 'ls' >/dev/null 2>&1 \
    && fail "a wrong password was accepted" || ok "wrong password refused"

# --- 4. refusals ----------------------------------------------------------
# Each entry below uses a uid inside the managed range (1000 + row id), so
# every refusal here fires for the reason it claims. Reusing the service uid
# would trip the uid-collision check first -- the same "refused", but proving
# nothing about the name or the group.
echo 'bob:x:4242:4242::/home/bob:/bin/sh' >> /etc/passwd
echo 'bob:x:1002:1000::/nonexistent:/usr/sbin/nologin' >> "$SC_SMB_CONFIG_DIR/passwd"
$AGENT --once >/dev/null 2>&1 && fail "synced despite a collision with a system account"
grep -q '^bob:x:4242:' /etc/passwd || fail "the pre-existing bob was modified"
ok "name collision with a system account refused, bob untouched"

sed -i '/^bob:/d' "$SC_SMB_CONFIG_DIR/passwd"
echo 'carol:x:1003:9999::/nonexistent:/usr/sbin/nologin' >> "$SC_SMB_CONFIG_DIR/passwd"
$AGENT --once >/dev/null 2>&1 && fail "synced despite gid 9999 having no group"
ok "missing group refused"
sed -i '/^carol:/d' "$SC_SMB_CONFIG_DIR/passwd"

# The uid guard itself: scsvc owns 1000 and the agent did not create it, so a
# rendered entry claiming that uid must be refused. Without this, `pdbedit -i`
# would resolve the line by uid and import the credential under scsvc's name.
echo 'dave:x:1000:1000::/nonexistent:/usr/sbin/nologin' >> "$SC_SMB_CONFIG_DIR/passwd"
$AGENT --once >/dev/null 2>&1 && fail "synced despite claiming a non-managed account's uid"
ok "uid collision with a system account refused"
sed -i '/^dave:/d' "$SC_SMB_CONFIG_DIR/passwd"

# --- 5. teardown: the settings-screen off switch --------------------------
rm -f "$SC_SMB_CONFIG_DIR/smb.conf" "$SC_SMB_CONFIG_DIR/smbpasswd" "$SC_SMB_CONFIG_DIR/passwd"
$AGENT --once >/dev/null 2>&1 || true
grep -q '^alice:' /etc/passwd && fail "alice survived teardown"
pdbedit -L 2>/dev/null | grep -q '^alice:' && fail "alice survived teardown in the passdb"
grep -q '^bob:x:4242:' /etc/passwd || fail "teardown removed a non-managed account"
ok "teardown dropped managed accounts and left system accounts alone"

# --- 6. the service glue --------------------------------------------------
# Everything above ran the agent with --once, which is the operator's "apply
# now": no init file, no install.sh, and none of the watch loop that is what
# actually runs in production. This section installs the service the way a
# real host would and drives it through its own front door.
#
# The two inits are not covered to the same depth, and cannot be: OpenRC
# starts from a plain container, so Alpine gets a real start/apply/teardown/
# stop. Booting systemd needs --privileged plus cgroup mounts, which is a
# steep price for the whole suite, so the systemd hosts get install.sh with
# systemctl shimmed plus `systemd-analyze verify`, which reads the unit
# offline. What that leaves untested is systemd executing the unit -- not the
# unit itself, and not our half of the install.
cp "$CFG_BACKUP"/* "$SC_SMB_CONFIG_DIR/"
# Section 3's smbd is still holding 445; the agent is about to ask the init
# system to start samba, and two of them fighting over the port is noise the
# assertions below would have to explain away.
pkill smbd 2>/dev/null || true

case "$ID" in
  alpine)
    apk add --quiet openrc >/dev/null
    # A container has never booted an init, so OpenRC has to be told three
    # things before `rc-service` does anything but warn. None of them are
    # about our service; all three were arrived at by watching it fail.
    #  - rc_sys=lxc: without it OpenRC tries to manage kernel state it cannot
    #    reach here (it still warns about the read-only /sys/fs/cgroup, which
    #    is noise, not a failure). Appended rather than sed'd over the
    #    commented default, which is one fewer thing to keep matching.
    #  - the state directories and softlevel: absent, every call answers
    #    "already starting" and then reports the service stopped.
    #  - /etc/network/interfaces: `depend() { need net; }` resolves to
    #    OpenRC's `networking`, which refuses to start with nothing to
    #    configure -- and refusing takes our service down with it. Giving it
    #    loopback means the dependency is really resolved rather than faked.
    printf 'rc_sys="lxc"\n' >> /etc/rc.conf
    printf 'auto lo\niface lo inet loopback\n' > /etc/network/interfaces
    mkdir -p /run/openrc/started /run/openrc/starting /run/openrc/stopping \
             /run/openrc/inactive /run/openrc/failed /run/openrc/exclusive \
             /run/openrc/daemons /run/openrc/options
    touch /run/openrc/softlevel

    $INSTALL --config-dir "$SC_SMB_CONFIG_DIR" --service-user scsvc \
        --binary /agent/bin/sc-smb-agent >/dev/null \
        || fail "install.sh (OpenRC) failed"
    [ -x /etc/init.d/sc-smb-agent ] || fail "init script not installed"
    grep -q "SC_SMB_CONFIG_DIR=$SC_SMB_CONFIG_DIR" /etc/conf.d/sc-smb-agent \
        || fail "install.sh did not write the config-dir override"
    ok "install.sh installed and started the OpenRC service"

    # conf.d is install.sh's own output, so the poll override can only go in
    # after it; the restart proves that file is sourced, at the same time.
    printf 'export SC_SMB_POLL_SECONDS=1\n' >> /etc/conf.d/sc-smb-agent
    rc-service sc-smb-agent restart >/dev/null 2>&1 || fail "rc-service restart failed"

    wait_for 'grep -q "^alice:" /etc/passwd' 20 \
        || { cat /var/log/sc-smb-agent.log >&2 || true; fail "the service never applied the config"; }
    rc-service sc-smb-agent status >/dev/null 2>&1 || fail "service reports itself stopped"
    ok "the service applied the config through its watch loop"

    # The settings-screen off switch again, this time reaching the daemon
    # through the poll loop rather than a fresh --once process.
    rm -f "$SC_SMB_CONFIG_DIR/smb.conf" "$SC_SMB_CONFIG_DIR/passwd"
    wait_for '! grep -q "^alice:" /etc/passwd' 20 \
        || { cat /var/log/sc-smb-agent.log >&2 || true; fail "the service never tore down"; }
    ok "the service tore down when the config disappeared"

    $INSTALL --uninstall >/dev/null || fail "install.sh --uninstall failed"
    [ -e /etc/init.d/sc-smb-agent ] && fail "the init script survived uninstall"
    [ -e /etc/conf.d/sc-smb-agent ] && fail "the config-dir override survived uninstall"
    # By process, not by pidfile: `command_background` leaves the pidfile
    # empty under rc_sys=lxc, and a stopped service that is still running is
    # exactly what this has to catch.
    pgrep -f sc-smb-agent >/dev/null 2>&1 && fail "the agent survived uninstall"
    ok "install.sh --uninstall stopped the service and removed its files"
    ;;

  rocky|rhel|centos|debian|ubuntu)
    case "$ID" in
      debian|ubuntu) apt-get -qq -y install systemd >/dev/null ;;
      *)             dnf -y -q install systemd >/dev/null ;;
    esac

    # Shimmed, not faked away: `daemon-reload` and `enable --now` are systemd's
    # own code and need a booted system, but *that install.sh calls them* is
    # ours to get right, and a shim is the only way to see it from here.
    mkdir -p /tmp/shim
    cat > /tmp/shim/systemctl <<'SHIM'
#!/bin/sh
echo "$*" >> /tmp/systemctl.log
SHIM
    chmod +x /tmp/shim/systemctl

    PATH=/tmp/shim:$PATH $INSTALL \
        --config-dir "$SC_SMB_CONFIG_DIR" --service-user scsvc \
        --binary /agent/bin/sc-smb-agent >/dev/null \
        || fail "install.sh (systemd) failed"
    grep -q 'daemon-reload' /tmp/systemctl.log || fail "install.sh never reloaded the unit"
    grep -q 'enable --now sc-smb-agent.service' /tmp/systemctl.log \
        || fail "install.sh never enabled the service"
    grep -q "SC_SMB_CONFIG_DIR=$SC_SMB_CONFIG_DIR" \
        /etc/systemd/system/sc-smb-agent.service.d/config-dir.conf \
        || fail "install.sh did not write the config-dir drop-in"
    ok "install.sh installed the unit and its config-dir drop-in"

    # The unit names the agent's path and install.sh chooses it, independently.
    # Nothing else would notice the two drifting apart.
    exec_start=$(sed -n 's/^ExecStart=//p' /etc/systemd/system/sc-smb-agent.service)
    [ -x "$exec_start" ] || fail "ExecStart=$exec_start is not where install.sh put the agent"
    ok "ExecStart matches the installed agent"

    # Offline, so it needs no init: parses the unit against *this distro's*
    # systemd and rejects a directive that version does not know.
    systemd-analyze verify /etc/systemd/system/sc-smb-agent.service > /tmp/verify.out 2>&1 \
        || { cat /tmp/verify.out >&2; fail "systemd-analyze rejected the unit"; }
    ok "systemd-analyze accepts the unit and its drop-in"

    PATH=/tmp/shim:$PATH $INSTALL --uninstall >/dev/null \
        || fail "install.sh --uninstall failed"
    [ -e /etc/systemd/system/sc-smb-agent.service ] && fail "the unit survived uninstall"
    [ -e /etc/systemd/system/sc-smb-agent.service.d ] && fail "the drop-in survived uninstall"
    ok "install.sh --uninstall removed the unit and its drop-in"
    ;;

  *) fail "unhandled distro $ID" ;;
esac

echo "PASS"
INNER
chmod +x "$WORK/run.sh"

rc=0
for image in $IMAGES; do
    name="sc-smb-native-$(echo "$image" | tr ':/.' '---')"
    echo "=== $image ==="
    docker rm -f "$name" >/dev/null 2>&1 || true
    # Fresh copy of the config per image: the run mutates it.
    rm -rf "$WORK/cfg-run"; cp -r "$WORK/cfg" "$WORK/cfg-run"
    if docker run --rm --name "$name" \
        -e "SC_PASSWORD=$PASSWORD" \
        -v "$REPO/deploy/smb/native:/agent:ro" \
        -v "$WORK/bin:/agent/bin:ro" \
        -v "$WORK/cfg-run:/config/smb" \
        -v "$WORK/run.sh:/run.sh:ro" \
        "$image" /run.sh
    then
        echo "=== $image: PASS ==="
    else
        echo "=== $image: FAIL ===" >&2
        rc=1
    fi
done

[ "$KEEP" = 1 ] && trap - EXIT && echo "workdir kept at $WORK"
exit $rc
