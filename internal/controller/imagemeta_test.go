package controller

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	ggcr "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

const goodAnnotation = `{"imageVersion":"1.2.3","variants":[{"id":"maven","name":"Maven","description":"Java with Maven","logoUrl":"https://example.org/maven.svg"},{"id":"gradle","name":"Gradle"}],` +
	`"includes":[{"group":"Languages","items":[{"name":"Java","version":"21.0.3"}]},{"group":"Tools","items":[{"name":"git"}]}]}`

func TestParseImageMetadata(t *testing.T) {
	m, err := parseImageMetadata(goodAnnotation)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "1.2.3" || len(m.Variants) != 2 || m.Variants[0].LogoURL != "https://example.org/maven.svg" ||
		len(m.Includes) != 2 || m.Includes[0].Items[0].Version != "21.0.3" || m.Includes[1].Items[0].Version != "" {
		t.Errorf("parsed: %+v", m)
	}
	if m, err := parseImageMetadata(`{}`); err != nil || m.Version != "" {
		t.Errorf("empty object is valid: %v %+v", err, m)
	}
	bad := map[string]string{
		"not json":          `nope`,
		"wrong shape":       `{"variants":"maven"}`,
		"version":           `{"imageVersion":"1 2"}`,
		"variant id":        `{"variants":[{"id":"Maven!","name":"M"}]}`,
		"duplicate id":      `{"variants":[{"id":"a","name":"A"},{"id":"a","name":"B"}]}`,
		"variant name":      `{"variants":[{"id":"a"}]}`,
		"javascript logo":   `{"variants":[{"id":"a","name":"A","logoUrl":"javascript:alert(1)"}]}`,
		"credentials logo":  `{"variants":[{"id":"a","name":"A","logoUrl":"https://u:p@example.org/a.svg"}]}`,
		"control character": "{\"variants\":[{\"id\":\"a\",\"name\":\"A\\u0007\"}]}",
		"group":             `{"includes":[{"group":"","items":[]}]}`,
		"too large":         `{"imageVersion":"1","includes":[{"group":"g","items":[{"name":"` + strings.Repeat("a", 70<<10) + `"}]}]}`,
		"too many variants": `{"variants":[` + strings.TrimSuffix(strings.Repeat(`{"id":"a","name":"A"},`, 33), ",") + `]}`,
	}
	for what, raw := range bad {
		if _, err := parseImageMetadata(raw); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
}

// pushImage writes a random image with annotations to the registry and returns its digest.
func pushImage(t *testing.T, ref string, annotations map[string]string) string {
	t.Helper()
	base, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	img := base
	if annotations != nil {
		img = mutate.Annotations(base, annotations).(ggcr.Image)
	}
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

func TestRemoteRegistryReadsTheManifestAnnotation(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	digest := pushImage(t, host+"/ide:1", map[string]string{v1.AnnotationImageMetadata: goodAnnotation})
	pushImage(t, host+"/plain:1", nil)
	ctx := context.Background()
	var rr remoteRegistry

	got, err := rr.Digest(ctx, host+"/ide:1")
	if err != nil || got != digest {
		t.Fatalf("digest %q, %v, want %q", got, err, digest)
	}
	val, ok, err := rr.Annotation(ctx, host+"/ide:1", digest, v1.AnnotationImageMetadata)
	if err != nil || !ok || val != goodAnnotation {
		t.Fatalf("annotation: %v %v %q", ok, err, val)
	}
	d2, _ := rr.Digest(ctx, host+"/plain:1")
	if _, ok, err := rr.Annotation(ctx, host+"/plain:1", d2, v1.AnnotationImageMetadata); ok || err != nil {
		t.Errorf("image without annotation: %v %v", ok, err)
	}
	if _, err := rr.Digest(ctx, host+"/missing:1"); err == nil {
		t.Error("unknown image must fail")
	}
	if _, err := url.Parse(srv.URL); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteRegistryReadsTheAnnotationOfAMultiArchImage(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	img = mutate.Annotations(img, map[string]string{v1.AnnotationImageMetadata: goodAnnotation}).(ggcr.Image)
	attestation, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	// The attestation comes first, so the test shows it is skipped.
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: attestation, Descriptor: ggcr.Descriptor{Platform: &ggcr.Platform{OS: "unknown", Architecture: "unknown"}}},
		mutate.IndexAddendum{Add: img, Descriptor: ggcr.Descriptor{Platform: &ggcr.Platform{OS: "linux", Architecture: "amd64"}}},
	)
	ref, err := name.ParseReference(host + "/multi:1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var rr remoteRegistry
	d, err := rr.Digest(ctx, host+"/multi:1")
	if err != nil {
		t.Fatal(err)
	}
	if val, ok, err := rr.Annotation(ctx, host+"/multi:1", d, v1.AnnotationImageMetadata); err != nil || !ok || val != goodAnnotation {
		t.Fatalf("annotation: %v %v %q", ok, err, val)
	}
	if _, _, err := rr.Annotation(ctx, host+"/multi:1", "../../v2/_catalog", v1.AnnotationImageMetadata); err == nil {
		t.Error("a digest that is no digest must fail")
	}
}

func TestRegistryResponsesAreCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxRegistryResponse+1))
	}))
	defer srv.Close()
	resp, err := (&http.Client{Transport: cappedTransport{http.DefaultTransport}}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if b, err := io.ReadAll(resp.Body); err == nil || len(b) != maxRegistryResponse {
		t.Errorf("read %d bytes, error %v", len(b), err)
	}
}

