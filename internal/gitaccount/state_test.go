package gitaccount

import (
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
	good := k.Sign(st)
	other, _ := NewKeys([]byte(strings.Repeat("o", 40)))
	p, sig, _ := strings.Cut(good, ".")
	flip := func(s string) string {
		return s[:len(s)-1] + map[bool]string{true: "B", false: "A"}[s[len(s)-1] == 'A']
	}
	for _, tc := range []struct {
		name string
		raw  string
		now  time.Time
		want error
	}{
		{"valid", good, now, nil},
		{"expired", good, now.Add(StateTTL), ErrStateExpired},
		{"tampered payload", flip(p) + "." + sig, now, ErrStateInvalid},
		{"tampered signature", p + "." + flip(sig), now, ErrStateInvalid},
		{"other key", other.Sign(st), now, ErrStateInvalid},
		{"no dot", "abc", now, ErrStateInvalid},
		{"empty", "", now, ErrStateInvalid},
		{"too long", strings.Repeat("a", 3000) + "." + sig, now, ErrStateInvalid},
	} {
		got, err := k.ParseState(tc.raw, tc.now)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err %v, want %v", tc.name, err, tc.want)
		}
		if err == nil && got != st {
			t.Errorf("%s: %+v", tc.name, got)
		}
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

func FuzzParseState(f *testing.F) {
	k := testKeys(f)
	f.Add(k.Sign(State{Owner: "a", Provider: "p", Nonce: "n", Expires: 1 << 40}))
	f.Add("")
	f.Add("a.b")
	f.Add("...")
	f.Fuzz(func(t *testing.T, raw string) {
		if st, err := k.ParseState(raw, time.Unix(0, 0)); err == nil && (st.Owner == "" || st.Provider == "") {
			t.Fatalf("accepted an incomplete state %+v from %q", st, raw)
		}
	})
}
