// Package authz is the Envoy ext_authz server of ADR 0005. It verifies the ID
// token cookie itself and lets a request pass to a session only for its owner.
package authz

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

// Server implements authv3.AuthorizationServer.
type Server struct {
	authv3.UnimplementedAuthorizationServer
	Sessions Sessions
	// UserClaim names the token claim that holds the owner.
	UserClaim string
	// IDTokenCookie is the cookie that carries the ID token.
	IDTokenCookie string
	// InstanceHost is the host sessions share; it is the cookie domain and the redirect fallback.
	InstanceHost string
	// WebviewHost is the second host that serves sessions.
	WebviewHost string
	// Salt feeds the pseudonymous user id in logs.
	Salt []byte

	verifier atomic.Pointer[oidc.IDTokenVerifier]
}

// SetVerifier is called once OIDC discovery succeeded. Until then every check is denied.
func (s *Server) SetVerifier(v *oidc.IDTokenVerifier) { s.verifier.Store(v) }

// Ready reports whether OIDC discovery is done.
func (s *Server) Ready() bool { return s.verifier.Load() != nil }

type decision struct {
	result, reason string
	redirect       *redirect
}

type redirect struct {
	location string
	expire   []string
}

func allow(reason string) decision { return decision{result: "allow", reason: reason} }
func deny(reason string) decision  { return decision{result: "deny", reason: reason} }

func (s *Server) Check(ctx context.Context, req *authv3.CheckRequest) (resp *authv3.CheckResponse, err error) {
	start := time.Now()
	d := deny("internal")
	user := ""
	// A span only when the caller's trace is sampled, so unsampled checks cost nothing.
	var span trace.Span
	ctx, span = checkSpan(ctx, req.GetAttributes().GetRequest().GetHttp())
	defer func() {
		if r := recover(); r != nil {
			d, user = deny("internal"), ""
			resp = s.response(d)
		}
		telemetry.AuthzDecisions.WithLabelValues(d.result, d.reason).Inc()
		telemetry.AuthzDuration.Observe(time.Since(start).Seconds())
		span.SetAttributes(attribute.String("eduide.authz_result", d.result), attribute.String("eduide.authz_reason", d.reason))
		span.End()
		slog.DebugContext(ctx, "authz decision", "result", d.result, "reason", d.reason, "user", user)
	}()
	d, user = s.decide(ctx, req.GetAttributes().GetRequest().GetHttp())
	return s.response(d), nil
}

func (s *Server) decide(ctx context.Context, h *authv3.AttributeContext_HttpRequest) (_ decision, user string) {
	if h == nil {
		return deny("bad-path"), ""
	}
	clean, instance, ok := splitPath(h.GetPath())
	if !ok {
		return deny("bad-path"), ""
	}
	if strings.HasPrefix(clean, "/oauth2/") {
		return allow("oauth2"), ""
	}
	cookies := parseCookies(cookieHeaders(h))
	var tokens, expire []string
	seen := map[string]bool{}
	for _, c := range cookies {
		if !isAuthCookie(c.name, s.IDTokenCookie) {
			continue
		}
		if !seen[c.name] {
			seen[c.name] = true
			expire = append(expire, c.name)
		}
		if c.name == s.IDTokenCookie {
			tokens = append(tokens, c.value)
		}
	}
	if len(expire) == 0 {
		return allow("no-cookie"), ""
	}
	v := s.verifier.Load()
	if v == nil {
		return deny("not-ready"), ""
	}
	// Every copy of the ID token cookie must verify and name the same owner.
	owner := ""
	for _, raw := range tokens {
		o, err := s.owner(ctx, v, raw)
		if err != nil {
			slog.DebugContext(ctx, "id token rejected", "err", err)
		}
		if err != nil || (owner != "" && o != owner) {
			return s.login(h, expire), ""
		}
		owner = o
	}
	if owner == "" {
		return s.login(h, expire), ""
	}
	user = telemetry.UserID(s.Salt, owner)
	sessions, err := s.Sessions.ByInstance(ctx, instance)
	if err != nil {
		return deny("internal"), user
	}
	if instance == "" || len(sessions) == 0 {
		return deny("unknown-instance"), user
	}
	reason := "not-owner"
	for i := range sessions {
		se := &sessions[i]
		switch {
		case se.Spec.Owner != owner:
		case se.DeletionTimestamp != nil:
			reason = "deleting"
		case se.Status.Phase != v1alpha1.SessionReady:
			reason = "not-ready"
		default:
			return allow("owner"), user
		}
	}
	return deny(reason), user
}

