package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	ggcrv1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

const (
	// imageRecheck is how often the registry is asked about a tag (U12).
	imageRecheck = 10 * time.Minute
	// maxImageAnnotation caps the annotation value that is parsed.
	maxImageAnnotation = 64 << 10
	registryBudget     = 15 * time.Second

	reasonRead              = "Read"
	reasonAnnotationMissing = "AnnotationMissing"
	reasonAnnotationInvalid = "AnnotationInvalid"
	reasonRegistryError     = "RegistryError"
)

// ImageRegistry reads template metadata from the image manifest without a running pod.
type ImageRegistry interface {
	// Digest resolves the image reference (tag or digest) to the digest of its manifest.
	Digest(ctx context.Context, image string) (string, error)
	// Annotation returns the annotation of the manifest with this digest; ok is false when it has none.
	Annotation(ctx context.Context, image, digest, key string) (value string, ok bool, err error)
}

// remoteRegistry talks to OCI registries with anonymous pulls.
// shortcut: no imagePullSecrets, so images of private registries get the RegistryError condition; add a keychain from the secrets when needed.
type remoteRegistry struct{}

func (remoteRegistry) opts(ctx context.Context) []remote.Option {
	return []remote.Option{remote.WithContext(ctx), remote.WithAuth(authn.Anonymous), remote.WithTransport(cappedTransport{remote.DefaultTransport})}
}

// maxRegistryResponse caps every registry and token response; the library alone allows manifests of 100 MiB.
const maxRegistryResponse = 4 << 20

type cappedTransport struct{ next http.RoundTripper }

func (c cappedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(req)
	if err == nil {
		resp.Body = &cappedBody{ReadCloser: resp.Body, left: maxRegistryResponse}
	}
	return resp, err
}

type cappedBody struct {
	io.ReadCloser
	left int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, fmt.Errorf("registry response is larger than %d bytes", maxRegistryResponse)
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, err
}

func (r remoteRegistry) Digest(ctx context.Context, image string) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", err
	}
	d, err := remote.Head(ref, r.opts(ctx)...)
	if err != nil {
		return "", err
	}
	return d.Digest.String(), nil
}

func (r remoteRegistry) Annotation(ctx context.Context, image, digest, key string) (string, bool, error) {
	return r.annotation(ctx, image, digest, key, true)
}

func (r remoteRegistry) annotation(ctx context.Context, image, digest, key string, follow bool) (string, bool, error) {
	if _, err := ggcrv1.NewHash(digest); err != nil {
		return "", false, err
	}
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", false, err
	}
	desc, err := remote.Get(ref.Context().Digest(digest), r.opts(ctx)...)
	if err != nil {
		return "", false, err
	}
	// Image manifests and image indexes both carry top-level annotations.
	var m struct {
		Annotations map[string]string `json:"annotations"`
		Manifests   []struct {
			Digest   string `json:"digest"`
			Platform *struct {
				OS string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(desc.Manifest, &m); err != nil {
		return "", false, err
	}
	if v, ok := m.Annotations[key]; ok || !follow || !desc.MediaType.IsIndex() {
		return v, ok, nil
	}
	// A multi-arch build may annotate only the image manifests: read the first one, skipping attestations (os unknown).
	for _, c := range m.Manifests {
		if c.Platform != nil && c.Platform.OS != "unknown" {
			return r.annotation(ctx, image, c.Digest, key, false)
		}
	}
	return "", false, nil
}

// imageEntry is what the operator knows about one image reference.
type imageEntry struct {
	checkedAt time.Time
	digest    string
	meta      *v1.ImageMetadata // without lastChecked; nil when the image has none
	reason    string
	message   string
}

type imageCache struct {
	mu sync.Mutex
	m  map[string]imageEntry
}

func (c *imageCache) get(image string) (imageEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[image]
	return e, ok
}

func (c *imageCache) put(image string, e imageEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]imageEntry{}
	}
	c.m[image] = e
}

// readImage asks the registry about image. The annotation is parsed once per digest; a registry error keeps the last good metadata.
func (r *templateReconciler) readImage(ctx context.Context, image string, prev imageEntry, hadPrev bool) imageEntry {
	now := r.clock.Now()
	ctx, cancel := context.WithTimeout(ctx, registryBudget)
	defer cancel()
	fail := func(reason string, err error) imageEntry {
		e := imageEntry{checkedAt: now, reason: reason, message: shorten(err.Error())}
		if hadPrev {
			e.digest, e.meta = prev.digest, prev.meta
		}
		return e
	}
	digest, err := r.registry.Digest(ctx, image)
	if err != nil {
		return fail(reasonRegistryError, err)
	}
	if hadPrev && prev.digest == digest && prev.reason != reasonRegistryError {
		prev.checkedAt = now
		return prev
	}
	val, ok, err := r.registry.Annotation(ctx, image, digest, v1.AnnotationImageMetadata)
	if err != nil {
		return fail(reasonRegistryError, err)
	}
	e := imageEntry{checkedAt: now, digest: digest}
	if !ok {
		e.reason, e.message = reasonAnnotationMissing, "the image manifest has no annotation "+v1.AnnotationImageMetadata
		return e
	}
	m, err := parseImageMetadata(val)
	if err != nil {
		e.reason, e.message = reasonAnnotationInvalid, shorten(err.Error())
		return e
	}
	m.Digest = digest
	e.meta, e.reason = m, reasonRead
	return e
}

