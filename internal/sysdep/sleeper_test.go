package sysdep

import (
	"context"
	"testing"
	"time"
)

func TestOSSleeperSleepsForDuration(t *testing.T) {
	start := time.Now()
	if err := (OSSleeper{}).Sleep(context.Background(), 10*time.Millisecond); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Errorf("Sleep returned after %v, want at least 10ms", elapsed)
	}
}

func TestOSSleeperHonoursContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (OSSleeper{}).Sleep(ctx, time.Hour); err == nil {
		t.Error("Sleep(cancelled ctx) = nil, want ctx.Err()")
	}
}
