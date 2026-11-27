package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitpolicy"
)

// Repository approval (ADR 0023): the user lets one session use the credential for a repository other than the
// one it was started with. Only the bearer token of the session owner can do it, so code in the pod cannot.

func (s *Server) approvals() gitpolicy.Store {
	return gitpolicy.Store{C: s.w, Namespace: s.cfg.Namespace}
}

// gitSession returns the caller's session for the approval endpoints; a deleting session counts as gone.
func (s *Server) gitSession(w http.ResponseWriter, r *http.Request, name string) (*v1alpha1.IDESession, bool) {
	if s.git == nil {
		writeProblem(w, problem(CodeNotFound, "no such endpoint"))
		return nil, false
	}
	owner := user(r).Owner
	if !s.allowGit("approve", owner) {
		writeProblem(w, problem(CodeRateLimited, "too many calls, retry shortly"))
		return nil, false
	}
	sess, perr := s.ownedSession(r.Context(), name, owner)
	if perr == nil && sess.DeletionTimestamp != nil {
		perr = problem(CodeNotFound, "session not found")
	}
	if perr != nil {
		writeProblem(w, perr)
		return nil, false
	}
	return sess, true
}

// repoKey turns a repository URL of a request into host and "host/path"; the host is lower case.
func repoKey(raw string) (host, key string, perr *apiError) {
	u, perr := parseGitRepoURL(raw, true)
	if perr != nil {
		return "", "", perr
	}
	if u.Port() != "" { // the proxy and the credential know hosts on 443 only
		return "", "", problem(CodeInvalidInput, "the repository URL must not name a port")
	}
	host = strings.ToLower(u.Hostname())
	return host, gitpolicy.Key(host, u.Path), nil
}

// sessionRepo is the repository of the launch data of the session as "host/path", empty without one.
func (s *Server) sessionRepo(r *http.Request, sess *v1alpha1.IDESession) string {
	if sess.Spec.DataSecretRef == "" {
		return ""
	}
	var sec corev1.Secret
	if s.w.Get(r.Context(), types.NamespacedName{Namespace: s.cfg.Namespace, Name: sess.Spec.DataSecretRef}, &sec) != nil {
		return ""
	}
	host, repo, ok := gitapi.SplitURL(string(sec.Data["GIT_URI"]))
	if !ok {
		return ""
	}
	return gitpolicy.Key(host, repo)
}

func (s *Server) providerOfHost(host string) (config.GitProvider, bool) {
	i := slices.IndexFunc(s.git.Providers(), func(p config.GitProvider) bool { return strings.EqualFold(p.Host, host) })
	if i < 0 {
		return config.GitProvider{}, false
	}
	return s.git.Providers()[i], true
}

func (s *Server) ListGitRepositories(w http.ResponseWriter, r *http.Request, name Name) {
	sess, ok := s.gitSession(w, r, name)
	if !ok {
		return
	}
	ap := s.cfg.GitAccounts.Approval
	out := GitRepositories{Mode: GitRepositoriesMode(ap.Mode), MaxPerSession: ap.MaxPerSession, Approved: []GitApproval{}}
	if k := s.sessionRepo(r, sess); k != "" {
		host, _, _ := strings.Cut(k, "/")
		if _, ok := s.providerOfHost(host); ok {
			out.SessionRepository = ptr("https://" + k)
		}
	}
	if ap.Mode != config.ApprovalOff {
		l, err := s.approvals().List(r.Context(), sess.Name)
		if err != nil {
			writeProblem(w, internalError(err))
			return
		}
		for _, a := range l {
			out.Approved = append(out.Approved, GitApproval{Repository: "https://" + a.Repository, Access: GitAccess(a.Access), ApprovedAt: a.ApprovedAt})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ApproveGitRepository(w http.ResponseWriter, r *http.Request, name Name) {
	sess, ok := s.gitSession(w, r, name)
	if !ok {
		return
	}
	var req GitApprovalRequest
	s.limitBodyRead(w)
	if perr := decodeJSON(r.Body, &req); perr != nil {
		writeProblem(w, perr)
		return
	}
	ap := s.cfg.GitAccounts.Approval
	host, key, perr := repoKey(req.Repository)
	switch {
	case ap.Mode == config.ApprovalOff:
		perr = problem(CodeGitApprovalDisabled, "repository approval is turned off")
	case perr != nil:
	case !req.Access.Valid():
		perr = problem(CodeInvalidInput, "access must be read or write")
	case req.Access == GitAccess(config.ApprovalWrite) && ap.Mode != config.ApprovalWrite:
		perr = problem(CodeGitAccessNotAllowed, "this deployment allows approvals for reading only")
	}
	var p config.GitProvider
	if perr == nil {
		if p, ok = s.providerOfHost(host); !ok {
			perr = problem(CodeGitNotAProvider, "the host is not a Git provider of this deployment")
		} else if _, connected := s.git.Connection(r.Context(), user(r).Owner, p.Name); !connected {
			perr = problem(CodeGitNotConnected, "connect your "+p.DisplayName+" account first")
		}
	}
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	if key == s.sessionRepo(r, sess) { // the session repository has full access already
		writeJSON(w, http.StatusOK, GitApproval{Repository: "https://" + key, Access: GitAccessWrite, ApprovedAt: sess.CreationTimestamp.UTC()})
		return
	}
	a, err := s.approvals().Put(r.Context(), sess, key, string(req.Access), ap.MaxPerSession, s.now())
	if errors.Is(err, gitpolicy.ErrApprovalLimit) {
		writeProblem(w, problem(CodeGitApprovalLimit, "this session has the most approved repositories"))
		return
	}
	if err != nil {
		writeProblem(w, internalError(err))
		return
	}
	slog.InfoContext(r.Context(), "git repository approved", "session", sess.Name, "provider", p.Name, "repository", key, "access", req.Access, "user", s.userID(user(r).Owner))
	writeJSON(w, http.StatusOK, GitApproval{Repository: "https://" + a.Repository, Access: GitAccess(a.Access), ApprovedAt: a.ApprovedAt})
}

func (s *Server) RemoveGitRepository(w http.ResponseWriter, r *http.Request, name Name) {
	sess, ok := s.gitSession(w, r, name)
	if !ok {
		return
	}
	var req GitRepositoryRef
	s.limitBodyRead(w)
	if perr := decodeJSON(r.Body, &req); perr != nil {
		writeProblem(w, perr)
		return
	}
	host, key, perr := repoKey(req.Repository)
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	if err := s.approvals().Remove(r.Context(), sess, key); err != nil {
		writeProblem(w, internalError(err))
		return
	}
	provider := ""
	if p, ok := s.providerOfHost(host); ok {
		provider = p.Name
	}
	slog.InfoContext(r.Context(), "git repository approval removed", "session", sess.Name, "provider", provider, "repository", key, "user", s.userID(user(r).Owner))
	w.WriteHeader(http.StatusNoContent)
}
