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

// The line decides the image an upgrade pulls. A release file without a GPU
// key (v0.0.7 and earlier) follows the full line, the tag its own updater
// already pulls; the agent has one line whatever the key says.
func TestReleaseLines(t *testing.T) {
	cases := []struct {
		flavor, gpu, line, image string
	}{
		{"sway", "", gpuFull, "ghcr.io/kairos-community/hadron-desktop:sway-v0.2.0"},
		{"sway", "full", gpuFull, "ghcr.io/kairos-community/hadron-desktop:sway-v0.2.0"},
		{"sway", "vm", gpuVM, "ghcr.io/kairos-community/hadron-desktop:sway-vm-v0.2.0"},
		{"cosmic", "vm", gpuVM, "ghcr.io/kairos-community/hadron-desktop:cosmic-vm-v0.2.0"},
		{"agent", "vm", gpuVM, "ghcr.io/kairos-community/hadron-desktop:agent-v0.2.0"},
		{"agent", "", gpuFull, "ghcr.io/kairos-community/hadron-desktop:agent-v0.2.0"},
	}
	for _, c := range cases {
		body := strings.Replace(goodRelease, "FLAVOR=sway", "FLAVOR="+c.flavor, 1)
		if c.gpu != "" {
			body += "GPU=" + c.gpu + "\n"
		}
		r, err := LoadRelease(writeRelease(t, body))
		if err != nil {
			t.Fatalf("%s/%q: %v", c.flavor, c.gpu, err)
		}
		if r.Line() != c.line {
			t.Errorf("%s/%q: Line = %q, want %q", c.flavor, c.gpu, r.Line(), c.line)
		}
		if got := r.ImageFor("v0.2.0"); got != c.image {
			t.Errorf("%s/%q: ImageFor = %q, want %q", c.flavor, c.gpu, got, c.image)
		}
		ref, err := ReleaseRef(r, r.Flavor, "v0.2.0")
		if err != nil || ref.String() != c.image {
			t.Errorf("%s/%q: ReleaseRef = %v, %v; want %s", c.flavor, c.gpu, ref, err, c.image)
		}
	}
}

func TestLoadReleaseRejectsUnknownGPU(t *testing.T) {
	_, err := LoadRelease(writeRelease(t, goodRelease+"GPU=nvidia\n"))
	if err == nil || !strings.Contains(err.Error(), "GPU") {
		t.Fatalf("err = %v, want one naming GPU", err)
	}
}
