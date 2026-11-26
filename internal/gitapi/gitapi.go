// Package gitapi is the internal contract between the git-proxy and the service (ADR 0023): the request and
// response of POST /internal/v1/git/authorize and the helpers both sides use to compare repositories.
// It is not part of the public REST API (api/openapi/v1.yaml).
package gitapi

import (
	"net/url"
	"regexp"
	"strings"
)

const (
	// AuthorizePath is served by the internal listener of the service, never routed by the gateway.
	AuthorizePath = "/internal/v1/git/authorize"
	// InstanceHeader carries the instance name from the agent to the git-proxy.
	InstanceHeader = "X-EduIDE-Instance"
)

// Kind of a Git request.
const (
	KindRead = "read"
	KindPush = "push"
)

// Reasons of an authorize answer. The set is fixed; the git-proxy uses it as a log field.
const (
	ReasonOK             = "ok"              // allowed, the answer carries the upstream credential
	ReasonAnonymous      = "anonymous"       // allowed without a credential: session repository, no connected account
	ReasonAnonymousOther = "anonymous-other" // read of another repository: allowed, never with the credential
	ReasonConnect        = "connect"         // push needs a connected account
	ReasonPushRule       = "push-rule"       // push to a repository other than the session's
	ReasonNotReady       = "not-ready"       // session is not Ready or is being deleted
	ReasonUnknownHost    = "unknown-host"    // host is not a configured provider
	ReasonUnauthorized   = "unauthorized"    // set by the git-proxy for a 401 of the service
)

// AuthorizeRequest is sent with the instance token as bearer.
type AuthorizeRequest struct {
	Instance   string `json:"instance"`
	Host       string `json:"host"`
	Repository string `json:"repository"`
	Kind       string `json:"kind"`
}

// AuthorizeResponse answers one request. Upstream is set only when Allow is true and an account is connected.
type AuthorizeResponse struct {
	Allow   bool   `json:"allow"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
	// User is the salted user id and Session the session name, both for the log line of the git-proxy.
	User    string `json:"user,omitempty"`
	Session string `json:"session,omitempty"`
	// Connected tells whether the owner has an account on the host. SessionRepository is "host/path" of the
	// session's GIT_URI, empty without one. The git-proxy needs both for its message after an anonymous 401, 403 or 404.
	Connected         bool   `json:"connected,omitempty"`
	SessionRepository string `json:"sessionRepository,omitempty"`
	// ApprovalMode is the deployment's repository approval mode ("read" or "write"); empty when approvals are off.
	// With it and Connected the git-proxy builds the approval link in its message.
	ApprovalMode string    `json:"approvalMode,omitempty"`
	Upstream     *Upstream `json:"upstream,omitempty"`
}

// Upstream is a basic auth credential for the provider host. The git-proxy never logs it or hands it to a client.
type Upstream struct {
	Username string `json:"username"`
	Password string `json:"password"` // #nosec G117 -- the credential is the payload
}

// NormalizeRepo makes repository paths comparable: no surrounding slashes, no trailing ".git".
func NormalizeRepo(p string) string {
	p = strings.Trim(p, "/")
	p = strings.TrimSuffix(p, ".git")
	return strings.Trim(p, "/")
}

// segmentRE limits a repository path segment to characters that need no escaping and cannot change the meaning
// of an upstream URL (no slash, percent, query or fragment).
var segmentRE = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// MaxRepoSegments bounds the depth of a repository path (GitLab subgroups).
const MaxRepoSegments = 10

// CanonicalRepo is the one place that decides what a repository path may look like, for the git-proxy, the
// approval API and the preflight. It returns the path without surrounding slashes and without a ".git" suffix,
// and false for anything else: fewer than 2 or more than MaxRepoSegments segments, other characters than
// letters, digits and "._~-", the segments ".", ".." and ".git" (any case), ".git" at the end of a segment other
// than the last, or a name that is empty, "." or "..", or still ends in ".git" after one is cut ("a/..git" would
// give "a/."). Other names with a leading dot are valid
// (".github", ".dotfiles"). The result is idempotent under NormalizeRepo and under CanonicalRepo itself, so no
// caller can normalise it a second time into another repository.
func CanonicalRepo(p string) (string, bool) {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) < 2 || len(segs) > MaxRepoSegments {
		return "", false
	}
	for i, s := range segs {
		if !segmentRE.MatchString(s) || s == "." || s == ".." || strings.EqualFold(s, ".git") {
			return "", false
		}
		if strings.HasSuffix(s, ".git") {
			if i != len(segs)-1 {
				return "", false
			}
			segs[i] = strings.TrimSuffix(s, ".git")
			if segs[i] == "" || segs[i] == "." || segs[i] == ".." || strings.HasSuffix(segs[i], ".git") {
				return "", false
			}
		}
	}
	return strings.Join(segs, "/"), true
}

// SameRepository compares two repositories on the same host. Hosts compare case-insensitively, paths exactly.
func SameRepository(hostA, repoA, hostB, repoB string) bool {
	return strings.EqualFold(hostA, hostB) && NormalizeRepo(repoA) == NormalizeRepo(repoB) && NormalizeRepo(repoA) != ""
}

// SplitURL returns host and normalized repository path of a repository URL without its credentials.
func SplitURL(raw string) (host, repo string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.Port() != "" || u.Scheme != "https" {
		return "", "", false
	}
	return strings.ToLower(u.Hostname()), NormalizeRepo(u.Path), true
}

// ConnectMessage is what a user sees when a repository needs a connected account.
func ConnectMessage(displayName, landingURL string) string {
	return "Connect your " + displayName + " account to open this repository: " + landingURL
}

// ApproveURL is the landing page link that lets the user approve a repository for one session. The landing page
// shows session and repository and needs an explicit click and the user's login. The link holds no secret.
// access is "read" or "write".
func ApproveURL(landingURL, session, host, repo, access string) string {
	return strings.TrimRight(landingURL, "/") + "/?session=" + url.QueryEscape(session) +
		"&approveRepo=" + url.QueryEscape("https://"+host+"/"+NormalizeRepo(repo)) + "&access=" + access
}

// ApproveMessage is what a user sees when approving the repository for the session could help.
func ApproveMessage(landingURL, session, host, repo, access string) string {
	verb := "use"
	if access == "write" {
		verb = "push to"
	}
	return "This session may not " + verb + " " + host + "/" + NormalizeRepo(repo) + " yet. Approve it here: " + ApproveURL(landingURL, session, host, repo, access)
}

// OtherRepositoryMessage is shown when an anonymous read of a repository other than the session's fails at the
// provider although the user has an account: the account is used for the session repository only.
func OtherRepositoryMessage(displayName, sessionRepository, host, repo string) string {
	if sessionRepository == "" {
		sessionRepository = "none"
	}
	return "EduIDE uses your " + displayName + " account only for the repository this session was started with (" +
		sessionRepository + "). Start a session from " + host + "/" + NormalizeRepo(repo) + " to open it."
}
