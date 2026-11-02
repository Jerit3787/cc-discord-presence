# Stop Discord Rich Presence daemon (Windows)
# WARNING: Windows support is untested. Please report issues on GitHub.

# Configuration
$ClaudeDir = Join-Path $env:USERPROFILE ".claude"
$PidFile = Join-Path $ClaudeDir "discord-presence.pid"
$RefcountFile = Join-Path $ClaudeDir "discord-presence.refcount"

# Session tracking: Use refcount (PID-based tracking is unreliable on Windows).
# Serialize the read-modify-write so concurrent stops do not lose updates.
$RefcountMutex = New-Object System.Threading.Mutex($false, "Local\cc-discord-presence-refcount")
try {
    $RefcountLocked = $RefcountMutex.WaitOne(10000)
} catch [System.Threading.AbandonedMutexException] {
    $RefcountLocked = $true
}
try {
    $CurrentCount = 1
    if (Test-Path $RefcountFile) {
        $RawCount = [string](Get-Content $RefcountFile -Raw)
        [int]::TryParse($RawCount.Trim(), [ref]$CurrentCount) | Out-Null
    }
    $ActiveSessions = [Math]::Max(0, $CurrentCount - 1)

    if ($ActiveSessions -gt 0) {
        $ActiveSessions | Out-File -FilePath $RefcountFile -Encoding ASCII -NoNewline
    } else {
        # No more sessions, clean up refcount file
        Remove-Item $RefcountFile -Force -ErrorAction SilentlyContinue
    }
} finally {
    if ($RefcountLocked) { $RefcountMutex.ReleaseMutex() }
    $RefcountMutex.Dispose()
}

if ($ActiveSessions -gt 0) {
    Write-Host "Discord Rich Presence still in use by $ActiveSessions session(s)"
    exit 0
}

# Stop the daemon
if (Test-Path $PidFile) {
    $ProcessId = 0
    $RawPid = [string](Get-Content $PidFile -Raw -ErrorAction SilentlyContinue)
    [int]::TryParse($RawPid.Trim(), [ref]$ProcessId) | Out-Null
    if ($ProcessId -gt 0) {
        $Process = Get-Process -Id $ProcessId -ErrorAction SilentlyContinue | Where-Object { $_.ProcessName -like "cc-discord-presence*" }
        if ($Process) {
            Stop-Process -Id $ProcessId -Force -ErrorAction SilentlyContinue
            Write-Host "Discord Rich Presence stopped (PID: $ProcessId)"
        }
    }
    Remove-Item $PidFile -Force -ErrorAction SilentlyContinue
} else {
    # Try to find and kill by process name
    $Processes = Get-Process -Name "cc-discord-presence*" -ErrorAction SilentlyContinue
    if ($Processes) {
        $Processes | Stop-Process -Force
        Write-Host "Discord Rich Presence stopped"
    }
}
