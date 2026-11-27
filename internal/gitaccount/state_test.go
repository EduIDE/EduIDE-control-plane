package gitaccount

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func testKeys(t testing.TB) *Keys {
	t.Helper()
	k, err := NewKeys([]byte(strings.Repeat("k", 40)))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestNewKeysNeedsMaterial(t *testing.T) {
	if _, err := NewKeys([]byte("short")); err == nil {
		t.Fatal("want an error for a short key")
	}
}

func TestState(t *testing.T) {
	k := testKeys(t)
	now := time.Unix(1_800_000_000, 0)
	st := State{Owner: "alice", Provider: "github", Nonce: NewNonce(), Expires: now.Add(StateTTL).Unix(), ReturnTo: "/x"}
	good := k.SealState(st)
	other, _ := NewKeys([]byte(strings.Repeat("o", 40)))
	for _, tc := range []struct {
		name string
		raw  string
		now  time.Time
		want error
	}{
		{"valid", good, now, nil},
		{"expired", good, now.Add(StateTTL), ErrStateExpired},
		{"other key", other.SealState(st), now, ErrStateInvalid},
		{"not base64", "a b", now, ErrStateInvalid},
		{"empty", "", now, ErrStateInvalid},
		{"too long", strings.Repeat("a", 3000), now, ErrStateInvalid},
		{"incomplete", k.SealState(State{Owner: "alice", Expires: st.Expires}), now, ErrStateInvalid},
	} {
		got, err := k.OpenState(tc.raw, tc.now)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err %v, want %v", tc.name, err, tc.want)
		}
		if err == nil && got != st {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
}

// Nothing readable about the user reaches the Git host, the browser history or an access log.
func TestStateIsSealed(t *testing.T) {
	k := testKeys(t)
	raw := k.SealState(State{Owner: "alice@example.org", Provider: "github-main", Nonce: "nonce-value", Expires: 1 << 40, ReturnTo: "/start?x=1"})
	dec, _ := base64.RawURLEncoding.DecodeString(raw)
	for _, plain := range []string{"alice", "github-main", "nonce-value", "/start"} {
		if strings.Contains(raw, plain) || bytes.Contains(dec, []byte(plain)) {
			t.Errorf("state shows %q", plain)
		}
	}
	// Two flows of one user differ, and a state is bound to the state purpose: the token store opens nothing of it.
	if raw == k.SealState(State{Owner: "alice@example.org", Provider: "github-main", Nonce: "nonce-value", Expires: 1 << 40, ReturnTo: "/start?x=1"}) {
		t.Error("state is deterministic")
	}
	if _, err := k.open(dec, []byte("git-account-x")); err == nil {
		t.Error("a state opens as a token blob")
	}
}

func TestVerifierIsKeyedAndStable(t *testing.T) {
	k := testKeys(t)
	other, _ := NewKeys([]byte(strings.Repeat("o", 40)))
	again := k.Verifier("n1")
	if k.Verifier("n1") != again || k.Verifier("n1") == k.Verifier("n2") || k.Verifier("n1") == other.Verifier("n1") {
		t.Fatal("verifier must be stable per nonce and key")
	}
	if len(k.Verifier("n1")) < 43 {
		t.Fatal("PKCE verifier needs at least 43 characters")
	}
}

// FuzzOpenState asserts two things for any input: open never panics, and a valid state never opens after a
// change of one byte (the AEAD authenticates the whole blob).
func FuzzOpenState(f *testing.F) {
	k := testKeys(f)
	st := State{Owner: "a", Provider: "p", Nonce: "n", Expires: 1 << 40}
	good := k.SealState(st)
	f.Add(good, 0, byte(1))
	f.Add(good, len(good)-1, byte(0x80))
	f.Add("", 0, byte(1))
	f.Add("...", 1, byte(255))
	f.Fuzz(func(t *testing.T, raw string, at int, flip byte) {
		_, _ = k.OpenState(raw, time.Unix(0, 0)) // must not panic
		if flip == 0 || len(good) == 0 {
			return
		}
		blob, _ := base64.RawURLEncoding.DecodeString(good)
		if at < 0 {
			at = -at
		}
		blob[at%len(blob)] ^= flip
		if got, err := k.OpenState(base64.RawURLEncoding.EncodeToString(blob), time.Unix(0, 0)); err == nil {
			t.Fatalf("a mutated state opened: %+v", got)
		}
	})
}
