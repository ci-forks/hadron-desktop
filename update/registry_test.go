package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestResolveRejectsAHeaderThatDisagreesWithTheBody(t *testing.T) {
	reg := newFakeRegistry(t, false)
	reg.image(t, "hadron-desktop", "sway-v1", "amd64", nil, false)
	reg.headerDigest = "sha256:" + strings.Repeat("a", 64)
	if _, err := Resolve(context.Background(), mustRef(t, testImage)); err == nil {
		t.Fatal("a Docker-Content-Digest that is not the body's digest was accepted")
	}
	reg.headerDigest = "sha256:short"
	if _, err := Resolve(context.Background(), mustRef(t, testImage)); err == nil {
		t.Fatal("a malformed Docker-Content-Digest was accepted")
	}
}

func TestResolveRejectsContentThatIsNotTheDigestPinned(t *testing.T) {
	reg := newFakeRegistry(t, false)
	reg.image(t, "hadron-desktop", "sway-v1", "amd64", nil, false)
	pinned := "sha256:" + strings.Repeat("b", 64)
	reg.docs["hadron-desktop/manifests/"+pinned] = reg.docs["hadron-desktop/manifests/sway-v1"]
	_, err := Resolve(context.Background(), mustRef(t, "ghcr.io/kairos-community/hadron-desktop@"+pinned))
	if err == nil {
		t.Fatal("content that does not hash to the pinned digest was accepted")
	}
}

func TestResolveRejectsATamperedConfigBlob(t *testing.T) {
	reg := newFakeRegistry(t, false)
	reg.image(t, "hadron-desktop", "sway-v1", "amd64", map[string]string{hdLabel: "sway"}, false)
	for k, d := range reg.docs {
		if strings.Contains(k, "/blobs/") {
			d.body = []byte(`{"os":"linux","architecture":"amd64","config":{"Labels":{"x":"y"}}}`)
			reg.docs[k] = d
		}
	}
	reg.omitDigest = true // so only the manifest's own digest can catch it
	if _, err := Resolve(context.Background(), mustRef(t, testImage)); err == nil {
		t.Fatal("a config blob that does not hash to its digest was accepted")
	}
}

func TestResolveRejectsATamperedIndexChild(t *testing.T) {
	reg := newFakeRegistry(t, false)
	reg.image(t, "hadron-desktop", "sway-v1", "arm64,amd64", nil, true)
	reg.omitDigest = true
	for k, d := range reg.docs {
		if strings.Contains(k, "/manifests/sha256:") && !strings.Contains(string(d.body), `"manifests"`) {
			d.body = append(d.body, ' ')
			reg.docs[k] = d
		}
	}
	if _, err := Resolve(context.Background(), mustRef(t, testImage)); err == nil {
		t.Fatal("an index child that does not hash to its digest was accepted")
	}
}

func TestResolveRejectsMalformedChildDigest(t *testing.T) {
	reg := newFakeRegistry(t, false)
	reg.docs["hadron-desktop/manifests/sway-v1"] = fakeDoc{[]byte(
		`{"manifests":[{"digest":"../../x","platform":{"os":"linux","architecture":"amd64"}}]}`), "application/vnd.oci.image.index.v1+json"}
	if _, err := Resolve(context.Background(), mustRef(t, testImage)); err == nil {
		t.Fatal("a malformed child digest was used in a URL")
	}
}

func TestResolveRejectsAnOversizedBody(t *testing.T) {
	reg := newFakeRegistry(t, false)
	reg.docs["hadron-desktop/manifests/sway-v1"] = fakeDoc{make([]byte, maxRegistryBody+1), "application/json"}
	_, err := Resolve(context.Background(), mustRef(t, testImage))
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want a size-limit error", err)
	}
}

// TestTokenRealmMustBeHTTPS: the realm comes from whatever registry the
// caller named, and the root daemon fetches it. A plain-http (or any other
// scheme) realm would let any local user make root fetch an arbitrary URL.
func TestTokenRealmMustBeHTTPS(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"token":"t"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HADRON_UPDATE_REGISTRY", "")
	for _, realm := range []string{srv.URL + "/token", "file:///etc/shadow", "ftp://example.com/token", "//example.com/token"} {
		_, err := anonymousToken(context.Background(), `Bearer realm="`+realm+`",service="x"`)
		if err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("realm %q: %v, want a refusal naming https", realm, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d requests to a refused realm", n)
	}
	// A test registry (HADRON_UPDATE_REGISTRY, set only by tests and the
	// root unit's own environment) may serve its token over http.
	t.Setenv("HADRON_UPDATE_REGISTRY", srv.URL)
	if tok, err := anonymousToken(context.Background(), `Bearer realm="`+srv.URL+`/token",service="x"`); err != nil || tok != "t" {
		t.Fatalf("test registry realm: %q, %v", tok, err)
	}
}
