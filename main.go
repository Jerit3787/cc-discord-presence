package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/tsanva/cc-discord-presence/discord"
)

const (
	// Discord Application ID for "Clawd Code"
	ClientID = "1455326944060248250"

	// Polling interval as fallback
	PollInterval = 3 * time.Second

	// StickyQuietPeriod is how long the currently displayed session must go
	// without transcript activity before the presence switches to a different
	// (more recently active) session. Prevents the presence from flickering
	// between projects when several Claude Code sessions run at once.
	StickyQuietPeriod = 75 * time.Second

	// ReconnectInterval is how long to wait between attempts to (re)connect to
	// Discord when it is not reachable.
	ReconnectInterval = 15 * time.Second

	// PresenceHeartbeatInterval re-sends an unchanged activity periodically so
	// a Discord restart cannot leave the presence blank until the text changes.
	PresenceHeartbeatInterval = 30 * time.Second
)

// modelPricing is first-party Claude API list pricing in USD per million tokens
// (input, output). Keys are normalized model IDs: any trailing -YYYYMMDD
// snapshot date is stripped before lookup, so "claude-sonnet-4-5-20241022" and
// "claude-sonnet-4-5" share an entry. Unknown IDs fall back to defaultPricing.
// Source: https://docs.anthropic.com/en/docs/about-claude/pricing
var modelPricing = map[string]struct{ Input, Output float64 }{
	// Current generation
	"claude-opus-5":     {5.0, 25.0},
	"claude-sonnet-5":   {2.0, 10.0},
	"claude-haiku-4-5":  {1.0, 5.0},
	"claude-fable-5":    {10.0, 50.0},
	"claude-fable-5-1":  {10.0, 50.0},
	"claude-opus-4-8":   {5.0, 25.0},
	"claude-opus-4-7":   {5.0, 25.0},
	"claude-opus-4-6":   {5.0, 25.0},
	"claude-sonnet-4-6": {3.0, 15.0},
	// Previous generation
	"claude-opus-4-5":   {15.0, 75.0},
	"claude-opus-4-1":   {15.0, 75.0},
	"claude-opus-4":     {15.0, 75.0},
	"claude-sonnet-4-5": {3.0, 15.0},
	"claude-sonnet-4":   {3.0, 15.0},
	"claude-haiku-3-5":  {0.8, 4.0},
}

// defaultPricing is used when a model ID matches no known entry. Sonnet-tier
// rates are the least-surprising middle ground for an unrecognized model.
var defaultPricing = struct{ Input, Output float64 }{3.0, 15.0}

// modelDisplayNames overrides the tier heuristic for specific normalized IDs.
var modelDisplayNames = map[string]string{
	"claude-sonnet-4": "Sonnet 4",
}

// modelIDQualifierPattern matches a trailing context qualifier, e.g. "[1m]".
var modelIDQualifierPattern = regexp.MustCompile(`\[[^\]]*\]$`)

// modelIDDatePattern matches a trailing 8-digit snapshot date, e.g. "-20241022".
var modelIDDatePattern = regexp.MustCompile(`-\d{8}$`)

// modelTierPattern extracts "<tier> <major>[.<minor>]" from a model ID,
// e.g. "claude-sonnet-4-5-20241022" -> sonnet, 4, 5.
var modelTierPattern = regexp.MustCompile(`claude-(opus|sonnet|haiku|fable)-(\d+)(?:-(\d+))?`)

// normalizeModelID strips a trailing context qualifier and -YYYYMMDD snapshot
// date, e.g. "claude-opus-4-8[1m]" -> "claude-opus-4-8".
func normalizeModelID(modelID string) string {
	modelID = modelIDQualifierPattern.ReplaceAllString(modelID, "")
	return modelIDDatePattern.ReplaceAllString(modelID, "")
}

