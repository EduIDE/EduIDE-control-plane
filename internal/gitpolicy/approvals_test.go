package gitpolicy

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

func TestMayUseCredential(t *testing.T) {
	ap := []Approval{{Repository: "github.com/o/ro", Access: "read"}, {Repository: "github.com/o/rw", Access: "write"}}
	const sh, sr = "github.com", "Owner/Repo"
	for _, tc := range []struct {
		name, mode, host, repo, kind string
		approved                     []Approval
		want                         bool
	}{
		{"session repo read", "off", "github.com", "owner/repo", "read", nil, false}, // paths compare exactly
		{"session repo read, exact", "off", "github.com", "Owner/Repo", "read", nil, true},
		{"session repo push, mode off", "off", "github.com", "Owner/Repo.git", "push", nil, true},
		{"session repo, empty mode", "", "GitHub.com", "Owner/Repo", "push", nil, true},
		{"other repo, mode off ignores approvals", "off", "github.com", "o/ro", "read", ap, false},
		{"read approval, read", "read", "github.com", "o/ro", "read", ap, true},
		{"read approval, push", "write", "github.com", "o/ro", "push", ap, false},
		{"write approval, read", "write", "github.com", "o/rw", "read", ap, true},
		{"write approval, push", "write", "github.com", "o/rw", "push", ap, true},
		{"write approval, push, mode read", "read", "github.com", "o/rw", "push", ap, false},
		{"write approval, read, mode read", "read", "github.com", "o/rw", "read", ap, true},
		{"unapproved", "write", "github.com", "o/none", "read", ap, false},
		{"same path other host", "write", "gitlab.example.org", "o/rw", "read", ap, false},
		{"host case and .git", "write", "GITHUB.com", "/o/rw.git/", "push", ap, true},
	} {
		if got := mayUseCredential(tc.mode, sh, sr, tc.approved, tc.host, tc.repo, tc.kind); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	if mayUseCredential("write", "", "", ap, "github.com", "o/ro", "read") != true {
		t.Error("approvals work in a session without a repository")
	}
}

func TestStore(t *testing.T) {
	r := newRig(t)
	st := Store{C: r.k, Namespace: ns}
	ctx := context.Background()
	sess := &v1alpha1.IDESession{}
	sess.Name, sess.UID = "s1", "uid-1"
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if l, err := st.List(ctx, "s1"); err != nil || len(l) != 0 {
		t.Fatalf("%v %v", l, err)
	}
	if _, err := st.Put(ctx, sess, "github.com/o/a", "read", 2, now); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.Put(ctx, sess, "github.com/o/a", "read", 2, now.Add(time.Hour)); !a.ApprovedAt.Equal(now) {
		t.Error("an idempotent put changed the time")
	}
	if a, _ := st.Put(ctx, sess, "github.com/o/a", "write", 2, now.Add(time.Hour)); a.Access != "write" {
		t.Error("raise")
	}
	_, _ = st.Put(ctx, sess, "github.com/o/b", "read", 2, now)
	if _, err := st.Put(ctx, sess, "github.com/o/c", "read", 2, now); !errors.Is(err, ErrApprovalLimit) {
		t.Errorf("limit: %v", err)
	}
	var sec corev1.Secret
	if err := r.k.Get(ctx, client.ObjectKey{Namespace: ns, Name: ApprovalSecretName("s1")}, &sec); err != nil {
		t.Fatal(err)
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Kind != "IDESession" || sec.OwnerReferences[0].UID != "uid-1" {
		t.Errorf("the Secret must go with the session: %+v", sec.OwnerReferences)
	}
	if _, has := sec.Labels[v1alpha1.LabelManagedBy]; has {
		t.Error("the operator's Secret cache must not hold this Secret")
	}
	if err := st.Remove(ctx, sess, "github.com/o/a"); err != nil {
		t.Fatal(err)
	}
	if l, _ := st.List(ctx, "s1"); len(l) != 1 || l[0].Repository != "github.com/o/b" {
		t.Errorf("%+v", l)
	}
	if l, _ := st.List(ctx, "other"); len(l) != 0 {
		t.Error("another session sees them")
	}
}

// Several service replicas write at the same time: no approval is lost.
func TestStoreConcurrentPuts(t *testing.T) {
	r := newRig(t)
	st := Store{C: r.k, Namespace: ns}
	sess := &v1alpha1.IDESession{}
	sess.Name = "s1"
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.Put(context.Background(), sess, "github.com/o/r"+string(rune('a'+i)), "read", 20, time.Now()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if l, _ := st.List(context.Background(), "s1"); len(l) != 8 {
		t.Errorf("%d approvals, want 8", len(l))
	}
}

func approvalRig(t *testing.T, mode, connectedOwner string) *rig {
	t.Helper()
	r := newRig(t, session("s", connectedOwner, "inst1", "data-alice", v1alpha1.SessionReady))
	r.h.ApprovalMode = mode
	r.h.Approvals = Store{C: r.k, Namespace: ns}
	return r
}

func TestAuthorizeWithApprovals(t *testing.T) {
	sess := &v1alpha1.IDESession{}
	sess.Name = "s"
	approve := func(r *rig, repo, access string) {
		if _, err := (Store{C: r.k, Namespace: ns}).Put(context.Background(), sess, "github.com/"+repo, access, 20, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(r *rig, repo, kind string) gitapi.AuthorizeResponse {
		_, resp, _ := r.call(instanceTok, gitapi.AuthorizeRequest{Instance: "inst1", Host: "github.com", Repository: repo, Kind: kind})
		return resp
	}
	up := func(resp gitapi.AuthorizeResponse) string {
		if resp.Upstream == nil {
			return ""
		}
		return resp.Upstream.Password
	}

	t.Run("approved for reading", func(t *testing.T) {
		r := approvalRig(t, "write", "alice")
		approve(r, "o/second", "read")
		if resp := ask(r, "o/second", "read"); !resp.Allow || resp.Reason != "ok" || up(resp) != ghToken {
			t.Errorf("read: %+v", resp)
		}
		resp := ask(r, "o/second", "push")
		if resp.Allow || resp.Reason != "push-rule" || !strings.Contains(resp.Message, "push to github.com/o/second") || up(resp) != "" {
			t.Errorf("push with read approval: %+v", resp)
		}
	})
	t.Run("approved for writing", func(t *testing.T) {
		r := approvalRig(t, "write", "alice")
		approve(r, "o/second", "write")
		if resp := ask(r, "o/second", "push"); !resp.Allow || up(resp) != ghToken {
			t.Errorf("%+v", resp)
		}
	})
	t.Run("not approved: read is anonymous and carries the mode, push names the link", func(t *testing.T) {
		r := approvalRig(t, "write", "alice")
		resp := ask(r, "o/second", "read")
		if !resp.Allow || resp.Reason != "anonymous-other" || resp.ApprovalMode != "write" || resp.Session != "s" || up(resp) != "" {
			t.Errorf("read: %+v", resp)
		}
		resp = ask(r, "o/second", "push")
		link := gitapi.ApproveURL("https://ide.example.org", "s", "github.com", "o/second", "write")
		if resp.Allow || !strings.Contains(resp.Message, link) {
			t.Errorf("push: %+v", resp)
		}
	})
	t.Run("mode read: no link for a push", func(t *testing.T) {
		r := approvalRig(t, "read", "alice")
		if resp := ask(r, "o/second", "read"); resp.ApprovalMode != "read" {
			t.Errorf("%+v", resp)
		}
		resp := ask(r, "o/second", "push")
		if resp.Allow || strings.Contains(resp.Message, "approveRepo") || !strings.Contains(resp.Message, "only pushes") {
			t.Errorf("%+v", resp)
		}
	})
	t.Run("mode off: stored approvals do not count and no link", func(t *testing.T) {
		r := approvalRig(t, "off", "alice")
		approve(r, "o/second", "write")
		if resp := ask(r, "o/second", "read"); resp.Reason != "anonymous-other" || resp.ApprovalMode != "" || up(resp) != "" {
			t.Errorf("%+v", resp)
		}
		if resp := ask(r, "o/second", "push"); resp.Allow || strings.Contains(resp.Message, "approveRepo") {
			t.Errorf("%+v", resp)
		}
	})
	t.Run("lowered mode clamps an existing write approval", func(t *testing.T) {
		r := approvalRig(t, "read", "alice")
		approve(r, "o/second", "write")
		if resp := ask(r, "o/second", "push"); resp.Allow {
			t.Errorf("%+v", resp)
		}
		if resp := ask(r, "o/second", "read"); up(resp) != ghToken {
			t.Errorf("%+v", resp)
		}
	})
	t.Run("without an account there is nothing to approve", func(t *testing.T) {
		r := approvalRig(t, "write", "bob")
		resp := ask(r, "o/second", "read")
		if resp.ApprovalMode != "" || resp.Connected {
			t.Errorf("%+v", resp)
		}
		if resp := ask(r, "o/second", "push"); strings.Contains(resp.Message, "approveRepo") {
			t.Errorf("%+v", resp)
		}
	})
	t.Run("an approval of another session does not count", func(t *testing.T) {
		r := approvalRig(t, "write", "alice")
		other := &v1alpha1.IDESession{}
		other.Name = "s-other"
		_, _ = (Store{C: r.k, Namespace: ns}).Put(context.Background(), other, "github.com/o/second", "write", 20, time.Now())
		if resp := ask(r, "o/second", "push"); resp.Allow {
			t.Errorf("%+v", resp)
		}
	})
}

func TestApproveURL(t *testing.T) {
	got := gitapi.ApproveURL("https://ide.example.org/", "s-abc", "gitlab.example.org", "/Group/Sub/proj.git", "read")
	want := "https://ide.example.org/?session=s-abc&approveRepo=https%3A%2F%2Fgitlab.example.org%2FGroup%2FSub%2Fproj&access=read"
	if got != want {
		t.Fatalf("%s", got)
	}
	u, _ := url.Parse(got)
	if u.Query().Get("approveRepo") != "https://gitlab.example.org/Group/Sub/proj" || u.Query().Get("session") != "s-abc" || u.Query().Get("access") != "read" {
		t.Errorf("%v", u.Query())
	}
	if c := config.ApprovalWrite; c != "write" {
		t.Error(c)
	}
}
