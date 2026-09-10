# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.4] - 2026-09-10

Reliability fork ([Jerit3787/cc-discord-presence](https://github.com/Jerit3787/cc-discord-presence)).

### Fixed
- **Presence flickering between projects** when several Claude Code sessions run
  at once. Selection is now sticky: the displayed session only changes after the
  current one has been quiet (no transcript writes) for 75s, then switches to
  whichever session is now most active.
- **Daemon exited permanently when Discord was not running** at session start
  (`os.Exit(1)`). It now retries the connection every 15s, and reconnects
  automatically if the IPC pipe breaks mid-session (Discord quit/restarted).
- **Session count drift.** A tracked PID is now verified to still be a `claude`
  process before it is counted, so PID reuse no longer keeps ended sessions
  "active" and prevents the daemon from ever shutting down.
- **Stale binary after an update.** `start.sh` only fetched the binary when it
  was missing, so `plugin update` kept running the old one. It now tracks the
  installed version and re-downloads (and restarts the daemon) on a mismatch.
- **Blank status line** when enabling the statusline integration without an
  existing `~/.claude/statusline.sh`. The wrapper now renders a default
  "dir | model | cost" line.

### Changed
- Model display names are derived from the model ID (e.g. `claude-sonnet-5` ->
  "Sonnet 5") instead of a hardcoded table that goes stale on every release.
  Added current-generation pricing entries.
- JSONL fallback now includes cache-read and cache-creation tokens in the token
  count, and prices them relative to the input rate, for a closer cost estimate.
- Discord elapsed timer uses the real session start (first transcript timestamp)
  rather than daemon uptime.

## [1.0.3] - 2026-01-20

### Added
- Windows support with native named pipe IPC ([@8bury](https://github.com/8bury)) [#2](https://github.com/tsanva/cc-discord-presence/pull/2)
  - PowerShell start script (`start.ps1`)
  - Windows binary cross-compilation
  - go-winio for named pipe communication
- Comprehensive test suite for both main package and discord client
  - `main_test.go`: Tests for calculateCost, formatModelName, formatNumber, readStatusLineData, parseJSONLSession, path decoding, findMostRecentJSONL
  - `discord/client_test.go`: Tests for IPC protocol, SetActivity, send/receive, frame format verification

### Contributors
- pedro ([@8bury](https://github.com/8bury)) - Windows support
- Claude ([@claude](https://github.com/claude)) - Test suite

## [1.0.2] - 2025-12-31

### Fixed
- Replaced reference counting with PID-based session tracking
  - Previous refcount could drift if sessions crashed or were killed
  - Now each session registers with its PID in `~/.claude/discord-presence-sessions/`
  - Orphaned sessions (dead PIDs) are automatically cleaned up
  - Self-healing: no manual intervention needed after crashes

## [1.0.1] - 2025-12-31

### Fixed
- Multi-instance support: daemon now stays running when multiple Claude Code instances are open
  - Added reference counting to track active instances
  - Daemon only stops when all instances are closed

## [1.0.0] - 2025-12-30

### Added
- Initial release
- Discord Rich Presence showing project name, git branch, model, tokens, and cost
- Two data sources: statusline integration (accurate) and JSONL fallback (zero-config)
- Cross-platform support: macOS (arm64/amd64), Linux (amd64/arm64), Windows (amd64)
- Automatic binary download on first run
- GitHub Actions workflow for automated releases

[Unreleased]: https://github.com/tsanva/cc-discord-presence/compare/v1.0.3...HEAD
[1.0.3]: https://github.com/tsanva/cc-discord-presence/compare/v1.0.2...v1.0.3
[1.0.2]: https://github.com/tsanva/cc-discord-presence/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/tsanva/cc-discord-presence/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/tsanva/cc-discord-presence/releases/tag/v1.0.0
