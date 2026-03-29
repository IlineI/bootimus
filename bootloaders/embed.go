package bootloaders

import "embed"

//go:embed *.efi *.kpxe *.usb wimboot
var Bootloaders embed.FS
