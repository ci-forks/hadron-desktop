package main

import (
	"os"
	"strings"
	"testing"
)

// The installer and the boot splash must show the same wordmark, so the
// handoff from one to the other reads as one machine. This pins the installer's
// copy to the ART rows in splash/main.c.
func TestLogoMatchesSplashWordmark(t *testing.T) {
	src, err := os.ReadFile("../splash/main.c")
	if err != nil {
		t.Skipf("splash source not available outside the repo: %v", err)
	}
	_, after, ok := strings.Cut(string(src), "static const char *ART[ART_ROWS] = {")
	if !ok {
		t.Fatal("ART table not found in splash/main.c")
	}
	body, _, _ := strings.Cut(after, "};")
	var want []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"`) {
			want = append(want, strings.TrimSuffix(strings.TrimPrefix(line, `"`), `",`))
		}
	}
	if strings.Join(logo, "\n") != strings.Join(want, "\n") {
		t.Fatalf("install-ui logo and the splash wordmark differ:\n%s\n---\n%s",
			strings.Join(logo, "\n"), strings.Join(want, "\n"))
	}
}
