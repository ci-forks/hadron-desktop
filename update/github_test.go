package main

import (
	"context"
	"reflect"
	"testing"
)

func TestFetchReleasesSkipsDraftsAndPrereleases(t *testing.T) {
	fakeGitHubReleases(t, "v0.0.5", `[
		{"tag_name":"v0.0.5","html_url":"u5"},
		{"tag_name":"v0.0.6-rc.1","prerelease":true},
		{"tag_name":"v0.0.7","draft":true},
		{"tag_name":"v0.0.4","html_url":"u4"}
	]`)
	got, err := FetchReleases(context.Background(), "kairos-community/hadron-desktop")
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, l := range got {
		tags = append(tags, l.Tag)
	}
	if want := []string{"v0.0.5", "v0.0.4"}; !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
}

func TestFetchReleasesSortsNewestFirst(t *testing.T) {
	fakeGitHubReleases(t, "v0.0.5", `[{"tag_name":"v0.0.4"},{"tag_name":"v0.0.10"},{"tag_name":"v0.0.5"}]`)
	got, err := FetchReleases(context.Background(), "kairos-community/hadron-desktop")
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, l := range got {
		tags = append(tags, l.Tag)
	}
	if want := []string{"v0.0.10", "v0.0.5", "v0.0.4"}; !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
}

func TestFetchReleasesUnknownRepo(t *testing.T) {
	fakeGitHub(t, "v0.0.5")
	if _, err := FetchReleases(context.Background(), "nobody/nothing"); err == nil {
		t.Fatal("want an error for a 404")
	}
}
