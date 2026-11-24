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

func TestCanonicalRepo(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"o/r", "o/r", true},
		{"/o/r/", "o/r", true},
		{"o/r.git", "o/r", true},
		{"G/Sub/P.git", "G/Sub/P", true},
		{"o/my.repo-1_x~", "o/my.repo-1_x~", true},
		{"o/r/.git/info", "", false}, // the dot segment that used to normalise to o/r
		{"o/.git", "", false},
		{"o/.hidden", "", false},
		{"o/..", "", false},
		{"o/./r", "", false},
		{"o/r.git.git", "", false},
		{"o.git/r", "", false}, // .git only at the end of the last segment
		{"o", "", false},
		{"", "", false},
		{"o/r%73", "", false},
		{"o/r s", "", false},
		{"o//r", "", false},
		{"a/b/c/d/e/f/g/h/i/j", "a/b/c/d/e/f/g/h/i/j", true},
		{"a/b/c/d/e/f/g/h/i/j/k", "", false},
	}
	for _, tc := range tests {
		got, ok := CanonicalRepo(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("CanonicalRepo(%q) = %q %v, want %q %v", tc.in, got, ok, tc.want, tc.ok)
		}
		if ok && NormalizeRepo(got) != got {
			t.Errorf("CanonicalRepo(%q) = %q is not stable under NormalizeRepo", tc.in, got)
		}
	}
}
