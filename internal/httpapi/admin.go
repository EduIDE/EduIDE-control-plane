package httpapi

// Read-only admin API (U16, ADR 0021): handlers under /api/v1/admin. Every handler reads from the cache
// (sessions, workspaces, pools, templates, events); Prometheus is optional. Users appear only as ownerHash (G17),
// launch data only as key names (G18).

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

const (
	adminDefaultLimit = 100
	adminMaxLimit     = 500
	expiringSoon      = 3 * 24 * time.Hour
)

func isAdminRoute(r *http.Request) bool {
	return strings.Contains(r.Pattern, " "+basePath+"/admin/")
}

// adminGate answers 404 for every admin route while the deployment configures no admin role. It runs before
// the token check, so the routes do not exist for anybody.
func (s *Server) adminGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAdminRoute(r) && s.cfg.OIDC.Admin == nil {
			writeProblem(w, problem(CodeNotFound, "no such endpoint"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminAuthz requires the admin role in the verified token and applies the own rate limit of admin routes.
func (s *Server) adminAuthz(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAdminRoute(r) {
			next.ServeHTTP(w, r)
			return
		}
		u := user(r)
		if a := s.cfg.OIDC.Admin; a == nil || !slices.Contains(u.Roles, a.Role) {
			writeProblem(w, problem(CodeForbidden, "the admin role is required"))
			return
		}
		if !s.allowAdmin(u.Owner) {
			writeProblem(w, problem(CodeRateLimited, "too many requests, retry shortly"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowAdmin(owner string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.adminLimiters[owner]
	if !ok {
		n := s.cfg.Admin.RateLimitPerMinute
		l = rate.NewLimiter(rate.Limit(float64(n)/60), n)
		s.adminLimiters[owner] = l
	}
	return l.Allow()
}

// --- pools and the rules the status shares with them ---

func effectiveTarget(p *v1alpha1.IDEPool) int32 {
	return min(max(p.Spec.MinIdle, p.Spec.TargetIdle), p.Spec.MaxInstances)
}

func poolPressure(p *v1alpha1.IDEPool) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == v1alpha1.ConditionCapacityPressure {
			return c.Status == "True"
		}
	}
	return false
}

// poolEmpty: the pool should keep idle instances (and may, there is no pressure) but has none ready.
func poolEmpty(p *v1alpha1.IDEPool) bool {
	return !poolPressure(p) && effectiveTarget(p) > 0 && p.Status.IdleReady == 0
}

func (s *Server) pools(ctx context.Context) ([]v1alpha1.IDEPool, *apiError) {
	var l v1alpha1.IDEPoolList
	if err := s.r.List(ctx, &l, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, internalError(err)
	}
	slices.SortFunc(l.Items, func(a, b v1alpha1.IDEPool) int { return strings.Compare(a.Name, b.Name) })
	return l.Items, nil
}

func (s *Server) allSessions(ctx context.Context) ([]v1alpha1.IDESession, *apiError) {
	var l v1alpha1.IDESessionList
	if err := s.r.List(ctx, &l, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, internalError(err)
	}
	l.Items = slices.DeleteFunc(l.Items, func(x v1alpha1.IDESession) bool { return x.DeletionTimestamp != nil })
	slices.SortFunc(l.Items, func(a, b v1alpha1.IDESession) int { return strings.Compare(a.Name, b.Name) })
	return l.Items, nil
}

func (s *Server) ListAdminPools(w http.ResponseWriter, r *http.Request) {
	ps, perr := s.pools(r.Context())
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	decisions, perr := s.scalerDecisions(r.Context())
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	out := AdminPools{Pools: []AdminPool{}, Decisions: decisions}
	for i := range ps {
		p := &ps[i]
		ap := AdminPool{
			Name: p.Name, Template: p.Spec.TemplateRef, MinIdle: int(p.Spec.MinIdle), TargetIdle: int(p.Spec.TargetIdle), MaxInstances: int(p.Spec.MaxInstances),
			IdleReady: int(p.Status.IdleReady), IdleStarting: int(p.Status.IdleStarting), Claimed: int(p.Status.Claimed),
			State: AdminPoolStateOk, CapacityPressure: poolPressure(p), ScalerMode: emptyToNil(p.Annotations[v1alpha1.AnnotationScalerMode]),
		}
		switch {
		case ap.CapacityPressure:
			ap.State = AdminPoolStatePressure
		case poolEmpty(p):
			ap.State = AdminPoolStateEmpty
			if t, ok := s.hub.poolEmptySince(p.Name); ok {
				ap.EmptySince = &t
			}
		}
		for j := range decisions { // newest first
			if decisions[j].Pool == p.Name {
				ap.LastDecision = &decisions[j]
				break
			}
		}
		out.Pools = append(out.Pools, ap)
	}
	writeJSON(w, http.StatusOK, out)
}

// --- sessions ---

var traceparentRE = regexp.MustCompile(`^[0-9a-f]{2}-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}$`)

// traceIDOf is the trace id of a W3C traceparent value, "" when it is malformed or all zero.
func traceIDOf(traceparent string) string {
	m := traceparentRE.FindStringSubmatch(traceparent)
	if m == nil || strings.Trim(m[1], "0") == "" {
		return ""
	}
	return m[1]
}

func (s *Server) traceURL(id string) *string {
	l := s.cfg.Admin.Links
	switch {
	case id == "":
		return nil
	case l.TraceURLTemplate != "":
		return ptr(strings.ReplaceAll(l.TraceURLTemplate, "{traceId}", id))
	case l.Jaeger != "":
		return ptr(strings.TrimRight(l.Jaeger, "/") + "/trace/" + id)
	}
	return nil
}

func (s *Server) adminSessionAPI(sess *v1alpha1.IDESession, t *v1alpha1.IDETemplate) AdminSession {
	base := sessionToAPI(sess, t)
	traceID := traceIDOf(sess.Annotations[v1alpha1.AnnotationTraceparent])
	out := AdminSession{
		Name: base.Name, Template: base.Template, Workspace: base.Workspace, Phase: base.Phase,
		Persistence: PersistenceMode(v1alpha1.PersistenceNone), ErrorCode: base.ErrorCode, Message: base.Message,
		Title:     s.adminTitle(sess.Spec.Display, sess.Spec.Owner),
		OwnerHash: s.userID(sess.Spec.Owner), CreatedAt: base.CreatedAt, ReadyAt: base.ReadyAt, LastActivity: base.LastActivity,
		StopsAt: base.StopsAt, TraceId: emptyToNil(traceID), TraceUrl: s.traceURL(traceID),
	}
	if sess.Spec.WorkspaceRef != "" {
		out.Persistence = PersistenceMode(v1alpha1.PersistenceVolume)
	}
	if base.StartStrategy != nil {
		out.StartStrategy = ptr(string(*base.StartStrategy))
	}
	if base.StopReason != nil {
		out.StopReason = ptr(string(*base.StopReason))
	}
	return out
}

// adminTitle is the title without the owner's user name: repository names often end in the user name, and the
// strip pattern may be unset or miss it.
func (s *Server) adminTitle(d v1alpha1.Display, owner string) *string {
	t := titleOf(d, s.titleStrip)
	if owner != "" && strings.Contains(strings.ToLower(t), strings.ToLower(owner)) {
		t = regexp.MustCompile("(?i)"+regexp.QuoteMeta(owner)).ReplaceAllLiteralString(t, "***")
	}
	return emptyToNil(t)
}

func (s *Server) templateMap(ctx context.Context) (map[string]*v1alpha1.IDETemplate, *apiError) {
	var l v1alpha1.IDETemplateList
	if err := s.r.List(ctx, &l, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, internalError(err)
	}
	m := map[string]*v1alpha1.IDETemplate{}
	for i := range l.Items {
		m[l.Items[i].Name] = &l.Items[i]
	}
	return m, nil
}

// pageParams checks cursor and limit; the limit defaults to 100 and is at most 500.
func pageParams(cursor *Cursor, limit *Limit) (string, int, *apiError) {
	n := adminDefaultLimit
	if limit != nil {
		n = *limit
	}
	if n < 1 || n > adminMaxLimit {
		return "", 0, problem(CodeInvalidInput, "limit must be between 1 and 500")
	}
	if cursor == nil || *cursor == "" {
		return "", n, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(*cursor)
	if err != nil || len(b) == 0 {
		return "", 0, problem(CodeInvalidInput, "invalid cursor")
	}
	return string(b), n, nil
}

// paginate cuts the page after the cursor out of items, which are sorted by name.
func paginate[T any](items []T, name func(T) string, cursor string, limit int) ([]T, *string) {
	if cursor != "" {
		items = items[sort.Search(len(items), func(i int) bool { return name(items[i]) > cursor }):]
	}
	if len(items) <= limit {
		return items, nil
	}
	return items[:limit], ptr(base64.RawURLEncoding.EncodeToString([]byte(name(items[limit-1]))))
}

type sessionFilter struct{ template, persistence, phase, q string }

func (f sessionFilter) match(a AdminSession) bool {
	return (f.template == "" || a.Template == f.template) &&
		(f.persistence == "" || string(a.Persistence) == f.persistence) &&
		(f.phase == "" || string(a.Phase) == f.phase) &&
		(f.q == "" || strings.Contains(strings.ToLower(a.Name), f.q) || strings.Contains(strings.ToLower(deref(a.Title)), f.q))
}

func (s *Server) adminSessionPage(ctx context.Context, f sessionFilter, cursor string, limit int) (AdminSessionList, *apiError) {
	sessions, perr := s.allSessions(ctx)
	if perr != nil {
		return AdminSessionList{}, perr
	}
	tpls, perr := s.templateMap(ctx)
	if perr != nil {
		return AdminSessionList{}, perr
	}
	all := []AdminSession{}
	for i := range sessions {
		if a := s.adminSessionAPI(&sessions[i], tpls[sessions[i].Spec.TemplateRef]); f.match(a) {
			all = append(all, a)
		}
	}
	items, next := paginate(all, func(a AdminSession) string { return a.Name }, cursor, limit)
	return AdminSessionList{Items: items, Total: len(all), Next: next}, nil
}

func (s *Server) ListAdminSessions(w http.ResponseWriter, r *http.Request, p ListAdminSessionsParams) {
	cursor, limit, perr := pageParams(p.Cursor, p.Limit)
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	if (p.Persistence != nil && !p.Persistence.Valid()) || (p.Phase != nil && !p.Phase.Valid()) {
		writeProblem(w, problem(CodeInvalidInput, "invalid filter value"))
		return
	}
	f := sessionFilter{template: deref(p.Template), q: strings.ToLower(deref(p.Q))}
	if p.Persistence != nil {
		f.persistence = string(*p.Persistence)
	}
	if p.Phase != nil {
		f.phase = string(*p.Phase)
	}
	out, perr := s.adminSessionPage(r.Context(), f, cursor, limit)
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) WatchAdminSessions(w http.ResponseWriter, r *http.Request) {
	s.streamEvery(w, r, adminKey, "sessions", adminEventGap, func(ctx context.Context) (any, bool, *apiError) {
		out, perr := s.adminSessionPage(ctx, sessionFilter{}, "", adminDefaultLimit)
		return out, false, perr
	})
}

func (s *Server) GetAdminSession(w http.ResponseWriter, r *http.Request, name Name) {
	var sess v1alpha1.IDESession
	err := s.r.Get(r.Context(), types.NamespacedName{Namespace: s.cfg.Namespace, Name: name}, &sess)
	if apierrors.IsNotFound(err) {
		writeProblem(w, problem(CodeNotFound, "session not found"))
		return
	}
	if err != nil {
		writeProblem(w, internalError(err))
		return
	}
	// The detail is the list item plus two fields; one JSON round trip keeps the two schemas in step.
	var out AdminSessionDetail
	b, _ := json.Marshal(s.adminSessionAPI(&sess, s.template(r.Context(), sess.Spec.TemplateRef)))
	_ = json.Unmarshal(b, &out)
	out.LaunchDataKeys = []string{}
	if ref := sess.Spec.DataSecretRef; ref != "" {
		var sec corev1.Secret
		err := s.w.Get(r.Context(), types.NamespacedName{Namespace: s.cfg.Namespace, Name: ref}, &sec)
		if err != nil && !apierrors.IsNotFound(err) {
			writeProblem(w, internalError(err))
			return
		}
		for k := range sec.Data { // key names only, the values are never read into a response (G18)
			out.LaunchDataKeys = append(out.LaunchDataKeys, k)
		}
		slices.Sort(out.LaunchDataKeys)
	}
	ts := sess.Status.Timestamps
	if ts.Claimed != nil || ts.PodReady != nil || ts.AgentReady != nil || ts.Ready != nil {
		out.Timestamps = &AdminSessionTimestamps{Claimed: timePtr(ts.Claimed), PodReady: timePtr(ts.PodReady), AgentReady: timePtr(ts.AgentReady), Ready: timePtr(ts.Ready)}
	}
	writeJSON(w, http.StatusOK, out)
}

// --- workspaces ---

func (s *Server) adminWorkspaceAPI(ws *v1alpha1.IDEWorkspace, tpls map[string]*v1alpha1.IDETemplate, busy map[string]bool, now time.Time) AdminWorkspace {
	base := workspaceToAPI(ws, busy[ws.Name], s.cfg.Workspaces.Grace.Duration)
	size := s.cfg.Workspaces.DefaultSize
	if t := tpls[ws.Spec.TemplateRef]; t != nil && t.Spec.Persistence.Size != nil {
		size = t.Spec.Persistence.Size
	}
	if ws.Spec.Persistence.Size != nil {
		size = ws.Spec.Persistence.Size
	}
	out := AdminWorkspace{
		Name: ws.Name, Template: ws.Spec.TemplateRef, Phase: base.Phase, State: AdminWorkspaceStateActive,
		Title: s.adminTitle(ws.Spec.Display, ws.Spec.Owner), OwnerHash: s.userID(ws.Spec.Owner),
		CreatedAt: base.CreatedAt, LastUsed: base.LastUsed, ExpiresAt: base.ExpiresAt, DeletesAt: base.DeletesAt,
	}
	if size != nil {
		out.RequestedBytes = size.Value()
	}
	switch {
	case busy[ws.Name]:
		out.State = AdminWorkspaceStateInUse
	case base.Phase == WorkspacePhase(v1alpha1.WorkspaceExpired):
		out.State = AdminWorkspaceStateExpired
	case base.DeletesAt != nil && base.DeletesAt.Sub(now) <= expiringSoon:
		out.State = AdminWorkspaceStateExpiring
	}
	return out
}

// storageQuota is the requests.storage quota of the namespace, nil when there is none. It reads the API
// directly (a cache would need a watch) and keeps the answer for 30 s.
func (s *Server) storageQuota(ctx context.Context) *StorageQuota {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	if s.quotaAt.IsZero() || s.now().Sub(s.quotaAt) > 30*time.Second {
		s.quotaAt, s.quota = s.now(), nil
		var l corev1.ResourceQuotaList
		if err := s.w.List(ctx, &l, client.InNamespace(s.cfg.Namespace)); err != nil {
			return nil
		}
		for _, q := range l.Items {
			hard, ok := q.Status.Hard[corev1.ResourceRequestsStorage]
			if !ok {
				if hard, ok = q.Spec.Hard[corev1.ResourceRequestsStorage]; !ok {
					continue
				}
			}
			used := q.Status.Used[corev1.ResourceRequestsStorage]
			if s.quota == nil || hard.Value() < s.quota.HardBytes {
				s.quota = &StorageQuota{HardBytes: hard.Value(), UsedBytes: used.Value()}
			}
		}
	}
	return s.quota
}

func (s *Server) ListAdminWorkspaces(w http.ResponseWriter, r *http.Request, p ListAdminWorkspacesParams) {
	cursor, limit, perr := pageParams(p.Cursor, p.Limit)
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	if p.State != nil && !p.State.Valid() {
		writeProblem(w, problem(CodeInvalidInput, "invalid state"))
		return
	}
	var l v1alpha1.IDEWorkspaceList
	if err := s.r.List(r.Context(), &l, client.InNamespace(s.cfg.Namespace)); err != nil {
		writeProblem(w, internalError(err))
		return
	}
	sessions, perr := s.allSessions(r.Context())
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	tpls, perr := s.templateMap(r.Context())
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	busy, now := busyWorkspaces(sessions), s.now()
	sum := AdminWorkspaceSummary{Quota: s.storageQuota(r.Context())}
	q, matched := strings.ToLower(deref(p.Q)), []AdminWorkspace{}
	slices.SortFunc(l.Items, func(a, b v1alpha1.IDEWorkspace) int { return strings.Compare(a.Name, b.Name) })
	for i := range l.Items {
		if l.Items[i].DeletionTimestamp != nil {
			continue
		}
		a := s.adminWorkspaceAPI(&l.Items[i], tpls, busy, now)
		sum.Total++
		sum.RequestedBytes += a.RequestedBytes
		switch a.State {
		case AdminWorkspaceStateInUse:
			sum.InUse++
		case AdminWorkspaceStateExpired:
			sum.Expired++
		case AdminWorkspaceStateExpiring:
			sum.ExpiringIn3Days++
		}
		if (p.Template == nil || a.Template == *p.Template) && (p.State == nil || a.State == *p.State) &&
			(q == "" || strings.Contains(strings.ToLower(a.Name), q) || strings.Contains(strings.ToLower(deref(a.Title)), q) || strings.Contains(a.OwnerHash, q)) {
			matched = append(matched, a)
		}
	}
	items, next := paginate(matched, func(a AdminWorkspace) string { return a.Name }, cursor, limit)
	writeJSON(w, http.StatusOK, AdminWorkspaceList{Items: items, Total: len(matched), Next: next, Summary: sum})
}

// --- templates and config ---

func (s *Server) ListAdminTemplates(w http.ResponseWriter, r *http.Request) {
	base, perr := s.templates(r.Context())
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	tpls, perr := s.templateMap(r.Context())
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	ps, perr := s.pools(r.Context())
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	secs := func(d *metav1.Duration, def time.Duration) int {
		if d != nil {
			return int(d.Seconds())
		}
		return int(def.Seconds())
	}
	out := []AdminTemplate{}
	for _, b := range base {
		t := tpls[b.Name]
		a := AdminTemplate{
			Name: b.Name, DisplayName: b.DisplayName, LogoUrl: b.LogoUrl, Image: t.Spec.Image, ImageVersion: b.ImageVersion,
			Persistence: b.Persistence, DefaultPersistence: b.DefaultPersistence, Variants: b.Variants, Includes: b.Includes, StartEstimate: b.StartEstimate,
			InjectableKeys: []AdminInjectableKey{},
			Timeouts: AdminTimeouts{
				IdleSeconds: secs(t.Spec.Timeouts.Idle, v1alpha1.DefaultIdleTimeout), WarningSeconds: secs(t.Spec.Timeouts.Warning, v1alpha1.DefaultWarningTimeout),
				MaxSeconds: secs(t.Spec.Timeouts.Max, v1alpha1.DefaultMaxTimeout),
			},
		}
		if t.Spec.Retention != nil {
			a.RetentionSeconds = ptr(int(t.Spec.Retention.Seconds()))
		}
		if t.Status.Image != nil {
			a.ImageDigest = emptyToNil(t.Status.Image.Digest)
		}
		for _, k := range t.Spec.InjectableKeys {
			ak := AdminInjectableKey{Name: k.Name, Source: AdminInjectableKeySource(cmp.Or(k.Source, v1alpha1.SourceRequest)), Required: k.Required}
			if k.Format != "" {
				ak.Format = ptr(AdminInjectableKeyFormat(k.Format))
			}
			if k.MaxLength > 0 {
				ak.MaxLength = ptr(int(k.MaxLength))
			}
			a.InjectableKeys = append(a.InjectableKeys, ak)
		}
		for _, p := range ps {
			if p.Spec.TemplateRef == b.Name {
				a.Pool = &AdminPoolRef{Name: p.Name, MinIdle: int(p.Spec.MinIdle), TargetIdle: int(p.Spec.TargetIdle), MaxInstances: int(p.Spec.MaxInstances)}
				break
			}
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetAdminConfig(w http.ResponseWriter, _ *http.Request) {
	c := s.cfg
	out := AdminConfig{
		Deployment: c.Deployment, Namespace: c.Namespace, SessionsPerUser: c.SessionsPerUser, AllowedHosts: nonNil(c.AllowedHosts),
		LaunchDataMaxBytes: maxDataBytes, TitleStripPattern: emptyToNil(c.Overview.TitleStripPattern),
	}
	out.Hosts.Service, out.Hosts.Instance, out.Hosts.Webview = c.Hosts.Service, c.Hosts.Instance, c.Hosts.Webview
	out.Oidc.IssuerURL, out.Oidc.ClientIDs, out.Oidc.UserClaim = c.OIDC.IssuerURL, nonNil(c.OIDC.ClientIDs), c.OIDC.UserClaim
	if a := c.OIDC.Admin; a != nil {
		out.Oidc.AdminRolePath, out.Oidc.AdminRole = a.RolePath, a.Role
	}
	out.Workspaces.RetentionSeconds = int(c.Workspaces.Retention.Seconds())
	out.Workspaces.GraceSeconds = int(c.Workspaces.Grace.Seconds())
	out.Workspaces.StorageClassName = emptyToNil(c.Workspaces.StorageClassName)
	if c.Workspaces.DefaultSize != nil {
		out.Workspaces.DefaultSizeBytes = c.Workspaces.DefaultSize.Value()
	}
	if c.Capacity.ExhaustedAfter != nil {
		out.Capacity.ExhaustedAfterSeconds = int(c.Capacity.ExhaustedAfter.Seconds())
	}
	out.RateLimit.LaunchesPerMinute, out.RateLimit.AdminPerMinute = c.RateLimit.LaunchesPerMinute, c.Admin.RateLimitPerMinute
	out.Telemetry.OtlpEndpoint, out.Telemetry.UserIdsHashed = emptyToNil(c.Telemetry.OTLPEndpoint), len(s.salt) > 0
	out.Admin.PrometheusConfigured = c.Admin.PrometheusURL != ""
	out.Admin.PrometheusURL = emptyToNil(c.Admin.PrometheusURL)
	if l := c.Admin.Links; l != (config.AdminLinks{}) {
		out.Admin.Links = &struct {
			Grafana          *string `json:"grafana,omitempty"`
			Jaeger           *string `json:"jaeger,omitempty"`
			TraceUrlTemplate *string `json:"traceUrlTemplate,omitempty"`
		}{emptyToNil(l.Grafana), emptyToNil(l.Jaeger), emptyToNil(l.TraceURLTemplate)}
	}
	writeJSON(w, http.StatusOK, out)
}

func nonNil[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}
