package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// registryBase maps an image reference's registry host to the URL its v2 API
// is served at. HADRON_UPDATE_REGISTRY replaces it, so a test can point every
// host at a fake registry.
func registryBase(host string) string {
	if u := os.Getenv("HADRON_UPDATE_REGISTRY"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://" + host
}

var bearerParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// ImagePullable reports whether ref can be pulled anonymously, the way
// kairos-agent will pull it. It follows the registry v2 token handshake: an
// unauthenticated request answers 401 with a Bearer challenge naming the token
// service, which hands out an anonymous pull token for a public repository and
// refuses one for a private or missing one.
//
// It exists so that a release without an image, or a package nobody made
// public yet, fails here with a sentence, not deep inside kairos-agent with
// "DENIED: requested access to the resource is denied".
func ImagePullable(ctx context.Context, ref string) error {
	r, err := ParseRef(ref)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	manifest := fmt.Sprintf("%s/v2/%s/manifests/%s", registryBase(r.Host), r.Name, r.Reference())
	resp, err := (&regClient{}).do(ctx, http.MethodHead, manifest)
	if err != nil {
		return err
	}
	resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%s does not exist", ref)
	default:
		return fmt.Errorf("%s answered HTTP %d", ref, resp.StatusCode)
	}
}

// manifestAccept lists every manifest media type a registry may answer with,
// so a multi-arch index and a single manifest both come back as served.
const manifestAccept = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

// regClient carries the anonymous pull token between requests, so one Resolve
// does the 401 -> token handshake once, not once per manifest and blob.
type regClient struct{ token string }

// do sends one request. An unauthenticated attempt that gets a 401 Bearer
// challenge fetches an anonymous token and retries once. The caller closes the
// body.
func (c *regClient) do(ctx context.Context, method, u string) (*http.Response, error) {
	send := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", manifestAccept)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		return http.DefaultClient.Do(req)
	}
	resp, err := send()
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	resp.Body.Close()
	if c.token, err = anonymousToken(ctx, resp.Header.Get("WWW-Authenticate")); err != nil {
		return nil, err
	}
	return send()
}

