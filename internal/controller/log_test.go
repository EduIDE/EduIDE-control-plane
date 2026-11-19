package controller

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

func TestStatusPatchIsLoggedAtDebugOnly(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		var buf bytes.Buffer
		old := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level})))
		s := &v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-1", Namespace: "ns"}, Spec: v1.IDESessionSpec{Owner: "alice-owner-name"}}
		c := fake.NewClientBuilder().WithScheme(NewScheme()).WithObjects(s).WithStatusSubresource(s).Build()
		d := &deps{Client: c}
		orig := s.DeepCopy()
		s.Status.Phase = v1.SessionStarting
		if err := d.patchStatus(context.Background(), orig, s); err != nil {
			t.Fatal(err)
		}
		slog.SetDefault(old)
		out := buf.String()
		if got := strings.Contains(out, `"msg":"status patch"`); got != (level == slog.LevelDebug) {
			t.Errorf("level %v: status patch logged=%v\n%s", level, got, out)
		}
		if strings.Contains(out, "alice-owner-name") {
			t.Errorf("log contains the owner:\n%s", out)
		}
	}
}

func TestAgentGetsItsLogLevel(t *testing.T) {
	d := &deps{cfg: &config.Config{Agent: config.Agent{LogLevel: "debug"}}}
	tmpl := &v1.IDETemplate{ObjectMeta: metav1.ObjectMeta{Name: "java", Namespace: "ns"}}
	for _, c := range d.buildPod(tmpl, "inst", nil, "").Spec.Containers {
		if c.Name != "agent" {
			continue
		}
		for _, e := range c.Env {
			if e.Name == "AGENT_LOG_LEVEL" && e.Value == "debug" {
				return
			}
		}
	}
	t.Fatal("agent container lacks AGENT_LOG_LEVEL=debug")
}

func TestWorkspaceMountOnlyWithClaim(t *testing.T) {
	d := &deps{cfg: &config.Config{}}
	tmpl := &v1.IDETemplate{ObjectMeta: metav1.ObjectMeta{Name: "java", Namespace: "ns"}}
	for claim, want := range map[string]bool{"": false, "w-1": true} {
		pod := d.buildPod(tmpl, "inst", nil, claim)
		mounted := false
		for _, c := range pod.Spec.Containers {
			for _, m := range c.VolumeMounts {
				mounted = mounted || m.Name == "workspace"
			}
		}
		hasVol := false
		for _, v := range pod.Spec.Volumes {
			hasVol = hasVol || v.Name == "workspace" && v.EmptyDir == nil
		}
		if mounted != want || hasVol != want {
			t.Errorf("claim %q: mounted=%v volume=%v, want %v", claim, mounted, hasVol, want)
		}
	}
}
