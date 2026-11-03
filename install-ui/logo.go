package main

// logo is the ANSI-Shadow HADRON wordmark, the same art the boot splash paints
// from rootfs/etc/kairos/branding/splash/wordmark (logo_test.go keeps the two
// equal), so the boot splash and the installer read as the same machine. The block and box-drawing
// glyphs need a console font that covers them — see rootfs/etc/vconsole.conf.
var logo = []string{
	"██╗  ██╗ █████╗ ██████╗ ██████╗  ██████╗ ███╗   ██╗",
	"██║  ██║██╔══██╗██╔══██╗██╔══██╗██╔═══██╗████╗  ██║",
	"███████║███████║██║  ██║██████╔╝██║   ██║██╔██╗ ██║",
	"██╔══██║██╔══██║██║  ██║██╔══██╗██║   ██║██║╚██╗██║",
	"██║  ██║██║  ██║██████╔╝██║  ██║╚██████╔╝██║ ╚████║",
	"╚═╝  ╚═╝╚═╝  ╚═╝╚═════╝ ╚═╝  ╚═╝ ╚═════╝ ╚═╝  ╚═══╝",
}
