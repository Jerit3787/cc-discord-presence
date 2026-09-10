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

// modelIDDatePattern matches a trailing 8-digit snapshot date, e.g. "-20241022".
var modelIDDatePattern = regexp.MustCompile(`-\d{8}$`)

// modelTierPattern extracts "<tier> <major>[.<minor>]" from a model ID,
// e.g. "claude-sonnet-4-5-20241022" -> sonnet, 4, 5.
var modelTierPattern = regexp.MustCompile(`claude-(opus|sonnet|haiku|fable)-(\d+)(?:-(\d+))?`)

// normalizeModelID strips a trailing -YYYYMMDD snapshot date.
func normalizeModelID(modelID string) string {
	return modelIDDatePattern.ReplaceAllString(modelID, "")
}

// StatusLineData matches Claude Code's statusline JSON structure
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
	sessionStartTime = time.Now()
	discordClient    *discord.Client
	usingFallback    bool
	nudgeShown       bool

	// shownKey identifies the session currently on the presence ("sl:<id>" for
	// statusline data, "jl:<path>" for a JSONL transcript). shownSince is when
	// that session first took the presence, used for the Discord elapsed timer.
	shownKey   string
	shownSince time.Time
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

func init() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting home directory: %v\n", err)
		os.Exit(1)
	}
	claudeDir = filepath.Join(home, ".claude")
	projectsDir = filepath.Join(claudeDir, "projects")
	dataFilePath = filepath.Join(claudeDir, "discord-presence-data.json")
}

func main() {
	fmt.Println(`
╔═══════════════════════════════════════════════════════════╗
║     Clawd Code - Discord Rich Presence                    ║
║     Show your Claude Code session on Discord!             ║
╚═══════════════════════════════════════════════════════════╝`)

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\n⏹ Shutting down...")
		if discordClient != nil {
			discordClient.Close()
		}
		os.Exit(0)
	}()

	// Connect to Discord, retrying until it is reachable. Claude Code is often
	// launched before Discord; exiting here would leave the presence dead for
	// the rest of the session.
	fmt.Println("🔗 Connecting to Discord...")
	discordClient = discord.NewClient(ClientID)
	for attempt := 1; ; attempt++ {
		if err := discordClient.Connect(); err == nil {
			break
		} else if attempt == 1 {
			fmt.Fprintf(os.Stderr, "⏳ Discord not reachable (%v); retrying every %s...\n", err, ReconnectInterval)
		}
		time.Sleep(ReconnectInterval)
	}
	fmt.Println("✓ Discord RPC connected!")

	// Try initial read and show data source
	if session := readSessionData(); session != nil {
		updatePresence(session)
		if usingFallback {
			fmt.Printf("✓ Found active session: %s (using JSONL fallback)\n", session.ProjectName)
		} else {
			fmt.Printf("✓ Found active session: %s (using statusline data)\n", session.ProjectName)
		}
	} else {
		fmt.Println("⏳ Waiting for Claude Code session...")
	}

	fmt.Println("🎮 Discord Rich Presence is now active! Press Ctrl+C to stop.")

	// Start watching for changes
	watchForChanges()
}

