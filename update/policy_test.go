package main

import (
	"errors"
	"testing"
)

const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func policyRel() Release {
	return Release{Flavor: "sway", Version: "v0.0.3", ImageRepo: "ghcr.io/kairos-community/hadron-desktop", GitHubRepo: "kairos-community/hadron-desktop"}
}

func policyInfo(t *testing.T, ref string, labels map[string]string) ImageInfo {
	t.Helper()
	r, err := ParseRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	return ImageInfo{Ref: r, Labels: labels}
}

func TestClassifyTrustedByName(t *testing.T) {
	rel := policyRel()
	info := policyInfo(t, rel.ImageRepo+":niri-v0.0.4", nil)
	v, err := Classify(info, rel, []Latest{{Tag: "v0.0.4"}})
	want := Verdict{Flavor: "niri", Version: "v0.0.4", GPU: gpuFull, Trusted: true, Hadron: true}
	if err != nil || v != want {
		t.Fatalf("got %+v, %v; want %+v", v, err, want)
	}
}

func TestClassifyUnpublishedTagIsNotTrusted(t *testing.T) {
	rel := policyRel()
	info := policyInfo(t, rel.ImageRepo+":niri-v0.0.4", nil)
	if _, err := Classify(info, rel, []Latest{{Tag: "v0.0.3"}}); !errors.Is(err, ErrNotHadron) {
		t.Fatalf("got %v, want ErrNotHadron", err)
	}
}

func TestClassifyTrustedByNameRejectsLookalikes(t *testing.T) {
	rel := policyRel()
	pub := []Latest{{Tag: "v0.0.4"}, {Tag: "v0.0.4-rc1"}}
	for _, ref := range []string{
		rel.ImageRepo + ":foo-v0.0.4",             // not a desktop flavor
		rel.ImageRepo + ":niri-v0.0.4-rc1",        // prerelease
		rel.ImageRepo + ":niri-0.0.4",             // no v
		rel.ImageRepo + ":niri-vm-v0.0.4-rc1",     // prerelease, vm line
		rel.ImageRepo + ":niri-gpu-v0.0.4",        // not a line
		rel.ImageRepo + ":vm-niri-v0.0.4",         // line first
		rel.ImageRepo + ":niri-vm-vm-v0.0.4",      // line twice
		"ghcr.io/evil/hadron-desktop:niri-v0.0.4", // other repo
		rel.ImageRepo + "@sha256:" + hex64,        // digest only
	} {
		if _, err := Classify(policyInfo(t, ref, nil), rel, pub); !errors.Is(err, ErrNotHadron) {
			t.Errorf("%s: got %v, want ErrNotHadron", ref, err)
		}
	}
}

func TestClassifyLabelled(t *testing.T) {
	labels := map[string]string{labelFlavor: "sway", labelVersion: "v9.9.9"}
	info := policyInfo(t, "example.com/other/img:latest", labels)
	v, err := Classify(info, policyRel(), nil)
	want := Verdict{Flavor: "sway", Version: "v9.9.9", Hadron: true}
	if err != nil || v != want {
		t.Fatalf("got %+v, %v; want %+v", v, err, want)
	}
}

func TestClassifyLabelledNeedsFlavorAndVersion(t *testing.T) {
	for _, labels := range []map[string]string{
		{labelFlavor: "sway"},
		{labelVersion: "v1.0.0"},
		{labelFlavor: "bogus", labelVersion: "v1.0.0"},
	} {
		info := policyInfo(t, "example.com/other/img:latest", labels)
		if _, err := Classify(info, policyRel(), nil); !errors.Is(err, ErrNotHadron) {
			t.Errorf("%v: got %v, want ErrNotHadron", labels, err)
		}
	}
}

func TestClassifyRefusesAgent(t *testing.T) {
	rel := policyRel()
	byLabel := policyInfo(t, "example.com/other/img:latest", map[string]string{labelFlavor: "agent", labelVersion: "v1.0.0"})
	if _, err := Classify(byLabel, rel, nil); !errors.Is(err, ErrAgentImage) {
		t.Fatalf("label: got %v", err)
	}
	byTag := policyInfo(t, rel.ImageRepo+":agent-v0.0.5", nil)
	if _, err := Classify(byTag, rel, []Latest{{Tag: "v0.0.5"}}); !errors.Is(err, ErrAgentImage) {
		t.Fatalf("tag: got %v", err)
	}
}

