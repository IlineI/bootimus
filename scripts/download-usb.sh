#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
USB_DIR="$PROJECT_DIR/usb"
IPXE_VERSION="v2.0.0"

echo "Downloading iPXE ${IPXE_VERSION} USB boot images..."

mkdir -p "$USB_DIR"

TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

# Get asset IDs via GitHub API
RELEASE_JSON=$(curl -sL https://api.github.com/repos/ipxe/ipxe/releases/tags/${IPXE_VERSION})

download_asset() {
    local name="$1"
    local dest="$2"
    local asset_id
    asset_id=$(echo "$RELEASE_JSON" | python3 -c "import sys,json; [print(a['id']) for a in json.load(sys.stdin).get('assets',[]) if a['name']=='$name']")
    if [ -z "$asset_id" ]; then
        echo "Warning: Asset $name not found in release"
        return 1
    fi
    curl -sL -H "Accept: application/octet-stream" \
        "https://api.github.com/repos/ipxe/ipxe/releases/assets/${asset_id}" \
        -o "$dest"
}

# Download USB images
download_asset "ipxe.usb" "$USB_DIR/bootimus.usb"
download_asset "ipxe-x86_64-sb.usb" "$USB_DIR/bootimus-secureboot.usb"

echo ""
echo "USB boot images downloaded:"
ls -lh "$USB_DIR"/*.usb
echo ""
echo "Usage:"
echo "  Write to USB stick (BIOS/UEFI):"
echo "    sudo dd if=usb/bootimus.usb of=/dev/sdX bs=4M status=progress"
echo ""
echo "  Write to USB stick (UEFI Secure Boot):"
echo "    sudo dd if=usb/bootimus-secureboot.usb of=/dev/sdX bs=4M status=progress"
echo ""
echo "  The USB will boot iPXE which uses DHCP to find bootimus."
echo "  Make sure your DHCP server has next-server pointing to bootimus."
echo ""
echo "Done!"
