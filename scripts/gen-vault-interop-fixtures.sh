#!/bin/bash
# Regenerates the VeraCrypt interop fixtures under
# go/engine/infra/vault/testdata/interop, using the real veracrypt console
# binary rather than this repository's own writer.
#
# Prerequisites:
#   - podman (tested against 6.1.0), with network access to github.com to
#     fetch the veracrypt-console .deb the first time the image is built.
#   - qemu-system-x86_64 (tested against 11.1.1), with /dev/kvm accessible to
#     the invoking user. If /dev/kvm is root:kvm 0660 on your system rather
#     than world-writable, add yourself to the kvm group first.
#   - e2fsprogs' mke2fs (tested against 1.47.4).
#   - Enough free space in $TMPDIR (or /tmp) for a ~1.5 GiB scratch VM disk
#     plus a ~700 MiB output disk; both are deleted when this script exits.
#
# Why a VM and not a plain `podman run`: creating a container is plain file
# I/O and works in an unprivileged podman container, but mounting one to
# write the marker file veracrypt itself put inside needs a loop device, and
# this sandbox's podman has no path to one (no CAP_SYS_ADMIN over the host's
# /dev/loop-control, and the loop kernel module isn't loaded). A qemu guest
# booted from this same host kernel gets a fresh, fully-privileged kernel
# instance where loop devices work normally. Output comes back over a second
# raw virtio-blk disk this script reads directly by byte offset, because
# mounting anything the guest produced would hit the identical restriction
# back on the host.
#
# Re-running this script is safe: it rebuilds the podman image (cheap after
# the first run, since every layer is cached), rebuilds the scratch VM disk
# from scratch, and overwrites every fixture file.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
vault_dir="$repo_root/scripts/vault-interop"
out_dir="$repo_root/go/engine/infra/vault/testdata/interop"
scratch="${TMPDIR:-/tmp}/vault-interop-build"

for bin in podman qemu-system-x86_64 mke2fs truncate dd fallocate; do
	command -v "$bin" >/dev/null || {
		echo "gen-vault-interop-fixtures: missing required command: $bin" >&2
		exit 1
	}
done
if [ ! -e /dev/kvm ]; then
	echo "gen-vault-interop-fixtures: /dev/kvm not found; KVM acceleration is required" >&2
	exit 1
fi

rm -rf "$scratch"
mkdir -p "$scratch" "$out_dir"

echo "== building the veracrypt console image =="
podman build -t vault-interop-final -f "$vault_dir/Containerfile" "$vault_dir"

echo "== exporting the image filesystem with real ownership (setuid bits matter: mount(8) is setuid-root) =="
cid=$(podman create vault-interop-final true)
podman export "$cid" -o "$scratch/rootfs.tar"
podman rm "$cid" >/dev/null
mkdir -p "$scratch/rootfs"
podman unshare bash -c "
	set -e
	cd '$scratch/rootfs'
	tar --same-owner -xpf '$scratch/rootfs.tar'
	cp '$vault_dir/init.sh' ./init.sh
	mkdir -p opt
	cp '$vault_dir/generate.sh' opt/generate.sh
	chmod +x init.sh opt/generate.sh
	kver=\$(ls lib/modules)
	cp boot/vmlinuz-\$kver '$scratch/vmlinuz'
	cp boot/initrd.img-\$kver '$scratch/initrd.img'
"

echo "== building the scratch VM disk (mke2fs -d needs no loop device or root) =="
podman unshare mke2fs -q -F -t ext4 -d "$scratch/rootfs" "$scratch/root.img" 1536M

truncate -s 768M "$scratch/vdb.img"

echo "== booting the VM to generate fixtures (this runs veracrypt for real; several minutes) =="
timeout 1800 qemu-system-x86_64 \
	-M q35 -m 1024 -smp 2 -cpu host \
	-kernel "$scratch/vmlinuz" \
	-initrd "$scratch/initrd.img" \
	-append "root=/dev/vda rw console=ttyS0 init=/init.sh panic=-1" \
	-drive "file=$scratch/root.img,if=virtio,format=raw" \
	-drive "file=$scratch/vdb.img,if=virtio,format=raw" \
	-nographic -no-reboot -enable-kvm >"$scratch/qemu.log" 2>&1 || {
	echo "gen-vault-interop-fixtures: qemu run failed or timed out; see $scratch/qemu.log" >&2
	exit 1
}

echo "== extracting fixtures from the output disk =="
manifest=$(dd if="$scratch/vdb.img" bs=1M count=4 status=none | tr -d '\0')
if ! grep -q GENERATE_COMPLETE <(dd if="$scratch/vdb.img" bs=1M count=4 status=none); then
	echo "gen-vault-interop-fixtures: generator did not report completion; see $scratch/qemu.log" >&2
fi
echo "$manifest" | grep '^FIXTURE ' | while read -r _ name offset size; do
	dest="$out_dir/$name.hc"
	dd if="$scratch/vdb.img" of="$dest" bs=1M skip=$((offset / 1024 / 1024)) count=$(((size + 1024 * 1024 - 1) / 1024 / 1024)) status=none
	truncate -s "$size" "$dest"
	fallocate --dig-holes "$dest" 2>/dev/null || true
	echo "  $name.hc: $size bytes"
done

echo "== total size of generated fixtures =="
du -ch "$out_dir"/*.hc | tail -1

rm -rf "$scratch"
echo "done"
