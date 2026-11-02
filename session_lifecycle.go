package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SessionExitGracePeriod is how long the daemon waits after the last tracked
// Claude Code session disappears before clearing the presence and exiting. It
// is a safety net for abnormal exits (the desktop app quitting without a
// SessionEnd hook, a killed process, or a manually started daemon).
const SessionExitGracePeriod = 30 * time.Second

// anyTrackedSessionAlive reports whether any PID file in dir names a live
// process. alive is a parameter so tests can avoid depending on real process
// names.
func anyTrackedSessionAlive(dir string, alive func(int) bool) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if alive(pid) {
			return true
		}
	}
	return false
}

// processExists reports whether pid is a live process.
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// trackedProcessAlive also requires the process to look like Claude Code, so a
// reused PID does not keep the daemon alive forever.
func trackedProcessAlive(pid int) bool {
	if !processExists(pid) {
		return false
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "claude")
}

// anySessionAlive checks the same session registry the start/stop hooks use.
// Windows tracks sessions with a refcount instead of PID files.
func anySessionAlive() bool {
	if runtime.GOOS == "windows" {
		data, err := os.ReadFile(filepath.Join(claudeDir, "discord-presence.refcount"))
		if err != nil {
			return false
		}
		count, err := strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && count > 0
	}
	return anyTrackedSessionAlive(
		filepath.Join(claudeDir, "discord-presence-sessions"),
		trackedProcessAlive,
	)
}

// cloudSessionIsActive reports whether a cloud session is active. Cloud
// sessions have no local process, so they must keep the daemon alive
// separately.
func cloudSessionIsActive() bool {
	return activeCloudSession(cloudSessionsSource(), latestLocalActivity(), time.Now()) != nil
}

// sessionExitDue reports whether the no-session period has lasted long enough.
func sessionExitDue(noSessionsSince, now time.Time, grace time.Duration) bool {
	return !noSessionsSince.IsZero() && now.Sub(noSessionsSince) >= grace
}

// checkSessionLiveness updates the no-session timer and reports whether the
// daemon should clear the presence and exit.
func checkSessionLiveness(noSessionsSince *time.Time, now time.Time) bool {
	if anySessionAlive() || cloudSessionIsActive() {
		*noSessionsSince = time.Time{}
		return false
	}
	if noSessionsSince.IsZero() {
		*noSessionsSince = now
		return false
	}
	return sessionExitDue(*noSessionsSince, now, SessionExitGracePeriod)
}

// shutdownIfIdle exits when no local or cloud session has been alive for the
// grace period. Closing the Discord connection removes the presence.
func shutdownIfIdle(noSessionsSince *time.Time) {
	if !checkSessionLiveness(noSessionsSince, time.Now()) {
		return
	}
	fmt.Println("⏹ No active Claude Code session; shutting down...")
	discordClient.Close()
	os.Exit(0)
}