// StatusLineData matches Claude Code's statusline JSON structure. The hooks
// module (hooks/register.ts) writes the same shape to its own file.
type StatusLineData struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Model     struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
	Cost struct {
		TotalCostUSD       float64 `json:"total_cost_usd"`
		TotalDurationMS    int64   `json:"total_duration_ms"`
		TotalAPIDurationMS int64   `json:"total_api_duration_ms"`
	} `json:"cost"`
	ContextWindow struct {
		TotalInputTokens  int64 `json:"total_input_tokens"`
		TotalOutputTokens int64 `json:"total_output_tokens"`
	} `json:"context_window"`
}

// SessionData holds parsed session information
type SessionData struct {
	ProjectName string
	ProjectPath string
	GitBranch   string
	ModelName   string
	TotalTokens int64
	TotalCost   float64
	StartTime   time.Time

	// IsCloud marks a Claude Code cloud session. Cloud transcripts and token
	// counts live on Anthropic's infrastructure, so only locally recorded
	// model and cost are available.
	IsCloud bool

	// sessionID identifies a hooks-module/statusline/cloud source for sticky
	// selection. It is not displayed.
	sessionID string
}

// JSONLMessage represents a message entry in JSONL files
type JSONLMessage struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	Message   struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens         int64 `json:"input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			CacheReadTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

var (
	claudeDir        string
	projectsDir      string
	dataFilePath     string
	moduleFilePath   string
	sessionStartTime = time.Now()
	discordClient    *discord.Client
	currentSource    string
	nudgeShown       bool
	lastPresence     string
	lastPresenceAt   time.Time

	// cloudSessionsSource is a variable so tests can make cloud detection
	// deterministic. The cached reader is used in production.
	cloudSessionsSource = readCloudSessionsCached

	// shownKey identifies the session currently on the presence ("sl:<id>" for
	// statusline data, "module:<id>" for the hooks module, "jl:<path>" for a
	// JSONL transcript, "cloud:<id>" for a cloud session). shownSince is when
	// that session first took the presence, used for the Discord elapsed timer.
	shownKey   string
	shownSince time.Time
)

// Where session data came from, in the order readSessionData tries them.
const (
	sourceCloud      = "cloud session data"
	sourceModule     = "hooks module"
	sourceStatusLine = "statusline data"
	sourceJSONL      = "JSONL fallback"
)

// markShown records which session is on the presence. When the session changes
// it resets the elapsed timer; otherwise it returns the existing start time.
func markShown(key string) time.Time {
	if key != shownKey {
		shownKey = key
		shownSince = time.Now()
	}
	return shownSince
}

// fileSessionCache is the hooks-module/statusline session currently selected.
// Those data sources each use a single last-writer-wins file, so when another
// concurrent session overwrites it, keep the previous session for
// StickyQuietPeriod instead of flickering between projects.
type fileSessionCache struct {
	data     *SessionData
	id       string
	source   string
	lastSeen time.Time
}

var fileSession fileSessionCache

// applyFileStickiness returns the file-backed session to display. It updates
// the cache when the same session is seen again and holds the previous session
// until it has been quiet for StickyQuietPeriod.
func applyFileStickiness(candidate *SessionData, source string) (*SessionData, string) {
	now := time.Now()
	if candidate == nil {
		return nil, ""
	}

	if fileSession.data != nil && fileSession.id == candidate.sessionID {
		candidate.StartTime = fileSession.data.StartTime
		fileSession.data = candidate
		fileSession.source = source
		fileSession.lastSeen = now
		return candidate, source
	}

	if shownKey == "file:"+fileSession.id && fileSession.data != nil &&
		time.Since(fileSession.lastSeen) < StickyQuietPeriod {
		return fileSession.data, fileSession.source
	}

	candidate.StartTime = markShown("file:" + candidate.sessionID)
	fileSession = fileSessionCache{
		data:     candidate,
		id:       candidate.sessionID,
		source:   source,
		lastSeen: now,
	}
	return candidate, source
}

func init() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting home directory: %v\n", err)
		os.Exit(1)
	}
	claudeDir = filepath.Join(home, ".claude")
	projectsDir = filepath.Join(claudeDir, "projects")
	dataFilePath = filepath.Join(claudeDir, "discord-presence-data.json")
	moduleFilePath = filepath.Join(claudeDir, "discord-presence-module.json")
}

func main() {
	fmt.Println(`
