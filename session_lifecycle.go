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

// desktopAppRunningFn is a variable so tests can control the app check.
var desktopAppRunningFn = desktopAppRunning

// desktopAppRunning reports whether the Claude desktop app process is running.
// Cloud session data is only meaningful while the app is open; otherwise the
// last cloud record could keep a stale presence alive for hours.
func desktopAppRunning() bool {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("pgrep", "-x", "Claude").Output()
		return err == nil && strings.TrimSpace(string(out)) != ""
	case "windows":
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq Claude.exe", "/NH").Output()
		return err == nil && strings.Contains(strings.ToLower(string(out)), "claude.exe")
	default:
		out, err := exec.Command("pgrep", "-x", "claude").Output()
		return err == nil && strings.TrimSpace(string(out)) != ""
	}
}

// relevantLocalActivity returns the local activity that should suppress a
// cloud session. In a managed daemon, a file left by a dead session must not
// hide a live cloud session; only local activity from a registered live
// session counts. Manual daemons have no registry, so they use file times.
func relevantLocalActivity() time.Time {
	if anySessionAlive() || !idleExitEnabled() {
		return latestLocalActivity()
	}
	return time.Time{}
}

// cloudSessionState reports whether a cloud session is active and whether that
// answer is known. A database read error is "unknown", which keeps the daemon
// alive rather than falsely concluding that no session exists.
func cloudSessionState() (active, known bool) {
	if !desktopAppRunningFn() {
		return false, true
	}
	sessions, err := cloudSessionsSource()
	if err != nil {
		return false, false
	}
	return activeCloudSession(sessions, relevantLocalActivity(), time.Now()) != nil, true
}

// sessionExitDue reports whether the no-session period has lasted long enough.
func sessionExitDue(noSessionsSince, now time.Time, grace time.Duration) bool {
	return !noSessionsSince.IsZero() && now.Sub(noSessionsSince) >= grace
}

// checkSessionLiveness updates the no-session timer and reports whether the
// daemon should clear the presence and exit.
func checkSessionLiveness(noSessionsSince *time.Time, now time.Time) bool {
	cloudActive, cloudKnown := cloudSessionState()
	if anySessionAlive() || cloudActive || !cloudKnown {
		*noSessionsSince = time.Time{}
		return false
	}
	if noSessionsSince.IsZero() {
		*noSessionsSince = now
		return false
	}
	return sessionExitDue(*noSessionsSince, now, SessionExitGracePeriod)
}

// idleExitEnabled reports whether this daemon was started by the plugin. A
// manually started daemon (./cc-discord-presence) is left running until the
// user stops it, because it has no session registry to consult.
func idleExitEnabled() bool {
	return os.Getenv("CCDP_MANAGED") == "1"
}

// removePIDFileIfOurs removes the daemon PID file only when it names this
// process, so an idle exit does not leave a stale PID behind.
func removePIDFileIfOurs() {
	path := filepath.Join(claudeDir, "discord-presence.pid")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid != os.Getpid() {
		return
	}
	_ = os.Remove(path)
}

// shutdownIfIdle exits when no local or cloud session has been alive for the
// grace period. Closing the Discord connection removes the presence.
func shutdownIfIdle(noSessionsSince *time.Time) {
	if !idleExitEnabled() || !checkSessionLiveness(noSessionsSince, time.Now()) {
		return
	}
	removePIDFileIfOurs()
	fmt.Println("⏹ No active Claude Code session; shutting down...")
	discordClient.Close()
	os.Exit(0)
}
