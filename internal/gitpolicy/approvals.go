package gitpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

// ErrApprovalLimit means the session already has the most approvals the config allows.
var ErrApprovalLimit = errors.New("too many approved repositories")

const approvalKey = "approvals"

// Approval is one repository a user approved for the credential of one session.
type Approval struct {
	// Repository is "host/path", normalized like gitapi.NormalizeRepo.
	Repository string    `json:"repository"`
	Access     string    `json:"access"` // "read" or "write"
	ApprovedAt time.Time `json:"approvedAt"`
}

// Store keeps the approvals of a session in a Secret owned by the session, so they go with it (ADR 0012: user
// data lives in Secrets). The Secret holds no credential. C must read the API server, not a cache.
type Store struct {
	C         client.Client
	Namespace string
}

// ApprovalSecretName is the Secret of the approvals of a session.
func ApprovalSecretName(session string) string { return session + "-git-repos" }

func (s Store) key(session string) types.NamespacedName {
	return types.NamespacedName{Namespace: s.Namespace, Name: ApprovalSecretName(session)}
}

func decode(sec *corev1.Secret) []Approval {
	var l []Approval
	_ = json.Unmarshal(sec.Data[approvalKey], &l)
	return l
}

// List returns the approvals of the session, empty when there are none.
func (s Store) List(ctx context.Context, session string) ([]Approval, error) {
	sec := &corev1.Secret{}
	if err := s.C.Get(ctx, s.key(session), sec); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return decode(sec), nil
}

// change reads the Secret, lets f edit the list and writes it back. Several service replicas may do this at the
// same time: an update conflict or a lost create race starts over with a fresh read.
func (s Store) change(ctx context.Context, sess *v1alpha1.IDESession, f func([]Approval) ([]Approval, error)) error {
	retriable := func(err error) bool { return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) }
	return retry.OnError(retry.DefaultRetry, retriable, func() error {
		sec := &corev1.Secret{}
		err := s.C.Get(ctx, s.key(sess.Name), sec)
		exists := err == nil
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		l, err := f(decode(sec))
		if err != nil {
			return err
		}
		b, _ := json.Marshal(l)
		if exists {
			sec.Data = map[string][]byte{approvalKey: b}
			return s.C.Update(ctx, sec)
		}
		return s.C.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: ApprovalSecretName(sess.Name), Namespace: s.Namespace,
				Labels:          map[string]string{v1alpha1.LabelComponent: v1alpha1.ComponentGitApprovals, v1alpha1.LabelSession: sess.Name},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "IDESession", Name: sess.Name, UID: sess.UID}}},
			Data: map[string][]byte{approvalKey: b},
		})
	})
}

// Put approves repo with access, or changes the access of an earlier approval. It is idempotent. max bounds the
// number of approvals of the session.
func (s Store) Put(ctx context.Context, sess *v1alpha1.IDESession, repo, access string, max int, now time.Time) (Approval, error) {
	var out Approval
	err := s.change(ctx, sess, func(l []Approval) ([]Approval, error) {
		for i := range l {
			if l[i].Repository == repo {
				if l[i].Access != access {
					l[i].Access, l[i].ApprovedAt = access, now.UTC()
				}
				out = l[i]
				return l, nil
			}
		}
		if len(l) >= max {
			return nil, ErrApprovalLimit
		}
		out = Approval{Repository: repo, Access: access, ApprovedAt: now.UTC()}
		return append(l, out), nil
	})
	return out, err
}

// Remove drops the approval of repo. A repository without approval is fine.
func (s Store) Remove(ctx context.Context, sess *v1alpha1.IDESession, repo string) error {
	return s.change(ctx, sess, func(l []Approval) ([]Approval, error) {
		out := l[:0:0]
		for _, a := range l {
			if a.Repository != repo {
				out = append(out, a)
			}
		}
		return out, nil
	})
}

// Key is the "host/path" form of a repository that Approval.Repository uses.
func Key(host, repo string) string { return strings.ToLower(host) + "/" + gitapi.NormalizeRepo(repo) }

// credentialAccess is the access the credential has on a repository for a session under the approval mode:
// "write" (read and push), "read" or "" (none). The session repository always has write. An approval counts only as
// far as the mode allows, so lowering the mode takes effect on approvals that already exist.
func credentialAccess(mode, sessionHost, sessionRepo string, approved []Approval, host, repo string) string {
	if sessionRepo != "" && gitapi.SameRepository(host, repo, sessionHost, sessionRepo) {
		return config.ApprovalWrite
	}
	key := Key(host, repo)
	for _, a := range approved {
		if a.Repository != key {
			continue
		}
		switch {
		case mode == config.ApprovalWrite && a.Access == config.ApprovalWrite:
			return config.ApprovalWrite
		case mode == config.ApprovalRead || mode == config.ApprovalWrite:
			return config.ApprovalRead
		}
	}
	return ""
}