func readStatusLineData() *SessionData {
	data, err := os.ReadFile(dataFilePath)
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
	if modelName == "" {
		modelName = formatModelName(statusLine.Model.ID)
	}

	return &SessionData{
		ProjectName: projectName,
		ProjectPath: projectPath,
		GitBranch:   getGitBranch(projectPath),
		ModelName:   modelName,
		TotalTokens: statusLine.ContextWindow.TotalInputTokens + statusLine.ContextWindow.TotalOutputTokens,
		TotalCost:   statusLine.Cost.TotalCostUSD,
		StartTime:   markShown("sl:" + statusLine.SessionID),
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

// pickJSONLSession chooses which transcript to display, applying a stickiness
// rule: keep showing the session already on the presence until it has gone
// quiet for StickyQuietPeriod, then switch to whichever session is now most
// active. This stops the presence flickering between projects when several
// Claude Code sessions run concurrently.
func pickJSONLSession() (string, string, error) {
	files, err := listJSONLSessions()
	if err != nil {
		return "", "", err
	}

	newest := files[0]

	// Is the currently displayed session still one of the known transcripts?
	if strings.HasPrefix(shownKey, "jl:") {
		currentPath := strings.TrimPrefix(shownKey, "jl:")
		for _, f := range files {
			if f.path != currentPath {
				continue
			}
			// Keep the current session unless it has been quiet too long.
			if time.Since(f.modTime) < StickyQuietPeriod {
				return f.path, f.projectPath, nil
			}
			break
		}
	}

	return newest.path, newest.projectPath, nil
}

// parseJSONLSession parses a JSONL file and extracts session data
func parseJSONLSession(jsonlPath, _ string) *SessionData {
	file, err := os.Open(jsonlPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var (
		totalInputTokens   int64
		totalOutputTokens  int64
		totalCacheRead     int64
		totalCacheCreation int64
		lastModel          string
		projectPath        string
		firstTimestamp     string
	)

	scanner := bufio.NewScanner(file)
	// Increase buffer size for large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var msg JSONLMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}

		if firstTimestamp == "" && msg.Timestamp != "" {
			firstTimestamp = msg.Timestamp
		}

		// Extract cwd from any message that has it (usually first message)
		if msg.Cwd != "" && projectPath == "" {
			projectPath = msg.Cwd
		}

		// Only process assistant messages with usage data
		if msg.Type == "assistant" && msg.Message.Model != "" {
			lastModel = msg.Message.Model
			totalInputTokens += msg.Message.Usage.InputTokens
			totalOutputTokens += msg.Message.Usage.OutputTokens
			totalCacheRead += msg.Message.Usage.CacheReadTokens
			totalCacheCreation += msg.Message.Usage.CacheCreationTokens
		}
	}

	if lastModel == "" {
		return nil
	}

	totalCost := calculateCost(lastModel, totalInputTokens, totalOutputTokens, totalCacheRead, totalCacheCreation)
	modelName := formatModelName(lastModel)

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
		TotalTokens: totalInputTokens + totalOutputTokens + totalCacheRead + totalCacheCreation,
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

// readSessionData tries statusline data first, then falls back to JSONL parsing
func readSessionData() *SessionData {
	// First try statusline data (most accurate)
	if data := readStatusLineData(); data != nil {
		if usingFallback {
			usingFallback = false
			fmt.Println("📊 Now using statusline data (more accurate)")
		}
		return data
	}

	// Fall back to JSONL parsing, with sticky session selection.
	jsonlPath, projectPath, err := pickJSONLSession()
	if err != nil {
		return nil
	}

	if !usingFallback && !nudgeShown {
		usingFallback = true
		nudgeShown = true
		fmt.Println("\n💡 Tip: For more accurate token/cost data, configure the statusline wrapper.")
		fmt.Println("   See: https://github.com/Jerit3787/cc-discord-presence#statusline-setup")
	}

	session := parseJSONLSession(jsonlPath, projectPath)
	if session == nil {
		return nil
	}

	// Record which session now holds the presence; use the takeover time for the
	// elapsed timer when the transcript had no parseable start timestamp.
	takeover := markShown("jl:" + jsonlPath)
	if session.StartTime.IsZero() {
		session.StartTime = takeover
	}
	return session
}

func updatePresence(session *SessionData) {
	// Build details line with prefix
	details := fmt.Sprintf("Working on: %s", session.ProjectName)
	if session.GitBranch != "" {
		details = fmt.Sprintf("Working on: %s (%s)", session.ProjectName, session.GitBranch)
	}

	// Build state line: model | tokens | cost
	state := fmt.Sprintf("%s | %s tokens | $%.4f",
		session.ModelName,
		formatNumber(session.TotalTokens),
		session.TotalCost)

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
		}
	}
}

func formatNumber(n int64) string {
	if n >= 1_000_000 {
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

	// Watch both the main claude dir (for statusline data) and projects dir (for JSONL fallback)
	if err := watcher.Add(claudeDir); err != nil {
		fmt.Println("Using polling mode for session tracking")
		pollForChanges()
		return
	}

	// Also poll as backup (especially important for JSONL which is in subdirs)
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Respond to statusline data file changes
			if filepath.Base(event.Name) == "discord-presence-data.json" {
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
			// Poll reads from either statusline or JSONL fallback
			if session := readSessionData(); session != nil {
				updatePresence(session)
			}
		}
	}
}

func pollForChanges() {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for range ticker.C {
		if session := readSessionData(); session != nil {
			updatePresence(session)
		}
	}
}