func shorten(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// reconcileImage fills status.image and the ImageMetadata condition. It never fails the template.
// It returns when to look again.
func (r *templateReconciler) reconcileImage(ctx context.Context, t *v1.IDETemplate) time.Duration {
	now := r.clock.Now()
	e, ok := r.images.get(t.Spec.Image)
	if !ok || now.Sub(e.checkedAt) >= imageRecheck {
		e = r.readImage(ctx, t.Spec.Image, e, ok)
		r.images.put(t.Spec.Image, e)
	}
	// status.image changes only with the content; lastChecked is the time of the last such change.
	want := e.meta
	if want != nil {
		want = want.DeepCopy()
		want.LastChecked = nil
	}
	have := t.Status.Image
	if have != nil {
		cp := have.DeepCopy()
		cp.LastChecked = nil
		have = cp
	}
	if !equality.Semantic.DeepEqual(want, have) {
		if want != nil {
			ts := metav1.NewTime(now)
			want.LastChecked = &ts
		}
		t.Status.Image = want
	}
	cond := metav1.Condition{Type: v1.ConditionImageMetadata, Status: metav1.ConditionFalse, Reason: e.reason, Message: e.message, ObservedGeneration: t.Generation}
	if e.reason == reasonRead {
		cond.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&t.Status.Conditions, cond)
	return max(imageRecheck-now.Sub(e.checkedAt), time.Second)
}

// imageAnnotation is the JSON of the OCI annotation io.eduide.template.
type imageAnnotation struct {
	ImageVersion string `json:"imageVersion"`
	Variants     []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		LogoURL     string `json:"logoUrl"`
	} `json:"variants"`
	Includes []struct {
		Group string `json:"group"`
		Items []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"items"`
	} `json:"includes"`
}

var (
	variantID    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	versionValue = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
)

// parseImageMetadata validates the annotation value and size-caps it. Limits match the CRD schema.
func parseImageMetadata(raw string) (*v1.ImageMetadata, error) {
	if len(raw) > maxImageAnnotation {
		return nil, fmt.Errorf("annotation is larger than %d bytes", maxImageAnnotation)
	}
	var a imageAnnotation
	if err := json.NewDecoder(bytes.NewReader([]byte(raw))).Decode(&a); err != nil {
		return nil, errors.New("annotation is not valid JSON of the expected shape")
	}
	text := func(what, s string, limit int, required bool) error {
		switch {
		case required && s == "":
			return fmt.Errorf("%s is empty", what)
		case len(s) > limit || !utf8.ValidString(s):
			return fmt.Errorf("%s is too long or not UTF-8", what)
		}
		for _, c := range s {
			if c < 0x20 || c == 0x7f {
				return fmt.Errorf("%s has control characters", what)
			}
		}
		return nil
	}
	out := &v1.ImageMetadata{}
	if a.ImageVersion != "" && !versionValue.MatchString(a.ImageVersion) {
		return nil, errors.New("imageVersion is not a version")
	}
	out.Version = a.ImageVersion
	if len(a.Variants) > 32 || len(a.Includes) > 32 {
		return nil, errors.New("too many variants or include groups")
	}
	seen := map[string]bool{}
	for _, v := range a.Variants {
		if !variantID.MatchString(v.ID) || seen[v.ID] {
			return nil, fmt.Errorf("variant id %q is invalid or duplicate", shorten(v.ID))
		}
		seen[v.ID] = true
		if err := errors.Join(text("variant name", v.Name, 200, true), text("variant description", v.Description, 1000, false), text("variant logoUrl", v.LogoURL, 2048, false)); err != nil {
			return nil, err
		}
		if v.LogoURL != "" {
			// The landing page renders it as an image source: plain http(s) only.
			u, err := url.Parse(v.LogoURL)
			if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || u.User != nil {
				return nil, errors.New("variant logoUrl must be an http(s) URL without credentials")
			}
		}
		out.Variants = append(out.Variants, v1.ImageVariant{ID: v.ID, Name: v.Name, Description: v.Description, LogoURL: v.LogoURL})
	}
	for _, g := range a.Includes {
		if err := text("include group", g.Group, 100, true); err != nil {
			return nil, err
		}
		if len(g.Items) > 256 {
			return nil, errors.New("too many items in an include group")
		}
		grp := v1.IncludedGroup{Group: g.Group, Items: []v1.IncludedItem{}}
		for _, it := range g.Items {
			if err := errors.Join(text("include item name", it.Name, 200, true), text("include item version", it.Version, 100, false)); err != nil {
				return nil, err
			}
			grp.Items = append(grp.Items, v1.IncludedItem{Name: it.Name, Version: it.Version})
		}
		out.Includes = append(out.Includes, grp)
	}
	return out, nil
}
