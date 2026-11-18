package logx

import (
	"testing"
	"time"
)

func TestLimiterCountsSuppressedLines(t *testing.T) {
	now := time.Now()
	l := NewLimiter(2)
	l.now = func() time.Time { return now }
	for i := range 2 {
		if ok, n := l.Allow("deny"); !ok || n != 0 {
			t.Fatalf("line %d: ok=%v suppressed=%d", i, ok, n)
		}
	}
	for range 3 {
		if ok, _ := l.Allow("deny"); ok {
			t.Fatal("third line in the minute must be suppressed")
		}
	}
	if ok, _ := l.Allow("login"); !ok {
		t.Fatal("keys are independent")
	}
	now = now.Add(time.Minute)
	if ok, n := l.Allow("deny"); !ok || n != 3 {
		t.Fatalf("new minute: ok=%v suppressed=%d, want true and 3", ok, n)
	}
}

func TestLevel(t *testing.T) {
	for in, want := range map[string]string{"debug": "DEBUG", "warn": "WARN", "": "INFO", "bogus": "INFO"} {
		t.Setenv("LOG_LEVEL", in)
		if got := Level().String(); got != want {
			t.Errorf("LOG_LEVEL=%q: %s, want %s", in, got, want)
		}
	}
}
