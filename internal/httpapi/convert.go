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

func sessionToAPI(s *v1alpha1.IDESession) Session {
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
	return out
}

func workspaceToAPI(w *v1alpha1.IDEWorkspace) Workspace {
	return Workspace{
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
	return out
}

// userSuffix matches the user abbreviation some Git hosts append to repository names, e.g. "-ab12cde".
var userSuffix = regexp.MustCompile(`-[a-z]{2}[0-9]{2}[a-z]{3}$`)

// deriveTitle is the overview title for a display source (G15): the last path segment
// without ".git" and without a user suffix.
func deriveTitle(source string) string {
	s := strings.TrimRight(source, "/")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(s, ".git")
	if t := userSuffix.ReplaceAllString(s, ""); t != "" {
		s = t
	}
	return s
}

func titleOf(d v1alpha1.Display) string {
	if d.Title != "" {
		return d.Title
	}
	return deriveTitle(d.Source)
}
