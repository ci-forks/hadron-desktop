# Tray Updater Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A tray icon in sway, i3, COSMIC and niri that shows, installs and reboots into new Hadron Desktop releases, another release, another desktop variant, or (advanced) any OCI image, with installs allowed for `admin` only through polkit.

**Architecture:** One Go module (`update/`, package `main`, one binary `hadron-update`) gains a root D-Bus service (`hadron-update daemon`, bus name `io.kairos.HadronUpdate1`, D-Bus activated) and a session tray (`hadron-update tray`). The daemon owns every network call, the job, the pin and the reboot; the tray and the CLI are clients. The daemon logic sits behind small interfaces so it is unit-tested without root, D-Bus or a network.

**Tech Stack:** Go 1.25, `github.com/godbus/dbus/v5`, `fyne.io/systray` (pure Go on Linux), polkit 126 (already built), snixembed (Vala, GTK3) for i3, cosign keyless signing in GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-11-05-tray-updater-design.md`

## Global Constraints

- Bus name, object path and interface: `io.kairos.HadronUpdate1`, `/io/kairos/HadronUpdate1`, `io.kairos.HadronUpdate1`.
- polkit actions: `io.kairos.hadron-update.apply`, `io.kairos.hadron-update.reboot`. The rule returns YES for group `admin`, NO for everyone else. No polkit agent is involved.
- Default image repository: `ghcr.io/kairos-community/hadron-desktop`; release images are `<repo>:<flavor>-<tag>`.
- Image labels (new): `io.kairos.hadron-desktop.flavor`, `io.kairos.hadron-desktop.version`, `io.kairos.hadron-desktop.hadron-version`.
- Pin file: `/var/lib/hadron-update/pin` (override `HADRON_UPDATE_PIN_FILE`). Job state dir: `/run/hadron-update/`.
- Advanced mode is on unless `/etc/hadron-desktop/update.conf` has `advanced=false` (override `HADRON_UPDATE_CONF`).
- Platform: `linux/amd64` only.
- Every install runs `kairos-agent upgrade --source oci:<repo>@<digest>` with the digest `Inspect` returned.
- The agent flavor gets no daemon, no tray and no notify; its CLI keeps today's direct `sudo` path.
- `CGO_ENABLED=0` everywhere in `update/`; the `update-build` stage still runs `go test ./...`.
- Existing env seams stay: `HADRON_UPDATE_AGENT`, `HADRON_UPDATE_API`, `HADRON_UPDATE_REGISTRY`, `HADRON_UPDATE_RELEASE_FILE`.
- Inspect is anonymous. A private image is refused with "needs registry credentials" (this narrows the spec's non-goal; say so in the PR).
- Commits: `git commit -s`, conventional subject (`feat(update): …`), trailer `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.

## Review Focus

- A digest reference (`repo@sha256:…`) or a registry with a port (`localhost:5000/x:y`): must parse; today `splitRef` breaks on both. Test in Task 1.
- An image index with no `linux/amd64` entry, or a single-arch arm64 manifest: must be refused before any install. Test in Task 2.
- `Apply` while a reboot is pending, or while another job runs: must be refused with a sentence, never queued. Test in Task 6.
- A release published before the labels existed (`<repo>:sway-v0.0.5`, no labels): must stay installable from "Other releases". Test in Task 4.
- The system was installed from a third-party image with no `/etc/hadron-desktop/release`: the tray must show "unmanaged image" and stop checking, not crash-loop. Test in Task 9.

---

### Task 1: Image references

**Files:**
- Create: `update/ref.go`, `update/ref_test.go`
- Modify: `update/registry.go:25-33` (delete `splitRef`), `update/registry.go:46-50` (use `ParseRef`)

**Interfaces:**
- Produces: `type ImageRef struct { Host, Name, Tag, Digest string }`; `func ParseRef(s string) (ImageRef, error)`; `func (r ImageRef) String() string`; `func (r ImageRef) Repo() string` (`host/name`); `func (r ImageRef) Reference() string` (digest if set, else tag).

- [ ] **Step 1: Write the failing test** `TestParseRef`, table-driven:
  - `ghcr.io/kairos-community/hadron-desktop:sway-v0.0.5` → Host `ghcr.io`, Name `kairos-community/hadron-desktop`, Tag `sway-v0.0.5`
  - `ghcr.io/a/b@sha256:<64 hex>` → Digest `sha256:<64 hex>`, Tag ``
  - `ghcr.io/a/b:t@sha256:<64 hex>` → both set; `Reference()` is the digest
  - `localhost:5000/x:y` → Host `localhost:5000`, Name `x`, Tag `y`
  - errors: `ubuntu` (no host), `ghcr.io/a/b` (no tag or digest), `ghcr.io/a/b@sha256:xyz` (bad digest), `GHCR.io/A:b` is accepted but `ghcr.io/A:b` with uppercase name is an error
  - `String()` round-trips every valid case.
