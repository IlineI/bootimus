package bootloaders

import "embed"

//go:embed *.efi *.kpxe wimboot
var Bootloaders embed.FS
