# SMB Link-Scoped Access - Design

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-10                       |
| Status     | Superseded                       |
| Superseded by | [`../stowcloud-14-smb-and-oidc.md`](../stowcloud-14-smb-and-oidc.md) §4.3 |

---

## 1. Summary

This is a historical Rust implementation design, not an instruction for the
Go port. The parent proposal is authoritative. In particular, this document's
POSIX shell layout was replaced by the Rust `sc-smb-agent`, and its description
of the earlier core behaviour is not the baseline the Go port carries over.

Replace the fixed private-CIDR allow list with detection of the networks this machine is
actually attached to. The SMB sidecar (and its bare-metal twin) enumerates the host's
interface addresses, classifies each as internal or global, and expands the two Samba
directives that decide reach: `interfaces` and `hosts allow`. `sc-core` renders a
loopback-only baseline, so an un-expanded config is closed rather than open.

Internal networks need no configuration at all. Global addresses stay behind
`smb.allow_public_bind`, which keeps its meaning, its audit event and its admin banner.

## 2. What is wrong today

`crates/sc-smb/src/bind.rs` hardcodes eight CIDRs as "private" and `conf.rs` renders all of
them into both `interfaces` and `hosts allow`, on every host, regardless of what that host
is connected to. `SmbOrchestrator::validate_bind` then refuses to render at all if any
local address falls outside that set.

Four concrete failures follow.

- **`validate_bind` is all-or-nothing over the whole box.** A host with a public WAN
  address and a private LAN cannot render SMB config, even though smbd would only ever
  answer on the LAN. One public address anywhere blocks the feature entirely. This is the
  reported symptom: a machine that is plainly on an internal network is refused.
- **Real LANs are IPv6-global.** `fc00::/7` covers ULA, which almost no home or office
  network uses; a normal dual-stack LAN gets a `2xxx::/64` delegated prefix and is
  classified public.
- **`hosts allow` is unrelated to reality.** It admits `10.0.0.0/8` on a box that has never
  seen a 10.x address, and cannot admit anything the list did not anticipate.
- **The addresses are read in the wrong namespace.** `diagnostics::local_interface_addrs()`
  runs inside `sc-core`. On a bridged container that is `172.17.x`, not the host addresses
  smbd binds. The gate inspects one machine and constrains another.

## 3. Decisions taken

Two questions were put to the maintainer before design.

**On-link IPv6 GUA is opt-in, not auto-admitted.** A directly-attached globally-routable
`/64` is treated as global and requires `smb.allow_public_bind`. Dual-stack LANs keep
working over IPv4 with zero configuration, and 445 never quietly lands on the public
internet behind a router with no inbound filter.

**Detection runs in the sidecar.** Under `network_mode: host` the container sees the real
host devices, which is exactly what smbd binds. One source of truth, no namespace
mismatch, no second best-effort implementation in `sc-core`.

## 4. Model

A **scope** is one interface address together with its prefix length and its device name.
Scopes come from the host, are classified, and become directives.

### 4.1 Classification

Internal, admitted with no configuration:

| Family | Ranges |
|--------|--------|
| IPv4   | `10/8`, `172.16/12`, `192.168/16`, `100.64/10`, `169.254/16`, `127/8` |
| IPv6   | `fc00::/7`, `fe80::/10`, `::1/128` |

`100.64/10` is listed deliberately: Tailscale peers live there, and the maintainer expects
tailnet clients to mount without configuration.

Everything else is global.

### 4.2 From scope to directive

For each scope on an interface that is UP:

- **Internal** contributes its **enclosing well-known range** from the table above.
- **Global** contributes nothing unless `allow_public_bind` is set, and then contributes
  `ALL`.

`ALL` rather than the address's own on-link subnet, because the documented meaning of
`allow_public_bind` is "SMB is on the internet" and naming a `/64` would understate it.
Samba evaluates `hosts allow` before `hosts deny`, so the rendered `hosts deny = 0.0.0.0/0`
stays in place and stops mattering exactly when the operator has said it should.

The enclosing range rather than the on-link prefix is what makes routed internal networks
work. A server on `10.10.0.5/24` serves clients on `10.10.20.0/24` reached through a
router; admitting only the on-link `/24` would lock those clients out, which is the exact
complaint this design answers. It also solves Tailscale without reading a routing table:
`tailscale0` holds a `/32`, whose on-link subnet admits nobody, but whose enclosing range
`100.64.0.0/10` admits the whole tailnet.

The result is strictly narrower than today (a box with only `192.168.x` no longer admits
`10/8`) and never narrower than what the box is attached to.

### 4.3 What gets bound

`interfaces` names what smbd listens on, and must not silently include a public address.

- If **every** address on a device is admitted, emit the **device name**. It survives a
  DHCP lease change without a re-render.
- If the device carries a mix, emit the **admitted addresses** individually.

`lo` is always emitted.

### 4.4 Bridged containers

`network_mode: host` is what the compose file ships and what §3 decided on, but a bridged
container still has to work: the CI smoke test runs one, and so does anyone who publishes
445 with `-p`. There, detection sees a single veth on `172.17.x` and nothing else, while
LAN clients arrive through DNAT carrying their real source addresses. Those addresses are
in networks this namespace cannot see, and §4.2 would refuse every one of them.

