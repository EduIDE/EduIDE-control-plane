package gitaccount

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
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
)

const (
	blobKey = "account"
	// lastUsedEvery bounds the writes of the last-used annotation to one per day and account.
	lastUsedEvery = 24 * time.Hour
	// refreshSkew refreshes a token this long before it expires.
	refreshSkew = time.Minute
)

// ErrNotConnected means the user has no account for the provider (or it was revoked at the host).
var ErrNotConnected = errors.New("git account not connected")

// Record is the encrypted content of a token Secret.
type Record struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Login        string    `json:"login"`
	ID           int64     `json:"id"`
	DisplayName  string    `json:"displayName"`
	NoReplyEmail string    `json:"noReplyEmail"`
	ConnectedAt  time.Time `json:"connectedAt"`
}

// Entry is one configured provider with its client.
type Entry struct {
	Config config.GitProvider
	Client Provider
}

// Manager stores and refreshes the accounts of all users. It writes Secrets through c, which must not be a cache:
// the refresh lock relies on reading the latest resourceVersion.
type Manager struct {
	c         client.Client
	ns        string
	keys      *Keys
	providers map[string]Entry
	order     []string
	// now, poll and lockTTL are fields so tests need no real waiting.
	now     func() time.Time
	poll    time.Duration
	lockTTL time.Duration
}

func NewManager(c client.Client, ns string, keys *Keys, entries []Entry) *Manager {
	m := &Manager{c: c, ns: ns, keys: keys, providers: map[string]Entry{}, now: time.Now, poll: 200 * time.Millisecond, lockTTL: 30 * time.Second}
	for _, e := range entries {
		m.providers[e.Config.Name] = e
		m.order = append(m.order, e.Config.Name)
	}
	return m
}

// NewFromConfig reads the key and the client secrets from their files. It returns nil when no provider is configured.
func NewFromConfig(c client.Client, cfg *config.Config) (*Manager, error) {
	g := cfg.GitAccounts
	if len(g.Providers) == 0 {
		return nil, nil
	}
	material, err := os.ReadFile(g.EncryptionKeyFile)
	if err != nil {
		return nil, err
	}
	keys, err := NewKeys([]byte(strings.TrimSpace(string(material))))
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, p := range g.Providers {
		secret, err := os.ReadFile(p.ClientSecretFile)
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Config: p, Client: NewProvider(p, strings.TrimSpace(string(secret)), DefaultEndpoints(p), nil)})
	}
	return NewManager(c, cfg.Namespace, keys, entries), nil
}

func (m *Manager) Keys() *Keys { return m.keys }

// Providers returns the configured providers in config order.
func (m *Manager) Providers() []config.GitProvider {
	out := make([]config.GitProvider, 0, len(m.order))
	for _, n := range m.order {
		out = append(out, m.providers[n].Config)
	}
	return out
}

// Provider returns the entry with this name.
func (m *Manager) Provider(name string) (Entry, bool) {
	e, ok := m.providers[name]
	return e, ok
}

// SecretName is the name of the token Secret of one user and provider.
func SecretName(owner, provider string) string {
	h := sha256.Sum256([]byte(owner + "\n" + provider))
	return "git-account-" + hex.EncodeToString(h[:16])
}

func (m *Manager) key(owner, provider string) types.NamespacedName {
	return types.NamespacedName{Namespace: m.ns, Name: SecretName(owner, provider)}
}

func (m *Manager) decode(sec *corev1.Secret) (Record, error) {
	var rec Record
	plain, err := m.keys.open(sec.Data[blobKey], []byte(sec.Name))
	if err == nil {
		err = json.Unmarshal(plain, &rec)
	}
	return rec, err
}

func (m *Manager) encode(name string, rec Record) []byte {
	b, _ := json.Marshal(rec) // #nosec G117 -- the blob is encrypted right below
	return m.keys.seal(b, []byte(name))
}

func (m *Manager) read(ctx context.Context, owner, provider string) (*corev1.Secret, Record, error) {
	sec := &corev1.Secret{}
	if err := m.c.Get(ctx, m.key(owner, provider), sec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, Record{}, ErrNotConnected
		}
		return nil, Record{}, err
	}
	rec, err := m.decode(sec)
	if err != nil {
		return nil, Record{}, fmt.Errorf("git account %s cannot be decrypted", sec.Name)
	}
	return sec, rec, nil
}