- [ ] **Step 2: Run** `cd update && go test -run TestParseRef ./...` — Expected: FAIL, `undefined: ParseRef`.
- [ ] **Step 3: Implement** `ParseRef` in `update/ref.go`: split `@` first, then the last `:` after the last `/`. The host is the part before the first `/` and is required (no Docker Hub shorthand). `ImagePullable` calls `ParseRef` and uses `Reference()` in the manifest URL.
- [ ] **Step 4: Run** `cd update && go test ./...` — Expected: PASS, including the existing `TestUpgrade*` tests.
- [ ] **Step 5: Commit** `fix(update): parse digest and host:port image references`.

### Task 2: Resolve an image

**Files:**
- Modify: `update/registry.go`, `update/main_test.go:47-78` (extend `fakeRegistry`)
- Create: `update/registry_test.go`

**Interfaces:**
- Consumes: `ImageRef`, `ParseRef` (Task 1); `anonymousToken` (existing).
- Produces: `type ImageInfo struct { Ref ImageRef; Digest string; OS, Arch string; Labels map[string]string }`; `func Resolve(ctx context.Context, ref ImageRef) (ImageInfo, error)`; error sentinels `ErrNeedsCredentials`, `ErrWrongPlatform`.

- [ ] **Step 1: Extend `fakeRegistry`** so a test can publish an image with a chosen platform and labels: `fakeImage(t, name, tag string, arch string, labels map[string]string, index bool)`; it serves GET manifest (and index when `index`), and GET config blob, with `Docker-Content-Digest`.
- [ ] **Step 2: Write the failing tests** in `registry_test.go`:
  - `TestResolveSingleManifest`: amd64, labels `{flavor: sway}` → `Digest` equals the served digest, `Labels["io.kairos.hadron-desktop.flavor"] == "sway"`.
  - `TestResolveIndexPicksAmd64`: index with arm64 + amd64 → `Digest` is the **index** digest (what kairos-agent pulls), `Labels` come from the amd64 config.
  - `TestResolveRefusesOtherPlatforms`: index with only arm64, and a single arm64 manifest → `errors.Is(err, ErrWrongPlatform)`.
  - `TestResolvePrivate`: token service answers 403 → `errors.Is(err, ErrNeedsCredentials)`.
- [ ] **Step 3: Run** `cd update && go test -run TestResolve ./...` — Expected: FAIL, `undefined: Resolve`.
- [ ] **Step 4: Implement `Resolve`**: GET the manifest with the four Accept types already listed in `ImagePullable`; read `Docker-Content-Digest` (compute sha256 of the body if absent); for an index pick `platform.os=linux, architecture=amd64` and GET it; GET the config blob; read `config.Labels`, `os`, `architecture`. Share the token handshake with `ImagePullable` (factor a `getWithToken` helper).
- [ ] **Step 5: Run** `cd update && go test ./...` — Expected: PASS.
- [ ] **Step 6: Commit** `feat(update): resolve an image's digest, platform and labels`.

### Task 3: Published releases

**Files:**
- Modify: `update/github.go`, `update/main_test.go:16-28` (`fakeGitHub` serves `/releases` too)
- Test: `update/github_test.go` (create)

**Interfaces:**
- Produces: `func FetchReleases(ctx context.Context, repo string) ([]Latest, error)` — published, non-draft, non-prerelease, newest first, first page of 100.

- [ ] **Step 1: Write the failing test** `TestFetchReleasesSkipsDraftsAndPrereleases`: the fake returns `v0.0.5`, `v0.0.6-rc.1` (prerelease), `v0.0.7` (draft), `v0.0.4` → result tags `[v0.0.5 v0.0.4]`.
- [ ] **Step 2: Run** — Expected: FAIL, `undefined: FetchReleases`.
- [ ] **Step 3: Implement** `GET /repos/<repo>/releases?per_page=100`; same status handling as `FetchLatest`; sort with `Newer`.
- [ ] **Step 4: Run** `cd update && go test ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): list published releases`.

### Task 4: Install policy

**Files:**
- Create: `update/policy.go`, `update/policy_test.go`

