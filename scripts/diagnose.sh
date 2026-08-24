#!/usr/bin/env bash
# What the server thinks is wrong with itself, and what the container can see.
#
#   bash scripts/diagnose.sh [container-name]
#
# Reads only. Nothing here changes a setting, a file or a container.
set -u

C="${1:-${SC_CONTAINER:-sc}}"

say() { printf '\n== %s ==\n' "$1"; }

say "container"
docker inspect "$C" --format \
  'name={{.Name}}
state={{.State.Status}} exit={{.State.ExitCode}} restarts={{.RestartCount}}
health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}
user={{if .Config.User}}{{.Config.User}}{{else}}(image default){{end}}
readonly={{.HostConfig.ReadonlyRootfs}}
capdrop={{.HostConfig.CapDrop}} capadd={{.HostConfig.CapAdd}}' 2>&1 || {
    echo "no container named '$C'. Pass the name: bash scripts/diagnose.sh <name>"
    exit 1
}

say "environment the server got"
docker inspect "$C" --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null |
    grep -E '^(PUID|PGID|TZ)=' || echo "  PUID/PGID unset, so the image default applies"

# The uid the server runs as, which is not the uid `docker exec` gives you:
# exec enters as root, so every writability probe below has to be run as the
# server's own account or it answers about root instead.
SRV_UID=$(docker exec "$C" sh -c 'cat /proc/1/status 2>/dev/null | awk "/^Uid:/{print \$2}"' 2>/dev/null | tr -d '\r')
SRV_GID=$(docker exec "$C" sh -c 'cat /proc/1/status 2>/dev/null | awk "/^Gid:/{print \$2}"' 2>/dev/null | tr -d '\r')
SRV_UID=${SRV_UID:-1000}
SRV_GID=${SRV_GID:-1000}

# su-exec is in the image for exactly this drop; without it fall back to
# reporting as root and say so.
asme() { docker exec "$C" su-exec "$SRV_UID:$SRV_GID" sh -c "$1" 2>/dev/null; }

say "who the process actually runs as"
echo "  the server runs as ${SRV_UID}:${SRV_GID}"
echo "  (docker exec enters as root, so the probes below are run as that uid)"

say "the directories the server writes"
asme '
for d in /var/lib/stowcloud /config/smb; do
    [ -e "$d" ] || { echo "  $d MISSING"; continue; }
    printf "  %-22s owner=%s mode=%s " "$d" "$(stat -c %u:%g "$d")" "$(stat -c %a "$d")"
    if touch "$d/.probe" 2>/dev/null; then rm -f "$d/.probe"; echo "writable"; else echo "NOT WRITABLE  <-- an SMB save lands here"; fi
done'

say "the shares it was asked to serve"
asme '
for d in /shares/*; do
    [ -e "$d" ] || continue
    printf "  %-22s owner=%s mode=%s " "$d" "$(stat -c %u:%g "$d")" "$(stat -c %a "$d")"
    if touch "$d/.probe" 2>/dev/null; then rm -f "$d/.probe"; echo "writable"; else echo "NOT WRITABLE by the server"; fi
done'

say "health, as the server reports it"
docker exec "$C" /entrypoint.sh healthcheck /etc/stowcloud/sc.toml >/dev/null 2>&1 &&
    echo "  the probe passes" || echo "  the probe FAILS"
docker exec "$C" sh -c 'wget -qO- --no-check-certificate https://127.0.0.1:8443/api/health 2>/dev/null ||
                        curl -sk https://127.0.0.1:8443/api/health 2>/dev/null' 2>&1 | head -c 600
echo

say "app_hosts, which decides 421 vs 200"
docker exec "$C" sh -c 'sed -n "/^\[http\]/,/^\[/p" /etc/stowcloud/sc.toml' 2>&1 | head -8
echo "  A name you browse to that is not listed above answers 421, not 500."

say "the last refusals in the log"
docker logs --tail 400 "$C" 2>&1 |
    grep -iE 'error|refus|denied|degrad|panic|fail' | tail -25

say "restart history"
docker logs --tail 2000 "$C" 2>&1 | grep -c 'msg=listening' |
    sed 's/^/  times this process has started serving: /'
echo "  more than one, with a low uptime, means it is crash-looping:"
echo "  every restart is a window where a proxy gets a refused connection."