func anonymousToken(ctx context.Context, challenge string) (string, error) {
	if !strings.HasPrefix(challenge, "Bearer ") {
		return "", fmt.Errorf("%w (the registry's challenge: %q)", ErrNeedsCredentials, challenge)
	}
	params := map[string]string{}
	for _, m := range bearerParam.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	if params["realm"] == "" {
		return "", fmt.Errorf("the registry's challenge names no token service")
	}
	if !tokenRealmAllowed(params["realm"]) {
		return "", fmt.Errorf("the registry's token service %q is not an https URL, so it is not asked", params["realm"])
	}
	q := url.Values{}
	for _, k := range []string{"service", "scope"} {
		if params[k] != "" {
			q.Set(k, params[k])
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params["realm"]+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// ghcr answers 403 DENIED here for a private or missing repository.
		return "", fmt.Errorf("it is private or does not exist (token service answered HTTP %d): %w", resp.StatusCode, ErrNeedsCredentials)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Token != "" {
		return body.Token, nil
	}
	return body.AccessToken, nil
}

// tokenRealmAllowed accepts only an https realm. The realm comes from the
// registry the caller named, and the daemon fetches it as root, so another
// scheme would let any local user make root fetch a plain-http (or file, or
// ftp) URL. The test registry of HADRON_UPDATE_REGISTRY may serve its token
// over http.
func tokenRealmAllowed(realm string) bool {
	if u, err := url.Parse(realm); err == nil && u.Scheme == "https" && u.Host != "" {
		return true
	}
	base := os.Getenv("HADRON_UPDATE_REGISTRY")
	return base != "" && strings.HasPrefix(realm, strings.TrimRight(base, "/")+"/")
}

// ErrNeedsCredentials means the registry refused an anonymous pull. Inspect is
// anonymous on purpose, so a private image is refused, not prompted for.
var ErrNeedsCredentials = errors.New("needs registry credentials")

// ErrWrongPlatform means the image has no linux/amd64 build, the only
// platform Hadron Desktop runs on.
var ErrWrongPlatform = errors.New("image is not built for linux/amd64")

// ImageInfo is what an install needs to know about an image. Digest is the
// digest of what the reference points at (the index for a multi-arch image),
// because that is what kairos-agent pulls.
type ImageInfo struct {
	Ref      ImageRef
	Digest   string
	OS, Arch string
	Labels   map[string]string
}

const maxRegistryBody = 8 << 20

// Resolve looks ref up on its registry, anonymously, and returns its digest,
// platform and labels.
func Resolve(ctx context.Context, ref ImageRef) (ImageInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := &regClient{}
	base := registryBase(ref.Host)
	manifestURL := func(r string) string { return fmt.Sprintf("%s/v2/%s/manifests/%s", base, ref.Name, r) }

	body, digest, err := c.fetch(ctx, manifestURL(ref.Reference()), ref.String(), ref.Digest)
	if err != nil {
		return ImageInfo{}, err
	}
	info := ImageInfo{Ref: ref, Digest: digest}

	var m struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return ImageInfo{}, fmt.Errorf("%s: unreadable manifest: %w", ref, err)
	}
	if len(m.Manifests) > 0 {
		child := ""
		for _, e := range m.Manifests {
			if e.Platform.OS == "linux" && e.Platform.Architecture == "amd64" {
				child = e.Digest
				break
			}
		}
		if child == "" {
			return ImageInfo{}, fmt.Errorf("%s: %w", ref, ErrWrongPlatform)
		}
		// A nested index is not followed: images that matter here are one level.
		if !sha256Digest.MatchString(child) {
			return ImageInfo{}, fmt.Errorf("%s: the index lists a malformed digest %q", ref, child)
		}
		if body, _, err = c.fetch(ctx, manifestURL(child), ref.String(), child); err != nil {
			return ImageInfo{}, err
		}
		m.Config.Digest = ""
		if err := json.Unmarshal(body, &m); err != nil {
			return ImageInfo{}, fmt.Errorf("%s: unreadable manifest: %w", ref, err)
		}
	}
	if m.Config.Digest == "" {
		return ImageInfo{}, fmt.Errorf("%s: the manifest names no config", ref)
	}

	if !sha256Digest.MatchString(m.Config.Digest) {
		return ImageInfo{}, fmt.Errorf("%s: the manifest names a malformed config digest %q", ref, m.Config.Digest)
	}
	blob, _, err := c.fetch(ctx, fmt.Sprintf("%s/v2/%s/blobs/%s", base, ref.Name, m.Config.Digest), ref.String(), m.Config.Digest)
	if err != nil {
		return ImageInfo{}, err
	}
	var cfg struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Config       struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(blob, &cfg); err != nil {
		return ImageInfo{}, fmt.Errorf("%s: unreadable image config: %w", ref, err)
	}
	info.OS, info.Arch, info.Labels = cfg.OS, cfg.Architecture, cfg.Config.Labels
	if info.OS != "linux" || info.Arch != "amd64" {
		return ImageInfo{}, fmt.Errorf("%s is %s/%s: %w", ref, info.OS, info.Arch, ErrWrongPlatform)
	}
	return info, nil
}

var sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// fetch GETs u and returns the body and its sha256 digest, computed over the
// exact bytes served. A Docker-Content-Digest header is only a claim: it must
// match the body, and so must want (the digest the caller asked for, "" when
// it asked by tag), else the registry or a middlebox swapped the content.
func (c *regClient) fetch(ctx context.Context, u, name, want string) ([]byte, string, error) {
	resp, err := c.do(ctx, http.MethodGet, u)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, "", fmt.Errorf("%s does not exist", name)
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, "", fmt.Errorf("%s: %w", name, ErrNeedsCredentials)
	default:
		return nil, "", fmt.Errorf("%s answered HTTP %d", name, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryBody+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxRegistryBody {
		return nil, "", fmt.Errorf("%s: the registry's answer is larger than %d bytes", name, maxRegistryBody)
	}
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if h := resp.Header.Get("Docker-Content-Digest"); h != "" {
		if !sha256Digest.MatchString(h) {
			return nil, "", fmt.Errorf("%s: the registry sent a malformed digest %q", name, h)
		}
		if h != digest {
			return nil, "", fmt.Errorf("%s: the registry claims digest %s but the content is %s", name, h, digest)
		}
	}
	if want != "" && want != digest {
		return nil, "", fmt.Errorf("%s: expected digest %s but the content is %s", name, want, digest)
	}
	return body, digest, nil
}
