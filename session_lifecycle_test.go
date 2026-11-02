package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAnyTrackedSessionAlive(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"111", "222", "not-a-pid"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if !anyTrackedSessionAlive(dir, func(pid int) bool { return pid == 222 }) {
		t.Error("anyTrackedSessionAlive() = false, want true for live PID 222")
	}
	if anyTrackedSessionAlive(dir, func(pid int) bool { return false }) {
		t.Error("anyTrackedSessionAlive() = true, want false when no PID is alive")
	}
	if anyTrackedSessionAlive(filepath.Join(dir, "missing"), func(pid int) bool { return true }) {
		t.Error("anyTrackedSessionAlive() = true for a missing directory")
	}
}

func TestSessionExitDue(t *testing.T) {
	now := time.Now()
	const grace = 30 * time.Second

	if sessionExitDue(time.Time{}, now, grace) {
		t.Error("sessionExitDue() = true before the timer starts")
	}
	if sessionExitDue(now.Add(-10*time.Second), now, grace) {
		t.Error("sessionExitDue() = true before the grace period")
	}
	if !sessionExitDue(now.Add(-31*time.Second), now, grace) {
		t.Error("sessionExitDue() = false after the grace period")
	}
}

func TestProcessExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("processExists uses Unix signals")
	}
	if !processExists(os.Getpid()) {
		t.Error("processExists(current PID) = false")
	}
	if processExists(1 << 30) {
		t.Error("processExists(1<<30) = true, want false")
	}
}
