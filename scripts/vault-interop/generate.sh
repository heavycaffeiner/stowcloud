#!/bin/bash
# Runs inside the throwaway VM booted by scripts/gen-vault-interop-fixtures.sh,
# as PID 1's root shell, with loop, fuse, fat, vfat and msdos already insmod'd
# and /dev/loop1..7 pre-created. It drives the real `veracrypt` binary to
# build the interop fixture matrix, then writes a plain-text manifest and the
# fixture bytes to /dev/vdb, which the host-side script reads back without
# ever needing to mount anything itself.
#
# Every mount and format here uses `-m nokernelcrypto`: this sandbox's kernel
# deadlocks inside dm-crypt/cryptd for a container-backed loop device (a VM
# artifact, not something the driver under test can hit), and nokernelcrypto's
# pure-FUSE path avoids the kernel crypto layer entirely. The final vfat mount
# still needs a loop device, so a raw device is always what write_marker
# manipulates, never a container file directly.
set -u

PASSWORD='veracrypt interop fixture password'
HIDDEN_PASSWORD='veracrypt interop hidden password'
STANDARD_SIZE=20M
HIDDEN_OUTER_SIZE=52M
HIDDEN_SIZE=20M
PIM_VALUE=20

WORK=/work
OUT=/mnt/out
mkdir -p "$WORK" /mnt/w

# CURSOR tracks the next free byte offset in /dev/vdb, past the manifest
# region reserved at the front.
MANIFEST_RESERVED=$((4 * 1024 * 1024))
CURSOR=$MANIFEST_RESERVED
MANIFEST=""

# dump_blob writes path's bytes to /dev/vdb at the current cursor, records a
# manifest line, and advances the cursor to the next 1 MiB boundary.
dump_blob() {
	local name=$1 path=$2
	local size
	size=$(stat -c %s "$path")
	local mib=$((CURSOR / 1024 / 1024))
	dd if="$path" of=/dev/vdb bs=1M seek="$mib" conv=notrunc status=none
	MANIFEST="${MANIFEST}FIXTURE $name $CURSOR $size
"
	local rounded=$(( (size + 1024 * 1024 - 1) / (1024 * 1024) * 1024 * 1024 ))
	CURSOR=$((CURSOR + rounded))
}

# create_container makes one container at $WORK/$1.hc with the given cipher,
# hash and PIM, formatted FAT, filled (no --quick) so the whole data area is
# genuinely encrypted under the real derived keys.
create_container() {
	local name=$1 size=$2 cipher=$3 hash=$4 pim=$5
	veracrypt --text --create "$WORK/$name.hc" --volume-type=normal --size="$size" \
		--encryption="$cipher" --hash="$hash" --pim="$pim" --filesystem=FAT -m nokernelcrypto \
		--random-source=/dev/urandom --password="$PASSWORD" --new-password="$PASSWORD" \
		--non-interactive
}

# write_marker mounts container $1 with $2/$3 (password/pim), writes a
# MARKER.TXT with $4 as content through a real vfat kernel mount of the
# FUSE-exposed plaintext, and unmounts. The plaintext is copied to a tmpfs
# file first because this sandbox's loop driver refuses to attach a second
# loop device on top of a FUSE-backed one; /dev/loop3 is dedicated to this
# helper so it never collides with veracrypt's own /dev/loop0.
write_marker() {
	local path=$1 password=$2 pim=$3 content=$4
	veracrypt -t --mount "$path" --password="$password" --pim="$pim" \
		--filesystem=none -m nokernelcrypto --protect-hidden=no --non-interactive -k '' >/dev/null 2>&1
	local auxvol
	auxvol=$(grep veracrypt /proc/mounts | awk '{print $2}')/volume
	cp "$auxvol" /tmp/plain.img
	losetup /dev/loop3 /tmp/plain.img
	mount /dev/loop3 /mnt/w
	printf '%s\n' "$content" >/mnt/w/MARKER.TXT
	sync
	umount /mnt/w
	losetup -d /dev/loop3
	dd if=/tmp/plain.img of="$auxvol" conv=notrunc,nocreat bs=1M status=none
	sync
	veracrypt --text --unmount "$path" --non-interactive >/dev/null 2>&1
}

