#!/usr/bin/env bash
# Let the tailnet reach the development server.
#
#   sudo bash scripts/dev-open-firewall.sh
#
# It needs root, which is why it is a separate script rather than part of
# dev.sh: everything in that one runs as you, and a script that asks for a
# password partway through is one nobody can read the effects of first.
#
# What it does and why, so this can be checked before it is run:
#
# firewalld puts an interface it does not recognise into the default zone,
# which here is `public`, and public allows only SSH. tailscale0 is in no zone
# at all, so every tailnet packet to a port other than 22 is dropped. The
# server binds correctly and answers on this machine; another node simply
# cannot reach it.
#
# The fix is to put tailscale0 in the `trusted` zone. That is the arrangement
# tailscale's own documentation describes: the tailnet is already an
# authenticated network, membership in it is the access decision, and treating
# it as trusted is what makes the node's own services reachable inside it.
#
# It is scoped to the interface. The `public` zone keeps its rules, so nothing
# changes for Wi-Fi or ethernet: a machine on the coffee shop's network still
# reaches nothing but SSH.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "this needs root: sudo bash scripts/dev-open-firewall.sh" >&2
  exit 1
fi

if ! command -v firewall-cmd >/dev/null 2>&1; then
  echo "no firewall-cmd; this script is for a firewalld host" >&2
  exit 1
fi

echo "==> before"
firewall-cmd --get-active-zones

echo
echo "==> putting tailscale0 in the trusted zone"
# --permanent survives a reboot; the second call applies it now. Both, because
# one without the other is a change that either does not last or does not take
# effect until something reloads.
firewall-cmd --permanent --zone=trusted --change-interface=tailscale0
firewall-cmd --zone=trusted --change-interface=tailscale0

echo
echo "==> after"
firewall-cmd --get-active-zones

echo
echo "The tailnet reaches this machine's services now. The public zone is"
echo "unchanged, so anything arriving on Wi-Fi or ethernet still meets it."
echo
echo "To undo:"
echo "  sudo firewall-cmd --permanent --zone=trusted --remove-interface=tailscale0"
echo "  sudo firewall-cmd --reload"
