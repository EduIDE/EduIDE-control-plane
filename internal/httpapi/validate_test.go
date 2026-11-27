package httpapi

import (
	"regexp"
	"strings"
	"testing"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

func TestValidateData(t *testing.T) {
	long := strings.Repeat("a", 65)
	tests := []struct {
		name string
		in   map[string]string
		code ProblemCode
	}{
		{"ok", map[string]string{"GIT_URI": "https://git.example.org/a/b.git", "ARTEMIS_TOKEN": "abc123", "BUILD": "maven"}, ""},
		{"unknown key", map[string]string{"GIT_URI": "https://git.example.org/a", "FROM_SECRET": "x"}, CodeUnknownKey},
		{"user key from request", map[string]string{"GIT_URI": "https://git.example.org/a", "GIT_USER": "mallory"}, CodeUnknownKey},
		{"missing required", map[string]string{"BUILD": "maven"}, CodeInvalidInput},
		{"http url", map[string]string{"GIT_URI": "http://git.example.org/a"}, CodeInvalidInput},
		{"foreign host", map[string]string{"GIT_URI": "https://evil.example.com/a"}, CodeInvalidInput},
		{"userinfo", map[string]string{"GIT_URI": "https://u:p@git.example.org/a"}, CodeInvalidInput},
		{"host case", map[string]string{"GIT_URI": "https://GIT.example.org/a"}, ""},
		{"host suffix trick", map[string]string{"GIT_URI": "https://git.example.org.evil.com/a"}, CodeInvalidInput},
		{"no host", map[string]string{"GIT_URI": "https:///a"}, CodeInvalidInput},
		{"token space", map[string]string{"GIT_URI": "https://git.example.org/a", "ARTEMIS_TOKEN": "a b"}, CodeInvalidInput},
		{"token control char", map[string]string{"GIT_URI": "https://git.example.org/a", "ARTEMIS_TOKEN": "a\nb"}, CodeInvalidInput},
		{"token non ascii", map[string]string{"GIT_URI": "https://git.example.org/a", "ARTEMIS_TOKEN": "aé"}, CodeInvalidInput},
		{"token too long", map[string]string{"GIT_URI": "https://git.example.org/a", "ARTEMIS_TOKEN": long}, CodeInvalidInput},
		{"empty value", map[string]string{"GIT_URI": "https://git.example.org/a", "ARTEMIS_TOKEN": ""}, CodeInvalidInput},
		{"enum miss", map[string]string{"GIT_URI": "https://git.example.org/a", "BUILD": "make"}, CodeInvalidInput},
		{"too large", map[string]string{"GIT_URI": "https://git.example.org/a", "ARTEMIS_TOKEN": strings.Repeat("a", 9000)}, CodePayloadTooLarge},
	}
	tpl := javaTemplate()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := validateData(tpl, tt.in, alice, hostsFor("git.example.org"))
			if tt.code == "" {
				if err != nil {
					t.Fatalf("unexpected %v", err)
				}
				if out["GIT_USER"] != "Alice A" || out["GIT_MAIL"] != "alice@example.org" {
					t.Fatalf("user keys not taken from token: %v", out)
				}
				return
			}
			if err == nil || err.code != tt.code {
				t.Fatalf("got %v, want code %s", err, tt.code)
			}
		})
	}
}

func TestValidatePattern(t *testing.T) {
	tpl := &v1alpha1.IDETemplate{Spec: v1alpha1.IDETemplateSpec{InjectableKeys: []v1alpha1.InjectableKey{
		{Name: "TAG", Format: v1alpha1.FormatPattern, Pattern: `v[0-9]+|latest`},
	}}}
	for val, ok := range map[string]bool{"v12": true, "latest": true, "xv12": false, "v12x": false, "v1\nlatest": false} {
		_, err := validateData(tpl, map[string]string{"TAG": val}, alice, hostsFor())
		if (err == nil) != ok {
			t.Errorf("%q: err=%v, want ok=%v", val, err, ok)
		}
	}
}

