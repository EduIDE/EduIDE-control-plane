package gitaccount

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

const ns = "eduide"

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	m    *Manager
	k    client.Client
	host *fakeHost
	clk  *clock
}

func newRig(t *testing.T) *rig {
	t.Helper()
	sch := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(sch)
	k := fake.NewClientBuilder().WithScheme(sch).Build()
	h := newFakeHost(t)
	cfg := func(name, typ, host string) Entry {
		c := config.GitProvider{Name: name, Type: typ, Host: host, ClientID: "cid", InactivityExpiry: &metav1.Duration{Duration: 100 * time.Hour}}
		return Entry{Config: c, Client: h.provider(typ, host)}
	}
	m := NewManager(k, ns, testKeys(t), []Entry{cfg("gl", "gitlab", "gitlab.example.org"), cfg("gh", "github", "github.com")})
	clk := &clock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	m.now, m.poll, m.lockTTL = clk.now, time.Millisecond, 5*time.Second
	return &rig{m: m, k: k, host: h, clk: clk}
}

func (r *rig) save(t *testing.T, owner, provider string, exp time.Duration) {
	t.Helper()
	tok := Token{Access: tokAccess, Refresh: tokRefresh}
	if exp != 0 {
		tok.Expiry = r.clk.now().Add(exp)
	}
	if err := r.m.Save(context.Background(), owner, provider, tok, Account{ID: 7, Login: "gitty", DisplayName: "Gitty", NoReplyEmail: "7-gitty@users.noreply.gitlab.example.org"}); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) secret(t *testing.T, owner, provider string) *corev1.Secret {
	t.Helper()
	s := &corev1.Secret{}
	if err := r.k.Get(context.Background(), r.m.key(owner, provider), s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSecretName(t *testing.T) {
	a, b := SecretName("alice", "gl"), SecretName("alice", "gh")
	if a == b || a == SecretName("bob", "gl") || !strings.HasPrefix(a, "git-account-") || len(a) != len("git-account-")+32 {
		t.Fatalf("%s %s", a, b)
	}
	// "ab"+"c" and "a"+"bc" must not collide.
	if SecretName("ab", "c") == SecretName("a", "bc") {
		t.Fatal("owner and provider are not separated")
	}
}

func TestSaveAndRead(t *testing.T) {
	r := newRig(t)
	r.save(t, "alice", "gl", time.Hour)
	sec := r.secret(t, "alice", "gl")
	if _, has := sec.Labels[v1alpha1.LabelManagedBy]; has {
		t.Error("the operator's Secret cache selects on managed-by and must not hold the token Secrets")
	}
	if sec.Labels[v1alpha1.LabelGitProvider] != "gl" || sec.Labels[v1alpha1.LabelComponent] != v1alpha1.ComponentGitAccount {
		t.Errorf("labels %v", sec.Labels)
	}
	if bytes.Contains(sec.Data[blobKey], []byte(tokAccess)) || bytes.Contains(sec.Data[blobKey], []byte("gitty")) {
		t.Error("the blob is not encrypted")
	}
	if login, ok := r.m.Connection(context.Background(), "alice", "gl"); !ok || login != "gitty" {
		t.Errorf("%q %v", login, ok)
	}
	if _, ok := r.m.Connection(context.Background(), "bob", "gl"); ok {
		t.Error("bob is connected")
	}
	// Reconnect replaces the record.
	r.save(t, "alice", "gl", time.Hour)
}

func TestBlobIsBoundToTheSecretName(t *testing.T) {
	r := newRig(t)
	r.save(t, "alice", "gl", time.Hour)
	blob := r.secret(t, "alice", "gl").Data[blobKey]
	// Bob's Secret carries Alice's blob.
	bob := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SecretName("bob", "gl"), Namespace: ns}, Data: map[string][]byte{blobKey: blob}}
	if err := r.k.Create(context.Background(), bob); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Token(context.Background(), "bob", "gl"); err == nil {
		t.Fatal("a moved blob was accepted")
	}
	// Another key cannot read it either.
	other, _ := NewKeys([]byte(strings.Repeat("o", 40)))
	if _, err := other.open(blob, []byte(SecretName("alice", "gl"))); err == nil {
		t.Fatal("a different key opened the blob")
	}
}

