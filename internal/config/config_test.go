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