func TestDeriveTitle(t *testing.T) {
	strip := regexp.MustCompile(`-[a-z]{2}[0-9]{2}[a-z]{3}$`)
	if got := deriveTitle("https://git.example.org/course/ex01-ab12cde.git", nil); got != "ex01-ab12cde" {
		t.Errorf("without a strip pattern got %q", got)
	}
	tests := map[string]string{
		"https://git.example.org/course/ex01-ab12cde.git": "ex01",
		"https://git.example.org/course/project":          "project",
		"https://git.example.org/course/project/":         "project",
		"git@git.example.org:course/proj.git":             "proj",
		"https://git.example.org/-ab12cde":                "-ab12cde", // never an empty title
		"":                                                "",
	}
	for in, want := range tests {
		if got := deriveTitle(in, strip); got != want {
			t.Errorf("deriveTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeLaunchRequest(t *testing.T) {
	tests := []struct {
		name, body string
		code       ProblemCode
	}{
		{"ok", `{"template":"java","data":{"GIT_URI":"https://git.example.org/a"}}`, ""},
		{"unknown field", `{"template":"java","fromSecrets":["x"]}`, CodeInvalidInput},
		{"no template", `{}`, CodeInvalidInput},
		{"bad persistence", `{"template":"java","persistence":"snapshot"}`, CodeInvalidInput},
		{"trailing", `{"template":"java"}{"template":"x"}`, CodeInvalidInput},
		{"not json", `nope`, CodeInvalidInput},
		{"too big", `{"template":"java","data":{"A":"` + strings.Repeat("a", 70000) + `"}}`, CodePayloadTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeLaunchRequest(strings.NewReader(tt.body))
			if (tt.code == "") != (err == nil) || err != nil && err.code != tt.code {
				t.Fatalf("got %v, want %q", err, tt.code)
			}
		})
	}
}

func FuzzValidateData(f *testing.F) {
	f.Add("GIT_URI", "https://git.example.org/a", "ARTEMIS_TOKEN", "tok")
	f.Add("GIT_USER", "x", "BUILD", "maven")
	f.Add("GIT_URI", "https://u@git.example.org", "", "")
	tpl := javaTemplate()
	f.Fuzz(func(t *testing.T, k1, v1, k2, v2 string) {
		out, err := validateData(tpl, map[string]string{k1: v1, k2: v2}, alice, hostsFor("git.example.org"))
		if err != nil {
			return
		}
		for k := range out {
			if !strings.HasPrefix(k, "GIT_") && k != "ARTEMIS_TOKEN" && k != "BUILD" {
				t.Fatalf("key %q is not in the template", k)
			}
		}
		if v, ok := out["GIT_URI"]; ok && !strings.HasPrefix(v, "https://") {
			t.Fatalf("GIT_URI %q passed without https", v)
		}
		if out["GIT_USER"] != "Alice A" {
			t.Fatalf("GIT_USER not from the token: %q", out["GIT_USER"])
		}
	})
}

func FuzzDecodeLaunchRequest(f *testing.F) {
	f.Add(`{"template":"java","data":{"A":"b"}}`)
	f.Add(`{"template":""}`)
	f.Add(`[]`)
	f.Fuzz(func(t *testing.T, body string) {
		req, err := decodeLaunchRequest(strings.NewReader(body))
		if (req == nil) == (err == nil) {
			t.Fatalf("exactly one of request and error must be set: %v %v", req, err)
		}
		if req != nil && (req.Template == "" || len(req.Template) > 63) {
			t.Fatalf("accepted template %q", req.Template)
		}
	})
}

func TestAllowCredentials(t *testing.T) {
	tpl := &v1alpha1.IDETemplate{Spec: v1alpha1.IDETemplateSpec{InjectableKeys: []v1alpha1.InjectableKey{
		{Name: "GIT_URI", Format: v1alpha1.FormatHTTPSURL, AllowCredentials: true},
		{Name: "ARTEMIS_URL", Format: v1alpha1.FormatHTTPSURL},
	}}}
	hosts := hostsFor("artemis.example.org")
	tests := []struct {
		name, key, val, detail string
	}{
		{"credentials allowed on GIT_URI", "GIT_URI", "https://u:vcs-token@artemis.example.org/git/x.git", ""},
		{"credentials still need an allowed host", "GIT_URI", "https://u:t@evil.example.com/x.git", "invalid value for key GIT_URI: host is not allowed"},
		{"credentials still need https", "GIT_URI", "http://u:t@artemis.example.org/x.git", "invalid value for key GIT_URI: must be an https URL"},
		{"credentials refused where not allowed", "ARTEMIS_URL", "https://u:t@artemis.example.org", "invalid value for key ARTEMIS_URL: must be an https URL without credentials"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := validateData(tpl, map[string]string{tt.key: tt.val}, alice, hosts)
			if tt.detail == "" {
				if err != nil || out[tt.key] != tt.val {
					t.Fatalf("got %v %v", out, err)
				}
				return
			}
			if err == nil || err.detail != tt.detail {
				t.Fatalf("got %v, want detail %q", err, tt.detail)
			}
			// The detail names key and rule, never the value or its credentials.
			if strings.Contains(err.detail, "vcs-token") || strings.Contains(err.detail, "u:t@") {
				t.Fatalf("detail leaks the value: %q", err.detail)
			}
		})
	}
}

func TestWithoutCredentials(t *testing.T) {
	for in, want := range map[string]string{
		"https://u:vcs-token@artemis.example.org/git/x.git": "https://artemis.example.org/git/x.git",
		"https://u@artemis.example.org/git/x.git":           "https://artemis.example.org/git/x.git",
		"https://github.com/a/b.git":                        "https://github.com/a/b.git",
		"":                                                  "",
	} {
		if got := withoutCredentials(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// hostsFor is an allowed host list that is the same for every key.
func hostsFor(h ...string) func(string) []string { return func(string) []string { return h } }
