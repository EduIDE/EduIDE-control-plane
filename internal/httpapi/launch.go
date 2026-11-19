package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/auth"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

// LaunchSession continues the trace of the incoming traceparent header (params.Traceparent) with a span
// "launch"; the session carries the trace context of that span so operator and agent join it.
func (s *Server) LaunchSession(w http.ResponseWriter, r *http.Request, _ LaunchSessionParams) {
	ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := otel.Tracer(tracerName).Start(ctx, "launch")
	defer span.End()
	u := user(r)
	if len(s.salt) > 0 {
		span.SetAttributes(attribute.String("eduide.user_id", telemetry.UserID(s.salt, u.Owner)))
	}
	if !s.allowLaunch(u.Owner) {
		span.SetStatus(codes.Error, string(CodeRateLimited))
		writeProblem(w, problem(CodeRateLimited, "too many launches, retry shortly"))
		return
	}
	req, perr := decodeLaunchRequest(r.Body)
	template := "unknown"
	if perr == nil {
		template = req.Template
		var sess *v1alpha1.IDESession
		var created bool
		sess, created, perr = s.launch(ctx, u, req)
		if perr == nil {
			res, status := "existing", http.StatusOK
			if created {
				res, status = "created", http.StatusCreated
			}
			span.SetAttributes(attribute.String("eduide.template", template), attribute.String("eduide.session", sess.Name), attribute.String("eduide.launch_result", res), attribute.String("eduide.persistence", persistenceOfSession(sess)))
			telemetry.Launches.WithLabelValues(template, res).Inc()
			slog.InfoContext(ctx, "launch", "session", sess.Name, "template", template, "result", res, "user", s.userID(u.Owner))
			writeJSON(w, status, s.sessionAPI(ctx, sess))
			return
		}
		if perr.code == CodeTemplateNotFound {
			template = "unknown" // keeps the label bounded
		}
	}
	span.SetAttributes(attribute.String("eduide.template", template), attribute.String("eduide.launch_result", "rejected"))
	span.SetStatus(codes.Error, string(perr.code)) // the code only: details may name launch data keys
	span.SetAttributes(attribute.String("eduide.error_code", string(perr.code)))
	telemetry.Launches.WithLabelValues(template, "rejected").Inc()
	// The detail names keys and rules, never values.
	slog.DebugContext(ctx, "launch rejected", "template", template, "code", string(perr.code), "detail", perr.detail)
	// The detail names the key and the rule that failed, never a value (validateData and problem() keep it so).
	slog.InfoContext(ctx, "launch", "template", template, "result", "rejected", "code", string(perr.code), "detail", perr.detail, "user", s.userID(u.Owner))
	writeProblem(w, perr)
}

func isActive(s *v1alpha1.IDESession) bool {
	return s.DeletionTimestamp == nil && s.Status.Phase != v1alpha1.SessionFailed
}

const tracerName = "github.com/EduIDE/EduIDE-control-plane/internal/httpapi"

// phase runs f in a child span of the launch, one per step a person can reason about. A failure sets the span
// status to the stable error code; the detail stays out because it may name launch data keys.
func phase[T any](ctx context.Context, name string, attrs []attribute.KeyValue, f func(context.Context) (T, *apiError)) (T, *apiError) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, name, trace.WithAttributes(attrs...))
	defer span.End()
	v, perr := f(ctx)
	if perr != nil {
		span.SetStatus(codes.Error, string(perr.code))
		span.SetAttributes(attribute.String("eduide.error_code", string(perr.code)))
	}
	return v, perr
}

// launchPlan is the validated request: everything the create steps need.
type launchPlan struct {
	tpl      v1alpha1.IDETemplate
	display  v1alpha1.Display
	data     map[string]string
	wsName   string
	createWS bool
	key      string
	// existing is the active session with the same launch key; nothing is created then.
	existing *v1alpha1.IDESession
}

func (s *Server) launch(ctx context.Context, u auth.User, req *LaunchRequest) (*v1alpha1.IDESession, bool, *apiError) {
	attrs := []attribute.KeyValue{attribute.String("eduide.template", req.Template)}
	if len(s.salt) > 0 {
		attrs = append(attrs, attribute.String("eduide.user_id", telemetry.UserID(s.salt, u.Owner)))
	}
	defer s.lockOwner(u.Owner)()
	p, perr := phase(ctx, "validate", attrs, func(ctx context.Context) (*launchPlan, *apiError) { return s.validate(ctx, u, req) })
	if perr != nil {
		return nil, false, perr
	}
	if p.existing != nil {
		return p.existing, false, nil
	}
	attrs = append(attrs, attribute.String("eduide.persistence", persistenceName(p)))
	var ws *v1alpha1.IDEWorkspace
	if p.wsName != "" {
		ws, perr = phase(ctx, "workspace.get_or_create", attrs, func(ctx context.Context) (*v1alpha1.IDEWorkspace, *apiError) {
			if p.createWS {
				return s.ensureWorkspace(ctx, p.wsName, u.Owner, p.tpl.Name, p.display)
			}
			return s.ownedWorkspace(ctx, p.wsName, u.Owner)
		})
		if perr != nil {
			return nil, false, perr
		}
		if ws.VolumeLost() {
			return nil, false, problem(CodeWorkspaceLost, "the volume of the workspace is gone")
		}
	}
	sess, created, perr := s.createSession(ctx, attrs, u.Owner, &p.tpl, ws, p.key, p.data, p.display)
	if perr == nil && created {
		perr = s.overLimit(ctx, u.Owner, sess)
	}
	if perr != nil {
		return nil, false, perr
	}
	return sess, created, nil
}

