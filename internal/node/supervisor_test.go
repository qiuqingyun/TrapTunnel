package node

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"traptunnel/internal/config"
)

func TestSuperviseReloadsOnSIGHUP(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	loadCount := 0
	runProfiles := make(chan config.Profile, 4)
	stopRuntime := make(chan struct{}, 4)

	load := func(_ string) (config.NodeConfig, error) {
		mu.Lock()
		defer mu.Unlock()
		loadCount++
		if loadCount == 1 {
			return config.NodeConfig{Profile: config.ProfileEdge}, nil
		}
		return config.NodeConfig{Profile: config.ProfileRelay}, nil
	}

	run := func(ctx context.Context, cfg config.NodeConfig) error {
		runProfiles <- cfg.Profile
		<-ctx.Done()
		stopRuntime <- struct{}{}
		return nil
	}

	signals := make(chan os.Signal, 4)
	done := make(chan int, 1)
	go func() {
		done <- supervise("node.toml", load, func(config.NodeConfig) {}, run, signals)
	}()

	if got := waitProfile(t, runProfiles); got != config.ProfileEdge {
		t.Fatalf("expected first runtime profile=edge, got %q", got)
	}

	signals <- syscall.SIGHUP

	waitStop(t, stopRuntime)
	if got := waitProfile(t, runProfiles); got != config.ProfileRelay {
		t.Fatalf("expected reloaded runtime profile=relay, got %q", got)
	}

	signals <- syscall.SIGTERM
	waitStop(t, stopRuntime)

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("expected exit code 0, got %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("supervise did not exit")
	}
}

func TestSuperviseKeepsRunningWhenReloadFails(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	loadCount := 0
	runProfiles := make(chan config.Profile, 4)
	stopRuntime := make(chan struct{}, 4)

	load := func(_ string) (config.NodeConfig, error) {
		mu.Lock()
		defer mu.Unlock()
		loadCount++
		if loadCount == 1 {
			return config.NodeConfig{Profile: config.ProfileEdge}, nil
		}
		return config.NodeConfig{}, errors.New("bad config")
	}

	run := func(ctx context.Context, cfg config.NodeConfig) error {
		runProfiles <- cfg.Profile
		<-ctx.Done()
		stopRuntime <- struct{}{}
		return nil
	}

	signals := make(chan os.Signal, 4)
	done := make(chan int, 1)
	go func() {
		done <- supervise("node.toml", load, func(config.NodeConfig) {}, run, signals)
	}()

	if got := waitProfile(t, runProfiles); got != config.ProfileEdge {
		t.Fatalf("expected first runtime profile=edge, got %q", got)
	}

	signals <- syscall.SIGHUP

	select {
	case profile := <-runProfiles:
		t.Fatalf("did not expect runtime restart after failed reload, got %q", profile)
	case <-time.After(300 * time.Millisecond):
	}

	signals <- syscall.SIGTERM
	waitStop(t, stopRuntime)

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("expected exit code 0, got %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("supervise did not exit")
	}
}

func waitProfile(t *testing.T, ch <-chan config.Profile) config.Profile {
	t.Helper()
	select {
	case profile := <-ch:
		return profile
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime start")
		return ""
	}
}

func waitStop(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime stop")
	}
}
