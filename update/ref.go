package main

import (
	"fmt"
	"regexp"
	"strings"
)

// ImageRef is a parsed OCI image reference. Host is required: there is no
// Docker Hub shorthand, because a bare "ubuntu" would silently pick a registry
// the user never named. A reference carries a Tag, a Digest or both; when both
// are set the digest is what identifies the image and the tag is a hint.
type ImageRef struct {
	Host, Name, Tag, Digest string
}

var (
	// Repository path components per the OCI distribution spec: lowercase only.
	refNameRe   = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	refTagRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	refDigestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	// host or host:port; DNS names are case-insensitive, so any case is fine.
	refHostRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?(?::[0-9]+)?$`)
)

// ParseRef parses "host[:port]/name[:tag][@sha256:digest]". It splits the
// digest off first (it contains a colon), then takes the tag as the last colon
// after the last slash, so a host:port never reads as a tag.
func ParseRef(s string) (ImageRef, error) {
	bad := func(why string) (ImageRef, error) {
		return ImageRef{}, fmt.Errorf("%q is not a host/name[:tag][@digest] image reference: %s", s, why)
	}
	var r ImageRef
	rest := s
	if i := strings.Index(rest, "@"); i >= 0 {
		r.Digest = rest[i+1:]
		rest = rest[:i]
		if !refDigestRe.MatchString(r.Digest) {
			return bad("digest must be sha256: followed by 64 hex digits")
		}
	}
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return bad("registry host is required")
	}
	r.Host, rest = rest[:slash], rest[slash+1:]
	// "kairos/x:t" has a first component that is not a host; without this it
	// would read as host "kairos", which only Docker Hub shorthand could mean.
	if !refHostRe.MatchString(r.Host) || !(strings.ContainsAny(r.Host, ".:") || r.Host == "localhost") {
		return bad("registry host is required")
	}
	if c := strings.LastIndex(rest, ":"); c > strings.LastIndex(rest, "/") {
		r.Tag = rest[c+1:]
		rest = rest[:c]
		if !refTagRe.MatchString(r.Tag) {
			return bad("invalid tag")
		}
	}
	r.Name = rest
	if !refNameRe.MatchString(r.Name) {
		return bad("repository name must be lowercase path components")
	}
	if r.Tag == "" && r.Digest == "" {
		return bad("a tag or digest is required")
	}
	return r, nil
}

// String renders the reference back in the form ParseRef accepts.
func (r ImageRef) String() string {
	s := r.Repo()
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}

// Repo is the reference without tag or digest: host/name.
func (r ImageRef) Repo() string { return r.Host + "/" + r.Name }

// Reference is what a registry manifest URL takes: the digest if set, else the tag.
func (r ImageRef) Reference() string {
	if r.Digest != "" {
		return r.Digest
	}
	return r.Tag
}