// overLimit closes the gap between the session count in validate and the create for launches that ran in
// another replica at the same time (lockOwner covers this one). With the new session over the limit, it stays
// only if fewer than the limit are as old or older; otherwise it is deleted again. On equal timestamps every
// one of them goes: the limit holds, and the client retries.
func (s *Server) overLimit(ctx context.Context, owner string, sess *v1alpha1.IDESession) *apiError {
	sessions, err := s.ownedSessions(ctx, s.w, owner)
	if err != nil {
		return internalError(err)
	}
	var before []v1alpha1.IDESession
	for i := range sessions {
		if o := &sessions[i]; o.Name != sess.Name && isActive(o) && !o.CreationTimestamp.After(sess.CreationTimestamp.Time) {
			before = append(before, *o)
		}
	}
	if len(before) < s.cfg.SessionsPerUser {
		return nil
	}
	if err := s.w.Delete(ctx, sess); client.IgnoreNotFound(err) != nil {
		return internalError(err)
	}
	return s.sessionLimit(ctx, before)
}

func (s *Server) sessionLimit(ctx context.Context, running []v1alpha1.IDESession) *apiError {
	e := problem(CodeSessionLimit, "session limit reached")
	e.sessions = []Session{}
	for i := range running {
		e.sessions = append(e.sessions, s.sessionAPI(ctx, &running[i]))
	}
	slices.SortFunc(e.sessions, func(a, b Session) int { return strings.Compare(a.Name, b.Name) })
	return e
}

func persistenceOfSession(s *v1alpha1.IDESession) string {
	if s.Spec.WorkspaceRef != "" {
		return string(v1alpha1.PersistenceVolume)
	}
	return string(v1alpha1.PersistenceNone)
}

func persistenceName(p *launchPlan) string {
	if p.wsName != "" {
		return string(v1alpha1.PersistenceVolume)
	}
	return string(v1alpha1.PersistenceNone)
}

// validate checks the request against the template, resolves persistence and finds a session with the same launch key.
func (s *Server) validate(ctx context.Context, u auth.User, req *LaunchRequest) (*launchPlan, *apiError) {
	var tpl v1alpha1.IDETemplate
	if err := s.r.Get(ctx, types.NamespacedName{Namespace: s.cfg.Namespace, Name: req.Template}, &tpl); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, problem(CodeTemplateNotFound, "template not found")
		}
		return nil, internalError(err)
	}
	if perr := validateDisplay(req.Display); perr != nil {
		return nil, perr
	}
	var in map[string]string
	if req.Data != nil {
		in = *req.Data
	}
	data, perr := validateData(&tpl, in, u, s.cfg.AllowedHosts)
	if perr != nil {
		return nil, perr
	}
	var display v1alpha1.Display
	if req.Display != nil {
		display = v1alpha1.Display{Title: deref(req.Display.Title), Source: deref(req.Display.Source)}
	}
	if display.Source == "" {
		if src := withoutCredentials(data["GIT_URI"]); validateDisplay(&Display{Source: &src}) == nil {
			display.Source = src
		}
	}

	wsName, createWS, perr := s.resolvePersistence(ctx, u.Owner, &tpl, req, data, display)
	if perr != nil {
		return nil, perr
	}

	key := launchKey(u.Owner, tpl.Name, wsName, data)
	slog.DebugContext(ctx, "launch validated", "template", tpl.Name, "workspace", wsName, "keys", len(data), "launch_key", key)
	// Live read: the cache may not hold a session a launch created a moment ago.
	sessions, err := s.ownedSessions(ctx, s.w, u.Owner)
	if err != nil {
		return nil, internalError(err)
	}
	var running []v1alpha1.IDESession
	for i := range sessions {
		if !isActive(&sessions[i]) {
			continue
		}
		if sessions[i].Labels[v1alpha1.LabelLaunchKey] == key {
			slog.DebugContext(ctx, "launch is idempotent: session with the same launch key exists", "session", sessions[i].Name, "launch_key", key)
			return &launchPlan{existing: &sessions[i]}, nil
		}
		running = append(running, sessions[i])
	}
	if len(running) >= s.cfg.SessionsPerUser {
		return nil, s.sessionLimit(ctx, running)
	}

	return &launchPlan{tpl: tpl, display: display, data: data, wsName: wsName, createWS: createWS, key: key}, nil
}

