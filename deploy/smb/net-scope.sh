# shellcheck shell=sh
# Network scope detection, shared by the sidecar entrypoint and the bare-metal
# agent. POSIX sh: the agent runs without bash.
#
# sc-core renders `interfaces = lo` / `hosts allow = 127.0.0.0/8 ::1/128` and
# nothing else, because it sits in its own network namespace and cannot see the
# host's devices. This runs where smbd runs, reads the interfaces that are
# actually there, and rewrites those two lines. An unexpanded config binds
# loopback and nowhere else, so a failure here closes SMB rather than opening
# it.
#
# The rule is: a network this machine is attached to is admitted, no
# configuration needed. `interfaces` gets the device, `hosts allow` gets the
# well-known private range enclosing its address -- the enclosing range and not
# the on-link prefix, because an internal network is routinely several subnets
# behind a router, and because tailscale0 carries a /32 whose own subnet admits
# nobody while 100.64.0.0/10 admits the whole tailnet.
#
# Globally routable addresses, IPv6 GUA included, are left out unless
# network.policy says allow_public_bind=1. And none of this runs at all when it
# says pinned_interfaces=1: the operator named the addresses in smb.interfaces,
# so sc-core rendered the final answer and there is nothing to detect.

# The enclosing well-known private range for an address, or nothing if it is
# globally routable. Case patterns rather than arithmetic: sh has no integers
# wide enough for v6 and no need for them here.
sc_scope_range() {
    case "$2" in
    4)
        case "$1" in
        10.*)      echo 10.0.0.0/8 ;;
        192.168.*) echo 192.168.0.0/16 ;;
        127.*)     echo 127.0.0.0/8 ;;
        169.254.*) echo 169.254.0.0/16 ;;
        172.1[6-9].*|172.2[0-9].*|172.3[01].*) echo 172.16.0.0/12 ;;
        # 100.64.0.0/10 is CGNAT, which is where every Tailscale address
        # lives: second octet 64..127.
        100.6[4-9].*|100.[7-9][0-9].*|100.1[01][0-9].*|100.12[0-7].*) echo 100.64.0.0/10 ;;
        esac
        ;;
    6)
        case "$1" in
        ::1)                     echo ::1/128 ;;
        [Ff][Cc]*:*|[Ff][Dd]*:*) echo fc00::/7 ;;
        [Ff][Ee]8*:*|[Ff][Ee]9*:*|[Ff][Ee][Aa]*:*|[Ff][Ee][Bb]*:*) echo fe80::/10 ;;
        esac
        ;;
    esac
}

# Append unless already present.
sc_scope_add() {
    case " $1 " in
    *" $2 "*) echo "$1" ;;
    *)        echo "$1 $2" ;;
    esac
}

# `device address family` per configured address on an up device, lo excluded.
# Both iproute2 and busybox `ip` print this shape, so nothing extra has to be
# installed. LOWER_UP must not be read as UP, hence the delimiters.
sc_scope_scan() {
    { ip addr show 2>/dev/null || true; } | awk '
        /^[0-9]+: / {
            dev = $2
            sub(/:$/, "", dev)
            sub(/@.*$/, "", dev)
            up = ($0 ~ /[<,]UP[,>]/)
            next
        }
        !up || dev == "lo" { next }
        $1 == "inet"  { split($2, a, "/"); print dev, a[1], 4 }
        $1 == "inet6" { split($2, a, "/"); print dev, a[1], 6 }
    '
}

# True when every device we can see is one end of a veth pair, i.e. we are
# inside a container network namespace and the addresses above describe the
# docker bridge rather than the host's networks. A veth's iflink names its peer
# in the other namespace; a physical device, a bridge and a tun all point at
# themselves.
sc_scope_is_namespaced() {
    _found=0
    for _d in $(printf '%s\n' "$1" | awk '{print $1}' | sort -u); do
        [ -n "$_d" ] || continue
        _found=1
        [ -r "/sys/class/net/$_d/iflink" ] || return 1
        if [ "$(cat "/sys/class/net/$_d/iflink")" = "$(cat "/sys/class/net/$_d/ifindex")" ]; then
            return 1
        fi
    done
    [ "$_found" = 1 ]
}