**Interfaces:**
- Consumes: `ImageInfo` (Task 2), `Release` (existing), `Latest` (existing).
- Produces:
  - `type Verdict struct { Flavor, Version string; Trusted, Hadron bool }`
  - `func Classify(info ImageInfo, rel Release, published []Latest) (Verdict, error)`
  - `var ErrAgentImage, ErrNotHadron = errors.New(…), errors.New(…)`
  - `func ReleaseRef(rel Release, flavor, tag string) ImageRef`

- [ ] **Step 1: Write the failing tests:**
  - `TestClassifyTrustedByName`: ref `<rel.ImageRepo>:niri-v0.0.4`, no labels, `v0.0.4` published → `Verdict{Flavor: "niri", Version: "v0.0.4", Trusted: true, Hadron: true}`, nil.
  - `TestClassifyUnpublishedTagIsNotTrusted`: same ref, `v0.0.4` not in `published`, no labels → `ErrNotHadron`.
  - `TestClassifyLabelled`: other repo, labels flavor `sway` version `v9.9.9` → `Trusted: false, Hadron: true`.
  - `TestClassifyRefusesAgent`: label flavor `agent` → `ErrAgentImage`; tag `agent-v0.0.5` with no labels → `ErrAgentImage`.
  - `TestClassifyUnlabelledThirdParty`: no labels, other repo → `ErrNotHadron` (the daemon lets "Advanced…" override this one error only).
- [ ] **Step 2: Run** `cd update && go test -run TestClassify ./...` — Expected: FAIL.
- [ ] **Step 3: Implement** in the order: agent check (label or `agent-` tag prefix) → trusted by name (`Repo()==rel.ImageRepo`, tag `<flavor>-<vX.Y.Z>` with flavor in `sway i3 cosmic niri` and tag in `published`) → labels → `ErrNotHadron`.
- [ ] **Step 4: Run** `cd update && go test ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): decide which images may be installed`.

### Task 5: Pin and settings

**Files:**
- Create: `update/pin.go`, `update/pin_test.go`

**Interfaces:**
- Produces: `func LoadPin() (string, error)` (empty when no file), `func SavePin(ref string) error`, `func ClearPin() error`, `func AdvancedEnabled() bool`.

- [ ] **Step 1: Write the failing tests** with `HADRON_UPDATE_PIN_FILE` and `HADRON_UPDATE_CONF` in `t.TempDir()`: save/load round-trip; clear on a missing file is not an error; `AdvancedEnabled()` true with no file, false with `advanced=false`, true with `advanced=true`, true on an unreadable value.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**; `SavePin` creates the directory 0755 and writes atomically (temp file + rename).
- [ ] **Step 4: Run** `cd update && go test ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): pin a deliberately installed image`.

### Task 6: Daemon core

**Files:**
- Create: `update/daemon.go`, `update/daemon_test.go`

**Interfaces:**
- Consumes: Tasks 2-5.
- Produces:
  - `type State string` with constants `StateIdle "idle"`, `StateChecking "checking"`, `StateUpToDate "up-to-date"`, `StateUpdateAvailable "update-available"`, `StateInspecting "inspecting"`, `StateApplying "applying"`, `StateRebootPending "reboot-pending"`, `StateError "error"`, `StateUnmanaged "unmanaged"`.
  - `type Status struct { State State; Installed, Latest, Pin, Detail string; Advanced bool }`
  - `type Backend interface { Upgrade(ctx context.Context, source string, progress func(line string)) error; Reboot() error }`
  - `type Daemon struct` with `func NewDaemon(b Backend) *Daemon` and methods `Status() Status`, `Check(ctx) error`, `Releases(ctx) ([]Latest, error)`, `Inspect(ctx, ref string, advanced bool) (ImageInfo, Verdict, error)`, `Apply(ctx, ref, digest string, advanced bool) error` (starts the job, returns at once), `Cancel() error`, `Reboot() error`, `OnChange(func(Status))`, `OnProgress(func(string))`.
  - `var ErrBusy, ErrRebootPending, ErrNotCancellable`.