func TestTokenUsesFreshTokenAndCountsOncePerDay(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.save(t, "alice", "gl", 100*time.Hour)
	for range 2 {
		rec, err := r.m.Token(ctx, "alice", "gl")
		if err != nil || rec.AccessToken != tokAccess {
			t.Fatalf("%+v %v", rec, err)
		}
	}
	if r.host.refreshCalls.Load() != 0 {
		t.Fatal("refreshed a fresh token")
	}
	first := r.secret(t, "alice", "gl").Annotations[v1alpha1.AnnotationGitLastUsed]
	r.clk.add(time.Hour)
	_, _ = r.m.Token(ctx, "alice", "gl")
	if got := r.secret(t, "alice", "gl").Annotations[v1alpha1.AnnotationGitLastUsed]; got != first {
		t.Errorf("last-used written again within 24h: %s -> %s", first, got)
	}
	r.clk.add(24 * time.Hour)
	_, _ = r.m.Token(ctx, "alice", "gl")
	if got := r.secret(t, "alice", "gl").Annotations[v1alpha1.AnnotationGitLastUsed]; got == first {
		t.Error("last-used not written after 24h")
	}
	if _, err := r.m.Token(ctx, "bob", "gl"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("bob: %v", err)
	}
	if _, err := r.m.Token(ctx, "alice", "nope"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("unknown provider: %v", err)
	}
}

func TestGitHubTokenNeverRefreshes(t *testing.T) {
	r := newRig(t)
	r.save(t, "alice", "gh", 0)
	r.clk.add(10000 * time.Hour)
	rec, err := r.m.Token(context.Background(), "alice", "gh")
	if err != nil || rec.AccessToken != tokAccess {
		t.Fatalf("%+v %v", rec, err)
	}
}

func TestRefreshRotatesAndStores(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.save(t, "alice", "gl", 30*time.Second) // inside the refresh skew
	rec, err := r.m.Token(ctx, "alice", "gl")
	if err != nil || rec.AccessToken != "access-1" || rec.RefreshToken != "refresh-1" {
		t.Fatalf("%+v %v", rec, err)
	}
	sec := r.secret(t, "alice", "gl")
	if _, locked := sec.Annotations[v1alpha1.AnnotationGitRefreshLock]; locked {
		t.Error("lock left behind")
	}
	again, err := r.m.Token(ctx, "alice", "gl")
	if err != nil || again.AccessToken != "access-1" || r.host.refreshCalls.Load() != 1 {
		t.Errorf("%+v %v calls=%d", again, err, r.host.refreshCalls.Load())
	}
}

func TestConcurrentRefreshCallsTheProviderOnce(t *testing.T) {
	r := newRig(t)
	r.save(t, "alice", "gl", time.Second)
	var wg sync.WaitGroup
	got := make([]Record, 8)
	errs := make([]error, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = r.m.Token(context.Background(), "alice", "gl")
		}()
	}
	wg.Wait()
	if n := r.host.refreshCalls.Load(); n != 1 {
		t.Fatalf("provider called %d times, want 1", n)
	}
	for i := range got {
		if errs[i] != nil || got[i].RefreshToken != "refresh-1" {
			t.Errorf("caller %d: %+v %v", i, got[i], errs[i])
		}
	}
}