func TestClassifyUnlabelledThirdParty(t *testing.T) {
	info := policyInfo(t, "example.com/other/img:latest", nil)
	if _, err := Classify(info, policyRel(), nil); !errors.Is(err, ErrNotHadron) {
		t.Fatalf("got %v, want ErrNotHadron", err)
	}
}

func TestReleaseRef(t *testing.T) {
	rel := policyRel()
	ref, err := ReleaseRef(rel, "niri", "v0.0.4")
	if err != nil || ref.Repo() != rel.ImageRepo || ref.Tag != "niri-v0.0.4" {
		t.Fatalf("got %+v, %v", ref, err)
	}
	if _, err := ReleaseRef(Release{}, "niri", "v0.0.4"); err == nil {
		t.Fatal("empty ImageRepo must fail")
	}
}

func TestClassifyTagPlusDigestIsNotTrustedByName(t *testing.T) {
	rel := policyRel()
	ref := rel.ImageRepo + ":niri-v0.0.4@sha256:" + hex64
	pub := []Latest{{Tag: "v0.0.4"}}
	if _, err := Classify(policyInfo(t, ref, nil), rel, pub); !errors.Is(err, ErrNotHadron) {
		t.Fatalf("got %v, want ErrNotHadron", err)
	}
	agent := map[string]string{labelFlavor: "agent", labelVersion: "v0.0.4"}
	if _, err := Classify(policyInfo(t, ref, agent), rel, pub); !errors.Is(err, ErrAgentImage) {
		t.Fatalf("got %v, want ErrAgentImage", err)
	}
	labelled := map[string]string{labelFlavor: "niri", labelVersion: "v0.0.4"}
	v, err := Classify(policyInfo(t, ref, labelled), rel, pub)
	if err != nil || v.Trusted || !v.Hadron {
		t.Fatalf("got %+v, %v; want labelled, untrusted", v, err)
	}
}

func TestClassifyVersionLabelMustBeStrict(t *testing.T) {
	for _, v := range []string{"latest", "v1.0", "v1.0.0-rc1", "1.0.0", "v01.0.0"} {
		info := policyInfo(t, "example.com/o/i:x", map[string]string{labelFlavor: "sway", labelVersion: v})
		if _, err := Classify(info, policyRel(), nil); !errors.Is(err, ErrNotHadron) {
			t.Errorf("%q: got %v, want ErrNotHadron", v, err)
		}
	}
}

// An image of the other line is a published release like any other: trusted
// by name, and its line reported so the CLI can warn, as for another flavor.
func TestClassifyLines(t *testing.T) {
	pub := []Latest{{Tag: "v0.0.4"}}
	for _, rel := range []Release{policyRel(), func() Release { r := policyRel(); r.GPU = gpuVM; return r }()} {
		for tag, line := range map[string]string{"niri-v0.0.4": gpuFull, "niri-vm-v0.0.4": gpuVM, "sway-vm-v0.0.4": gpuVM} {
			v, err := Classify(policyInfo(t, rel.ImageRepo+":"+tag, nil), rel, pub)
			if err != nil || !v.Trusted || v.GPU != line || v.Version != "v0.0.4" {
				t.Errorf("%s on a %s system: got %+v, %v; want trusted, line %s", tag, rel.Line(), v, err, line)
			}
		}
	}
	if _, err := Classify(policyInfo(t, policyRel().ImageRepo+":agent-vm-v0.0.4", nil), policyRel(), pub); !errors.Is(err, ErrAgentImage) {
		t.Errorf("agent-vm: got %v, want ErrAgentImage", err)
	}
}

func TestClassifyLabelledLine(t *testing.T) {
	for label, want := range map[string]string{"vm": gpuVM, "full": gpuFull, "": "", "bogus": ""} {
		labels := map[string]string{labelFlavor: "sway", labelVersion: "v1.0.0"}
		if label != "" {
			labels[labelGPU] = label
		}
		v, err := Classify(policyInfo(t, "example.com/o/i:x", labels), policyRel(), nil)
		if err != nil || v.GPU != want {
			t.Errorf("label %q: got %+v, %v; want GPU %q", label, v, err, want)
		}
	}
}

func TestLineRef(t *testing.T) {
	rel := policyRel()
	for line, tag := range map[string]string{gpuFull: "i3-v0.0.4", gpuVM: "i3-vm-v0.0.4"} {
		ref, err := LineRef(rel, "i3", line, "v0.0.4")
		if err != nil || ref.Tag != tag {
			t.Errorf("%s: got %+v, %v; want tag %s", line, ref, err, tag)
		}
	}
}
