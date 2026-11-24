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
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
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
	// refreshTimeout bounds the provider call and the store of a refresh together.
	refreshTimeout = 45 * time.Second
)

// ErrNotConnected means the user has no account for the provider (or it was revoked at the host).
var ErrNotConnected = errors.New("git account not connected")

// ErrBusy means a refresh of the account is running: a reconnect now would be overwritten by it. Retry shortly.
var ErrBusy = errors.New("git account is being refreshed")

// errRaced means the account changed while this replica refreshed it, so the caller reads it again.
var errRaced = errors.New("git account changed during refresh")

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
	// HTTP reaches the provider host (private CA, no redirects) for calls other than OAuth, such as the preflight.
	// Nil means a default client.
	HTTP *http.Client
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
	// Only a trailing newline goes: the key is binary material, and trimming more could shorten it.
	keys, err := NewKeys([]byte(strings.TrimSuffix(strings.TrimSuffix(string(material), "\n"), "\r")))
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, p := range g.Providers {
		secret, err := os.ReadFile(p.ClientSecretFile)
		if err != nil {
			return nil, err
		}
		hc, err := HTTPClient(p.CAFile, 15*time.Second)
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Config: p, HTTP: hc, Client: NewProvider(p, strings.TrimSpace(string(secret)), DefaultEndpoints(p), hc)})
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

// Save stores the account of the user. A reconnect replaces the earlier record and revokes the old token at the
// host after the update succeeded. It fails with ErrBusy while a refresh holds the lock: the refresh would store
// the old record over the new one.
func (m *Manager) Save(ctx context.Context, owner, provider string, t Token, a Account) error {
	e, ok := m.providers[provider]
	if !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}
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
	var old *Record
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &corev1.Secret{}
		if err := m.c.Get(ctx, client.ObjectKeyFromObject(sec), cur); err != nil {
			return err
		}
		if until, err := time.Parse(time.RFC3339Nano, cur.Annotations[v1alpha1.AnnotationGitRefreshLock]); err == nil && m.now().Before(until) {
			return ErrBusy
		}
		old = nil
		if r, err := m.decode(cur); err == nil { // an unreadable record (key changed) has nothing to revoke
			old = &r
		}
		cur.Data, cur.Annotations = sec.Data, sec.Annotations
		return m.c.Update(ctx, cur)
	})
	if err != nil {
		return err
	}
	if old != nil && old.AccessToken != rec.AccessToken {
		m.revoke(ctx, e, *old)
	}
	return nil
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
	// Delete first, with the resourceVersion that was read, and revoke only after the delete succeeded (the order of
	// the sweep). A reconnect or a refresh in between changes the version: the delete fails, and the next round
	// reads the new record. So a new account is never deleted unrevoked and a revoked token never stays valid.
	var rec Record
	var readable, deleted bool
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		sec := &corev1.Secret{}
		if err := m.c.Get(ctx, m.key(owner, provider), sec); err != nil {
			return client.IgnoreNotFound(err)
		}
		r, derr := m.decode(sec) // an account that cannot be decrypted is deleted without a revoke
		rv := sec.ResourceVersion
		if err := m.c.Delete(ctx, sec, client.Preconditions{ResourceVersion: &rv}); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		rec, readable, deleted = r, derr == nil, true
		return nil
	})
	if err != nil {
		return err
	}
	if deleted && readable {
		m.revoke(ctx, e, rec)
	}
	return nil
}