// resolvePersistence returns the workspace name for volume sessions and whether it still has to be created.
func (s *Server) resolvePersistence(ctx context.Context, owner string, tpl *v1alpha1.IDETemplate, req *LaunchRequest, data map[string]string, display v1alpha1.Display) (string, bool, *apiError) {
	mode := cmpOr(tpl.Spec.Persistence.Default, v1alpha1.PersistenceNone)
	if req.Persistence != nil {
		mode = v1alpha1.PersistenceMode(*req.Persistence)
	}
	var name string
	if req.Workspace != nil && *req.Workspace != "" {
		if req.Persistence != nil && mode != v1alpha1.PersistenceVolume {
			return "", false, problem(CodeInvalidInput, "workspace needs persistence volume")
		}
		mode = v1alpha1.PersistenceVolume
		ws, perr := s.ownedWorkspace(ctx, *req.Workspace, owner)
		if perr != nil {
			return "", false, perr
		}
		if ws.Spec.TemplateRef != tpl.Name {
			return "", false, problem(CodeInvalidInput, "workspace belongs to another template")
		}
		name = ws.Name
	}
	if !slices.Contains(tpl.Spec.Persistence.Allowed, mode) {
		return "", false, problem(CodePersistence, "template does not allow persistence "+string(mode))
	}
	if mode != v1alpha1.PersistenceVolume {
		return "", false, nil
	}
	if name != "" {
		return name, false, nil
	}
	// Without user info: a new Artemis VCS token for the same repository must reattach the same workspace.
	src := withoutCredentials(data["GIT_URI"])
	if src == "" {
		src = display.Source
	}
	return workspaceName(owner, tpl.Name, src), true, nil
}

func cmpOr[T comparable](a, b T) T {
	var zero T
	if a != zero {
		return a
	}
	return b
}

func workspaceName(owner, template, source string) string {
	sum := sha256.Sum256([]byte(owner + "\x00" + template + "\x00" + source))
	return "w-" + hex.EncodeToString(sum[:])[:16]
}

// launchKey is the idempotency key (G14): owner and template plus the workspace, or for
// sessions without persistence a hash of the sorted launch data. It is a label value.
func launchKey(owner, template, workspace string, data map[string]string) string {
	h := sha256.New()
	h.Write([]byte(owner + "\x00" + template + "\x00" + workspace))
	if workspace == "" {
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			h.Write([]byte("\x00" + k + "\x00" + data[k]))
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:40]
}

func managedLabels(owner string, extra map[string]string) map[string]string {
	l := map[string]string{v1alpha1.LabelManagedBy: v1alpha1.LabelManagedByValue, v1alpha1.LabelOwnerHash: v1alpha1.OwnerHash(owner)}
	for k, v := range extra {
		l[k] = v
	}
	return l
}

// ensureWorkspace gets or creates the workspace with this deterministic name.
func (s *Server) ensureWorkspace(ctx context.Context, name, owner, template string, display v1alpha1.Display) (*v1alpha1.IDEWorkspace, *apiError) {
	key := types.NamespacedName{Namespace: s.cfg.Namespace, Name: name}
	var ws v1alpha1.IDEWorkspace
	err := s.r.Get(ctx, key, &ws)
	if apierrors.IsNotFound(err) {
		// Each workspace is a volume. Live read with the owner label, as for sessions: the cache may lag behind
		// a workspace this user created a moment ago.
		var owned v1alpha1.IDEWorkspaceList
		if err := s.w.List(ctx, &owned, client.InNamespace(s.cfg.Namespace), client.MatchingLabels{v1alpha1.LabelOwnerHash: v1alpha1.OwnerHash(owner)}); err != nil {
			return nil, internalError(err)
		}
		n := 0
		for i := range owned.Items {
			if o := &owned.Items[i]; o.Spec.Owner == owner && o.DeletionTimestamp == nil {
				n++
			}
		}
		if n >= s.cfg.Workspaces.PerUser {
			return nil, problem(CodeWorkspaceLimit, "workspace limit reached, delete a workspace first")
		}
		ws = v1alpha1.IDEWorkspace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.cfg.Namespace, Labels: managedLabels(owner, map[string]string{v1alpha1.LabelTemplate: template})},
			Spec: v1alpha1.IDEWorkspaceSpec{
				Owner: owner, TemplateRef: template, Display: display,
				Persistence: v1alpha1.WorkspacePersistence{Mode: v1alpha1.WorkspaceVolume},
			},
		}
		err = s.w.Create(ctx, &ws)
		if apierrors.IsAlreadyExists(err) { // the cache lags behind a concurrent launch
			err = s.w.Get(ctx, key, &ws)
		}
	}
	if err != nil {
		return nil, internalError(err)
	}
	if ws.Spec.Owner != owner {
		return nil, problem(CodeWorkspaceNotFound, "workspace not found")
	}
	return &ws, nil
}

