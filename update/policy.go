package main

import (
	"errors"
	"regexp"
	"strings"
)

// Image labels the Dockerfile's kairos stage writes (Task 11 uses the same
// strings). They let an image outside the release repo, such as a mirror,
// declare that it is an Isotope image.
const (
	labelFlavor  = "io.kairos.isotope.flavor"
	labelVersion = "io.kairos.isotope.version"
	labelHadron  = "io.kairos.isotope.hadron-version"
	// labelGPU is the image line, full or vm. Images up to v0.0.7 have
	// none.
	labelGPU = "io.kairos.isotope.gpu"
)

var (
	// ErrAgentImage is never overridable: the agent flavor has no daemon, so
	// switching a desktop system onto it would remove its own updater.
	ErrAgentImage = errors.New("this is an agent image; the desktop updater cannot install it")
	// ErrServerImage and ErrDesktopImage keep the two editions apart, and
	// are never overridable either: a desktop installed over a server, or a
	// server over a desktop, is a different system, not an upgrade.
	ErrServerImage  = errors.New("this is a server edition image; a desktop system cannot install it")
	ErrDesktopImage = errors.New("this is a desktop image; the server edition installs only server images")
	// ErrNotIsotope and ErrNotRelease are the refusals the daemon's advanced
	// mode may override.
	ErrNotIsotope = errors.New("this is not an Isotope image")
	// ErrNotRelease is the daemon's refusal of an image that Classify
	// accepts but does not trust: labelled as Isotope, but not a published
	// release of the release repository. The spec accepts images from other
	// repositories only through advanced mode.
	ErrNotRelease = errors.New("this is not a published Isotope release")
)

// desktopFlavors are the flavors that ship the daemon and tray.
var desktopFlavors = map[string]bool{"sway": true, "i3": true, "cosmic": true, "niri": true}

// installable are the flavors a system of rel's flavor may install: a server
// only server images, anything else only the desktops.
func installable(rel Release) map[string]bool {
	if rel.Flavor == flavorServer {
		return map[string]bool{flavorServer: true}
	}
	return desktopFlavors
}

// editionRefusal is the error for an image of flavor on a system of rel's
// flavor when the two are not the same edition, or nil.
func editionRefusal(rel Release, flavor string) error {
	switch {
	case rel.Flavor == flavorServer && desktopFlavors[flavor]:
		return ErrDesktopImage
	case rel.Flavor != flavorServer && flavor == flavorServer:
		return ErrServerImage
	}
	return nil
}

// strictTag is vX.Y.Z only: no prerelease, no build metadata, no leading zeros.
var strictTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Verdict says what an image is and how far it is trusted.
type Verdict struct {
	Flavor, Version string
	// GPU is the image line, gpuFull or gpuVM, or "" when a labelled image
	// does not say. An image on the other line than the running system is
	// installed like one of another flavor: allowed, and the CLI says so.
	GPU     string
	Trusted bool // named like a release published in the release repo
	Isotope bool // trusted, or labelled as a desktop image
}

// Classify decides whether info may be installed. The agent check runs first
// and wins over every other signal, so a label cannot launder an agent image.
// The edition check runs next, on the label and on a release repo tag, so a
// server image cannot pass for a desktop one or the other way round.
func Classify(info ImageInfo, rel Release, published []Latest) (Verdict, error) {
	if label(info, labelFlavor) == "agent" || strings.HasPrefix(info.Ref.Tag, "agent-") {
		return Verdict{}, ErrAgentImage
	}
	if err := editionRefusal(rel, label(info, labelFlavor)); err != nil {
		return Verdict{}, err
	}
	if tagFlavor, _, ok := strings.Cut(info.Ref.Tag, "-"); ok && rel.ImageRepo != "" && info.Ref.Repo() == rel.ImageRepo {
		if err := editionRefusal(rel, tagFlavor); err != nil {
			return Verdict{}, err
		}
	}
	allowed := installable(rel)

	// Trusted by name: the repo and a tag that a published release maps to.
	// A digest-only ref has no tag, and a tag+digest ref is resolved by the
	// digest, so the tag says nothing about the content: neither is trusted by
	// name. Both can still be labelled.
	if rel.ImageRepo != "" && info.Ref.Digest == "" && info.Ref.Repo() == rel.ImageRepo {
		flavor, tag, ok := strings.Cut(info.Ref.Tag, "-")
		line := gpuFull
		if rest, isVM := strings.CutPrefix(tag, gpuVM+"-"); isVM {
			line, tag = gpuVM, rest
		}
		if ok && allowed[flavor] && (flavor != flavorServer || line == gpuFull) && strictTag.MatchString(tag) {
			if isPublished(tag, published) {
				return Verdict{Flavor: flavor, Version: tag, GPU: line, Trusted: true, Isotope: true}, nil
			}
		}
	}

	flavor, version := label(info, labelFlavor), label(info, labelVersion)
	if allowed[flavor] && strictTag.MatchString(version) {
		line := label(info, labelGPU)
		if line != gpuFull && line != gpuVM {
			line = ""
		}
		return Verdict{Flavor: flavor, Version: version, GPU: line, Isotope: true}, nil
	}
	return Verdict{}, ErrNotIsotope
}

// legacyLabelPrefix is the label prefix from before the rename to Isotope.
// Images built before it carry only these labels, and an agent image among
// them must still be refused.
const (
	labelPrefix       = "io.kairos.isotope."
	legacyLabelPrefix = "io.kairos.hadron-desktop."
)

// label returns the image's label key, or the same label under the old
// prefix when the image has no label of the new name.
func label(info ImageInfo, key string) string {
	if v, ok := info.Labels[key]; ok {
		return v
	}
	return info.Labels[legacyLabelPrefix+strings.TrimPrefix(key, labelPrefix)]
}

func isPublished(tag string, published []Latest) bool {
	for _, p := range published {
		if p.Tag == tag {
			return true
		}
	}
	return false
}

// ReleaseRef is the reference release.yml pushes for flavor and tag on the
// running system's line. It returns an error, not a zero value, when the
// release has no image repo or the parts do not form a valid reference.
func ReleaseRef(rel Release, flavor, tag string) (ImageRef, error) {
	return LineRef(rel, flavor, rel.Line(), tag)
}

// LineRef is ReleaseRef for a given line, which `switch --gpu` changes.
func LineRef(rel Release, flavor, line, tag string) (ImageRef, error) {
	if rel.ImageRepo == "" {
		return ImageRef{}, errors.New("release has no image repository")
	}
	return ParseRef(rel.ImageRepo + ":" + lineTag(flavor, line, tag))
}

// lineLabels are the words the CLI and the tray use for the lines.
var lineLabels = map[string]string{
	gpuFull: "hardware (Intel and AMD GPU drivers, firmware)",
	gpuVM:   "virtual machine (no GPU drivers or firmware)",
}
