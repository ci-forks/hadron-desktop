package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGitHub serves /repos/<repo>/releases/latest with the given tag.
func fakeGitHub(t *testing.T, tag string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/kairos-community/hadron-desktop/releases/latest" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://example.invalid/%s"}`, tag, tag)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HADRON_UPDATE_API", srv.URL)
}

// fakeTool writes a script that records its argv to a file and exits code.
func fakeTool(t *testing.T, code int) (path, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	path = filepath.Join(dir, "tool")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nexit %d\n", argvFile, code)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, argvFile
}

// fakeRegistry speaks the v2 token handshake ghcr uses: an anonymous HEAD on a
// manifest gets a 401 Bearer challenge, the token service hands out a token
// for public repositories only, and the manifest answers 200 for the tags in
// published. private makes the token service refuse, as ghcr does for a
// package nobody made public.
func fakeRegistry(t *testing.T, private bool, published ...string) {
	t.Helper()
	newFakeRegistry(t, private, published...)
}

// fakeDoc is a manifest or blob a fakeReg serves.
type fakeDoc struct {
	body        []byte
	contentType string
}

// fakeReg is fakeRegistry's server plus the images fakeImage published on it.
// docs is keyed by "manifests/<tag or digest>" or "blobs/<digest>".
type fakeReg struct {
	docs map[string]fakeDoc
	// omitDigest drops Docker-Content-Digest, as some registries do.
	omitDigest bool
}

func newFakeRegistry(t *testing.T, private bool, published ...string) *fakeReg {
	t.Helper()
	reg := &fakeReg{docs: map[string]fakeDoc{}}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if private {
				http.Error(w, `{"errors":[{"code":"DENIED"}]}`, http.StatusForbidden)
				return
			}
			fmt.Fprint(w, `{"token":"anon"}`)
		case strings.HasPrefix(r.URL.Path, "/v2/kairos-community/hadron-desktop/"):
			if r.Header.Get("Authorization") != "Bearer anon" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(
					`Bearer realm="%s/token",service="ghcr.io",scope="repository:kairos-community/hadron-desktop:pull"`, srv.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			key := strings.TrimPrefix(r.URL.Path, "/v2/kairos-community/hadron-desktop/")
			if d, ok := reg.docs[key]; ok {
				w.Header().Set("Content-Type", d.contentType)
				if !reg.omitDigest {
					sum := sha256.Sum256(d.body)
					w.Header().Set("Docker-Content-Digest", "sha256:"+hex.EncodeToString(sum[:]))
				}
				w.Write(d.body)
				return
			}
			tag := strings.TrimPrefix(key, "manifests/")
			for _, p := range published {
				if p == tag {
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HADRON_UPDATE_REGISTRY", srv.URL)
	return reg
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// image publishes name:tag. arch is a comma-separated list: one entry makes a
// single manifest, several (or index) make an index. labels go on the amd64
// config only; every other architecture gets {"wrong-arch": "yes"}, so a test
// can tell which config Resolve read. It returns the digest of what the tag
// points at.
func (r *fakeReg) image(t *testing.T, name, tag, arch string, labels map[string]string, index bool) string {
	t.Helper()
	const oci = "application/vnd.oci.image.manifest.v1+json"
	var entries []map[string]any
	var single []byte
	for _, a := range strings.Split(arch, ",") {
		l := map[string]string{"wrong-arch": "yes"}
		if a == "amd64" {
			l = labels
		}
		cfg, _ := json.Marshal(map[string]any{
			"os": "linux", "architecture": a, "config": map[string]any{"Labels": l},
		})
		r.docs["blobs/"+digestOf(cfg)] = fakeDoc{cfg, "application/vnd.oci.image.config.v1+json"}
		m, _ := json.Marshal(map[string]any{
			"schemaVersion": 2, "mediaType": oci,
			"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digestOf(cfg), "size": len(cfg)},
		})
		r.docs["manifests/"+digestOf(m)] = fakeDoc{m, oci}
		entries = append(entries, map[string]any{
			"mediaType": oci, "digest": digestOf(m), "size": len(m),
			"platform": map[string]string{"os": "linux", "architecture": a},
		})
		single = m
	}
	if !index && len(entries) == 1 {
		r.docs["manifests/"+tag] = fakeDoc{single, oci}
		return digestOf(single)
	}
	idx, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": entries,
	})
	r.docs["manifests/"+tag] = fakeDoc{idx, "application/vnd.oci.image.index.v1+json"}
	r.docs["manifests/"+digestOf(idx)] = r.docs["manifests/"+tag]
	return digestOf(idx)
}

// fakeImage starts a public fake registry holding one image; see fakeReg.image.
func fakeImage(t *testing.T, name, tag, arch string, labels map[string]string, index bool) string {
	t.Helper()
	return newFakeRegistry(t, false).image(t, name, tag, arch, labels, index)
}

func setup(t *testing.T, installed, latest string) {
	t.Helper()
	fakeRegistry(t, false, "sway-v0.1.0", "sway-v0.2.0")
	t.Setenv("HADRON_UPDATE_RELEASE_FILE",
		writeRelease(t, strings.Replace(goodRelease, `"v0.1.0"`, installed, 1)))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fakeGitHub(t, latest)
	oldRoot, oldReboot := needRoot, rebootCommand
	needRoot = func() bool { return false }
	// Never let a test reach the real `systemctl reboot`.
	rebootCommand = func() *exec.Cmd { return exec.Command("false") }
	t.Cleanup(func() { needRoot, rebootCommand = oldRoot, oldReboot })
}

func runCLI(stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestCheckExitCodes(t *testing.T) {
	setup(t, "v0.1.0", "v0.1.0")
	if code, out, _ := runCLI("", "check"); code != exitOK || !strings.Contains(out, "up to date") {
		t.Fatalf("up to date: code %d, out %q", code, out)
	}

	setup(t, "v0.1.0", "v0.2.0")
	code, out, _ := runCLI("", "check")
	if code != exitUpdate || !strings.Contains(out, "v0.2.0 is available") {
		t.Fatalf("update available: code %d, out %q", code, out)
	}
	// status says the same thing but is not a predicate.
	if code, _, _ := runCLI("", "status"); code != exitOK {
		t.Fatalf("status exit = %d, want 0", code)
	}
}

func TestUpgradeRunsKairosAgentWithTheFlavorImage(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	agent, argv := fakeTool(t, 0)
	t.Setenv("HADRON_UPDATE_AGENT", agent)

	reboot, rebootArgv := fakeTool(t, 0)
	rebootCommand = func() *exec.Cmd { return exec.Command(reboot, "reboot") }

	// Yes to "Continue?" and yes to "Reboot now?". The second answer only
	// arrives if both questions read through one buffered reader; a second
	// reader would see EOF and fall back to "no".
	code, out, errOut := runCLI("y\ny\n", "upgrade")
	if code != exitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	got, _ := os.ReadFile(argv)
	want := "upgrade\n--source\noci:ghcr.io/kairos-community/hadron-desktop:sway-v0.2.0\n"
	if string(got) != want {
		t.Fatalf("kairos-agent argv = %q, want %q", got, want)
	}
	if !strings.Contains(out, "Reboot now?") || !strings.Contains(out, "starts on the next boot") {
		t.Fatalf("missing reboot prompt or success line: %q", out)
	}
	if _, err := os.Stat(rebootArgv); err != nil {
		t.Fatal("the user said yes to rebooting but no reboot ran")
	}
}

func TestUpgradeCancelledTouchesNothing(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	agent, argv := fakeTool(t, 0)
	t.Setenv("HADRON_UPDATE_AGENT", agent)

	if code, out, _ := runCLI("n\n", "upgrade"); code != exitOK || !strings.Contains(out, "Cancelled") {
		t.Fatalf("code %d, out %q", code, out)
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatal("kairos-agent ran although the user said no")
	}
}

func TestUpgradeUpToDateIsANoop(t *testing.T) {
	setup(t, "v0.2.0", "v0.2.0")
	agent, argv := fakeTool(t, 0)
	t.Setenv("HADRON_UPDATE_AGENT", agent)

	if code, out, _ := runCLI("", "upgrade", "--yes"); code != exitOK || !strings.Contains(out, "nothing to do") {
		t.Fatalf("code %d, out %q", code, out)
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatal("kairos-agent ran on an up-to-date system")
	}
}

func TestUpgradePinnedVersionAndFailure(t *testing.T) {
	setup(t, "v0.2.0", "v0.2.0")
	agent, argv := fakeTool(t, 3)
	t.Setenv("HADRON_UPDATE_AGENT", agent)

	// --version installs what was asked for even when it is not the latest,
	// which is also how a user rolls back.
	code, _, errOut := runCLI("", "upgrade", "--yes", "--version", "v0.1.0")
	if code != exitError || !strings.Contains(errOut, "retry safely") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	got, _ := os.ReadFile(argv)
	if !strings.Contains(string(got), "hadron-desktop:sway-v0.1.0") {
		t.Fatalf("kairos-agent argv = %q", got)
	}

	if code, _, _ := runCLI("", "upgrade", "--yes", "--version", "latest"); code != exitUsage {
		t.Fatalf("a non-semver --version must be a usage error, got %d", code)
	}
}

func TestNotifyOnceNotifiesOncePerRelease(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	busctl, argv := fakeTool(t, 0)
	t.Setenv("HADRON_UPDATE_BUSCTL", busctl)

	if code, _, errOut := runCLI("", "notify", "--once"); code != exitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	got, _ := os.ReadFile(argv)
	if !strings.Contains(string(got), "Hadron Desktop v0.2.0 is available") ||
		!strings.Contains(string(got), "org.freedesktop.Notifications") {
		t.Fatalf("busctl argv = %q", got)
	}

	os.Remove(argv)
	if code, _, _ := runCLI("", "notify", "--once"); code != exitOK {
		t.Fatal("second run failed")
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatal("the same release was announced twice")
	}
}

func TestNotifyOnceSilentWhenUpToDate(t *testing.T) {
	setup(t, "v0.2.0", "v0.2.0")
	busctl, argv := fakeTool(t, 0)
	t.Setenv("HADRON_UPDATE_BUSCTL", busctl)

	if code, _, _ := runCLI("", "notify", "--once"); code != exitOK {
		t.Fatal("notify failed")
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatal("notified on an up-to-date system")
	}
}

func TestUnknownCommand(t *testing.T) {
	if code, _, errOut := runCLI("", "frobnicate"); code != exitUsage || !strings.Contains(errOut, "Usage") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

// busctl parses options anywhere in argv until `--`, so any positional value
// that starts with '-' (the -1 default timeout, a body beginning with a dash)
// must come after it. Found in a VM: busctl: unrecognized option '-1'.
func TestNotifyArgsEndOptionParsing(t *testing.T) {
	args := notifyArgs("-summary", "-body")
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		t.Fatalf("no -- in %q", args)
	}
	for _, a := range args[sep+1:] {
		if a == "--user" {
			t.Fatalf("--user must come before --: %q", args)
		}
	}
	for _, a := range args[:sep] {
		if a != "--user" {
			t.Fatalf("unexpected argument %q before --", a)
		}
	}
}

func TestUpgradeRefusesAnImageThatIsNotThere(t *testing.T) {
	setup(t, "v0.1.0", "v0.3.0") // v0.3.0 has a release but no image
	agent, argv := fakeTool(t, 0)
	t.Setenv("HADRON_UPDATE_AGENT", agent)

	code, _, errOut := runCLI("", "upgrade", "--yes")
	if code != exitError || !strings.Contains(errOut, "sway-v0.3.0 does not exist") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatal("kairos-agent ran for an image that does not exist")
	}
}

func TestUpgradeExplainsAPrivatePackage(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	fakeRegistry(t, true, "sway-v0.2.0")
	agent, argv := fakeTool(t, 0)
	t.Setenv("HADRON_UPDATE_AGENT", agent)

	code, _, errOut := runCLI("", "upgrade", "--yes")
	if code != exitError || !strings.Contains(errOut, "private or does not exist") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatal("kairos-agent ran for a private image")
	}
}

// The sudo child must not repeat the plan or the question the user already
// answered, and must not need the registry: the parent checked it.
func TestRootChildOnlyRunsTheAgent(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	t.Setenv("HADRON_UPDATE_REGISTRY", "http://127.0.0.1:1") // unreachable
	agent, argv := fakeTool(t, 0)
	t.Setenv("HADRON_UPDATE_AGENT", agent)

	code, out, errOut := runCLI("", "upgrade", "--as-root", "--yes", "--version", "v0.2.0")
	if code != exitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if strings.Contains(out, "Upgrading Hadron Desktop") || strings.Contains(out, "Continue?") {
		t.Fatalf("the root child repeated the plan: %q", out)
	}
	if _, err := os.Stat(argv); err != nil {
		t.Fatal("kairos-agent did not run")
	}
}

func TestNotifyOnceStaysQuietWithoutAnImage(t *testing.T) {
	setup(t, "v0.1.0", "v0.3.0") // released, but no sway-v0.3.0 image
	busctl, argv := fakeTool(t, 0)
	t.Setenv("HADRON_UPDATE_BUSCTL", busctl)

	if code, _, _ := runCLI("", "notify", "--once"); code != exitError {
		t.Fatalf("code %d, want an error naming the missing image", code)
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatal("announced a release that cannot be installed")
	}
	if alreadyNotified("v0.3.0") {
		t.Fatal("marked as notified, so it would never be announced once the image lands")
	}
}
