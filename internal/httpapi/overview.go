package httpapi

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

func (s *Server) overview(ctx context.Context, owner string) (*Overview, *apiError) {
	sessions, err := s.ownedSessions(ctx, s.r, owner)
	if err != nil {
		return nil, internalError(err)
	}
	workspaces, err := s.ownedWorkspaces(ctx, owner)
	if err != nil {
		return nil, internalError(err)
	}
	var ts v1alpha1.IDETemplateList
	if err := s.r.List(ctx, &ts, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, internalError(err)
	}
	tpls := map[string]*v1alpha1.IDETemplate{}
	for i := range ts.Items {
		tpls[ts.Items[i].Name] = &ts.Items[i]
	}
	wsByName := map[string]*v1alpha1.IDEWorkspace{}
	for i := range workspaces {
		if workspaces[i].DeletionTimestamp == nil {
			wsByName[workspaces[i].Name] = &workspaces[i]
		}
	}
	item := func(kind OverviewItemKind, d v1alpha1.Display, template string, running bool) OverviewItem {
		it := OverviewItem{Kind: kind, Title: titleOf(d, s.titleStrip), Source: emptyToNil(d.Source), Template: template, Running: running}
		if t := tpls[template]; t != nil {
			it.TemplateDisplayName = ptr(cmp.Or(t.Spec.DisplayName, t.Name))
			it.TemplateLogoUrl = emptyToNil(t.Spec.LogoURL)
		}
		return it
	}

	var items []OverviewItem
	used := map[string]bool{}
	for i := range sessions {
		sess := &sessions[i]
		if sess.DeletionTimestamp != nil {
			continue
		}
		it := item(OverviewItemKindSession, sess.Spec.Display, sess.Spec.TemplateRef, true)
		api := sessionToAPI(sess)
		it.Session = &api
		it.LastUsed = cmp.Or(api.LastActivity, api.ReadyAt, &api.CreatedAt)
		if ws := wsByName[sess.Spec.WorkspaceRef]; ws != nil {
			used[ws.Name] = true
			wsAPI := workspaceToAPI(ws)
			it.Workspace = &wsAPI
			it.ExpiresAt = wsAPI.ExpiresAt
			if it.Title == "" {
				it.Title = titleOf(ws.Spec.Display, s.titleStrip)
			}
		}
		items = append(items, it)
	}
	for _, ws := range wsByName {
		if used[ws.Name] {
			continue
		}
		it := item(OverviewItemKindWorkspace, ws.Spec.Display, ws.Spec.TemplateRef, false)
		api := workspaceToAPI(ws)
		it.Workspace = &api
		it.LastUsed = cmp.Or(api.LastUsed, &api.CreatedAt)
		it.ExpiresAt = api.ExpiresAt
		items = append(items, it)
	}
	for i := range items {
		if items[i].Title == "" {
			items[i].Title = cmp.Or(deref(items[i].TemplateDisplayName), items[i].Template)
		}
	}
	slices.SortFunc(items, func(a, b OverviewItem) int {
		if a.Running != b.Running {
			if a.Running {
				return -1
			}
			return 1
		}
		if c := timeOf(b.LastUsed).Compare(timeOf(a.LastUsed)); c != 0 {
			return c
		}
		return strings.Compare(a.Title, b.Title)
	})
	if items == nil {
		items = []OverviewItem{}
	}
	return &Overview{Items: items}, nil
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func (s *Server) GetOverview(w http.ResponseWriter, r *http.Request) {
	o, perr := s.overview(r.Context(), user(r).Owner)
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) WatchOverview(w http.ResponseWriter, r *http.Request) {
	owner := user(r).Owner
	s.stream(w, r, v1alpha1.OwnerHash(owner), "overview", func(ctx context.Context) (any, bool, *apiError) {
		o, perr := s.overview(ctx, owner)
		return o, false, perr
	})
}