// createSession names the session after the launch key, so two concurrent launches with the same key
// create one session: the loser gets AlreadyExists and returns the winner. A Failed or deleting session
// with that name must not block a relaunch, so the next attempts use random names.
func (s *Server) createSession(ctx context.Context, attrs []attribute.KeyValue, owner string, tpl *v1alpha1.IDETemplate, ws *v1alpha1.IDEWorkspace, key string, data map[string]string, display v1alpha1.Display) (*v1alpha1.IDESession, bool, *apiError) {
	extra := map[string]string{v1alpha1.LabelTemplate: tpl.Name, v1alpha1.LabelLaunchKey: key}
	sess := &v1alpha1.IDESession{
		ObjectMeta: metav1.ObjectMeta{Namespace: s.cfg.Namespace, Labels: managedLabels(owner, extra)},
		Spec:       v1alpha1.IDESessionSpec{Owner: owner, TemplateRef: tpl.Name, Display: display},
	}
	if ws != nil {
		sess.Labels[v1alpha1.LabelWorkspace] = ws.Name
		sess.Spec.WorkspaceRef = ws.Name
		// A session launched while its workspace is deleted (U14) goes with it: the garbage collector removes
		// sessions whose workspace UID is gone, so none mounts a deleted volume.
		sess.OwnerReferences = []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "IDEWorkspace", Name: ws.Name, UID: ws.UID}}
	}
	tc := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, tc)
	if tp := tc.Get("traceparent"); tp != "" {
		sess.Annotations = map[string]string{v1alpha1.AnnotationTraceparent: tp}
	}
	sctx, sspan := otel.Tracer(tracerName).Start(ctx, "session.create", trace.WithAttributes(attrs...))
	var err error
	for i, name := range []string{"s-" + key[:10], randomName(), randomName()} {
		sess.Name = name
		sess.Spec.DataSecretRef = sess.Name + "-data"
		if err = s.w.Create(sctx, sess); !apierrors.IsAlreadyExists(err) {
			break
		}
		var cur v1alpha1.IDESession
		if i == 0 && s.w.Get(sctx, types.NamespacedName{Namespace: s.cfg.Namespace, Name: name}, &cur) == nil &&
			cur.Spec.Owner == owner && cur.Labels[v1alpha1.LabelLaunchKey] == key && isActive(&cur) {
			sspan.SetAttributes(attribute.String("eduide.session", cur.Name), attribute.Bool("eduide.launch_existing", true))
			sspan.End()
			return &cur, false, nil
		}
	}
	if err != nil {
		sspan.SetStatus(codes.Error, string(CodeInternal))
		sspan.End()
		return nil, false, internalError(err)
	}
	sspan.SetAttributes(attribute.String("eduide.session", sess.Name))
	sspan.End()
	ctx, kspan := otel.Tracer(tracerName).Start(ctx, "secret.create", trace.WithAttributes(attrs...))
	defer kspan.End()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: sess.Spec.DataSecretRef, Namespace: s.cfg.Namespace,
			Labels: managedLabels(owner, map[string]string{v1alpha1.LabelSession: sess.Name}),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.GroupVersion.String(), Kind: "IDESession", Name: sess.Name, UID: sess.UID,
			}},
		},
		Data: map[string][]byte{},
	}
	for k, v := range data {
		secret.Data[k] = []byte(v)
	}
	err = s.w.Create(ctx, secret)
	if apierrors.IsAlreadyExists(err) { // left over from an earlier session of this name that the GC has not removed yet
		stale := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secret.Name, Namespace: secret.Namespace}}
		if err = client.IgnoreNotFound(s.w.Delete(ctx, stale)); err == nil {
			err = s.w.Create(ctx, secret)
		}
	}
	if err != nil {
		kspan.SetStatus(codes.Error, string(CodeInternal))
		_ = s.w.Delete(ctx, sess) // no session without its data
		return nil, false, internalError(err)
	}
	return sess, true, nil
}

func randomName() string { return "s-" + strings.ToLower(rand.Text()[:10]) }

// userID is the pseudonymous user id of logs and spans (G17).
func (s *Server) userID(owner string) string { return telemetry.UserID(s.salt, owner) }
