#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BOOTLOADERS_DIR="$PROJECT_DIR/bootloaders"
IPXE_VERSION="v2.0.0"
TARBALL_URL="https://github.com/ipxe/ipxe/releases/download/${IPXE_VERSION}/ipxeboot.tar.gz"

echo "Downloading iPXE ${IPXE_VERSION} Secure Boot bootloaders..."

TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

# Get the asset download URL via GitHub API (direct URL redirects to HTML)
ASSET_ID=$(curl -sL https://api.github.com/repos/ipxe/ipxe/releases/tags/${IPXE_VERSION} | \
    python3 -c "import sys,json; [print(a['id']) for a in json.load(sys.stdin).get('assets',[]) if a['name']=='ipxeboot.tar.gz']")

curl -sL -H "Accept: application/octet-stream" \
    "https://api.github.com/repos/ipxe/ipxe/releases/assets/${ASSET_ID}" \
    -o "$TMPDIR/ipxeboot.tar.gz"

tar -xzf "$TMPDIR/ipxeboot.tar.gz" -C "$TMPDIR"

# x86_64 Secure Boot
# The shim derives the iPXE filename by stripping "shim" from its own name.
# So "bootimus-shimx64.efi" will load "bootimus.efi".
cp "$TMPDIR/ipxeboot/x86_64-sb/shimx64.efi" "$BOOTLOADERS_DIR/bootimus-shimx64.efi"
cp "$TMPDIR/ipxeboot/x86_64-sb/ipxe.efi" "$BOOTLOADERS_DIR/bootimus.efi"

# ARM64 Secure Boot
cp "$TMPDIR/ipxeboot/arm64-sb/shimaa64.efi" "$BOOTLOADERS_DIR/bootimus-shimaa64.efi"
cp "$TMPDIR/ipxeboot/arm64-sb/ipxe.efi" "$BOOTLOADERS_DIR/bootimus-arm64.efi"

echo ""
echo "Secure Boot bootloaders downloaded:"
ls -lh "$BOOTLOADERS_DIR"/bootimus-shim* "$BOOTLOADERS_DIR"/bootimus.efi "$BOOTLOADERS_DIR"/bootimus-arm64.efi
echo ""
echo "DHCP configuration:"
echo "  UEFI Secure Boot (x86_64): filename = 'bootimus-shimx64.efi'"
echo "  UEFI Secure Boot (ARM64):  filename = 'bootimus-shimaa64.efi'"
echo "  UEFI (no Secure Boot):     filename = 'ipxe.efi'"
echo "  Legacy BIOS:               filename = 'undionly.kpxe'"
echo ""
echo "Done!"