So detection asks whether it is in a namespace at all. Every device it can see is checked
against `/sys/class/net/$dev/iflink`: a veth's `iflink` names its peer in another
namespace, while a physical device, a bridge and a tun all point at their own `ifindex`.
When they are all veths, `hosts allow` gets the full internal list from §4.1 rather than
just the detected range.

Binding is untouched. Only the veth exists to bind, so §4.3 still emits exactly what is
there and the widening is confined to the source-address filter.

### 4.5 Rendered shape

`sc-core` renders a closed baseline:

```
  # ── network scope ──
  # Expanded by the sc-smb sidecar/agent from the host's own interfaces.
  # Left as-is, SMB answers on loopback only.
  bind interfaces only = yes
  interfaces = lo
  hosts allow = 127.0.0.0/8 ::1/128
  hosts deny = 0.0.0.0/0
```

The sidecar rewrites the `interfaces` and `hosts allow` lines in the candidate file before
`testparm -s`, the same way `inject_logging` already inserts logging directives. A typical
expansion:

```
  interfaces = lo eth0 tailscale0
  hosts allow = 127.0.0.0/8 ::1/128 10.0.0.0/8 100.64.0.0/10 fe80::/10
```

A failed or absent expansion leaves loopback-only SMB. Failure is closed.

### 4.6 Policy handoff

`sc-core` writes one more file into the shared config directory:

```
# config_dir/network.policy
allow_public_bind=0
pinned_interfaces=0
```

The sidecar reads it and admits global scopes only when `allow_public_bind` is `1`.
`testparm` strips comments from its output, so the flags cannot travel inside `smb.conf`; a
separate file is also easier to assert on in a test. A missing file means both are `0`.

### 4.6.1 The operator override

`smb.interfaces` in `sc.toml` names the addresses smbd binds outright. It is the off
switch for everything above: `sc-core` renders the operator's list plus the full internal
`hosts allow`, sets `pinned_interfaces=1`, and detection returns without touching the
file.

This does not reintroduce what §2 complains about. Detection stays the default and needs
no configuration; the pin is opt-in narrowing by someone who already knows which address
they want served. Because the operator wrote the address down rather than the machine
reporting it, it is the one bind decision `sc-core` can check, and a globally routable
entry there is refused unless `allow_public_bind` is set.

`hosts allow` stays wide under a pin on purpose. Narrowing what smbd binds must not also
narrow who may reach the address it bound: the pin answers "which interface", not "which
clients".

### 4.7 Detection mechanics

`ip addr show`, parsed with awk. Both real iproute2 and busybox `ip` produce the same
shape, so nothing new is installed in the Alpine image and the dependency-free native
agent keeps working:

```
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 ...
    inet 192.168.1.10/24 brd 192.168.1.255 scope global eth0
    inet6 fe80::1/64 scope link
```

The header line sets the current device and its UP flag; `inet`/`inet6` lines emit
`device addr/prefix`. Non-UP devices and `lo` are dropped.

The detection logic lives in one POSIX-sh file, `deploy/smb/net-scope.sh`, sourced by both
`deploy/smb/entrypoint.sh` and `deploy/smb/native/sc-smb-agent.sh`. `Dockerfile.smb` COPYs
it; `deploy/smb/native/install.sh` installs it beside the agent.

### 4.8 Re-detection

An interface can appear after startup: Tailscale connecting, a VPN dialling, a VLAN coming
up. Today the sidecar only re-syncs when a config file changes.

The computed scope is folded into the sync fingerprint, and the sidecar's `inotifywait`
gains a 60-second timeout so the loop wakes even when no file changed. A new interface
therefore reaches smbd within a minute without operator action.

## 5. Changes in `sc-core`

`validate_bind` is the "ban by source address" structure being removed. `sc-core` no longer
has authority over what gets bound, so a gate there can only be wrong.

- `SmbOrchestrator::validate_bind` and `bind::public_addrs` are deleted, along with their
  call sites in `sc-server/src/lib.rs`, `smb_cmd.rs` (`run` and `render_live`).
- `SmbError::PublicBindRefused` is deleted.
- `bind::PRIVATE_CIDRS_V4` / `PRIVATE_CIDRS_V6` are deleted; `conf.rs` renders the baseline
  instead.
- `bind::is_private` stays. It is the shared definition of §4.1 and it is public API.
- `public_bind_warning` stays but is latched at render time whenever
  `allow_public_bind == true`, rather than when a public address happens to be present.
  The banner and the audit event then say what is true and knowable in `sc-core`: the
  operator has opted in. The settings API DTO is unchanged.
- `diagnostics::local_interface_addrs()` loses its only caller and is deleted, both the
  `getifaddrs` implementation and the non-Linux stub.

The 422 `SmbConfigRefused` path documented in the proposal's §5-2 loses its
public-bind trigger. The other refusals (invalid share, invalid server name) keep it.

## 6. What this gives up

Rendering `smb.conf` no longer fails when the host is exposed. Exposure is now decided
where binding happens, so a misconfigured sidecar that skips expansion serves loopback
only, and a sidecar that expands correctly never binds a global address without the flag.
The loss is the early, loud refusal at render time; the gain is that a dual-homed host
works at all.

An operator who points smbd directly at `sc-core`'s output, with neither sidecar nor
agent, gets loopback-only SMB where they previously got the private-CIDR list. That path
was never supported and now fails closed.
