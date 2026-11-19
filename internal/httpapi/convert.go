package httpapi

import (
	"cmp"
	"regexp"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

func ptr[T any](v T) *T { return &v }

func timePtr(t *metav1.Time) *time.Time {
	if t == nil {
		return nil
	}
	return &t.Time
}

func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func displayToAPI(d v1alpha1.Display) *Display {
	if d.Title == "" && d.Source == "" {
		return nil
	}
	return &Display{Title: emptyToNil(d.Title), Source: emptyToNil(d.Source)}
}

// stopsAt is when the session ends (U13): the earlier of the idle end and the maximum lifetime, with the same
// timeouts and base time as the operator (internal/controller/lifecycle.go). t may be nil for the defaults.
func stopsAt(s *v1alpha1.IDESession, t *v1alpha1.IDETemplate) (time.Time, SessionStopReason, bool) {
	st := &s.Status
	base := cmp.Or(st.Timestamps.AgentReady, st.Timestamps.Claimed, st.Timestamps.Ready)
	if s.DeletionTimestamp != nil || st.Phase == v1alpha1.SessionFailed || base == nil {
		return time.Time{}, "", false
	}
	at := base.Time
	if st.LastActivity != nil && st.LastActivity.After(at) {
		at = st.LastActivity.Time
	}
	idle, _, max := t.ResolvedTimeouts() // the values the operator ends the session by
	idleAt, maxAt := at.Add(idle), s.CreationTimestamp.Add(max)
	if maxAt.Before(idleAt) {
		return maxAt, Max, true
	}
	return idleAt, Idle, true
}

// deletesAt is when the workspace and its volume are deleted (U13); not while a session runs or with the retain annotation.
func deletesAt(w *v1alpha1.IDEWorkspace, inUse bool, grace time.Duration) *time.Time {
	if inUse || w.Annotations[v1alpha1.AnnotationRetain] == "true" {
		return nil
	}
	from := cmp.Or(w.Status.ExpiredAt, w.Status.ExpiresAt)
	if from == nil {
		return nil
	}
	return ptr(from.Add(grace))
}

func sessionToAPI(s *v1alpha1.IDESession, t *v1alpha1.IDETemplate) Session {
	out := Session{
		Name:         s.Name,
		Template:     s.Spec.TemplateRef,
		Workspace:    emptyToNil(s.Spec.WorkspaceRef),
		Phase:        SessionPhase(cmp.Or(s.Status.Phase, v1alpha1.SessionPending)),
		Display:      displayToAPI(s.Spec.Display),
		CreatedAt:    s.CreationTimestamp.Time,
		ReadyAt:      timePtr(s.Status.Timestamps.Ready),
		LastActivity: timePtr(s.Status.LastActivity),
		ErrorCode:    emptyToNil(s.Status.ErrorCode),
		Message:      emptyToNil(s.Status.Message),
	}
	if s.Status.Phase == v1alpha1.SessionReady {
		out.Url = emptyToNil(s.Status.URL)
	}
	if s.Status.StartStrategy != "" {
		out.StartStrategy = ptr(SessionStartStrategy(s.Status.StartStrategy))
	}
	if at, why, ok := stopsAt(s, t); ok {
		out.StopsAt, out.StopReason = &at, &why
	}
	return out
}

func workspaceToAPI(w *v1alpha1.IDEWorkspace, inUse bool, grace time.Duration) Workspace {
	return Workspace{
		DeletesAt: deletesAt(w, inUse, grace),
		Name:      w.Name,
		Template:  w.Spec.TemplateRef,
		Phase:     WorkspacePhase(cmp.Or(w.Status.Phase, v1alpha1.WorkspacePending)),
		Display:   displayToAPI(w.Spec.Display),
		CreatedAt: w.CreationTimestamp.Time,
		LastUsed:  timePtr(w.Status.LastUsed),
		ExpiresAt: timePtr(w.Status.ExpiresAt),
	}
}

func templateToAPI(t *v1alpha1.IDETemplate, warm bool) Template {
	out := Template{
		Name:               t.Name,
		DisplayName:        cmp.Or(t.Spec.DisplayName, t.Name),
		LogoUrl:            emptyToNil(t.Spec.LogoURL),
		DefaultPersistence: PersistenceMode(cmp.Or(t.Spec.Persistence.Default, v1alpha1.PersistenceNone)),
		Persistence:        []PersistenceMode{},
		InjectableKeys:     []string{},
	}
	for _, m := range t.Spec.Persistence.Allowed {
		out.Persistence = append(out.Persistence, PersistenceMode(m))
	}
	for _, k := range t.Spec.InjectableKeys {
		if k.Source == v1alpha1.SourceRequest || k.Source == "" {
			out.InjectableKeys = append(out.InjectableKeys, k.Name)
		}
	}
	if warm {
		out.Warm = ptr(true)
	}
	if img := t.Status.Image; img != nil {
		out.ImageVersion = emptyToNil(img.Version)
		if len(img.Variants) > 0 {
			vs := []TemplateVariant{}
			for _, v := range img.Variants {
				vs = append(vs, TemplateVariant{Id: v.ID, Name: v.Name, Description: emptyToNil(v.Description), LogoUrl: emptyToNil(v.LogoURL)})
			}
			out.Variants = &vs
		}
		if len(img.Includes) > 0 {
			gs := []TemplateIncludeGroup{}
			for _, g := range img.Includes {
				tg := TemplateIncludeGroup{Group: g.Group, Items: []TemplateIncludeItem{}}
				for _, it := range g.Items {
					tg.Items = append(tg.Items, TemplateIncludeItem{Name: it.Name, Version: emptyToNil(it.Version)})
				}
				gs = append(gs, tg)
			}
			out.Includes = &gs
		}
	}
	return out
}

// deriveTitle is the overview title for a display source (G15): the last path segment
// without ".git" and without matches of strip (config overview.titleStripPattern, may be nil).
func deriveTitle(source string, strip *regexp.Regexp) string {
	s := strings.TrimRight(source, "/")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(s, ".git")
	if strip != nil {
		if t := strip.ReplaceAllString(s, ""); t != "" {
			s = t
		}
	}
	return s
}

func titleOf(d v1alpha1.Display, strip *regexp.Regexp) string {
	if d.Title != "" {
		return d.Title
	}
	return deriveTitle(d.Source, strip)
}