// recheck moves the clock past the recheck interval and wakes the reconciler (the real requeue timer is 10 minutes).
func recheck(t *testing.T, tm *v1.IDETemplate) {
	t.Helper()
	clk.Step(imageRecheck + time.Second)
	_ = get(tm)
	if tm.Annotations == nil {
		tm.Annotations = map[string]string{}
	}
	tm.Annotations["test/poke"] = time.Now().String()
	if err := k8s.Update(context.Background(), tm); err != nil {
		t.Fatal(err)
	}
}

func templateWith(name, image string) *v1.IDETemplate {
	tm := newTemplate(name)
	tm.Spec.Image = image
	return tm
}

func imageCondition(tm *v1.IDETemplate) *metav1.Condition {
	return meta.FindStatusCondition(tm.Status.Conditions, v1.ConditionImageMetadata)
}

func TestTemplateReadsImageMetadata(t *testing.T) {
	requireEnv(t)
	reg.set("reg.example.org/ok:1", fakeImage{digest: "sha256:aaa", annotation: goodAnnotation, hasAnnotation: true})
	tm := templateWith("tpl-img-ok", "reg.example.org/ok:1")
	mustCreate(t, tm)
	eventually(t, "image metadata", func() bool { _ = get(tm); return tm.Status.Image != nil })
	img := tm.Status.Image
	if img.Digest != "sha256:aaa" || img.Version != "1.2.3" || len(img.Variants) != 2 || img.Includes[0].Group != "Languages" || img.LastChecked == nil {
		t.Errorf("status.image: %+v", img)
	}
	if c := imageCondition(tm); c == nil || c.Status != metav1.ConditionTrue || c.Reason != "Read" {
		t.Errorf("condition: %+v", c)
	}
	if !meta.IsStatusConditionTrue(tm.Status.Conditions, v1.ConditionReady) {
		t.Error("template must be ready")
	}
	first := img.LastChecked.DeepCopy()

	// Within the recheck interval the registry is not asked again; afterwards only the digest, and the status stays as it is.
	heads, gets := reg.counts()
	recheck(t, tm)
	eventually(t, "digest recheck", func() bool { h, _ := reg.counts(); return h > heads })
	time.Sleep(200 * time.Millisecond)
	if _, g := reg.counts(); g != gets {
		t.Errorf("annotation read again for the same digest: %d, was %d", g, gets)
	}
	_ = get(tm)
	if !tm.Status.Image.LastChecked.Equal(first) {
		t.Error("status.image written without a change")
	}

	// A new digest is read again and written.
	reg.set("reg.example.org/ok:1", fakeImage{digest: "sha256:bbb", annotation: `{"imageVersion":"2.0.0"}`, hasAnnotation: true})
	recheck(t, tm)
	eventually(t, "new digest", func() bool { _ = get(tm); return tm.Status.Image != nil && tm.Status.Image.Digest == "sha256:bbb" })
	if tm.Status.Image.Version != "2.0.0" || len(tm.Status.Image.Variants) != 0 {
		t.Errorf("status.image: %+v", tm.Status.Image)
	}

	// A registry error keeps the last good metadata and sets the condition.
	reg.set("reg.example.org/ok:1", fakeImage{err: errors.New("boom")})
	recheck(t, tm)
	eventually(t, "registry error condition", func() bool { _ = get(tm); c := imageCondition(tm); return c != nil && c.Reason == "RegistryError" })
	if tm.Status.Image == nil || tm.Status.Image.Version != "2.0.0" || !meta.IsStatusConditionTrue(tm.Status.Conditions, v1.ConditionReady) {
		t.Errorf("after the error: %+v", tm.Status)
	}
}

func TestTemplateWithoutOrWithBadImageMetadata(t *testing.T) {
	requireEnv(t)
	reg.set("reg.example.org/none:1", fakeImage{digest: "sha256:ccc"})
	reg.set("reg.example.org/bad:1", fakeImage{digest: "sha256:ddd", annotation: `{"variants":[{"id":"BAD","name":"x"}]}`, hasAnnotation: true})
	for name, want := range map[string]string{
		"tpl-img-none":  "AnnotationMissing",
		"tpl-img-bad":   "AnnotationInvalid",
		"tpl-img-error": "RegistryError", // the registry knows no such image
	} {
		image := map[string]string{"tpl-img-none": "reg.example.org/none:1", "tpl-img-bad": "reg.example.org/bad:1", "tpl-img-error": "reg.example.org/unknown:1"}[name]
		tm := templateWith(name, image)
		mustCreate(t, tm)
		eventually(t, name+" condition", func() bool { _ = get(tm); c := imageCondition(tm); return c != nil && c.Reason == want })
		if tm.Status.Image != nil || imageCondition(tm).Status != metav1.ConditionFalse {
			t.Errorf("%s: %+v", name, tm.Status)
		}
		eventually(t, name+" ready", func() bool { _ = get(tm); return meta.IsStatusConditionTrue(tm.Status.Conditions, v1.ConditionReady) })
	}
}
