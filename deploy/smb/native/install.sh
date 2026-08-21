#!/bin/sh
# Install sc-smb-agent on a bare-metal host.
#
# Installs the agent and its init file, and nothing else: it does not install
# samba, create the service account, or turn SMB on. Those are the operator's
# and the settings screen's, respectively.
#
#   ./install.sh [--config-dir DIR] [--service-user NAME] [--binary PATH]
#                [--uninstall]
set -eu

CONFIG_DIR=/var/lib/sc/smbcfg
SERVICE_USER=scsvc
BINARY=
UNINSTALL=0

while [ $# -gt 0 ]; do
    case "$1" in
        --config-dir)   CONFIG_DIR=$2; shift 2 ;;
        --service-user) SERVICE_USER=$2; shift 2 ;;
        --binary)       BINARY=$2; shift 2 ;;
        --uninstall)    UNINSTALL=1; shift ;;
        -h|--help)      sed -n '2,10p' "$0"; exit 0 ;;
        *)              echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done

SRC=$(cd "$(dirname "$0")" && pwd)
AGENT=/usr/local/bin/sc-smb-agent

# The agent is a compiled binary now, not a shell script that could be copied
# out of this directory. Look where a release artifact and a source build each
# land, in that order.
if [ -z "$BINARY" ]; then
    for candidate in \
        "$SRC/sc-smb-agent" \
        "$SRC/../../../go/sc-smb-agent"
    do
        [ -x "$candidate" ] || continue
        BINARY=$candidate
        break
    done
fi

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }

if command -v systemctl >/dev/null 2>&1; then
    INIT=systemd
elif command -v rc-update >/dev/null 2>&1; then
    INIT=openrc
else
    echo "no systemd and no OpenRC: install the sc-smb-agent binary by hand" >&2
    exit 1
fi

if [ "$UNINSTALL" = 1 ]; then
    if [ "$INIT" = systemd ]; then
        systemctl disable --now sc-smb-agent.service 2>/dev/null || true
        rm -f /etc/systemd/system/sc-smb-agent.service
        # The --config-dir drop-in too: left behind, it silently re-points a
        # later reinstall at the old directory.
        rm -rf /etc/systemd/system/sc-smb-agent.service.d
        systemctl daemon-reload
    else
        rc-update del sc-smb-agent default 2>/dev/null || true
        rc-service sc-smb-agent stop 2>/dev/null || true
        rm -f /etc/init.d/sc-smb-agent /etc/conf.d/sc-smb-agent
    fi
    rm -f "$AGENT"
    # Left over from the shell version this replaced.
    rm -f /usr/local/lib/sc/sc-smb-agent.sh /usr/local/lib/sc/net-scope.sh
    echo "sc-smb-agent removed. Managed accounts and the passdb were left"
    echo "alone -- disable SMB in the settings screen first if you want the"
    echo "agent to tear those down."
    exit 0
fi

# Samba has to be there already: the whole premise of this path is "only the
# samba package present", so a missing one is the operator's step, not ours.
command -v smbd >/dev/null 2>&1 || {
    echo "samba is not installed. Install it first:" >&2
    echo "  Rocky/RHEL:   dnf install samba" >&2
    echo "  Debian/Ubuntu: apt install samba" >&2
    echo "  Alpine:        apk add samba" >&2
    exit 1
}

# `force user`/`force group` in the rendered smb.conf name this account, and
# the agent refuses to sync if the gid it is told to use has no group.
awk -F: -v u="$SERVICE_USER" '$1 == u { found = 1 } END { exit !found }' /etc/passwd || {
    echo "service account '$SERVICE_USER' does not exist. Create it first, e.g." >&2
    echo "  useradd --system --no-create-home --shell /sbin/nologin $SERVICE_USER" >&2
    exit 1
}

[ -n "$BINARY" ] && [ -x "$BINARY" ] || {
    echo "no sc-smb-agent binary found. Build it first:" >&2
    echo "  cd go && CGO_ENABLED=0 go build ./cmd/sc-smb-agent" >&2
    echo "or point at one: ./install.sh --binary /path/to/sc-smb-agent" >&2
    exit 1
}

mkdir -p "$CONFIG_DIR" /run/sc-smb
# sc-server writes here as its own (unprivileged) user and the agent reads it
# as root; smbpasswd inside carries NT hashes, so nothing else may look.
chmod 700 "$CONFIG_DIR"
install -m 755 "$BINARY" "$AGENT"

if [ "$INIT" = systemd ]; then
    install -m 644 "$SRC/sc-smb-agent.service" /etc/systemd/system/sc-smb-agent.service
    if [ "$CONFIG_DIR" != /var/lib/sc/smbcfg ]; then
        mkdir -p /etc/systemd/system/sc-smb-agent.service.d
        printf '[Service]\nEnvironment=SC_SMB_CONFIG_DIR=%s\n' "$CONFIG_DIR" \
            > /etc/systemd/system/sc-smb-agent.service.d/config-dir.conf
    fi
    systemctl daemon-reload
    systemctl enable --now sc-smb-agent.service
    echo "installed; follow it with: journalctl -fu sc-smb-agent"
else
    install -m 755 "$SRC/sc-smb-agent.openrc" /etc/init.d/sc-smb-agent
    if [ "$CONFIG_DIR" != /var/lib/sc/smbcfg ]; then
        printf 'export SC_SMB_CONFIG_DIR=%s\n' "$CONFIG_DIR" > /etc/conf.d/sc-smb-agent
    fi
    rc-update add sc-smb-agent default
    rc-service sc-smb-agent start
    echo "installed; follow it with: tail -f /var/log/sc-smb-agent.log"
fi

echo
echo "Point sc.toml at the same directory and enable SMB from the settings"
echo "screen (or run 'sc-server smb-sync'):"
echo "  [smb]"
echo "  config_dir = \"$CONFIG_DIR\""
