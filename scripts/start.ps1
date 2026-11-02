# Start Discord Rich Presence daemon (Windows)
# WARNING: Windows support is untested. Please report issues on GitHub.

$ErrorActionPreference = "Stop"

# Configuration
$ClaudeDir = Join-Path $env:USERPROFILE ".claude"
$BinDir = Join-Path $ClaudeDir "bin"
$PidFile = Join-Path $ClaudeDir "discord-presence.pid"
$LogFile = Join-Path $ClaudeDir "discord-presence.log"
$RefcountFile = Join-Path $ClaudeDir "discord-presence.refcount"
$Repo = "Jerit3787/cc-discord-presence"
$Version = "v1.0.10"

# Ensure directories exist
New-Item -ItemType Directory -Path $ClaudeDir -Force | Out-Null
New-Item -ItemType Directory -Path $BinDir -Force | Out-Null

# Serialize startup so two sessions cannot launch duplicate daemons. A stale
# lock older than five minutes is ignored and removed.
$StartLockFile = Join-Path $ClaudeDir "discord-presence.lock"
$script:StartLock = $null
for ($i = 0; $i -lt 300 -and $script:StartLock -eq $null; $i++) {
    try {
        $script:StartLock = [System.IO.File]::Open($StartLockFile, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    } catch {
        $LockItem = Get-Item $StartLockFile -ErrorAction SilentlyContinue
        if ($LockItem -and ((Get-Date) - $LockItem.LastWriteTime).TotalMinutes -gt 5) {
            Remove-Item $StartLockFile -Force -ErrorAction SilentlyContinue
            continue
        }
        Start-Sleep -Milliseconds 200
    }
}
if ($script:StartLock -eq $null) {
    Write-Warning "Another start is in progress; continuing without the lock."
}
function Release-StartLock {
    if ($script:StartLock -ne $null) {
        $script:StartLock.Close()
        Remove-Item $StartLockFile -Force -ErrorAction SilentlyContinue
        $script:StartLock = $null
    }
}

# Session tracking: Use refcount (PID-based tracking is unreliable on Windows).
# Serialize the read-modify-write so concurrent starts do not lose updates.
$RefcountMutex = New-Object System.Threading.Mutex($false, "Local\cc-discord-presence-refcount")
try {
    $RefcountLocked = $RefcountMutex.WaitOne(10000)
} catch [System.Threading.AbandonedMutexException] {
    $RefcountLocked = $true
}
try {
    $CurrentCount = 0
    if (Test-Path $RefcountFile) {
        $RawCount = [string](Get-Content $RefcountFile -Raw)
        [int]::TryParse($RawCount.Trim(), [ref]$CurrentCount) | Out-Null
    }
    $ActiveSessions = $CurrentCount + 1
    $ActiveSessions | Out-File -FilePath $RefcountFile -Encoding ASCII -NoNewline
} finally {
    if ($RefcountLocked) { $RefcountMutex.ReleaseMutex() }
    $RefcountMutex.Dispose()
}

$BinaryName = "cc-discord-presence-windows-amd64.exe"
$Binary = Join-Path $BinDir $BinaryName
$VersionFile = "$Binary.version"
$InstalledVersion = if (Test-Path $VersionFile) { ([string](Get-Content $VersionFile -Raw)).Trim() } else { "" }

# If the daemon is already running, just exit, unless it runs a binary from
# another release: then stop it so the current one is downloaded and started.
# (claude plugin update replaces this script, never the binary.)
if (Test-Path $PidFile) {
    $OldPid = 0
    $RawPid = [string](Get-Content $PidFile -Raw -ErrorAction SilentlyContinue)
    [int]::TryParse($RawPid.Trim(), [ref]$OldPid) | Out-Null
    if ($OldPid -gt 0) {
        $Process = Get-Process -Id $OldPid -ErrorAction SilentlyContinue | Where-Object { $_.ProcessName -like "cc-discord-presence*" }
        if ($Process) {
            if ($InstalledVersion -eq $Version) {
                Write-Host "Discord Rich Presence already running (PID: $OldPid, sessions: $ActiveSessions)"
                Release-StartLock
                exit 0
            }
            Write-Host "Updating the running daemon to $Version..."
            Stop-Process -Id $OldPid -Force -ErrorAction SilentlyContinue
            Start-Sleep -Seconds 1
        }
    }
}

# Download the binary when it is missing or from another release. The
# version file beside it records which release it came from; installs from
# before it existed have none and update once.
if (-not (Test-Path $Binary) -or $InstalledVersion -ne $Version) {
    Write-Host "Downloading cc-discord-presence $Version for windows-amd64..."

    $DownloadUrl = "https://github.com/$Repo/releases/download/$Version/$BinaryName"
    $TmpBinary = "$Binary.download"

    try {
        Invoke-WebRequest -Uri $DownloadUrl -OutFile $TmpBinary -UseBasicParsing
        Move-Item -Path $TmpBinary -Destination $Binary -Force
        $Version | Out-File -FilePath $VersionFile -Encoding ASCII -NoNewline
        Write-Host "Downloaded successfully!"
    } catch {
        # Keep a binary from an earlier release rather than none
        Remove-Item -Path $TmpBinary -ErrorAction SilentlyContinue
        if (-not (Test-Path $Binary)) {
            Write-Error "Failed to download binary: $_"
            Release-StartLock
            exit 1
        }
        Write-Warning "Download failed; using the existing binary: $_"
    }

    # Retire a running daemon so the block below starts the new binary
    if (Test-Path $PidFile) {
        $OldDaemon = 0
        $RawDaemon = [string](Get-Content $PidFile -Raw -ErrorAction SilentlyContinue)
        [int]::TryParse($RawDaemon.Trim(), [ref]$OldDaemon) | Out-Null
        if ($OldDaemon -gt 0) {
            Stop-Process -Id $OldDaemon -Force -ErrorAction SilentlyContinue
        }
        Remove-Item $PidFile -ErrorAction SilentlyContinue
    }
}

if (-not (Test-Path $Binary)) {
    Write-Error "Error: Binary not found at $Binary"
    Release-StartLock
    exit 1
}

# Start the daemon in background. CCDP_MANAGED lets it clear the presence and
# exit after the last session ends; manually started daemons stay running.
$env:CCDP_MANAGED = "1"
$Process = Start-Process -FilePath $Binary -NoNewWindow -PassThru -RedirectStandardOutput $LogFile -RedirectStandardError "$LogFile.err"
$Process.Id | Out-File -FilePath $PidFile -Encoding ASCII

Release-StartLock
Write-Host "Discord Rich Presence started (PID: $($Process.Id), sessions: $ActiveSessions)"