// Save stores the account of the user, replacing an earlier connection.
func (m *Manager) Save(ctx context.Context, owner, provider string, t Token, a Account) error {
	name := SecretName(owner, provider)
	rec := Record{AccessToken: t.Access, RefreshToken: t.Refresh, Expiry: t.Expiry, Login: a.Login, ID: a.ID,
		DisplayName: a.DisplayName, NoReplyEmail: a.NoReplyEmail, ConnectedAt: m.now().UTC()}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: m.ns, Labels: map[string]string{
			// No managed-by label: it selects what the operator caches, and the operator has no use for these Secrets.
			v1alpha1.LabelComponent:   v1alpha1.ComponentGitAccount,
			v1alpha1.LabelGitProvider: provider, v1alpha1.LabelOwnerHash: v1alpha1.OwnerHash(owner)},
			Annotations: map[string]string{v1alpha1.AnnotationGitLastUsed: m.now().UTC().Format(time.RFC3339)}},
		Data: map[string][]byte{blobKey: m.encode(name, rec)},
	}
	err := m.c.Create(ctx, sec)
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &corev1.Secret{}
		if err := m.c.Get(ctx, client.ObjectKeyFromObject(sec), cur); err != nil {
			return err
		}
		cur.Data, cur.Annotations = sec.Data, sec.Annotations
		return m.c.Update(ctx, cur)
	})
}

// Connection reports the login of the connected account, if any. It does not count as a use.
func (m *Manager) Connection(ctx context.Context, owner, provider string) (login string, connected bool) {
	_, rec, err := m.read(ctx, owner, provider)
	return rec.Login, err == nil
}

// Disconnect revokes the account at the host (best effort) and deletes the Secret. A missing account is fine.
func (m *Manager) Disconnect(ctx context.Context, owner, provider string) error {
	e, ok := m.providers[provider]
	if !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}
	_, rec, err := m.read(ctx, owner, provider)
	if errors.Is(err, ErrNotConnected) {
		return nil
	}
	if err == nil { // an account that cannot be decrypted is deleted without a revoke
		m.revoke(ctx, e, rec)
	}
	return client.IgnoreNotFound(m.c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SecretName(owner, provider), Namespace: m.ns}}))
}

func (m *Manager) revoke(ctx context.Context, e Entry, rec Record) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := e.Client.Revoke(ctx, Token{Access: rec.AccessToken, Refresh: rec.RefreshToken}); err != nil {
		slog.WarnContext(ctx, "git account revoke failed", "provider", e.Config.Name, "error", err.Error())
	}
}

// TokenForHost is the seam for the Git proxy: the account of the user on the provider with this host.
// ok is false when no provider has the host. Without a connected account it returns ErrNotConnected.
func (m *Manager) TokenForHost(ctx context.Context, owner, host string) (rec Record, ok bool, err error) {
	for _, n := range m.order {
		if strings.EqualFold(m.providers[n].Config.Host, host) {
			rec, err = m.Token(ctx, owner, n)
			return rec, true, err
		}
	}
	return Record{}, false, nil
}

func (m *Manager) expiring(rec Record) bool {
	return !rec.Expiry.IsZero() && !m.now().Add(refreshSkew).Before(rec.Expiry)
}

// Token returns the account with a usable access token and counts as a use. An expiring token is refreshed first.
// A refresh that fails with invalid_grant means the user revoked the grant at the host: the account is deleted.
func (m *Manager) Token(ctx context.Context, owner, provider string) (Record, error) {
	e, ok := m.providers[provider]
	if !ok {
		return Record{}, ErrNotConnected
	}
	// Two replicas must never present the same GitLab refresh token, so one takes the lock and the others wait.
	giveUp := time.Now().Add(m.lockTTL + 10*time.Second)
	for {
		sec, rec, err := m.read(ctx, owner, provider)
		if err != nil {
			return Record{}, err
		}
		if !m.expiring(rec) || rec.RefreshToken == "" {
			m.touch(ctx, sec)
			return rec, nil
		}
		locked, err := m.lock(ctx, sec)
		if err != nil {
			return Record{}, err
		}
		if locked {
			return m.refresh(ctx, e, sec, rec)
		}
		if time.Now().After(giveUp) {
			return Record{}, errors.New("git account refresh is locked by another replica")
		}
		select {
		case <-ctx.Done():
			return Record{}, ctx.Err()
		case <-time.After(m.poll):
		}
	}
}

