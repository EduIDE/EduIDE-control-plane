package controller

import (
	"testing"

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
