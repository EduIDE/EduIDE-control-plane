// Package gitproxy is the Git smart HTTP proxy between session pods and the provider hosts (ADR 0023).
// It holds no state except a short cache of authorize answers, so any number of replicas can run.
package gitproxy

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

// Op is what a request wants from the provider.
type Op int

const (
	OpInfoRefs Op = iota + 1
	OpUploadPack
	OpReceivePack
	// OpLFS is a Git LFS request. It is recognised only to answer with a clear error.
	OpLFS
)

// Target is a request that passed the path allowlist: /{host}/{repo...}/{info/refs|git-upload-pack|git-receive-pack}.
type Target struct {
	Host string // lower case
	Repo string // as sent, without surrounding slashes
	Op   Op
	// Service is git-upload-pack or git-receive-pack.
	Service string
}

// Kind is push for everything that talks to receive-pack, read otherwise.
func (t Target) Kind() string {
	if t.Service == "git-receive-pack" {
		return gitapi.KindPush
	}
	return gitapi.KindRead
}

// upstreamPath is the path below the repository.
func (t Target) upstreamPath() string {
	if t.Op == OpInfoRefs {
		return "/info/refs"
	}
	return "/" + t.Service
}

// segmentRE limits a repository path segment to characters that need no escaping and cannot change the meaning
// of the upstream URL (no slash, percent, query or fragment).
var segmentRE = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

const maxRepoSegments = 10

// ParsePath checks method, path and query against the allowlist. ok is false for everything else.
func ParsePath(method, path, rawQuery string) (Target, bool) {
	if !strings.HasPrefix(path, "/") {
		return Target{}, false
	}
	segs := strings.Split(path[1:], "/")
	if len(segs) < 3 || segs[0] == "" {
		return Target{}, false
	}
	t := Target{Host: strings.ToLower(segs[0])}
	rest := segs[1:]
	for i := 0; i+1 < len(rest); i++ {
		if rest[i] == "info" && rest[i+1] == "lfs" {
			t.Op, t.Repo = OpLFS, strings.Join(rest[:i], "/")
			return t, true
		}
	}
	n := len(rest)
	switch {
	case n >= 3 && method == "GET" && rest[n-2] == "info" && rest[n-1] == "refs":
		q, err := url.ParseQuery(rawQuery)
		svc := q["service"]
		if err != nil || len(svc) != 1 || (svc[0] != "git-upload-pack" && svc[0] != "git-receive-pack") {
			return Target{}, false
		}
		t.Op, t.Service, rest = OpInfoRefs, svc[0], rest[:n-2]
	case n >= 2 && method == "POST" && (rest[n-1] == "git-upload-pack" || rest[n-1] == "git-receive-pack"):
		t.Op, t.Service, rest = OpUploadPack, rest[n-1], rest[:n-1]
		if t.Service == "git-receive-pack" {
			t.Op = OpReceivePack
		}
	default:
		return Target{}, false
	}
	if len(rest) < 2 || len(rest) > maxRepoSegments {
		return Target{}, false
	}
	for _, s := range rest {
		if s == "." || s == ".." || !segmentRE.MatchString(s) {
			return Target{}, false
		}
	}
	t.Repo = strings.Join(rest, "/")
	return t, true
}
