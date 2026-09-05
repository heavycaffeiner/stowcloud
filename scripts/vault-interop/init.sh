#!/bin/bash
# PID 1 for the throwaway VM scripts/gen-vault-interop-fixtures.sh boots.
# Brings up just enough of a system for generate.sh to run veracrypt, then
# powers off. Never runs at `go test` time.
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
mount -t proc proc /proc 2>/dev/null
mount -t sysfs sysfs /sys 2>/dev/null
mount -t devtmpfs devtmpfs /dev 2>/dev/null
mkdir -p /dev/pts /dev/shm
mount -t devpts devpts /dev/pts 2>/dev/null
mount -t tmpfs tmpfs /dev/shm 2>/dev/null
mount -t tmpfs tmpfs /run 2>/dev/null
mount -t tmpfs tmpfs /tmp 2>/dev/null

KVER=$(uname -r)
MOD=/lib/modules/$KVER/kernel
insmod $MOD/drivers/block/loop.ko
insmod $MOD/fs/fuse/fuse.ko
insmod $MOD/fs/fat/fat.ko
insmod $MOD/fs/fat/vfat.ko
insmod $MOD/fs/fat/msdos.ko
# devtmpfs only auto-creates /dev/loop0 on first use; generate.sh's
# write_marker needs a second, independent minor number of its own.
for i in 1 2 3 4 5 6 7; do
	[ -e /dev/loop$i ] || mknod /dev/loop$i b 7 $i
done

bash /opt/generate.sh >/tmp/generate.log 2>&1
echo "GENERATE_EXIT=$?" >>/tmp/generate.log
sync
dd if=/tmp/generate.log of=/dev/vdb bs=1M conv=notrunc 2>/dev/null
sync

# A clean shutdown syscall, not reliant on any particular init system being
# present past this point.
echo 1 >/proc/sys/kernel/sysrq
echo o >/proc/sysrq-trigger
sleep 30
