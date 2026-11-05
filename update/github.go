package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Latest is the newest published release, as GitHub reports it.
type Latest struct {
	Tag  string `json:"tag_name"`
	URL  string `json:"html_url"`
	Body string `json:"body"`
}

func apiBase() string {
	if u := os.Getenv("HADRON_UPDATE_API"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://api.github.com"
}

// getJSON GETs a GitHub API path and decodes the JSON answer into out. The
// status handling lives here so every release query reports the same
// sentences for a missing repository and for the rate limit.
func getJSON(ctx context.Context, repo, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s", apiBase(), repo, path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hadron-update")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return fmt.Errorf("%s has no published release yet", repo)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return fmt.Errorf("GitHub rate limit reached, try again later (HTTP %d)", resp.StatusCode)
	default:
		return fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding the release: %w", err)
	}
	return nil
}

// FetchLatest asks GitHub for the repository's latest release. GitHub's
// "latest" already skips drafts and prereleases, and release.yml only
// publishes a release after every flavor's image has been pushed, so a tag
// returned here always has an image behind it.
func FetchLatest(ctx context.Context, repo string) (Latest, error) {
	var l Latest
	if err := getJSON(ctx, repo, "releases/latest", &l); err != nil {
		return Latest{}, err
	}
	if l.Tag == "" {
		return Latest{}, fmt.Errorf("the latest release of %s has no tag", repo)
	}
	return l, nil
}

// FetchReleases lists the repository's published releases, newest first, so
// the user can deliberately install an older one. Drafts and prereleases are
// dropped for the same reason "latest" drops them: only a published release
// is guaranteed to have an image for every flavor. One page of 100 is plenty
// for a picker.
func FetchReleases(ctx context.Context, repo string) ([]Latest, error) {
	var raw []struct {
		Latest
		Draft      bool `json:"draft"`
		Prerelease bool `json:"prerelease"`
	}
	if err := getJSON(ctx, repo, "releases?per_page=100", &raw); err != nil {
		return nil, err
	}
	var out []Latest
	for _, r := range raw {
		if r.Draft || r.Prerelease || r.Tag == "" {
			continue
		}
		out = append(out, r.Latest)
	}
	// GitHub orders by creation date, which a backported patch release
	// breaks; order by version instead.
	sort.SliceStable(out, func(i, j int) bool { return Newer(out[i].Tag, out[j].Tag) })
	return out, nil
}
