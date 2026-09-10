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
$Version = "v1.0.5"

# Ensure directories exist
New-Item -ItemType Directory -Path $ClaudeDir -Force | Out-Null
New-Item -ItemType Directory -Path $BinDir -Force | Out-Null

# Session tracking: Use refcount (PID-based tracking is unreliable on Windows)
$CurrentCount = 0
if (Test-Path $RefcountFile) {
    $CurrentCount = [int](Get-Content $RefcountFile -ErrorAction SilentlyContinue)
}
$ActiveSessions = $CurrentCount + 1
$ActiveSessions | Out-File -FilePath $RefcountFile -Encoding ASCII -NoNewline

# If daemon is already running, just exit
if (Test-Path $PidFile) {
    $OldPid = Get-Content $PidFile -ErrorAction SilentlyContinue
    if ($OldPid) {
        $Process = Get-Process -Id $OldPid -ErrorAction SilentlyContinue
        if ($Process) {
            Write-Host "Discord Rich Presence already running (PID: $OldPid, sessions: $ActiveSessions)"
            exit 0
        }
    }
}

$BinaryName = "cc-discord-presence-windows-amd64.exe"
$Binary = Join-Path $BinDir $BinaryName
$VersionFile = Join-Path $BinDir ".version"

# Download the binary if it is missing or from a different version
$InstalledVersion = ""
if (Test-Path $VersionFile) {
    $InstalledVersion = (Get-Content $VersionFile -ErrorAction SilentlyContinue)
}
if ((-not (Test-Path $Binary)) -or ($InstalledVersion -ne $Version)) {
    Write-Host "Downloading cc-discord-presence $Version for windows-amd64..."

    $DownloadUrl = "https://github.com/$Repo/releases/download/$Version/$BinaryName"

    try {
        Invoke-WebRequest -Uri $DownloadUrl -OutFile $Binary -UseBasicParsing
        $Version | Out-File -FilePath $VersionFile -Encoding ASCII -NoNewline
        Write-Host "Downloaded successfully!"
    } catch {
        Write-Error "Failed to download binary: $_"
        exit 1
    }

    # Retire a running daemon so the block below starts the new binary
    if (Test-Path $PidFile) {
        $OldDaemon = Get-Content $PidFile -ErrorAction SilentlyContinue
        if ($OldDaemon) {
            Stop-Process -Id $OldDaemon -Force -ErrorAction SilentlyContinue
        }
        Remove-Item $PidFile -ErrorAction SilentlyContinue
    }
}

if (-not (Test-Path $Binary)) {
    Write-Error "Error: Binary not found at $Binary"
    exit 1
}

# Start the daemon in background
$Process = Start-Process -FilePath $Binary -NoNewWindow -PassThru -RedirectStandardOutput $LogFile -RedirectStandardError $LogFile
$Process.Id | Out-File -FilePath $PidFile -Encoding ASCII

Write-Host "Discord Rich Presence started (PID: $($Process.Id), sessions: $ActiveSessions)"