echo "=== hash matrix (cipher AES) ==="
for entry in \
	"hash_sha512:sha-512" \
	"hash_sha256:sha-256" \
	"hash_blake2s:blake2s-256" \
	"hash_whirlpool:whirlpool" \
	"hash_streebog:streebog"; do
	name=${entry%%:*}
	hash=${entry#*:}
	echo "--- $name (AES/$hash) ---"
	create_container "$name" "$STANDARD_SIZE" AES "$hash" 0
	write_marker "$WORK/$name.hc" "$PASSWORD" 0 "interop fixture $name AES $hash"
	dump_blob "$name" "$WORK/$name.hc"
done

echo "=== cipher and cascade matrix (hash SHA-512) ==="
for cipher in \
	Serpent Twofish Camellia Kuznyechik \
	AES-Twofish Serpent-AES Twofish-Serpent Camellia-Kuznyechik Camellia-Serpent \
	Kuznyechik-AES Kuznyechik-Twofish \
	AES-Twofish-Serpent Serpent-Twofish-AES Kuznyechik-Serpent-Camellia; do
	name=cipher_$(echo "$cipher" | tr '[:upper:]-' '[:lower:]_')
	echo "--- $name ($cipher/sha-512) ---"
	create_container "$name" "$STANDARD_SIZE" "$cipher" sha-512 0
	write_marker "$WORK/$name.hc" "$PASSWORD" 0 "interop fixture $name $cipher sha-512"
	dump_blob "$name" "$WORK/$name.hc"
done

echo "=== PIM ==="
create_container pim "$STANDARD_SIZE" AES sha-512 "$PIM_VALUE"
write_marker "$WORK/pim.hc" "$PASSWORD" "$PIM_VALUE" "interop fixture pim AES sha-512 pim=$PIM_VALUE"
dump_blob pim "$WORK/pim.hc"

echo "=== dynamic (sparse via punched hole) ==="
create_container dynamic "$STANDARD_SIZE" AES sha-512 0
write_marker "$WORK/dynamic.hc" "$PASSWORD" 0 "interop fixture dynamic AES sha-512"
# Punch a hole well past the marker's cluster and well before the backup
# header, in the tail of the data area a nearly-empty FAT volume never
# allocates: this is the "equivalent of a dynamic container" the assignment
# asks for when the console binary offers no --dynamic flag of its own.
HEADER_REGION=65536
DATA_SIZE=$(( $(stat -c %s "$WORK/dynamic.hc") - 2 * HEADER_REGION ))
HOLE_OFF=$((HEADER_REGION + DATA_SIZE - 8 * 1024 * 1024))
fallocate --punch-hole --offset "$HOLE_OFF" --length $((6 * 1024 * 1024)) "$WORK/dynamic.hc"
dump_blob dynamic "$WORK/dynamic.hc"

echo "=== hidden volume ==="
rm -f "$WORK/hidden.hc"
veracrypt --text --create "$WORK/hidden.hc" --volume-type=normal --size="$HIDDEN_OUTER_SIZE" \
	--encryption=AES --hash=sha-512 --filesystem=FAT --pim=0 -m nokernelcrypto \
	--random-source=/dev/urandom --password="$PASSWORD" --new-password="$PASSWORD" --non-interactive
# --new-password only takes effect with -C; the create step below leaves the
# hidden volume sharing the outer's password until -C changes it explicitly.
veracrypt --text --create "$WORK/hidden.hc" --volume-type=hidden --size="$HIDDEN_SIZE" \
	--encryption=AES --hash=sha-512 --pim=0 --filesystem=FAT -m nokernelcrypto \
	--random-source=/dev/urandom --password="$PASSWORD" --new-password="$PASSWORD" --non-interactive
veracrypt --text -C "$WORK/hidden.hc" --volume-type=hidden -p "$PASSWORD" --new-password="$HIDDEN_PASSWORD" \
	--hash=sha-512 --new-hash=sha-512 --non-interactive
write_marker "$WORK/hidden.hc" "$PASSWORD" 0 "interop fixture hidden outer AES sha-512"
write_marker "$WORK/hidden.hc" "$HIDDEN_PASSWORD" 0 "interop fixture hidden inner AES sha-512"
dump_blob hidden "$WORK/hidden.hc"

printf '%s' "$MANIFEST" >/tmp/manifest.txt
dd if=/tmp/manifest.txt of=/dev/vdb bs=1M conv=notrunc status=none
echo "=== manifest ==="
cat /tmp/manifest.txt
echo GENERATE_COMPLETE
