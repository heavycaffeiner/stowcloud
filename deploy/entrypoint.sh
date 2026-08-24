#!/bin/sh
# Reconcile the service account with PUID/PGID, then drop to it and exec the
# server.
#
# This runs as root and is the only thing in the image that does. Everything
# after the exec at the bottom runs as the unprivileged account, so a
# compromise of the server is not a compromise of the container.
#
# The uid is settable at run time because the folders a deployment mounts are
# somebody else's already: a NAS share owned by 1000 cannot be handed to a
# server that insists on being 65532, and asking an operator to rebuild the
# image to change a number is asking too much. PUID and PGID are the names the
# rest of the self-hosting ecosystem uses for exactly this.
set -eu

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"

# Refuse anything that is not a plain number before it reaches usermod, which
# would otherwise take a name and produce an account nobody asked for.
case "$PUID$PGID" in
    *[!0-9]*) echo "stowcloud: PUID and PGID must be numeric, got PUID=$PUID PGID=$PGID" >&2; exit 78 ;;
esac
if [ "$PUID" = 0 ] || [ "$PGID" = 0 ]; then
    echo "stowcloud: PUID/PGID 0 is root, which this server does not run as" >&2
    exit 78
fi

# The account already exists at the image's build-time ids. Move it only when
# asked for something else, so the common case does no work at all: that is
# also what lets `read_only: true` work, because rewriting these two files is
# the one step that has to write outside a volume.
#
# The fields are rewritten in place rather than through usermod, which would
# pull in the shadow package for two calls. Both values are known to be plain
# numbers by the check above, so there is nothing here to quote around.
cur_uid="$(id -u stowcloud)"
cur_gid="$(id -g stowcloud)"
if [ "$cur_gid" != "$PGID" ] || [ "$cur_uid" != "$PUID" ]; then
    if ! touch /etc/.sc-write-probe 2>/dev/null; then
        echo "stowcloud: PUID/PGID ask for $PUID:$PGID but the image was built as $cur_uid:$cur_gid," >&2
        echo "  and /etc is read-only so the account cannot be moved. Either drop" >&2
        echo "  read_only, or rebuild with --build-arg PUID=$PUID --build-arg PGID=$PGID." >&2
        exit 78
    fi
    rm -f /etc/.sc-write-probe
    # name:passwd:uid:gid:...
    sed -i "s|^stowcloud:x:[0-9]*:[0-9]*:|stowcloud:x:$PUID:$PGID:|" /etc/passwd
    sed -i "s|^stowcloud:x:[0-9]*:|stowcloud:x:$PGID:|" /etc/group
fi

# What the server owns, and nothing else.
#
# The data directory is this server's alone, so its ownership is ours to
# correct and an operator who changed PUID expects exactly that. The shares are
# deliberately absent: they are the operator's files, usually shared with other
# services, and a recursive chown on them cuts off whoever else was reading
# them and is not reversible from here. A share the server cannot write is
# reported as a refused share with the path in the message, which is a better
# outcome than silently taking ownership of somebody's media library.
#
# A directory that is already owned correctly is left alone, and one on a
# read-only mount is skipped rather than fatal: under `read_only: true` these
# paths are read-only unless a volume is mounted over them, and the image ships
# them owned by the build's uid, so there is nothing to fix in that case.
#
# -h so a symlink in the data directory is not followed out of it.
hand_over() {
    dir="$1"
    [ -d "$dir" ] || return 0
    [ "$(stat -c '%u:%g' "$dir")" = "$PUID:$PGID" ] && return 0
    if ! chown -h "$PUID:$PGID" "$dir" 2>/dev/null; then
        echo "stowcloud: $dir is not writable and is owned by $(stat -c '%u:%g' "$dir"), not $PUID:$PGID" >&2
        return 0
    fi
    find "$dir" -mindepth 1 -exec chown -h "$PUID:$PGID" {} + 2>/dev/null || true
}

# The data directory is this server's alone. The SMB render directory is
# written here and read by the sidecar.
hand_over /var/lib/stowcloud
hand_over /config/smb

# exec, so the server is PID 1 and receives the stop signal directly rather
# than through a shell that would not forward it.
exec su-exec "$PUID:$PGID" /stowcloud "$@"
