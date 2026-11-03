package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"v0.1.1", "v0.1.0", true},
		{"v0.2.0", "v0.1.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.10.0", "v0.9.0", true}, // numeric, not lexical
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.1.1", false},
		{"v0.1.0", "v0.1.0-rc1", true},
		{"v0.1.0-rc1", "v0.1.0", false},
		{"v0.1.0-rc2", "v0.1.0-rc1", true},
		{"v0.1.0", "v0.0.0", true}, // a dev build is offered real releases
		{"v0.1.0", "dev", true},    // and so is anything that does not parse
		{"latest", "v0.1.0", false},
		{"v0.1.0+build.7", "v0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.candidate, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.candidate, c.current, got, c.want)
		}
	}
}

func writeRelease(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "release")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodRelease = `# written by the Dockerfile
FLAVOR=sway
VERSION="v0.1.0"
HADRON_VERSION=v0.6.3
IMAGE_REPO=ghcr.io/kairos-community/hadron-desktop
GITHUB_REPO=kairos-community/hadron-desktop
`

func TestLoadRelease(t *testing.T) {
	r, err := LoadRelease(writeRelease(t, goodRelease))
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "v0.1.0" || r.Flavor != "sway" || r.HadronVersion != "v0.6.3" {
		t.Fatalf("unexpected release %+v", r)
	}
	if got, want := r.ImageFor("v0.2.0"), "ghcr.io/kairos-community/hadron-desktop:sway-v0.2.0"; got != want {
		t.Fatalf("ImageFor = %q, want %q", got, want)
	}
}

// A half-written release file must not turn into an image reference with an
// empty repository or flavor.
func TestLoadReleaseRejectsMissingKeys(t *testing.T) {
	for _, key := range []string{"FLAVOR", "VERSION", "IMAGE_REPO", "GITHUB_REPO"} {
		var kept []string
		for _, l := range strings.Split(goodRelease, "\n") {
			if !strings.HasPrefix(l, key+"=") {
				kept = append(kept, l)
			}
		}
		_, err := LoadRelease(writeRelease(t, strings.Join(kept, "\n")))
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("without %s: err = %v, want one naming the key", key, err)
		}
	}
}
