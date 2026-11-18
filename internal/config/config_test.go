package config

import (
	"strings"
	"testing"
	"time"
)

const minimal = `
deployment: test
namespace: eduide-test
hosts: {service: ide.example.org, instance: instance.ide.example.org}
gateway: {name: gw}
oidc: {issuerURL: https://kc.example.org/realms/r, clientIDs: [landing], gatewayClientID: gw-client}
agent: {image: example.org/agent:1}
`

func TestParseDefaults(t *testing.T) {
	c, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.Hosts.Webview != "webview.instance.ide.example.org" || c.SessionsPerUser != 3 ||
		c.OIDC.UserClaim != "preferred_username" || c.Workspaces.Retention.Duration != 14*24*time.Hour ||
		c.Capacity.ExhaustedAfter.Duration != 45*time.Second || c.Gateway.Namespace != "eduide-test" {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if got := c.InstanceURL("abc"); got != "https://instance.ide.example.org/abc/" {
		t.Fatal(got)
	}
}

func TestParseRejects(t *testing.T) {
	for name, in := range map[string]string{
		"missing":   "deployment: x\n",
		"unknown":   minimal + "bogus: 1\n",
		"no-limit":  minimal + "sessionsPerUser: -1\n",
		"bad-strip": minimal + "overview: {titleStripPattern: '(['}\n",
		"same-host": strings.Replace(minimal, "service: ide.example.org", "service: instance.ide.example.org", 1),
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	_, err := Parse([]byte("deployment: x\n"))
	if !strings.Contains(err.Error(), "hosts.instance") {
		t.Errorf("error should name missing field: %v", err)
	}
}

func TestParseOverview(t *testing.T) {
	c, err := Parse([]byte(minimal + "overview: {titleStripPattern: '-[a-z]{2}[0-9]{2}[a-z]{3}$'}\n"))
	if err != nil || c.Overview.TitleStripPattern != "-[a-z]{2}[0-9]{2}[a-z]{3}$" {
		t.Fatalf("%v %+v", err, c.Overview)
	}
}

func TestAdminConfig(t *testing.T) {
	const salt = "telemetry: {userSaltFile: /etc/eduide/secrets/user-salt}\n"
	ok := map[string]string{
		"realm":  "oidc: {issuerURL: https://kc.example.org/realms/r, clientIDs: [landing], gatewayClientID: gw-client, admin: {rolePath: realm_access.roles, role: eduide-admin}}",
		"client": "oidc: {issuerURL: https://kc.example.org/realms/r, clientIDs: [landing], gatewayClientID: gw-client, admin: {rolePath: resource_access.eduide-landing.roles, role: eduide-admin}}",
		"dotted": "oidc: {issuerURL: https://kc.example.org/realms/r, clientIDs: [landing], gatewayClientID: gw-client, admin: {rolePath: resource_access.eduide.landing.roles, role: eduide-admin}}",
	}
	for name, oidc := range ok {
		c, err := Parse([]byte(strings.Replace(minimal, minimal[strings.Index(minimal, "oidc:"):strings.Index(minimal, "agent:")], oidc+"\n", 1) + salt))
		if err != nil || c.OIDC.Admin == nil || c.Admin.RateLimitPerMinute != 60 {
			t.Errorf("%s: %v %+v", name, err, c)
		}
	}
	withRole := func(oidcAdmin string) string {
		return strings.Replace(minimal, "gatewayClientID: gw-client}", "gatewayClientID: gw-client, "+oidcAdmin+"}", 1)
	}
	base := func(oidcAdmin, admin string) string { return withRole(oidcAdmin) + salt + admin }
	for name, in := range map[string]string{
		"groups":          base("admin: {rolePath: groups, role: x}", ""),
		"nested":          base("admin: {rolePath: realm_access.roles.extra, role: x}", ""),
		"client-no-name":  base("admin: {rolePath: resource_access..roles, role: x}", ""),
		"client-blank":    base("admin: {rolePath: 'resource_access.a b.roles', role: x}", ""),
		"no-salt":         withRole("admin: {rolePath: realm_access.roles, role: x}"),
		"realm-wrong":     base("admin: {rolePath: realm_access.groups, role: x}", ""),
		"no-role":         base("admin: {rolePath: realm_access.roles, role: ''}", ""),
		"prom-userinfo":   minimal + "admin: {prometheusURL: 'http://u:p@prom:9090'}\n",
		"prom-scheme":     minimal + "admin: {prometheusURL: 'ftp://prom'}\n",
		"trace-no-marker": minimal + "admin: {links: {traceUrlTemplate: 'https://j.example.org/trace'}}\n",
		"rate":            minimal + "admin: {rateLimitPerMinute: -1}\n",
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := Parse([]byte(minimal + "admin: {prometheusURL: 'http://prom:9090', links: {grafana: 'https://g.example.org', traceUrlTemplate: 'https://j.example.org/trace/{traceId}'}}\n")); err != nil {
		t.Error(err)
	}
}
