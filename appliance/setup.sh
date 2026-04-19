#!/bin/sh
set -eu

echo "bootimus" > /etc/hostname

rc-update add devfs sysinit
rc-update add dmesg sysinit
rc-update add mdev sysinit
rc-update add hwclock boot
rc-update add modules boot
rc-update add sysctl boot
rc-update add hostname boot
rc-update add bootmisc boot
rc-update add syslog boot
rc-update add networking boot
rc-update add sshd default
rc-update add bootimus-firstboot default
rc-update add bootimus default
rc-update add samba default
rc-update add local default

mkdir -p /var/lib/bootimus/isos /var/log/samba
passwd -l root 2>/dev/null || true
