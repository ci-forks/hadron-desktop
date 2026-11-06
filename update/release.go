package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// defaultReleaseFile is written by the Dockerfile's kairos stage. It is the one
// place the installed system records which image line it upgrades along.
const defaultReleaseFile = "/etc/hadron-desktop/release"

// Release is the identity of the running system.
type Release struct {
	Flavor        string // sway, i3, cosmic, niri or agent
	Version       string // the hadron-desktop release tag, e.g. v0.1.0
	HadronVersion string // the Hadron base release, e.g. v0.6.3
	ImageRepo     string // e.g. ghcr.io/kairos-community/hadron-desktop
	GitHubRepo    string // e.g. kairos-community/hadron-desktop
	// GPU is the image line: gpuFull or gpuVM. Releases up to v0.0.7 wrote
	// no GPU key; LoadRelease reads them as gpuFull (see Line).
	GPU string
}

// The two image lines a desktop flavor is released in. Each release pushes
// <flavor>-<tag> (full: Mesa's Intel and AMD drivers and linux-firmware, for
// real machines) and <flavor>-vm-<tag> (virtual GPUs and llvmpipe only, and
// no firmware: smaller, for virtual machines). The agent has one line,
// agent-<tag>.
const (
	gpuFull = "full"
	gpuVM   = "vm"
)

// Line is the image line this system upgrades along. A release file without a
// GPU key comes from v0.0.7 or earlier: those were GPU=vm images, but their
// own hadron-update can only pull <flavor>-<tag>, which is the full line now,
// and the full image runs in a VM too. So they are on the full line.
func (r Release) Line() string {
	if r.GPU == gpuVM {
		return gpuVM
	}
	return gpuFull
}

// lineTag is the image tag release.yml pushes for a flavor, line and release.
func lineTag(flavor, line, tag string) string {
	if line == gpuVM && flavor != flavorAgent {
		return flavor + "-" + gpuVM + "-" + tag
	}
	return flavor + "-" + tag
}

// ImageFor is the OCI reference release.yml pushes for a tag of this
// system's flavor and line.
func (r Release) ImageFor(tag string) string {
	return fmt.Sprintf("%s:%s", r.ImageRepo, lineTag(r.Flavor, r.Line(), tag))
}

func releaseFile() string {
	if p := os.Getenv("HADRON_UPDATE_RELEASE_FILE"); p != "" {
		return p
	}
	return defaultReleaseFile
}

// LoadRelease reads a KEY=value file. Values may be quoted. Every key the
// upgrade path needs must be present: a half-written file would otherwise make
// hadron-update pull an image reference like ":-v1.0.0".
func LoadRelease(path string) (Release, error) {
	kv, err := readKV(path)
	if err != nil {
		return Release{}, err
	}

	r := Release{
		Flavor:        kv["FLAVOR"],
		Version:       kv["VERSION"],
		HadronVersion: kv["HADRON_VERSION"],
		ImageRepo:     kv["IMAGE_REPO"],
		GitHubRepo:    kv["GITHUB_REPO"],
		GPU:           kv["GPU"],
	}
	switch r.GPU {
	case "", gpuFull, gpuVM:
	default:
		return Release{}, fmt.Errorf("%s: GPU is %q, want %s or %s", path, r.GPU, gpuFull, gpuVM)
	}
	for name, v := range map[string]string{
		"FLAVOR": r.Flavor, "VERSION": r.Version,
		"IMAGE_REPO": r.ImageRepo, "GITHUB_REPO": r.GitHubRepo,
	} {
		if v == "" {
			return Release{}, fmt.Errorf("%s: %s is not set", path, name)
		}
	}
	return r, nil
}

// readKV reads a KEY=value file, the format of /etc/hadron-desktop/release
// and /etc/hadron-desktop/session. Blank lines and # comments are skipped;
// values may be quoted.
func readKV(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return kv, sc.Err()
}

// semver is the subset of semantic versioning release tags use: vMAJOR.MINOR.PATCH
// with an optional -prerelease. Build metadata is ignored.
type semver struct {
	major, minor, patch int
	pre                 string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return semver{}, false
		}
		n[i] = v
	}
	return semver{n[0], n[1], n[2], pre}, true
}

// Newer reports whether candidate is a later release than current. A current
// version that does not parse (a dev build) is always older than a real
// release, so a dev box is offered the latest one.
func Newer(candidate, current string) bool {
	c, ok := parseSemver(candidate)
	if !ok {
		return false
	}
	cur, ok := parseSemver(current)
	if !ok {
		return true
	}
	if c.major != cur.major {
		return c.major > cur.major
	}
	if c.minor != cur.minor {
		return c.minor > cur.minor
	}
	if c.patch != cur.patch {
		return c.patch > cur.patch
	}
	// Same core: a release beats its own prereleases.
	switch {
	case c.pre == cur.pre:
		return false
	case c.pre == "":
		return true
	case cur.pre == "":
		return false
	default:
		return c.pre > cur.pre
	}
}