╔═══════════════════════════════════════════════════════════╗
║     Clawd Code - Discord Rich Presence                    ║
║     Show your Claude Code session on Discord!             ║
╚═══════════════════════════════════════════════════════════╝`)

	discordClient = discord.NewClient(ClientID)

	// Setup graceful shutdown. The client is created first so the signal
	// handler never races with assigning it.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\n⏹ Shutting down...")
		discordClient.Close()
		os.Exit(0)
	}()

	// Connect to Discord, retrying until it is reachable. Claude Code is often
	// launched before Discord; exiting here would leave the presence dead for
	// the rest of the session. Keep checking session liveness while waiting so
	// a daemon started with no session does not linger forever.
	fmt.Println("🔗 Connecting to Discord...")
	var noSessionsSince time.Time
	for attempt := 1; ; attempt++ {
		if err := discordClient.Connect(); err == nil {
			break
		} else if attempt == 1 {
			fmt.Fprintf(os.Stderr, "⏳ Discord not reachable (%v); retrying every %s...\n", err, ReconnectInterval)
		}
		shutdownIfIdle(&noSessionsSince)
		time.Sleep(ReconnectInterval)
	}
	fmt.Println("✓ Discord RPC connected!")

	// Try initial read and show data source
	if session := readSessionData(); session != nil {
		updatePresence(session)
		fmt.Printf("✓ Found active session: %s (using %s)\n", session.ProjectName, currentSource)
	} else {
		fmt.Println("⏳ Waiting for Claude Code session...")
	}

	fmt.Println("🎮 Discord Rich Presence is now active! Press Ctrl+C to stop.")

	// Start watching for changes
	watchForChanges()
}

// fileNewer reports whether path a exists and is newer than path b (or b is
// missing). It is used to avoid preferring a stale hooks-module file over a
// newer statusline file after an abnormal session exit.
func fileNewer(a, b string) bool {
	aInfo, err := os.Stat(a)
	if err != nil {
		return false
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		return true
	}
	return aInfo.ModTime().After(bInfo.ModTime())
}

func readStatusLineData() *SessionData {
	return readPresenceFile(dataFilePath)
}

// readModuleData reads the file the plugin's hooks module writes. The module
// blanks it to "{}" when its session ends, which reads as no data.
func readModuleData() *SessionData {
	return readPresenceFile(moduleFilePath)
}

func readPresenceFile(path string) *SessionData {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var statusLine StatusLineData
	if err := json.Unmarshal(data, &statusLine); err != nil {
		return nil
	}

	if statusLine.SessionID == "" {
		return nil
	}

	projectPath := statusLine.Workspace.ProjectDir
	if projectPath == "" {
		projectPath = statusLine.Cwd
	}

	projectName := filepath.Base(projectPath)
	if projectName == "" || projectName == "." {
		projectName = "Unknown Project"
	}

	modelName := statusLine.Model.DisplayName
	if modelName == "" || modelName == statusLine.Model.ID {
		modelName = formatModelName(statusLine.Model.ID)
	}

	return &SessionData{
		ProjectName: projectName,
		ProjectPath: projectPath,
		GitBranch:   getGitBranch(projectPath),
		ModelName:   modelName,
		// Context size: the last request's input, cached tokens included.
		TotalTokens: statusLine.ContextWindow.TotalInputTokens,
		TotalCost:   statusLine.Cost.TotalCostUSD,
		sessionID:   statusLine.SessionID,
	}
}

func getGitBranch(projectPath string) string {
	if projectPath == "" {
		return ""
	}

	cmd := exec.Command("git", "-C", projectPath, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	branch := strings.TrimSpace(string(output))

	// If HEAD (no commits yet), try to get the branch name from symbolic-ref
	if branch == "HEAD" {
		cmd = exec.Command("git", "-C", projectPath, "symbolic-ref", "--short", "HEAD")
		output, err = cmd.Output()
		if err == nil {
			branch = strings.TrimSpace(string(output))
		}
	}

	return branch
}

// jsonlSession is one Claude Code transcript file with its decoded project path
// and last-modified time.
type jsonlSession struct {
	path        string
	projectPath string
	modTime     time.Time
}

// listJSONLSessions returns every transcript under ~/.claude/projects/, sorted
// most-recently-modified first.
func listJSONLSessions() ([]jsonlSession, error) {
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("projects directory does not exist")
	}

	var files []jsonlSession

	err := filepath.WalkDir(projectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		// Extract project path from the directory structure
		// ~/.claude/projects/<encoded-path>/<session>.jsonl
		// Encoded path uses dashes: -Users-vasantpns-Developer-project
		relPath, _ := filepath.Rel(projectsDir, path)
		parts := strings.SplitN(relPath, string(filepath.Separator), 2)
		if len(parts) < 1 {
			return nil
		}

		// Decode the project path
		// Claude Code encodes paths: / becomes -, and literal - becomes --
		// Example: /Users/foo/my-project -> -Users-foo-my--project
		// Must decode -- to - FIRST, then decode single - to /
		encodedPath := parts[0]
		// Use a placeholder for double dashes (escaped literal dashes)
		projectPath := strings.ReplaceAll(encodedPath, "--", "\x00")
		// Convert single dashes to path separators
		projectPath = strings.ReplaceAll(projectPath, "-", "/")
		// Restore literal dashes from placeholder
		projectPath = strings.ReplaceAll(projectPath, "\x00", "-")

		files = append(files, jsonlSession{
			path:        path,
			projectPath: projectPath,
			modTime:     info.ModTime(),
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no JSONL files found")
	}

	// Sort by modification time, most recent first
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	return files, nil
}

// findMostRecentJSONL returns the single most recently modified transcript.
func findMostRecentJSONL() (string, string, error) {
	files, err := listJSONLSessions()
	if err != nil {
		return "", "", err
	}
	return files[0].path, files[0].projectPath, nil
}

// orderedJSONLSessions returns transcripts in display order. The currently
// displayed transcript stays first while it is still active, so concurrent
// sessions do not flicker; otherwise the newest transcript is first.
func orderedJSONLSessions() ([]jsonlSession, error) {
	files, err := listJSONLSessions()
	if err != nil {
		return nil, err
	}

	if strings.HasPrefix(shownKey, "jl:") {
		currentPath := strings.TrimPrefix(shownKey, "jl:")
		for i, f := range files {
			if f.path == currentPath && time.Since(f.modTime) < StickyQuietPeriod {
				files[0], files[i] = files[i], files[0]
				break
			}
		}
	}
	return files, nil
}

// pickJSONLSession chooses which transcript to display.
func pickJSONLSession() (string, string, error) {
	files, err := orderedJSONLSessions()
	if err != nil {
		return "", "", err
	}
	return files[0].path, files[0].projectPath, nil
}

// parseJSONLSession parses a JSONL file and extracts session data
func parseJSONLSession(jsonlPath, _ string) *SessionData {
	file, err := os.Open(jsonlPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	type modelUsage struct {
		input, output, cacheRead, cacheCreation int64
	}

	var (
		usageByModel   = make(map[string]*modelUsage)
		contextTokens  int64
		lastModel      string
		projectPath    string
		firstTimestamp string
	)

	// A transcript line can be many MB (pasted images, large file reads / diffs).
	// bufio.Scanner aborts the whole scan on the first line past its buffer cap,
	// which would freeze the token total at that point forever, so read
	// line-by-line with a Reader that grows to fit each line instead.
	reader := bufio.NewReaderSize(file, 256*1024)
	for {
		line, readErr := reader.ReadBytes('\n')

		if len(line) > 0 {
			var msg JSONLMessage
			if json.Unmarshal(line, &msg) == nil {
				if firstTimestamp == "" && msg.Timestamp != "" {
					firstTimestamp = msg.Timestamp
				}
				// cwd usually appears on the first message
				if msg.Cwd != "" && projectPath == "" {
					projectPath = msg.Cwd
				}
				// Only assistant messages carry model + usage
				if msg.Type == "assistant" && msg.Message.Model != "" {
					lastModel = msg.Message.Model
					usage := msg.Message.Usage
					modelUse := usageByModel[lastModel]
					if modelUse == nil {
						modelUse = &modelUsage{}
						usageByModel[lastModel] = modelUse
					}
					modelUse.input += usage.InputTokens
					modelUse.output += usage.OutputTokens
					modelUse.cacheRead += usage.CacheReadTokens
					modelUse.cacheCreation += usage.CacheCreationTokens
					// Context size: the latest request's input, cached tokens included.
					contextTokens = usage.InputTokens + usage.CacheCreationTokens + usage.CacheReadTokens
				}
			}
		}

		if readErr != nil {
			break // io.EOF, or an unrecoverable read error
		}
	}

	if lastModel == "" {
		return nil
	}

	totalCost := 0.0
	for model, usage := range usageByModel {
		totalCost += calculateCost(model, usage.input, usage.output, usage.cacheRead, usage.cacheCreation)
	}
	modelName := formatModelName(lastModel)

	// Displayed token count is the context size (the last request's input,
	// cached tokens included), matching the hooks module and statusline sources.
	// Cost keeps using the cumulative totals above, including cache pricing.
	displayTokens := contextTokens

	projectName := filepath.Base(projectPath)
	if projectName == "" || projectName == "." {
		projectName = "Unknown Project"
	}

	// Elapsed time: prefer the real session start (first transcript timestamp).
	// A zero StartTime tells the caller to substitute the presence-takeover time.
	var startTime time.Time
	if firstTimestamp != "" {
		if t, err := time.Parse(time.RFC3339, firstTimestamp); err == nil {
			startTime = t
		}
	}

	return &SessionData{
		ProjectName: projectName,
		ProjectPath: projectPath,
		GitBranch:   getGitBranch(projectPath),
		ModelName:   modelName,
		TotalTokens: displayTokens,
		TotalCost:   totalCost,
		StartTime:   startTime,
	}
}

// Cache pricing multipliers relative to base input price (Anthropic pricing):
// reads are billed at 0.1x, 5-minute cache writes at 1.25x.
const (
	cacheReadMultiplier     = 0.1
	cacheCreationMultiplier = 1.25
)

// calculateCost estimates session cost from token usage and model pricing.
// Cache reads and cache-creation tokens are priced relative to the input rate.
func calculateCost(modelID string, inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int64) float64 {
	pricing, ok := modelPricing[normalizeModelID(modelID)]
	if !ok {
		pricing = defaultPricing
	}

	const perMillion = 1_000_000.0
	cost := float64(inputTokens) / perMillion * pricing.Input
	cost += float64(outputTokens) / perMillion * pricing.Output
	cost += float64(cacheReadTokens) / perMillion * pricing.Input * cacheReadMultiplier
	cost += float64(cacheCreationTokens) / perMillion * pricing.Input * cacheCreationMultiplier

	return cost
}

// formatModelName converts a model ID to a display name, e.g.
// "claude-sonnet-4-5-20241022" -> "Sonnet 4.5", "claude-opus-5" -> "Opus 5".
func formatModelName(modelID string) string {
	normalized := normalizeModelID(modelID)
	if name, ok := modelDisplayNames[normalized]; ok {
		return name
	}

	if m := modelTierPattern.FindStringSubmatch(normalized); m != nil {
		tier := strings.ToUpper(m[1][:1]) + m[1][1:]
		version := m[2]
		if m[3] != "" {
			version += "." + m[3]
		}
		return tier + " " + version
	}

	// Last-resort tier-only fallback.
	switch {
	case strings.Contains(normalized, "opus"):
		return "Opus"
	case strings.Contains(normalized, "sonnet"):
		return "Sonnet"
	case strings.Contains(normalized, "haiku"):
		return "Haiku"
	case strings.Contains(normalized, "fable"):
		return "Fable"
	}

	return "Claude"
}

// presenceFileHasSession reports whether a hooks-module or statusline file
// currently describes a session. The hooks module blanks its file to "{}" when
// the session ends; that write must not count as local activity.
func presenceFileHasSession(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var statusLine StatusLineData
	if err := json.Unmarshal(data, &statusLine); err != nil {
		return false
	}
	return statusLine.SessionID != ""
}

// latestLocalActivity returns the most recent modification time of the local
// data sources (hooks module, statusline file, or any transcript). Cloud
// sessions are only shown when their activity is newer than this, so returning
// to a local session takes the presence back.
func latestLocalActivity() time.Time {
	var latest time.Time

	for _, path := range []string{moduleFilePath, dataFilePath} {
		if info, err := os.Stat(path); err == nil && info.ModTime().After(latest) && presenceFileHasSession(path) {
			latest = info.ModTime()
		}
	}
	// Use the newest transcript across all local sessions, not the sticky
	// display selection: activity in any local session should beat a cloud one.
	if files, err := listJSONLSessions(); err == nil && len(files) > 0 && files[0].modTime.After(latest) {
		latest = files[0].modTime
	}
	return latest
}

// cloudSessionData converts a desktop cloud session into presence data. The
// cloud project/title is not stored locally, so a generic label is used rather
// than attributing the session to the wrong project.
func cloudSessionData(session *cloudSession) *SessionData {
	return &SessionData{
		ProjectName: "Cloud session",
		ModelName:   formatModelName(session.ModelID),
		TotalCost:   session.CostUSD,
		StartTime:   markShown("cloud:" + session.ID),
		IsCloud:     true,
	}
}

// readSessionData chooses the most recently active session. Cloud sessions are
// checked before local sources because a cloud session leaves no transcript
// locally and would otherwise be masked by the last local session.
func readSessionData() *SessionData {
	// Cloud sessions first: they leave no local transcript, so a stale local
	// session would otherwise mask them. The cloud check itself requires the
	// desktop app to be running and the cloud activity to be newer than any
	// local activity.
	if desktopAppRunningFn() {
		if sessions, err := cloudSessionsSource(); err == nil {
			if cloud := activeCloudSession(sessions, relevantLocalActivity(), time.Now()); cloud != nil {
				setSource(sourceCloud)
				return cloudSessionData(cloud)
			}
		}
	}

	// The hooks module is the primary local source, but only while its file is
	// at least as new as the statusline file. A module file left behind by a
	// crash must not mask a newer statusline session. Then JSONL as a last
	// resort.
	moduleFresh := fileNewer(moduleFilePath, dataFilePath)
	var candidate *SessionData
	var source string
	if moduleFresh {
		if candidate = readModuleData(); candidate != nil {
			source = sourceModule
		}
	}
	if candidate == nil {
		if candidate = readStatusLineData(); candidate != nil {
			source = sourceStatusLine
		}
	}
	if candidate == nil && !moduleFresh {
		if candidate = readModuleData(); candidate != nil {
			source = sourceModule
		}
	}
	if candidate != nil {
		data, chosenSource := applyFileStickiness(candidate, source)
		setSource(chosenSource)
		return data
	}

	// Fall back to JSONL parsing. Try candidates newest-first until one has
	// usable data; a freshly created/aborted transcript can otherwise hide a
	// valid session.
	files, err := orderedJSONLSessions()
	if err != nil {
		return nil
	}

	setSource(sourceJSONL)
	if !nudgeShown {
		nudgeShown = true
		fmt.Println("\n💡 Tip: For accurate token/cost data, update Claude Code (the plugin's hooks module")
		fmt.Println("   needs a recent version) or configure the statusline wrapper.")
		fmt.Println("   See: https://github.com/Jerit3787/cc-discord-presence#statusline-setup")
	}

	for _, file := range files {
		session := parseJSONLSession(file.path, file.projectPath)
		if session == nil {
			continue
		}

		// Record which session now holds the presence; use the takeover time
		// when the transcript had no parseable start timestamp.
		takeover := markShown("jl:" + file.path)
		if session.StartTime.IsZero() {
			session.StartTime = takeover
		}
		return session
	}
	return nil
}

// formatPresenceState builds the model | usage | cost line. Cloud sessions
// have no local token count, so they show a "cloud" marker instead.
func formatPresenceState(session *SessionData) string {
	if session.IsCloud {
		if session.TotalCost > 0 {
			return fmt.Sprintf("%s | cloud | $%.4f", session.ModelName, session.TotalCost)
		}
		return fmt.Sprintf("%s | cloud", session.ModelName)
	}
	return fmt.Sprintf("%s | %s tokens | $%.4f",
		session.ModelName,
		formatNumber(session.TotalTokens),
		session.TotalCost)
}

// setSource logs when the data source changes after startup.
func setSource(source string) {
	if currentSource != "" && currentSource != source {
		fmt.Printf("📊 Now using %s\n", source)
	}
	currentSource = source
}

// presenceText builds the two activity lines Discord shows.
func presenceText(session *SessionData) (details, state string) {
	details = fmt.Sprintf("Working on: %s", session.ProjectName)
	if session.GitBranch != "" {
		details = fmt.Sprintf("Working on: %s (%s)", session.ProjectName, session.GitBranch)
	}
	state = formatPresenceState(session)
	return details, state
}

// shouldSendPresence reports whether the presence should be pushed. Unchanged
// text is sent again periodically as a heartbeat so a Discord restart is
// detected and the presence is restored.
func shouldSendPresence(key, lastKey string, lastSentAt, now time.Time) bool {
	return key != lastKey || lastSentAt.IsZero() || now.Sub(lastSentAt) >= PresenceHeartbeatInterval
}

func updatePresence(session *SessionData) {
	details, state := presenceText(session)

	// The data files change often (the hooks module writes after every tool
	// call) and the poll runs every few seconds; only send what Discord
	// would show differently, with a periodic heartbeat.
	key := details + "\n" + state + "\n" + session.StartTime.String()
	now := time.Now()
	if !shouldSendPresence(key, lastPresence, lastPresenceAt, now) {
		return
	}

	activity := discord.Activity{
		Details:   details,
		State:     state,
		LargeText: "Clawd Code - Discord Rich Presence for Claude Code",
		StartTime: &session.StartTime,
	}

	if err := discordClient.SetActivity(activity); err != nil {
		// The IPC pipe breaks when Discord is quit or restarted. Try to
		// reconnect once and resend, rather than going silent until the next
		// Claude Code session starts.
		fmt.Fprintf(os.Stderr, "Presence update failed (%v); reconnecting to Discord...\n", err)
		if rerr := discordClient.Reconnect(); rerr != nil {
			fmt.Fprintf(os.Stderr, "Reconnect failed: %v\n", rerr)
			return
		}
		fmt.Println("✓ Reconnected to Discord")
		if err := discordClient.SetActivity(activity); err != nil {
			fmt.Fprintf(os.Stderr, "Error updating presence after reconnect: %v\n", err)
			return
		}
	}
	lastPresence = key
	lastPresenceAt = now
}

func formatNumber(n int64) string {
	if n >= 999_950 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	} else if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func watchForChanges() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Println("Using polling mode for session tracking")
		pollForChanges()
		return
	}
	defer watcher.Close()

	// Watch the main claude dir (module and statusline data files)
	if err := watcher.Add(claudeDir); err != nil {
		fmt.Println("Using polling mode for session tracking")
		pollForChanges()
		return
	}

	// Also poll as backup (especially important for JSONL which is in subdirs)
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	var noSessionsSince time.Time

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Respond to module and statusline data file changes
			if name := filepath.Base(event.Name); name == filepath.Base(moduleFilePath) || name == filepath.Base(dataFilePath) {
				if session := readSessionData(); session != nil {
					updatePresence(session)
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "Watcher error: %v\n", err)
		case <-ticker.C:
			// Poll reads from the hooks module, statusline, cloud sessions,
			// or JSONL fallback.
			if session := readSessionData(); session != nil {
				updatePresence(session)
			}
			shutdownIfIdle(&noSessionsSince)
		}
	}
}

func pollForChanges() {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	var noSessionsSince time.Time
	for range ticker.C {
		if session := readSessionData(); session != nil {
			updatePresence(session)
		}
		shutdownIfIdle(&noSessionsSince)
	}
}
