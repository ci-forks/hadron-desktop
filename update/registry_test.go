package main

import (
	"context"
	"errors"
	"testing"
)

const hdLabel = "io.kairos.hadron-desktop.flavor"

func mustRef(t *testing.T, s string) ImageRef {
	t.Helper()
	r, err := ParseRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const testImage = "ghcr.io/kairos-community/hadron-desktop:sway-v1"

func TestResolveSingleManifest(t *testing.T) {
	want := fakeImage(t, "hadron-desktop", "sway-v1", "amd64", map[string]string{hdLabel: "sway"}, false)
	got, err := Resolve(context.Background(), mustRef(t, testImage))
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != want || got.Labels[hdLabel] != "sway" || got.OS != "linux" || got.Arch != "amd64" {
		t.Fatalf("got %+v, want digest %s", got, want)
	}
}

func TestResolveIndexPicksAmd64(t *testing.T) {
	want := fakeImage(t, "hadron-desktop", "sway-v1", "arm64,amd64", map[string]string{hdLabel: "sway"}, true)
	got, err := Resolve(context.Background(), mustRef(t, testImage))
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != want {
		t.Fatalf("digest %s, want the index digest %s", got.Digest, want)
	}
	if got.Labels[hdLabel] != "sway" || got.Labels["wrong-arch"] != "" {
		t.Fatalf("labels %v are not the amd64 config's", got.Labels)
	}
}

func TestResolveByDigest(t *testing.T) {
	want := fakeImage(t, "hadron-desktop", "sway-v1", "arm64,amd64", map[string]string{hdLabel: "sway"}, true)
	got, err := Resolve(context.Background(), mustRef(t, "ghcr.io/kairos-community/hadron-desktop@"+want))
	if err != nil || got.Digest != want {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestResolveComputesDigestWhenHeaderIsMissing(t *testing.T) {
	reg := newFakeRegistry(t, false)
	reg.omitDigest = true
	want := reg.image(t, "hadron-desktop", "sway-v1", "amd64", nil, false)
	got, err := Resolve(context.Background(), mustRef(t, testImage))
	if err != nil || got.Digest != want {
		t.Fatalf("got %+v, %v; want digest %s", got, err, want)
	}
}

func TestResolveRefusesOtherPlatforms(t *testing.T) {
	for name, index := range map[string]bool{"index": true, "single manifest": false} {
		t.Run(name, func(t *testing.T) {
			fakeImage(t, "hadron-desktop", "sway-v1", "arm64", nil, index)
			_, err := Resolve(context.Background(), mustRef(t, testImage))
			if !errors.Is(err, ErrWrongPlatform) {
				t.Fatalf("err = %v, want ErrWrongPlatform", err)
			}
		})
	}
}

func TestResolvePrivate(t *testing.T) {
	fakeRegistry(t, true)
	_, err := Resolve(context.Background(), mustRef(t, testImage))
	if !errors.Is(err, ErrNeedsCredentials) {
		t.Fatalf("err = %v, want ErrNeedsCredentials", err)
	}
}

func TestResolveMissingImage(t *testing.T) {
	fakeRegistry(t, false)
	_, err := Resolve(context.Background(), mustRef(t, testImage))
	if err == nil || errors.Is(err, ErrNeedsCredentials) {
		t.Fatalf("err = %v, want a plain does-not-exist error", err)
	}
}
