package main

import "testing"

func TestParseRef(t *testing.T) {
	const hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	good := []struct {
		in        string
		want      ImageRef
		reference string
		repo      string
	}{
		{"ghcr.io/kairos-community/hadron-desktop:sway-v0.0.5",
			ImageRef{Host: "ghcr.io", Name: "kairos-community/hadron-desktop", Tag: "sway-v0.0.5"},
			"sway-v0.0.5", "ghcr.io/kairos-community/hadron-desktop"},
		{"ghcr.io/a/b@sha256:" + hex,
			ImageRef{Host: "ghcr.io", Name: "a/b", Digest: "sha256:" + hex},
			"sha256:" + hex, "ghcr.io/a/b"},
		{"ghcr.io/a/b:t@sha256:" + hex,
			ImageRef{Host: "ghcr.io", Name: "a/b", Tag: "t", Digest: "sha256:" + hex},
			"sha256:" + hex, "ghcr.io/a/b"},
		{"localhost:5000/x:y",
			ImageRef{Host: "localhost:5000", Name: "x", Tag: "y"}, "y", "localhost:5000/x"},
		{"reg.example:5000/a/b@sha256:" + hex,
			ImageRef{Host: "reg.example:5000", Name: "a/b", Digest: "sha256:" + hex},
			"sha256:" + hex, "reg.example:5000/a/b"},
		// The host is case-insensitive, so it is accepted as written.
		{"GHCR.io/a:b", ImageRef{Host: "GHCR.io", Name: "a", Tag: "b"}, "b", "GHCR.io/a"},
	}
	for _, c := range good {
		got, err := ParseRef(c.in)
		if err != nil {
			t.Errorf("ParseRef(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseRef(%q) = %+v, want %+v", c.in, got, c.want)
		}
		if got.String() != c.in {
			t.Errorf("String() = %q, want %q", got.String(), c.in)
		}
		if got.Reference() != c.reference {
			t.Errorf("Reference(%q) = %q, want %q", c.in, got.Reference(), c.reference)
		}
		if got.Repo() != c.repo {
			t.Errorf("Repo(%q) = %q, want %q", c.in, got.Repo(), c.repo)
		}
	}

	bad := []string{
		"",
		"ubuntu",
		"ubuntu:22.04",
		"ghcr.io/a/b",
		"ghcr.io/a/b@sha256:xyz",
		"ghcr.io/a/b@sha256:" + hex[:63],
		"ghcr.io/A:b",
		"ghcr.io/a/B:b",
		"ghcr.io/a/b:",
		"ghcr.io/a/b:bad tag",
		"ghcr.io//b:t",
		"ghcr.io/a/b/:t",
	}
	for _, in := range bad {
		if got, err := ParseRef(in); err == nil {
			t.Errorf("ParseRef(%q) = %+v, want error", in, got)
		}
	}
}
