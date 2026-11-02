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

func TestIdleExitEnabled(t *testing.T) {
	t.Setenv("CCDP_MANAGED", "")
	if idleExitEnabled() {
		t.Error("idleExitEnabled() = true without CCDP_MANAGED")
	}
	t.Setenv("CCDP_MANAGED", "1")
	if !idleExitEnabled() {
		t.Error("idleExitEnabled() = false with CCDP_MANAGED=1")
	}
}

func TestCloudSessionStateRequiresDesktopApp(t *testing.T) {
	origDesktop := desktopAppRunningFn
	origClouds := cloudSessionsSource
	origProjects, origData, origModule := projectsDir, dataFilePath, moduleFilePath
	defer func() {
		desktopAppRunningFn = origDesktop
		cloudSessionsSource = origClouds
		projectsDir, dataFilePath, moduleFilePath = origProjects, origData, origModule
	}()

	dir := t.TempDir()
	projectsDir = dir
	dataFilePath = filepath.Join(dir, "missing-data.json")
	moduleFilePath = filepath.Join(dir, "missing-module.json")
	cloudSessionsSource = func() ([]cloudSession, error) {
		return []cloudSession{{ID: "session_01a", LastActive: time.Now()}}, nil
	}

	desktopAppRunningFn = func() bool { return false }
	if active, known := cloudSessionState(); active || !known {
		t.Errorf("cloudSessionState() with app closed = (%v, %v), want (false, true)", active, known)
	}

	desktopAppRunningFn = func() bool { return true }
	if active, known := cloudSessionState(); !active || !known {
		t.Errorf("cloudSessionState() with app open = (%v, %v), want (true, true)", active, known)
	}
}

func TestCloudSessionStateUnknownOnReadError(t *testing.T) {
	origDesktop := desktopAppRunningFn
	origClouds := cloudSessionsSource
	defer func() {
		desktopAppRunningFn = origDesktop
		cloudSessionsSource = origClouds
	}()

	desktopAppRunningFn = func() bool { return true }
	cloudSessionsSource = func() ([]cloudSession, error) {
		return nil, os.ErrPermission
	}

	if active, known := cloudSessionState(); active || known {
		t.Errorf("cloudSessionState() on read error = (%v, %v), want (false, false)", active, known)
	}
}