func (s *Server) owner(ctx context.Context, v *oidc.IDTokenVerifier, raw string) (string, error) {
	tok, err := v.Verify(ctx, raw)
	if err != nil {
		return "", err
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return "", err
	}
	o, _ := claims[s.UserClaim].(string)
	if o == "" {
		return "", http.ErrNoCookie // any error: treated as an invalid token
	}
	return o, nil
}

// login builds the redirect that restarts the login; it falls back to a 403 for odd hosts or paths.
func (s *Server) login(h *authv3.AttributeContext_HttpRequest, expire []string) decision {
	host := h.GetHost()
	if host != s.InstanceHost && host != s.WebviewHost {
		host = s.InstanceHost
	}
	p := h.GetPath()
	if strings.ContainsAny(p, "\x00\r\n \\") {
		return deny("bad-path")
	}
	return decision{result: "login", reason: "invalid-token", redirect: &redirect{"https://" + host + p, expire}}
}

func cookieHeaders(h *authv3.AttributeContext_HttpRequest) []string {
	var out []string
	if m := h.GetHeaderMap(); m != nil {
		for _, hv := range m.GetHeaders() {
			if strings.EqualFold(hv.GetKey(), "cookie") {
				out = append(out, hv.GetValue(), string(hv.GetRawValue()))
			}
		}
		return out
	}
	for k, v := range h.GetHeaders() {
		if strings.EqualFold(k, "cookie") {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) response(d decision) *authv3.CheckResponse {
	switch {
	case d.result == "allow":
		return &authv3.CheckResponse{
			Status:       &status.Status{Code: int32(codes.OK)},
			HttpResponse: &authv3.CheckResponse_OkResponse{OkResponse: &authv3.OkHttpResponse{}},
		}
	case d.redirect != nil:
		hs := []*corev3.HeaderValueOption{header("Location", d.redirect.location)}
		for _, n := range d.redirect.expire {
			c := &http.Cookie{Name: n, Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
			c.Domain = s.InstanceHost
			if v := c.String(); v != "" {
				hs = append(hs, header("Set-Cookie", v))
			}
		}
		return denied(typev3.StatusCode_Found, hs)
	}
	return denied(typev3.StatusCode_Forbidden, nil)
}

func denied(code typev3.StatusCode, hs []*corev3.HeaderValueOption) *authv3.CheckResponse {
	return &authv3.CheckResponse{
		Status: &status.Status{Code: int32(codes.PermissionDenied)},
		HttpResponse: &authv3.CheckResponse_DeniedResponse{DeniedResponse: &authv3.DeniedHttpResponse{
			Status: &typev3.HttpStatus{Code: code}, Headers: hs,
		}},
	}
}

func header(k, v string) *corev3.HeaderValueOption {
	return &corev3.HeaderValueOption{
		Header:       &corev3.HeaderValue{Key: k, Value: v},
		AppendAction: corev3.HeaderValueOption_APPEND_IF_EXISTS_OR_ADD,
	}
}

// checkSpan starts the span "authz.check" below the traceparent of the request when that is sampled.
// Otherwise it returns a span that records nothing.
func checkSpan(ctx context.Context, h *authv3.AttributeContext_HttpRequest) (context.Context, trace.Span) {
	tp := h.GetHeaders()["traceparent"]
	if tp == "" {
		return ctx, trace.SpanFromContext(context.Background())
	}
	pctx := propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": tp})
	if !trace.SpanContextFromContext(pctx).IsSampled() {
		return ctx, trace.SpanFromContext(context.Background())
	}
	return otel.Tracer("eduide-authz").Start(pctx, "authz.check")
}
