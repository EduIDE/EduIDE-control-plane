package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

func TestGitEnv(t *testing.T) {
	provs := []config.GitProvider{{Name: "a", Host: "github.com"}, {Name: "b", Host: "gitlab.example.org"}}
	tests := []struct {
		name string
		cfg  config.GitAccounts
		want map[string]string
	}{
		{"off", config.GitAccounts{}, nil},
		{"providers without proxy URL", config.GitAccounts{Providers: provs}, nil},
		{"on", config.GitAccounts{Providers: provs, ProxyURL: "http://gp.ns.svc:8080"}, map[string]string{
			"AGENT_GIT_PROXY_URL": "http://gp.ns.svc:8080", "AGENT_GIT_HOSTS": "github.com,gitlab.example.org", "AGENT_GIT_LANDING_URL": "https://ide.example.org"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &deps{cfg: &config.Config{GitAccounts: tc.cfg, Hosts: config.Hosts{Service: "ide.example.org"}}}
			got := map[string]string{}
			for _, e := range d.gitEnv() {
				got[e.Name] = e.Value
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestSpecHashCoversGitSettings(t *testing.T) {
	provs := []config.GitProvider{{Name: "a", Host: "github.com"}}
	hash := func(g config.GitAccounts) string {
		d := &deps{cfg: &config.Config{GitAccounts: g, Hosts: config.Hosts{Service: "ide.example.org"}}}
		return d.specHash(&v1.IDETemplate{})
	}
	off, on := hash(config.GitAccounts{}), hash(config.GitAccounts{Providers: provs, ProxyURL: "http://gp:8080"})
	if off == on {
		t.Error("turning Git accounts on keeps the hash: idle pods never get the Git route")
	}
	if on == hash(config.GitAccounts{Providers: append(provs, config.GitProvider{Name: "b", Host: "gitlab.example.org"}), ProxyURL: "http://gp:8080"}) {
		t.Error("a new provider keeps the hash")
	}
	if on == hash(config.GitAccounts{Providers: provs, ProxyURL: "http://other:8080"}) {
		t.Error("a new proxy URL keeps the hash")
	}
	if off != hash(config.GitAccounts{Providers: provs}) {
		t.Error("providers without a proxy URL change nothing for the agent and must not change the hash")
	}
}

// TestSpecHashWithoutGitIsUnchanged pins the hash of a deployment without providers to the formula from before the
// Git accounts feature. A change here replaces every idle pod of every installation once on upgrade.
func TestSpecHashWithoutGitIsUnchanged(t *testing.T) {
	d := &deps{cfg: &config.Config{Hosts: config.Hosts{Service: "ide.example.org"}}}
	tmpl := &v1.IDETemplate{}
	b, _ := json.Marshal([]any{tmpl.Spec, d.cfg.Agent.Image, d.cfg.Placement, 2})
	sum := sha256.Sum256(b)
	if got, want := d.specHash(tmpl), hex.EncodeToString(sum[:])[:16]; got != want {
		t.Errorf("hash %s, want the pre-feature value %s", got, want)
	}
}
