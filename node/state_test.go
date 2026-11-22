package node

import (
	"context"
	"errors"
	"testing"
)

func TestStatePublishedTargets(t *testing.T) {
	var state State

	if target, ok := state.PublishedTarget("volume-1"); ok || target != "" {
		t.Fatalf("zero-value PublishedTarget() = %q, %v", target, ok)
	}

	state.SetPublished("volume-1", "/target/one")
	if target, ok := state.PublishedTarget("volume-1"); !ok || target != "/target/one" {
		t.Fatalf("PublishedTarget() = %q, %v", target, ok)
	}

	state.DeletePublished("volume-1")
	if _, ok := state.PublishedTarget("volume-1"); ok {
		t.Fatal("deleted target remains published")
	}
}

func TestStateLockVolumeRespectsCancellation(t *testing.T) {
	var state State

	first, err := state.LockVolume(context.Background(), "volume-1")
	if err != nil {
		t.Fatalf("first LockVolume() error = %v", err)
	}
	defer first.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if lock, err := state.LockVolume(ctx, "volume-1"); lock != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("contended LockVolume() = %v, %v; want nil, context canceled", lock, err)
	}

	other, err := state.LockVolume(context.Background(), "volume-2")
	if err != nil {
		t.Fatalf("independent LockVolume() error = %v", err)
	}
	other.Unlock()
}
