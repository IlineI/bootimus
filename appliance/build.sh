#!/bin/bash
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
BUILD_DIR="$HERE/build"
IMAGE_NAME="bootimus-appliance.img"
IMAGE_SIZE_BYTES="${IMAGE_SIZE_BYTES:-2147483648}"   # 2 GiB default
ALPINE_BRANCH="${ALPINE_BRANCH:-v3.20}"
ALPINE_MIRROR="${ALPINE_MIRROR:-http://dl-cdn.alpinelinux.org/alpine}"

mkdir -p "$BUILD_DIR"

echo ">> [1/3] Cross-compiling bootimus for linux/amd64…"
(
    cd "$REPO_ROOT"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
        go build -ldflags="-w -s -X bootimus/internal/server.Version=$(cat VERSION)-appliance" \
        -o "$BUILD_DIR/bootimus" .
)
echo "   $(du -h "$BUILD_DIR/bootimus" | cut -f1)"

STAGE="$BUILD_DIR/stage"
rm -rf "$STAGE"
mkdir -p "$STAGE/usr/local/bin"
cp -a "$HERE/overlay/." "$STAGE/"
cp "$BUILD_DIR/bootimus" "$STAGE/usr/local/bin/bootimus"

echo ">> [2/3] Building Alpine image in Docker (no host kernel state touched)…"
docker run --rm --privileged \
    -v /dev:/dev \
    -v "$BUILD_DIR:/out" \
    -v "$STAGE:/stage:ro" \
    -v "$HERE/setup.sh:/setup.sh:ro" \
    -e ALPINE_BRANCH="$ALPINE_BRANCH" \
    -e ALPINE_MIRROR="$ALPINE_MIRROR" \
    -e IMAGE_SIZE_BYTES="$IMAGE_SIZE_BYTES" \
    -e IMAGE_NAME="$IMAGE_NAME" \
    alpine:${ALPINE_BRANCH#v} sh -euxc '
        apk add --no-cache \
            apk-tools \
            bash \
            coreutils \
            e2fsprogs \
            parted \
            syslinux \
            util-linux

        IMG=/out/"$IMAGE_NAME"
        rm -f "$IMG"
        truncate -s "$IMAGE_SIZE_BYTES" "$IMG"

        parted -s "$IMG" mklabel msdos
        parted -s "$IMG" mkpart primary ext4 1MiB 100%
        parted -s "$IMG" set 1 boot on

        LOOP=$(losetup -f --show -P "$IMG")
        trap "umount -R /mnt/rootfs 2>/dev/null || true; losetup -d $LOOP 2>/dev/null || true" EXIT

        mkfs.ext4 -F -L bootimus "${LOOP}p1"

        mkdir -p /mnt/rootfs
        mount "${LOOP}p1" /mnt/rootfs

        REPO="$ALPINE_MIRROR/$ALPINE_BRANCH/main"
        COMMUNITY="$ALPINE_MIRROR/$ALPINE_BRANCH/community"

        apk --root=/mnt/rootfs --initdb \
            -X "$REPO" -X "$COMMUNITY" \
            --allow-untrusted \
            add alpine-base linux-lts syslinux openrc busybox-openrc \
                ca-certificates curl dhcpcd e2fsprogs iproute2 iptables \
                openssh-server samba samba-common-tools dnsmasq \
                bash mkinitfs nano htop tzdata

        mkdir -p /mnt/rootfs/etc/apk
        echo "$REPO" >  /mnt/rootfs/etc/apk/repositories
        echo "$COMMUNITY" >> /mnt/rootfs/etc/apk/repositories

        cp -a /stage/. /mnt/rootfs/
        cp /etc/resolv.conf /mnt/rootfs/etc/resolv.conf 2>/dev/null || true

        for d in proc sys dev dev/pts; do
            mkdir -p /mnt/rootfs/$d
            mount --bind /$d /mnt/rootfs/$d
        done

        cp /setup.sh /mnt/rootfs/setup.sh
        chmod +x /mnt/rootfs/setup.sh
        chroot /mnt/rootfs /setup.sh
        rm /mnt/rootfs/setup.sh

        UUID=$(blkid -s UUID -o value "${LOOP}p1")
        mkdir -p /mnt/rootfs/boot/extlinux
        cat > /mnt/rootfs/boot/extlinux/extlinux.conf <<CFG
DEFAULT bootimus
PROMPT 0
TIMEOUT 20
LABEL bootimus
    LINUX /boot/vmlinuz-lts
    INITRD /boot/initramfs-lts
    APPEND root=UUID=$UUID modules=sd-mod,usb-storage,ext4 rw quiet
CFG

        chroot /mnt/rootfs mkinitfs -c /etc/mkinitfs/mkinitfs.conf -b / $(ls /mnt/rootfs/lib/modules/ | head -1)

        extlinux --install /mnt/rootfs/boot/extlinux
        dd if=/mnt/rootfs/usr/share/syslinux/mbr.bin of="$LOOP" bs=440 count=1 conv=notrunc

        sync
        for d in dev/pts dev sys proc; do
            umount /mnt/rootfs/$d
        done
        umount /mnt/rootfs
        losetup -d "$LOOP"
        trap - EXIT

        echo "Built $IMG ($(du -h "$IMG" | cut -f1))"
    '

RAW_SIZE=$(du -b "$BUILD_DIR/$IMAGE_NAME" | cut -f1)
if [ "$RAW_SIZE" -lt 100000000 ]; then
    echo "ERROR: built image is only $(du -h "$BUILD_DIR/$IMAGE_NAME" | cut -f1) — build failed silently."
    rm -f "$BUILD_DIR/$IMAGE_NAME"
    exit 1
fi

echo ""
echo "   Image: $BUILD_DIR/$IMAGE_NAME ($(du -h "$BUILD_DIR/$IMAGE_NAME" | cut -f1))"
echo ""
echo "Flash to USB with Etcher, Rufus, or:"
echo "   sudo dd if=$BUILD_DIR/$IMAGE_NAME of=/dev/sdX bs=4M conv=fsync status=progress"
