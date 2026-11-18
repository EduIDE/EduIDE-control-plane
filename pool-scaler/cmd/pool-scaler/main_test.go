package main

import "testing"

func TestLogLevelFromEnv(t *testing.T) {
	for in, want := range map[string]string{"debug": "DEBUG", "warn": "WARN", "": "INFO", "x": "INFO"} {
		t.Setenv("LOG_LEVEL", in)
		if got := logLevel().String(); got != want {
			t.Errorf("LOG_LEVEL=%q: %s, want %s", in, got, want)
		}
	}
}

func TestVersionIsSet(t *testing.T) {
	if version() == "" {
		t.Fatal("empty version")
	}
}
