package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	head := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, manifest, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", strings.Join([]string{
			"application/vnd.oci.image.index.v1+json",
			"application/vnd.oci.image.manifest.v1+json",
			"application/vnd.docker.distribution.manifest.list.v2+json",
			"application/vnd.docker.distribution.manifest.v2+json",
		}, ", "))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return http.DefaultClient.Do(req)
	}

	resp, err := head("")
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		token, err := anonymousToken(ctx, resp.Header.Get("WWW-Authenticate"))
		if err != nil {
			return err
		}
		if resp, err = head(token); err != nil {
			return err
		}
		resp.Body.Close()
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%s does not exist", ref)
	default:
		return fmt.Errorf("%s answered HTTP %d", ref, resp.StatusCode)
	}
}

func anonymousToken(ctx context.Context, challenge string) (string, error) {
	if !strings.HasPrefix(challenge, "Bearer ") {
		return "", fmt.Errorf("the registry asked for credentials (%q)", challenge)
	}
	params := map[string]string{}
	for _, m := range bearerParam.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	if params["realm"] == "" {
		return "", fmt.Errorf("the registry's challenge names no token service")
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
		return "", fmt.Errorf("it is private or does not exist (token service answered HTTP %d)", resp.StatusCode)
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