- [ ] **Step 1: Write the failing tests** with a fake `Backend` (records the source; blocks on a channel; can fail):
  - `TestCheckFindsUpdate` → `StateUpdateAvailable`, `Latest == "v0.0.6"`.
  - `TestCheckPinnedStaysQuiet`: pin set → state `StateUpToDate`, `Status().Latest` still `v0.0.6`.
  - `TestUnmanagedWhenNoReleaseFile` → `StateUnmanaged`, `Check` returns nil and makes no HTTP call.
  - `TestApplyUsesTheDigest`: backend source == `oci:ghcr.io/kairos-community/hadron-desktop@sha256:…`.
  - `TestApplyRefusesWhileBusy` → `ErrBusy`; `TestApplyRefusesWhileRebootPending` → `ErrRebootPending`.
  - `TestApplyDigestMismatch`: digest arg differs from a fresh `Resolve` → error, backend not called.
  - `TestApplyPinRules`: latest release → pin cleared; older release → pin = ref; advanced third-party → pin = ref; another flavor's latest → pin cleared; pin written only after the backend returns nil.
  - `TestAdvancedOverridesOnlyNotHadron`: unlabelled third-party with `advanced=true` → allowed; `ErrAgentImage` with `advanced=true` → refused; with `AdvancedEnabled()==false` → refused.
  - `TestCancelOnlyBeforeUpgrade`: during `StateInspecting` → ok, back to previous state; during `StateApplying` → `ErrNotCancellable`.
  - `TestBackendFailureGoesToError`: `StateError`, `Detail` holds the last progress line.
