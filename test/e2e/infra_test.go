//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The Infra tests prove the cluster, TLS, DNS, Keycloak and the gateway OIDC filter without the control plane.

func TestInfraKeycloakThroughGateway(t *testing.T) {
	resp, err := testHTTPClient(t, false, true).Get(issuer + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var doc struct {
		Issuer string `json:"issuer"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	if doc.Issuer != issuer {
		t.Fatalf("issuer = %q, want %q", doc.Issuer, issuer)
	}
}

func TestInfraTokens(t *testing.T) {
	for _, u := range []user{alice, bob} {
		c := claims(t, token(t, u))
		if c["preferred_username"] != u.name || c["iss"] != issuer {
			t.Fatalf("claims of %s: %v", u.name, c)
		}
	}
}

// The smoke route has the OIDC filter of Envoy Gateway and the fake IDE behind it.
func TestInfraGatewayOIDC(t *testing.T) {
	anon := testHTTPClient(t, false, false)
	resp, err := anon.Get("https://" + smokeHost + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || !strings.Contains(resp.Header.Get("Location"), keycloakHost) {
		t.Fatalf("anonymous: status %d location %q, want redirect to Keycloak", resp.StatusCode, resp.Header.Get("Location"))
	}

	c := browserLogin(t, alice, "https://"+smokeHost+"/")
	resp, err = c.Get("https://" + smokeHost + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "fake-ide") {
		t.Fatalf("after login: status %d body %.80q", resp.StatusCode, b)
	}
}
