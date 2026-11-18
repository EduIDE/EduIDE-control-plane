package scaler

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/EduIDE/EduIDE-control-plane/pool-scaler/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/pool-scaler/internal/model"
)

func tickLogged(t *testing.T, level slog.Level) string {
	t.Helper()
	a := app("java-17")
	a.Annotations = map[string]string{"owner-note": "annotation-secret-value"}
	k := &fakeKube{apps: map[string]*model.App{"java-17": a}, patches: map[string]int{}}
	s := newScaler(t, config.ModeActive, config.StrategyReactive, k, &fakeProm{rates: map[string]float64{"java-17": 0.1}})
	var buf bytes.Buffer
	s.Log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level}))
	if err := s.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestDecisionIsLoggedAtInfo(t *testing.T) {
	out := tickLogged(t, slog.LevelInfo)
	for _, want := range []string{`"msg":"decision"`, `"pool":"java-17"`, `"targetIdle":8`, `"why":`, `"msg":"active strategy"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"level":"DEBUG"`) || strings.Contains(out, "annotation-secret-value") {
		t.Errorf("unexpected content:\n%s", out)
	}
}

func TestInputsAreLoggedAtDebugOnly(t *testing.T) {
	out := tickLogged(t, slog.LevelDebug)
	for _, want := range []string{`"msg":"pool inputs"`, `"claim_rate_per_s":0.1`, `"msg":"strategy recommendation"`, `"strategy":"reactive"`} {
		if !strings.Contains(out, want) {
			t.Errorf("debug log lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "annotation-secret-value") {
		t.Errorf("log contains annotations:\n%s", out)
	}
}