- [ ] **Step 2: Run** `cd update && go test -run 'Test(Check|Apply|Cancel|Backend|Advanced|Unmanaged)' ./...` — Expected: FAIL.
- [ ] **Step 3: Implement** with one `sync.Mutex`; the job runs in a goroutine; `Apply` re-runs `Resolve` and `Classify` itself (never trusts the client's verdict); `kairosBackend` (real) runs `kairosAgent() upgrade --source …` and scans stdout+stderr lines into `progress`; `Reboot` runs `systemctl reboot`.
- [ ] **Step 4: Run** `cd update && go test -race ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): add the update daemon's job and state machine`.

### Task 7: D-Bus service and polkit

**Files:**
- Create: `update/dbus.go`, `update/dbus_test.go`, `rootfs/usr/share/dbus-1/system.d/io.kairos.HadronUpdate1.conf`, `rootfs/usr/share/dbus-1/system-services/io.kairos.HadronUpdate1.service`, `rootfs/usr/lib/systemd/system/hadron-updated.service`, `rootfs/usr/share/polkit-1/actions/io.kairos.hadron-update.policy`, `rootfs/usr/share/polkit-1/rules.d/50-hadron-update.rules`
- Modify: `update/go.mod`, `update/go.sum`, `update/main.go` (command `daemon`, usage text), `Dockerfile:3778-3788` (stage comment: no longer stdlib-only)

**Interfaces:**
- Consumes: `Daemon` (Task 6).
- Produces: D-Bus methods `Status() (a{sv})`, `Check()`, `Releases() (as)`, `Inspect(s ref) (a{sv})`, `Apply(s ref, s digest, b advanced)`, `Cancel()`, `Reboot()`; signals `StateChanged(a{sv})`, `Progress(s line)`. `type Authorizer interface { Allowed(sender dbus.Sender, action string) (bool, error) }`, `polkitAuthorizer` (real), `func serve(conn *dbus.Conn, d *Daemon, a Authorizer) error`.

- [ ] **Step 1: Write the failing test** `TestBusRefusesUnauthorized`: start a private `dbus-daemon --session --print-address` (skip when the binary is missing, as in the golang build stage), export with a fake `Authorizer` that says no → calling `Apply` returns D-Bus error `io.kairos.HadronUpdate1.Error.NotAuthorized`, and `Status` still works. `TestBusAppliesWhenAuthorized`: fake says yes → the fake backend receives the source.
- [ ] **Step 2: Run** `cd update && go test -run TestBus ./...` — Expected: FAIL.
- [ ] **Step 3: Implement** `serve`; `polkitAuthorizer.Allowed` calls `org.freedesktop.PolicyKit1.Authority.CheckAuthorization` with subject `("system-bus-name", {"name": sender})`, no details, flags `0` (no interaction). `hadron-update daemon` connects to the system bus, requests the name, exits after 10 idle minutes with no job.
- [ ] **Step 4: Write the system files:**
  - D-Bus policy: `<allow own="io.kairos.HadronUpdate1"/>` for root; `<allow send_destination="io.kairos.HadronUpdate1"/>` for default context.
  - Activation file: `Name=io.kairos.HadronUpdate1`, `Exec=/bin/false`, `User=root`, `SystemdService=hadron-updated.service`.
  - Unit: `Type=dbus`, `BusName=io.kairos.HadronUpdate1`, `ExecStart=/usr/bin/hadron-update daemon`, `StateDirectory=hadron-update`, `RuntimeDirectory=hadron-update`. No `[Install]` section.
  - Action file: both actions, `allow_any/allow_inactive/allow_active` = `no`.
  - Rule: `polkit.addRule(function(action, subject) { if (action.id.indexOf("io.kairos.hadron-update.") == 0) { return subject.isInGroup("admin") ? polkit.Result.YES : polkit.Result.NO; } });`
- [ ] **Step 5: Run** `cd update && go test -race ./... && go vet ./...` — Expected: PASS.
- [ ] **Step 6: Commit** `feat(update): serve the updater on the system bus, authorized by polkit`.

### Task 8: CLI through the daemon

**Files:**
- Modify: `update/main.go`, `update/main_test.go`
- Create: `update/client.go`

**Interfaces:**
- Consumes: the D-Bus API (Task 7).
- Produces: `type client struct` with the same methods as the D-Bus API; `func dial() (*client, error)`. Commands:
  - `upgrade [--version vX] [--image REF] [--yes] [--reboot]`
  - `switch FLAVOR [--yes]`
  - `reboot`
  - `status` gains a `Pin:` line.

- [ ] **Step 1: Write the failing tests** using a fake `client` (inject through a package var `dialDaemon`): `upgrade --image` with advanced off → exit 2 and "advanced mode is off"; `switch agent` → exit 2; `switch niri` prints the per-user-settings warning and needs `y`; an unlabelled image asks a second, explicit question that defaults to no; `FLAVOR=agent` keeps the direct path (existing `TestUpgradeRunsKairosAgentWithTheFlavorImage` still passes with `FLAVOR=agent`).
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**: flow for every install is `Inspect` → print repo, digest, flavor, version, trusted/labelled → confirm → `Apply` → stream `Progress` until `reboot-pending` or `error` → ask "Reboot now?". When `dial` fails on a non-agent system, say the service is missing and exit 1 (no silent sudo fallback).
- [ ] **Step 4: Run** `cd update && go test -race ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): install releases, other flavors and images through the daemon`.

### Task 9: Tray

**Files:**
- Create: `update/tray.go`, `update/menu.go`, `update/menu_test.go`, `update/icons/` (embedded PNGs: `idle`, `update`, `busy`, `reboot`, `error`, 22 px and 44 px)
- Modify: `update/main.go` (command `tray`), `update/main.go:256-306` (`notify` becomes `tray --no-icon`, same once-per-tag rule, sends nothing while pinned)
- Modify: `rootfs-sway/etc/hadron-desktop/session`, `rootfs-i3/…`, `rootfs-cosmic/…`, `rootfs-niri/…` — add `TERMINAL_EXEC=`: sway `foot --app-id=floatterm`, i3 `st -c HadronFloat -e`, niri `foot --app-id=floatterm`, cosmic `cosmic-term -e` (verify the flag in the cosmic-term source pinned in the Dockerfile)

**Interfaces:**
- Consumes: `client` (Task 8), `Status` (Task 6).
- Produces: `type MenuItem struct { ID, Label string; Enabled bool; Children []MenuItem }`; `func BuildMenu(st Status, releases []Latest, flavor string) []MenuItem` (pure); IDs: `status`, `install-latest`, `releases/<tag>`, `switch/<flavor>`, `advanced`, `cancel`, `restart`.

- [ ] **Step 1: Write the failing tests** for `BuildMenu`:
  - update-available → `install-latest` label `Install v0.0.6`, enabled.
  - pinned → `install-latest` label `Install latest (v0.0.6)`.
  - applying → every install item disabled, `cancel` absent.
  - inspecting → `cancel` present.
  - reboot-pending → `restart` present, installs disabled.
  - `switch/*` lists the three other flavors, never `agent`, never the current one.
  - `advanced` absent when `Status.Advanced` is false.
  - unmanaged → only `status` (label `Unmanaged image`) and `advanced`.
- [ ] **Step 2: Run** `cd update && go test -run TestBuildMenu ./...` — Expected: FAIL.
- [ ] **Step 3: Implement** `BuildMenu`; then `tray.go`: `systray.Run`, rebuild the menu on `StateChanged`, tooltip = last `Progress` line, icon by state. Items that need input (`releases/*`, `switch/*`, `advanced`, `restart`) exec `$TERMINAL_EXEC hadron-update <subcommand>`; `advanced` runs a small prompt (`hadron-update upgrade --image` reading the ref from stdin). If the SNI watcher is missing, retry every 5 s. Exit 0 at once when `FLAVOR=agent`.
- [ ] **Step 4: Run** `cd update && go test -race ./...` — Expected: PASS. Then `CGO_ENABLED=0 GOOS=linux go build ./...` — Expected: builds.
- [ ] **Step 5: Commit** `feat(update): add the tray icon`.

### Task 10: Every variant hosts the tray

**Files:**
- Modify: `rootfs-sway/etc/sway/config:66`, `rootfs-i3/etc/i3/config:49`, `rootfs-niri/etc/niri/config.kdl:288`, `rootfs-cosmic/etc/xdg/autostart/hadron-update-notify.desktop` (rename to `hadron-update-tray.desktop`) — `notify` → `tray`
- Modify: `rootfs-i3/etc/i3/config` — `exec --no-startup-id snixembed --fork` before the tray line
- Modify: `Dockerfile` — `COPY --from=polkit /polkit /` in `sway-rootfs` (`:3436`) and `i3-rootfs` (`:3514`); new stages `vala` and `snixembed` copied into `i3-rootfs`; the comment at `:3907` ("polkit (COSMIC and niri)") names all four
- Modify: `Dockerfile.agent:181` area — remove the D-Bus policy, activation file, unit, polkit action and rule, and the i3 `hadron-update` exec line
- Modify: `test/boot-ux/` or a new `test/update/check-tray-wiring.sh` (wired into `Makefile` `check-boot-ux`)

**Interfaces:**
- Consumes: the files from Task 7, the `tray` command from Task 9.

- [ ] **Step 1: Write the failing check** `test/update/check-tray-wiring.sh IMAGE FLAVOR`: inside the image, assert `/usr/share/dbus-1/system-services/io.kairos.HadronUpdate1.service`, the polkit rule and `/usr/lib/polkit-1/polkitd` exist and the session config starts `hadron-update tray`; for i3 also `snixembed`; for the agent image assert all of them are absent and nothing starts `hadron-update`.
- [ ] **Step 2: Run** it against a current local image (or `--dry` on the config files) — Expected: FAIL.
- [ ] **Step 3: Implement** the stages and edits. `vala`: valac from the GNOME release tarball (renovate annotation like the GNOME libraries), build-time only, never copied into a rootfs. `snixembed`: pinned release from its GitHub mirror if one exists, else the sr.ht tarball with the existing retry flags; links GTK3 and libdbusmenu-gtk3 (both built). Run `scripts/check-renovate-pins.sh`.
- [ ] **Step 4: Verify** `docker build --target snixembed --build-arg HADRON_VERSION=v0.6.3 .` succeeds and `ldd` inside shows only libraries present in the i3 image; `bash -n` on the new script.
- [ ] **Step 5: Commit** `feat: start the update tray in every variant`.

### Task 11: Release labels and signatures, docs

**Files:**
- Modify: `Dockerfile` `kairos` stage (`:3974`) — `LABEL io.kairos.hadron-desktop.flavor="${DESKTOP}" io.kairos.hadron-desktop.version="${VERSION}" io.kairos.hadron-desktop.hadron-version="${HADRON_VERSION}"`; `Dockerfile.agent` sets `flavor="agent"`
- Modify: `.github/workflows/release.yml` — after each `docker push`, `cosign sign --yes "${IMAGE_REPO}@${digest}"` (install with `sigstore/cosign-installer`, pinned by version); add `id-token: write` to the two pushing jobs
- Modify: `scripts/release-manifest.sh` — fail if the image's labels disagree with the flavor and tag
- Modify: `README.md` (updates section), `AGENTS.md` (tray row: drop "until it lands"), `rootfs-*/etc/motd` ("Updates" line mentions the tray)

- [ ] **Step 1: Write the failing check** in `release-manifest.sh`: `docker image inspect --format '{{index .Config.Labels "io.kairos.hadron-desktop.flavor"}}'` must equal `$flavor`, the version label `$release_tag`. Run it on an image built without labels — Expected: exits 1 with a sentence.
- [ ] **Step 2: Implement** the labels, the signing step and the doc edits.
- [ ] **Step 3: Verify** `actionlint` (`docker run --rm -v "$PWD":/repo -w /repo rhysd/actionlint:1.7.7 -no-color`) — Expected: no output; `docker build --target kairos` is not run locally (hours) — say so in the PR; the next fork rehearsal (`git push ci-forks vX-rc.N`) checks labels and signatures end to end.
- [ ] **Step 4: Commit** `feat(release): label and sign the upgrade images`.
