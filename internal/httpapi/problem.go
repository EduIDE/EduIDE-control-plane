package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// apiError is an RFC 9457 problem with a stable code.
type apiError struct {
	status   int
	code     ProblemCode
	title    string
	detail   string
	sessions []Session
}

func (e *apiError) Error() string { return string(e.code) + ": " + e.detail }

const (
	CodeUnauthorized      ProblemCode = "unauthorized"
	CodeForbidden         ProblemCode = "forbidden"
	CodeNotFound          ProblemCode = "not-found"
	CodeInvalidInput      ProblemCode = "invalid-input"
	CodeUnknownKey        ProblemCode = "unknown-key"
	CodePersistence       ProblemCode = "persistence-not-allowed"
	CodeTemplateNotFound  ProblemCode = "template-not-found"
	CodeWorkspaceNotFound ProblemCode = "workspace-not-found"
	CodeWorkspaceLost     ProblemCode = "workspace-lost"
	CodeWorkspaceInUse    ProblemCode = "workspace-in-use"
	CodeSessionLimit      ProblemCode = "session-limit-reached"
	CodeWorkspaceLimit    ProblemCode = "workspace-limit-reached"
	CodeRateLimited       ProblemCode = "rate-limited"
	CodePayloadTooLarge   ProblemCode = "payload-too-large"
	CodeInternal          ProblemCode = "internal"
	// Git account flow (ADR 0023).
	CodeGitInvalidState  ProblemCode = "git-invalid-state"
	CodeGitStateExpired  ProblemCode = "git-state-expired"
	CodeGitDenied        ProblemCode = "git-denied"
	CodeGitProviderError ProblemCode = "git-provider-error"
	// Repository approval (ADR 0023).
	CodeGitApprovalDisabled ProblemCode = "git-approval-disabled"
	CodeGitAccessNotAllowed ProblemCode = "git-access-not-allowed"
	CodeGitApprovalLimit    ProblemCode = "git-approval-limit"
	CodeGitNotAProvider     ProblemCode = "git-not-a-provider"
	CodeGitNotConnected     ProblemCode = "git-not-connected"
)

var problemStatus = map[ProblemCode]int{
	CodeUnauthorized:        http.StatusUnauthorized,
	CodeForbidden:           http.StatusForbidden,
	CodeNotFound:            http.StatusNotFound,
	CodeInvalidInput:        http.StatusBadRequest,
	CodeUnknownKey:          http.StatusBadRequest,
	CodePersistence:         http.StatusBadRequest,
	CodeTemplateNotFound:    http.StatusNotFound,
	CodeWorkspaceNotFound:   http.StatusNotFound,
	CodeWorkspaceLost:       http.StatusConflict,
	CodeWorkspaceInUse:      http.StatusConflict,
	CodeSessionLimit:        http.StatusConflict,
	CodeWorkspaceLimit:      http.StatusConflict,
	CodeRateLimited:         http.StatusTooManyRequests,
	CodePayloadTooLarge:     http.StatusRequestEntityTooLarge,
	CodeInternal:            http.StatusInternalServerError,
	CodeGitInvalidState:     http.StatusBadRequest,
	CodeGitStateExpired:     http.StatusBadRequest,
	CodeGitDenied:           http.StatusBadRequest,
	CodeGitProviderError:    http.StatusBadGateway,
	CodeGitApprovalDisabled: http.StatusForbidden,
	CodeGitAccessNotAllowed: http.StatusForbidden,
	CodeGitApprovalLimit:    http.StatusConflict,
	CodeGitNotAProvider:     http.StatusBadRequest,
	CodeGitNotConnected:     http.StatusConflict,
}

func problem(code ProblemCode, detail string) *apiError {
	return &apiError{status: problemStatus[code], code: code, title: http.StatusText(problemStatus[code]), detail: detail}
}

// internalError logs the cause and hides it from the client.
func internalError(err error) *apiError {
	slog.Error("internal error", "error", err.Error())
	return problem(CodeInternal, "internal error")
}

func writeProblem(w http.ResponseWriter, e *apiError) {
	p := Problem{Type: "https://eduide.github.io/problems/" + string(e.code), Title: e.title, Status: e.status, Code: e.code}
	if e.detail != "" {
		p.Detail = &e.detail
	}
	if e.sessions != nil {
		p.Sessions = &e.sessions
	}
	w.Header().Set("Content-Type", "application/problem+json")
	if e.code == CodeRateLimited {
		w.Header().Set("Retry-After", "6")
	}
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(p)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
