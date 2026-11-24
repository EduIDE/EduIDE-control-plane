package gitapi

import "testing"

func TestNormalizeRepo(t *testing.T) {
	for in, want := range map[string]string{
		"o/r": "o/r", "/o/r/": "o/r", "o/r.git": "o/r", "/o/r.git/": "o/r", "o/r.git.git": "o/r.git", "": "", ".git": "", "G/Sub/P": "G/Sub/P",
	} {
		if got := NormalizeRepo(in); got != want {
			t.Errorf("NormalizeRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameRepository(t *testing.T) {
	tests := []struct {
		hostA, repoA, hostB, repoB string
		want                       bool
	}{
		{"github.com", "o/r", "GitHub.com", "o/r.git", true},
		{"github.com", "/o/r/", "github.com", "o/r", true},
		{"github.com", "o/r", "github.com", "o/R", false}, // paths are compared exactly
		{"github.com", "o/r", "gitlab.com", "o/r", false},
		{"github.com", "", "github.com", "", false},
		{"github.com", "o/r", "github.com", "o/r2", false},
	}
	for _, tc := range tests {
		if got := SameRepository(tc.hostA, tc.repoA, tc.hostB, tc.repoB); got != tc.want {
			t.Errorf("%+v = %v", tc, got)
		}
	}
}

func TestSplitURL(t *testing.T) {
	tests := []struct {
		in, host, repo string
		ok             bool
	}{
		{"https://github.com/o/r.git", "github.com", "o/r", true},
		{"https://user:token@GitHub.com/o/r/", "github.com", "o/r", true},
		{"http://github.com/o/r", "", "", false},
		{"https://github.com:8443/o/r", "", "", false},
		{"ssh://git@github.com/o/r", "", "", false},
		{"not a url", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range tests {
		h, r, ok := SplitURL(tc.in)
		if h != tc.host || r != tc.repo || ok != tc.ok {
			t.Errorf("SplitURL(%q) = %q %q %v", tc.in, h, r, ok)
		}
	}
}
