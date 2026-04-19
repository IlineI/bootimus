#!/bin/bash
set -euo pipefail

BOOTLOADERS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../bootloaders" && pwd)"
# iPXE v1.21.1 — pinned to avoid the keyboard-input regression in v2.0.0 menus.
IPXE_COMMIT="988d2c13cdf0f0b4140685af35ced70ac5b3283c"

docker build -t ipxe-builder -f - "$BOOTLOADERS_DIR" <<DOCKERFILE
FROM debian:bookworm
RUN apt-get update && apt-get install -y git make gcc libc6-dev liblzma-dev \
    gcc-aarch64-linux-gnu binutils-aarch64-linux-gnu libc6-dev-arm64-cross ca-certificates \
    mtools
RUN git clone https://github.com/ipxe/ipxe.git /build/ipxe && \
    cd /build/ipxe && git checkout ${IPXE_COMMIT}
COPY embed.ipxe /build/ipxe/src/embed.ipxe
WORKDIR /build/ipxe/src
# NO_WERROR=1 silences array-bounds -Werror hits from newer GCC (bookworm) on
# older iPXE sources. Safe: they're warnings, not correctness bugs.
RUN make NO_WERROR=1 bin/undionly.kpxe EMBED=embed.ipxe
RUN make NO_WERROR=1 bin-x86_64-efi/ipxe.efi EMBED=embed.ipxe
RUN make NO_WERROR=1 bin-x86_64-efi/ipxe.usb EMBED=embed.ipxe
RUN make NO_WERROR=1 CROSS=aarch64-linux-gnu- bin-arm64-efi/ipxe.efi EMBED=embed.ipxe
DOCKERFILE

CID=$(docker create ipxe-builder echo)
docker cp "$CID:/build/ipxe/src/bin/undionly.kpxe"       "$BOOTLOADERS_DIR/undionly.kpxe"
docker cp "$CID:/build/ipxe/src/bin-x86_64-efi/ipxe.efi" "$BOOTLOADERS_DIR/ipxe.efi"
docker cp "$CID:/build/ipxe/src/bin-x86_64-efi/ipxe.efi" "$BOOTLOADERS_DIR/bootimus.efi"
docker cp "$CID:/build/ipxe/src/bin-x86_64-efi/ipxe.usb" "$BOOTLOADERS_DIR/bootimus.usb"
docker cp "$CID:/build/ipxe/src/bin-arm64-efi/ipxe.efi"  "$BOOTLOADERS_DIR/ipxe-arm64.efi"
docker cp "$CID:/build/ipxe/src/bin-arm64-efi/ipxe.efi"  "$BOOTLOADERS_DIR/bootimus-arm64.efi"
docker rm "$CID" > /dev/null

echo "Done. Bootloaders in $BOOTLOADERS_DIR:"
ls -lh "$BOOTLOADERS_DIR"/*.{kpxe,efi,usb} 2>/dev/null
