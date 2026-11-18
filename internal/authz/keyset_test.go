package authz

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

func testVerifier(t *testing.T, iss *issuer, interval time.Duration) *oidc.IDTokenVerifier {
	t.Helper()
	p, err := oidc.NewProvider(context.Background(), iss.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verifier(p, clientID, http.DefaultClient, interval)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestKeySetUnknownKeyStorm(t *testing.T) {
	iss := newIssuer(t)
	v := testVerifier(t, iss, time.Hour)
	ctx := context.Background()
	if _, err := v.Verify(ctx, iss.token(t, nil, "", "", "alice", 0)); err != nil {
		t.Fatal(err)
	}
	forger, _ := rsa.GenerateKey(rand.Reader, 2048)
	forged := iss.token(t, forger, "", "", "alice", 0)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if _, err := v.Verify(ctx, forged); err == nil {
				t.Error("forged token verified")
			}
		})
	}
	wg.Wait()
	for range 10 {
		if _, err := v.Verify(ctx, iss.token(t, nil, "", "", "alice", 0)); err != nil {
			t.Fatal(err)
		}
	}
	if n := iss.hits.Load(); n != 1 {
		t.Fatalf("JWKS fetched %d times, want 1", n)
	}
}

func TestKeySetRotation(t *testing.T) {
	iss := newIssuer(t)
	v := testVerifier(t, iss, 2*time.Second) // wide enough that a loaded CI runner stays inside the window
	ctx := context.Background()
	if _, err := v.Verify(ctx, iss.token(t, nil, "", "", "alice", 0)); err != nil {
		t.Fatal(err)
	}
	iss.rotate(t)
	rotated := iss.token(t, nil, "", "", "alice", 0)
	if _, err := v.Verify(ctx, rotated); err == nil {
		t.Fatal("rotated key accepted inside the window, want one fetch per window")
	}
	time.Sleep(2100 * time.Millisecond)
	if _, err := v.Verify(ctx, rotated); err != nil {
		t.Fatalf("rotated key after the window: %v", err)
	}
	if n := iss.hits.Load(); n != 2 {
		t.Fatalf("JWKS fetched %d times, want 2", n)
	}
}

func TestKeySetFetchFailureStaysClosed(t *testing.T) {
	ks := &KeySet{URL: "http://127.0.0.1:1/jwks", Client: http.DefaultClient, Interval: time.Hour}
	if _, err := ks.VerifySignature(context.Background(), "a.b.c"); err == nil {
		t.Fatal("want error without keys")
	}
}