func (m *Manager) revoke(ctx context.Context, e Entry, rec Record) {
	// A revoke that the request's end cuts off would leave a live token behind.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
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

// Identity is what the commit identity of a launch needs from an account. It holds no token.
type Identity struct {
	Login, DisplayName, NoReplyEmail string
}

// IdentityForHost returns the identity of the user's account on the provider with this host. It neither refreshes
// the token nor counts as a use. ok is false without a provider for the host or without a readable account.
func (m *Manager) IdentityForHost(ctx context.Context, owner, host string) (Identity, bool) {
	for _, n := range m.order {
		if strings.EqualFold(m.providers[n].Config.Host, host) {
			_, rec, err := m.read(ctx, owner, n)
			return Identity{Login: rec.Login, DisplayName: rec.DisplayName, NoReplyEmail: rec.NoReplyEmail}, err == nil
		}
	}
	return Identity{}, false
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
			rec, err := m.refresh(ctx, e, sec, rec)
			if errors.Is(err, errRaced) && time.Now().Before(giveUp) {
				continue
			}
			return rec, err
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

// unlock removes our own lock. A lock another replica took over stays.
func (m *Manager) unlock(ctx context.Context, name, mine string) {
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &corev1.Secret{}
		if err := m.c.Get(ctx, types.NamespacedName{Namespace: m.ns, Name: name}, cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if cur.Annotations[v1alpha1.AnnotationGitRefreshLock] != mine {
			return nil
		}
		delete(cur.Annotations, v1alpha1.AnnotationGitRefreshLock)
		return m.c.Update(ctx, cur)
	})
}

// refresh runs under our lock. The provider rotates the refresh token on use, so from the call on the new token
// must reach the Secret even when the request ends: both steps run detached from ctx, with an own deadline.
func (m *Manager) refresh(ctx context.Context, e Entry, sec *corev1.Secret, rec Record) (Record, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	mine := sec.Annotations[v1alpha1.AnnotationGitRefreshLock]
	t, err := e.Client.Refresh(ctx, rec.RefreshToken)
	if errors.Is(err, ErrInvalidGrant) {
		return Record{}, m.revokedAtHost(ctx, e, sec.Name, rec.RefreshToken, mine)
	}
	if err != nil {
		m.unlock(ctx, sec.Name, mine)
		return Record{}, fmt.Errorf("refresh for provider %s: %w", e.Config.Name, err)
	}
	rec.AccessToken, rec.Expiry = t.Access, t.Expiry
	if t.Refresh != "" {
		rec.RefreshToken = t.Refresh
	}
	if err := m.store(ctx, e, sec.Name, rec, mine); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// store writes the refreshed record. It retries every error: losing the rotated refresh token costs the account.
// It stores even when our lock was taken over meanwhile, because the provider issued this token last, and it
// leaves a lock that is not ours alone.
func (m *Manager) store(ctx context.Context, e Entry, name string, rec Record, mine string) error {
	backoff := wait.Backoff{Steps: 6, Duration: 200 * time.Millisecond, Factor: 2, Jitter: 0.1}
	err := retry.OnError(backoff, func(err error) bool { return !apierrors.IsNotFound(err) }, func() error {
		cur := &corev1.Secret{}
		if err := m.c.Get(ctx, types.NamespacedName{Namespace: m.ns, Name: name}, cur); err != nil {
			return err
		}
		cur.Data = map[string][]byte{blobKey: m.encode(name, rec)}
		if cur.Annotations[v1alpha1.AnnotationGitRefreshLock] == mine {
			delete(cur.Annotations, v1alpha1.AnnotationGitRefreshLock)
		}
		if cur.Annotations == nil {
			cur.Annotations = map[string]string{}
		}
		cur.Annotations[v1alpha1.AnnotationGitLastUsed] = m.now().UTC().Format(time.RFC3339) // a refresh is a use
		return m.c.Update(ctx, cur)
	})
	if apierrors.IsNotFound(err) { // disconnected meanwhile: do not leave the fresh token alive at the host
		m.revoke(ctx, e, rec)
		return ErrNotConnected
	}
	if err != nil {
		slog.ErrorContext(ctx, "git account: the rotated refresh token could not be stored, the user must connect again", "provider", e.Config.Name, "error", err.Error())
		return fmt.Errorf("store refreshed token of provider %s: %w", e.Config.Name, err)
	}
	return nil
}

// revokedAtHost handles invalid_grant on a refresh. It deletes the account only if the Secret still holds the
// refresh token we sent and our lock: otherwise another replica or a reconnect changed it, the grant may be
// alive, and the caller reads the account again.
func (m *Manager) revokedAtHost(ctx context.Context, e Entry, name, sent, mine string) error {
	cur := &corev1.Secret{}
	if err := m.c.Get(ctx, types.NamespacedName{Namespace: m.ns, Name: name}, cur); err != nil {
		if apierrors.IsNotFound(err) {
			return ErrNotConnected
		}
		return err
	}
	rec, err := m.decode(cur)
	if err != nil {
		return fmt.Errorf("git account %s cannot be decrypted", name)
	}
	if rec.RefreshToken != sent || cur.Annotations[v1alpha1.AnnotationGitRefreshLock] != mine {
		return errRaced
	}
	rv := cur.ResourceVersion
	if err := m.c.Delete(ctx, cur, client.Preconditions{ResourceVersion: &rv}); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			return ErrNotConnected
		case apierrors.IsConflict(err):
			return errRaced
		}
		return errors.Join(ErrNotConnected, err)
	}
	slog.InfoContext(ctx, "git account revoked at the host, deleted it", "provider", e.Config.Name)
	return ErrNotConnected
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
		// Delete first, with the precondition: a use or refresh since the list leaves the account alone, and
		// nothing is revoked that someone still holds. The revoke follows only a successful delete.
		rec, derr := m.decode(sec)
		rv := sec.ResourceVersion
		if err := m.c.Delete(ctx, sec, client.Preconditions{ResourceVersion: &rv}); err != nil {
			if !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
				slog.WarnContext(ctx, "git account expiry delete failed", "provider", e.Config.Name, "error", err.Error())
			}
			continue
		}
		if derr == nil {
			m.revoke(ctx, e, rec)
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
