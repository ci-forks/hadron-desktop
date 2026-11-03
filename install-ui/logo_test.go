package main

import (
	"os"
	"strings"
	"testing"
)

// The installer and the boot splash must show the same wordmark, so the
// handoff from one to the other reads as one machine. The splash reads its art
// from the branding file; this pins the installer's copy to it.
func TestLogoMatchesSplashWordmark(t *testing.T) {
	raw, err := os.ReadFile("../rootfs/etc/kairos/branding/splash/wordmark")
	if err != nil {
		t.Skipf("branding file not available outside the repo: %v", err)
	}
	want := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if strings.Join(logo, "\n") != strings.Join(want, "\n") {
		t.Fatalf("install-ui logo and the splash wordmark differ:\n%s\n---\n%s",
			strings.Join(logo, "\n"), strings.Join(want, "\n"))
	}
}