func TestStaleLockIsTakenOver(t *testing.T) {
	r := newRig(t)
	r.save(t, "alice", "gl", time.Second)
	sec := r.secret(t, "alice", "gl")
	sec.Annotations[v1alpha1.AnnotationGitRefreshLock] = r.clk.now().Add(-time.Minute).Format(time.RFC3339Nano)
	if err := r.k.Update(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	if rec, err := r.m.Token(context.Background(), "alice", "gl"); err != nil || rec.RefreshToken != "refresh-1" {
		t.Fatalf("%+v %v", rec, err)
	}
}

func TestRefreshRevokedAtTheHostDeletesTheAccount(t *testing.T) {
	r := newRig(t)
	r.host.refreshAnswer = func(int32) (int, string) { return 400, `{"error":"invalid_grant"}` }
	r.save(t, "alice", "gl", time.Second)
	if _, err := r.m.Token(context.Background(), "alice", "gl"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("%v", err)
	}
	if err := r.k.Get(context.Background(), r.m.key("alice", "gl"), &corev1.Secret{}); err == nil {
		t.Fatal("the Secret is still there")
	}
}

func TestRefreshFailureKeepsTheAccountAndReleasesTheLock(t *testing.T) {
	r := newRig(t)
	r.host.refreshAnswer = func(int32) (int, string) { return 503, `` }
	r.save(t, "alice", "gl", time.Second)
	if _, err := r.m.Token(context.Background(), "alice", "gl"); err == nil || errors.Is(err, ErrNotConnected) {
		t.Fatalf("%v", err)
	}
	if _, locked := r.secret(t, "alice", "gl").Annotations[v1alpha1.AnnotationGitRefreshLock]; locked {
		t.Error("lock left behind")
	}
}

func TestTokenForHost(t *testing.T) {
	r := newRig(t)
	r.save(t, "alice", "gl", time.Hour)
	ctx := context.Background()
	if rec, ok, err := r.m.TokenForHost(ctx, "alice", "GitLab.Example.org"); !ok || err != nil || rec.Login != "gitty" {
		t.Errorf("%+v %v %v", rec, ok, err)
	}
	if _, ok, _ := r.m.TokenForHost(ctx, "alice", "other.example.org"); ok {
		t.Error("unknown host matched")
	}
	if _, ok, err := r.m.TokenForHost(ctx, "bob", "github.com"); !ok || !errors.Is(err, ErrNotConnected) {
		t.Errorf("%v %v", ok, err)
	}
}

func TestDisconnect(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.save(t, "alice", "gl", time.Hour)
	if err := r.m.Disconnect(ctx, "alice", "gl"); err != nil {
		t.Fatal(err)
	}
	if r.host.revokeCalls.Load() != 1 {
		t.Error("not revoked at the host")
	}
	if err := r.k.Get(ctx, r.m.key("alice", "gl"), &corev1.Secret{}); err == nil {
		t.Error("Secret not deleted")
	}
	if err := r.m.Disconnect(ctx, "alice", "gl"); err != nil {
		t.Errorf("second disconnect: %v", err)
	}
	// A failed revoke still deletes.
	r.save(t, "alice", "gl", time.Hour)
	r.host.revokeStatus = 500
	if err := r.m.Disconnect(ctx, "alice", "gl"); err != nil {
		t.Fatal(err)
	}
	if err := r.k.Get(ctx, r.m.key("alice", "gl"), &corev1.Secret{}); err == nil {
		t.Error("Secret not deleted after a failed revoke")
	}
	if err := r.m.Disconnect(ctx, "alice", "nope"); err == nil {
		t.Error("unknown provider accepted")
	}
}

func TestSweep(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.save(t, "old", "gl", time.Hour)
	r.save(t, "young", "gl", time.Hour)
	r.clk.add(99 * time.Hour)
	if _, err := r.m.Token(ctx, "young", "gl"); err != nil { // a use 99h after connecting
		t.Fatal(err)
	}
	r.clk.add(2 * time.Hour) // old: 101h unused, young: 2h
	r.m.Sweep(ctx)
	if err := r.k.Get(ctx, r.m.key("old", "gl"), &corev1.Secret{}); err == nil {
		t.Error("the unused account survived")
	}
	if err := r.k.Get(ctx, r.m.key("young", "gl"), &corev1.Secret{}); err != nil {
		t.Errorf("the used account was deleted: %v", err)
	}
	if r.host.revokeCalls.Load() != 1 {
		t.Errorf("revokes: %d", r.host.revokeCalls.Load())
	}
	r.m.Sweep(ctx) // idempotent
}

func TestSweepIgnoresOtherSecrets(t *testing.T) {
	r := newRig(t)
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s-data", Namespace: ns, Labels: map[string]string{v1alpha1.LabelManagedBy: "eduide"}}}
	_ = r.k.Create(context.Background(), other)
	r.clk.add(10000 * time.Hour)
	r.m.Sweep(context.Background())
	if err := r.k.Get(context.Background(), client.ObjectKeyFromObject(other), &corev1.Secret{}); err != nil {
		t.Fatal(err)
	}
}

func TestNoTokenInLogs(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)
	r := newRig(t)
	ctx := context.Background()
	r.save(t, "alice", "gl", time.Second)
	_, _ = r.m.Token(ctx, "alice", "gl")
	r.host.refreshAnswer = func(int32) (int, string) { return 400, `{"error":"invalid_grant"}` }
	r.clk.add(3 * time.Hour)
	_, _ = r.m.Token(ctx, "alice", "gl")
	r.save(t, "bob", "gl", time.Hour)
	r.host.revokeStatus = 500
	_ = r.m.Disconnect(ctx, "bob", "gl")
	for _, s := range []string{tokAccess, tokRefresh, "access-1", "refresh-1", clientSec} {
		if strings.Contains(buf.String(), s) {
			t.Errorf("log contains %q:\n%s", s, buf.String())
		}
	}
	if buf.Len() == 0 {
		t.Error("nothing was logged, the check proves nothing")
	}
}
