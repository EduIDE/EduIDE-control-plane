package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestServeWhenReady(t *testing.T) {
	h := health.NewServer()
	var ready atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		serveWhenReady(ctx, h, func(context.Context) bool { return ready.Load() }, time.Millisecond)
		close(done)
	}()
	status := func() healthpb.HealthCheckResponse_ServingStatus {
		r, err := h.Check(ctx, &healthpb.HealthCheckRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetStatus()
	}
	time.Sleep(20 * time.Millisecond)
	if s := status(); s != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("before ready: %v", s)
	}
	ready.Store(true)
	<-done
	if s := status(); s != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("after ready: %v", s)
	}
}
