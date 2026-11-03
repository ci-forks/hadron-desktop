package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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

// FetchLatest asks GitHub for the repository's latest release. GitHub's
// "latest" already skips drafts and prereleases, and release.yml only
// publishes a release after every flavor's image has been pushed, so a tag
// returned here always has an image behind it.
func FetchLatest(ctx context.Context, repo string) (Latest, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/releases/latest", apiBase(), repo), nil)
	if err != nil {
		return Latest{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hadron-update")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Latest{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Latest{}, fmt.Errorf("%s has no published release yet", repo)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return Latest{}, fmt.Errorf("GitHub rate limit reached, try again later (HTTP %d)", resp.StatusCode)
	default:
		return Latest{}, fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)
	}

	var l Latest
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		return Latest{}, fmt.Errorf("decoding the release: %w", err)
	}
	if l.Tag == "" {
		return Latest{}, fmt.Errorf("the latest release of %s has no tag", repo)
	}
	return l, nil
}
