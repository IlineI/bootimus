#!/bin/bash
set -euo pipefail

BOOTLOADERS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../bootloaders" && pwd)"
IPXE_COMMIT="8f1514a00450119b04b08642c55aa674bdf5a4ef"  # v1.20.1 — see ipxe/ipxe#1643
IPXE_SB_RELEASE="v2.0.0"

# Build iPXE from source with our embed script
docker build -t ipxe-builder -f - "$BOOTLOADERS_DIR" <<DOCKERFILE
FROM debian:bookworm
RUN apt-get update && apt-get install -y git make gcc libc6-dev liblzma-dev \
    gcc-aarch64-linux-gnu binutils-aarch64-linux-gnu libc6-dev-arm64-cross ca-certificates
RUN git clone https://github.com/ipxe/ipxe.git /build/ipxe && \
    cd /build/ipxe && git checkout ${IPXE_COMMIT}
COPY embed.ipxe /build/ipxe/src/embed.ipxe
WORKDIR /build/ipxe/src
RUN make bin/undionly.kpxe EMBED=embed.ipxe
RUN make bin-x86_64-efi/ipxe.efi EMBED=embed.ipxe
RUN make bin-x86_64-efi/ipxe.usb EMBED=embed.ipxe
RUN make CROSS=aarch64-linux-gnu- bin-arm64-efi/ipxe.efi EMBED=embed.ipxe
DOCKERFILE

CID=$(docker create ipxe-builder echo)
docker cp "$CID:/build/ipxe/src/bin/undionly.kpxe"       "$BOOTLOADERS_DIR/undionly.kpxe"
docker cp "$CID:/build/ipxe/src/bin-x86_64-efi/ipxe.efi" "$BOOTLOADERS_DIR/ipxe.efi"
docker cp "$CID:/build/ipxe/src/bin-x86_64-efi/ipxe.usb" "$BOOTLOADERS_DIR/bootimus.usb"
docker cp "$CID:/build/ipxe/src/bin-arm64-efi/ipxe.efi"  "$BOOTLOADERS_DIR/ipxe-arm64.efi"
docker rm "$CID" > /dev/null

# Download Secure Boot binaries (Microsoft-signed shim cannot be rebuilt locally)
TMP=$(mktemp -d)
trap "rm -rf $TMP" EXIT
curl -sL "https://github.com/ipxe/ipxe/releases/download/${IPXE_SB_RELEASE}/ipxeboot.tar.gz" | tar -xz -C "$TMP"
curl -sL -o "$BOOTLOADERS_DIR/bootimus-secureboot.usb" \
    "https://github.com/ipxe/ipxe/releases/download/${IPXE_SB_RELEASE}/ipxe-x86_64-sb.usb"

cp "$TMP/ipxeboot/x86_64-sb/shimx64.efi" "$BOOTLOADERS_DIR/bootimus-shimx64.efi"
cp "$TMP/ipxeboot/x86_64-sb/ipxe.efi"    "$BOOTLOADERS_DIR/bootimus.efi"
cp "$TMP/ipxeboot/arm64-sb/shimaa64.efi" "$BOOTLOADERS_DIR/bootimus-shimaa64.efi"
cp "$TMP/ipxeboot/arm64-sb/ipxe.efi"     "$BOOTLOADERS_DIR/bootimus-arm64.efi"

echo "Done. Bootloaders in $BOOTLOADERS_DIR:"
ls -lh "$BOOTLOADERS_DIR"/*.{kpxe,efi,usb} 2>/dev/null