# Print the expanded `interfaces` list, then the expanded `hosts allow` list,
# one per line. $1 is 1 when allow_public_bind is set.
sc_scope_compute() {
    _allow_public=$1
    _scan=$(sc_scope_scan)
    _ifaces=lo
    _hosts="127.0.0.0/8 ::1/128"

    for _dev in $(printf '%s\n' "$_scan" | awk '{print $1}' | sort -u); do
        [ -n "$_dev" ] || continue
        _ok=''
        _rejected=0
        while read -r _d _addr _fam; do
            [ "$_d" = "$_dev" ] || continue
            _range=$(sc_scope_range "$_addr" "$_fam")
            if [ -n "$_range" ]; then
                _ok="$_ok $_addr"
                _hosts=$(sc_scope_add "$_hosts" "$_range")
            elif [ "$_allow_public" = 1 ]; then
                # The documented meaning of the opt-in is "SMB is on the
                # internet". Naming a subnet here would understate it.
                _ok="$_ok $_addr"
                _hosts=$(sc_scope_add "$_hosts" ALL)
            else
                _rejected=1
            fi
        done <<SC_SCOPE_EOF
$_scan
SC_SCOPE_EOF
        [ -n "$_ok" ] || continue
        # The device name survives a DHCP lease change; individual addresses do
        # not. Only fall back to addresses when the device also carries one we
        # are refusing, since naming the device there would bind that too.
        if [ "$_rejected" = 0 ]; then
            _ifaces="$_ifaces $_dev"
        else
            _ifaces="$_ifaces$_ok"
        fi
    done

    # In a container namespace the detected subnet is the docker bridge, and
    # LAN clients arrive through DNAT with their own source addresses, which
    # that subnet does not cover. Detection cannot see their networks from
    # here, so admit private space wholesale rather than deny them. Binding is
    # unaffected: only the veth exists to bind.
    if sc_scope_is_namespaced "$_scan"; then
        for _r in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 169.254.0.0/16 fc00::/7 fe80::/10; do
            _hosts=$(sc_scope_add "$_hosts" "$_r")
        done
    fi

    printf '%s\n%s\n' "$_ifaces" "$_hosts"
}

# True when the operator pinned smb.interfaces. The rendered scope lines are
# then final and detection has nothing to say about them.
sc_scope_pinned() {
    [ -f "$1" ] && grep -q '^pinned_interfaces=1' "$1"
}

# sc_scope_compute against the policy file sc-core writes. Two lines out:
# `interfaces`, then `hosts allow`. Also the cheap way to notice the host's
# networks have moved -- compare this against the previous run. Under a pin the
# answer is a constant, which is exactly right: nothing on the host can move a
# scope the operator wrote down.
sc_scope_current() {
    if sc_scope_pinned "$1"; then
        printf 'pinned\npinned\n'
        return 0
    fi
    _allow_public=0
    if [ -f "$1" ] && grep -q '^allow_public_bind=1' "$1"; then
        _allow_public=1
    fi
    sc_scope_compute "$_allow_public"
}

# Rewrite the two scope lines of an smb.conf on stdout. Substitution, not
# insertion: sc-core always renders both lines, and a file missing them is one
# we should not be widening anyway.
sc_scope_apply() {
    _conf=$1
    if sc_scope_pinned "$2"; then
        cat "$_conf"
        return 0
    fi
    _computed=$(sc_scope_current "$2")
    _ifaces=$(printf '%s\n' "$_computed" | sed -n 1p)
    _hosts=$(printf '%s\n' "$_computed" | sed -n 2p)

    awk -v ifaces="$_ifaces" -v hosts="$_hosts" '
        /^[[:space:]]*interfaces[[:space:]]*=/  { print "  interfaces = " ifaces; next }
        /^[[:space:]]*hosts allow[[:space:]]*=/ { print "  hosts allow = " hosts; next }
        { print }
    ' "$_conf"
}
