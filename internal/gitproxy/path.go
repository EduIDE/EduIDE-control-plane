// Package gitproxy is the Git smart HTTP proxy between session pods and the provider hosts (ADR 0023).
// It holds no state except a short cache of authorize answers, so any number of replicas can run.
package gitproxy

import (
	"net/url"
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
	Repo string // canonical (gitapi.CanonicalRepo): no slashes around it, no .git suffix. Authorize, approvals and the push rule use this value; the upstream path adds ".git" itself.
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

// upstreamPath is the path at the provider: the repository with ".git", then the fixed Git suffix. The ".git" is
// not optional. GitLab answers a repository without it with a 301 on info/refs and a 422 on the POST, GitHub serves
// both forms. Repo itself stays without ".git": authorize, approvals and the push rule compare that form.
func (t Target) upstreamPath() string {
	p := "/" + t.Repo + ".git"
	if t.Op == OpInfoRefs {
		return p + "/info/refs"
	}
	return p + "/" + t.Service
}

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
	repo, ok := gitapi.CanonicalRepo(strings.Join(rest, "/"))
	if !ok {
		return Target{}, false
	}
	t.Repo = repo
	return t, true
}