// lock marks the Secret as refreshing with an optimistic update: of two writers with the same resourceVersion,
// only one wins. A lock that is older than lockTTL is taken over.
func (m *Manager) lock(ctx context.Context, sec *corev1.Secret) (bool, error) {
	if until, err := time.Parse(time.RFC3339Nano, sec.Annotations[v1alpha1.AnnotationGitRefreshLock]); err == nil && m.now().Before(until) {
		return false, nil
	}
	if sec.Annotations == nil {
		sec.Annotations = map[string]string{}
	}
	sec.Annotations[v1alpha1.AnnotationGitRefreshLock] = m.now().Add(m.lockTTL).UTC().Format(time.RFC3339Nano)
	if err := m.c.Update(ctx, sec); err != nil {
		if apierrors.IsConflict(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (m *Manager) unlock(ctx context.Context, name string) {
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &corev1.Secret{}
		if err := m.c.Get(ctx, types.NamespacedName{Namespace: m.ns, Name: name}, cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		delete(cur.Annotations, v1alpha1.AnnotationGitRefreshLock)
		return m.c.Update(ctx, cur)
	})
}

func (m *Manager) refresh(ctx context.Context, e Entry, sec *corev1.Secret, rec Record) (Record, error) {
	t, err := e.Client.Refresh(ctx, rec.RefreshToken)
	if errors.Is(err, ErrInvalidGrant) {
		slog.InfoContext(ctx, "git account revoked at the host, deleting it", "provider", e.Config.Name)
		if err := client.IgnoreNotFound(m.c.Delete(ctx, sec)); err != nil {
			return Record{}, errors.Join(ErrNotConnected, err)
		}
		return Record{}, ErrNotConnected
	}
	if err != nil {
		m.unlock(ctx, sec.Name)
		return Record{}, fmt.Errorf("refresh for provider %s: %w", e.Config.Name, err)
	}
	rec.AccessToken, rec.Expiry = t.Access, t.Expiry
	if t.Refresh != "" {
		rec.RefreshToken = t.Refresh
	}
	// The provider rotated the refresh token: the new one must reach the Secret, so a conflict only retries.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &corev1.Secret{}
		if err := m.c.Get(ctx, client.ObjectKeyFromObject(sec), cur); err != nil {
			return err
		}
		cur.Data = map[string][]byte{blobKey: m.encode(sec.Name, rec)}
		delete(cur.Annotations, v1alpha1.AnnotationGitRefreshLock)
		cur.Annotations[v1alpha1.AnnotationGitLastUsed] = m.now().UTC().Format(time.RFC3339) // a refresh is a use
		return m.c.Update(ctx, cur)
	})
	if err != nil {
		return Record{}, fmt.Errorf("store refreshed token of provider %s: %w", e.Config.Name, err)
	}
	return rec, nil
}

// touch records the use, at most once per lastUsedEvery.
func (m *Manager) touch(ctx context.Context, sec *corev1.Secret) {
	if at, err := time.Parse(time.RFC3339, sec.Annotations[v1alpha1.AnnotationGitLastUsed]); err == nil && m.now().Sub(at) < lastUsedEvery {
		return
	}
	patch := client.MergeFrom(sec.DeepCopy())
	if sec.Annotations == nil {
		sec.Annotations = map[string]string{}
	}
	sec.Annotations[v1alpha1.AnnotationGitLastUsed] = m.now().UTC().Format(time.RFC3339)
	if err := m.c.Patch(ctx, sec, patch); err != nil {
		slog.DebugContext(ctx, "git account last-used not written", "error", err.Error())
	}
}

// Sweep revokes and deletes the accounts nobody used for the inactivityExpiry of their provider. It is idempotent
// and safe to run on every replica: a Secret that changed since the list is left for the next run.
func (m *Manager) Sweep(ctx context.Context) {
	var l corev1.SecretList
	err := m.c.List(ctx, &l, client.InNamespace(m.ns), client.MatchingLabels{v1alpha1.LabelComponent: v1alpha1.ComponentGitAccount})
	if err != nil {
		slog.WarnContext(ctx, "git account sweep failed", "error", err.Error())
		return
	}
	for i := range l.Items {
		sec := &l.Items[i]
		e, ok := m.providers[sec.Labels[v1alpha1.LabelGitProvider]]
		if !ok {
			continue
		}
		last := sec.CreationTimestamp.Time
		if at, err := time.Parse(time.RFC3339, sec.Annotations[v1alpha1.AnnotationGitLastUsed]); err == nil {
			last = at
		}
		if m.now().Sub(last) < e.Config.InactivityExpiry.Duration {
			continue
		}
		if rec, err := m.decode(sec); err == nil {
			m.revoke(ctx, e, rec)
		}
		rv := sec.ResourceVersion
		if err := m.c.Delete(ctx, sec, client.Preconditions{ResourceVersion: &rv}); client.IgnoreNotFound(err) != nil {
			slog.WarnContext(ctx, "git account expiry delete failed", "provider", e.Config.Name, "error", err.Error())
			continue
		}
		slog.InfoContext(ctx, "git account expired", "provider", e.Config.Name)
	}
}

// RunSweep sweeps every hour, with jitter so the replicas do not run in step, until ctx ends.
func (m *Manager) RunSweep(ctx context.Context) {
	wait := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	// #nosec G404 -- jitter, not a secret
	jitter := func() time.Duration { return time.Duration(rand.Int64N(int64(10 * time.Minute))) }
	for wait(jitter()) {
		m.Sweep(ctx)
		if !wait(time.Hour) {
			return
		}
	}
}
