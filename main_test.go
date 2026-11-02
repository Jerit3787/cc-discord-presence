package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCalculateCost tests the cost calculation logic
func TestCalculateCost(t *testing.T) {
	tests := []struct {
		name          string
		modelID       string
		inputTokens   int64
		outputTokens  int64
		cacheRead     int64
		cacheCreation int64
		wantCost      float64
	}{
		{
			name:         "Opus 4.5 - basic usage",
			modelID:      "claude-opus-4-5-20251101",
			inputTokens:  1_000_000,
			outputTokens: 100_000,
			wantCost:     15.0 + 7.5, // $15/M input + $75/M * 0.1 output
		},
		{
			name:          "Cache tokens priced off input rate",
			modelID:       "claude-sonnet-4-5-20241022",
			inputTokens:   0,
			outputTokens:  0,
			cacheRead:     1_000_000, // $3/M * 0.1 = 0.30
			cacheCreation: 1_000_000, // $3/M * 1.25 = 3.75
			wantCost:      0.30 + 3.75,
		},
		{
			name:        "Unversioned current model ID",
			modelID:     "claude-sonnet-5",
			inputTokens: 1_000_000,
			wantCost:    2.0, // $2/M input
		},
		{
			name:         "Sonnet 4.5 - basic usage",
			modelID:      "claude-sonnet-4-5-20241022",
			inputTokens:  1_000_000,
			outputTokens: 100_000,
			wantCost:     3.0 + 1.5, // $3/M input + $15/M * 0.1 output
		},
		{
			name:         "Sonnet 4 - basic usage",
			modelID:      "claude-sonnet-4-20250514",
			inputTokens:  500_000,
			outputTokens: 50_000,
			wantCost:     1.5 + 0.75, // $3/M * 0.5 input + $15/M * 0.05 output
		},
		{
			name:        "Qualified 1M-context model ID",
			modelID:     "claude-opus-4-8[1m]",
			inputTokens: 1_000_000,
			wantCost:    5.0,
		},
		{
			name:         "Haiku 4.5 - basic usage",
			modelID:      "claude-haiku-4-5-20241022",
			inputTokens:  2_000_000,
			outputTokens: 200_000,
			wantCost:     2.0 + 1.0, // $1/M * 2 input + $5/M * 0.2 output
		},
		{
			name:         "Unknown model - defaults to Sonnet 4",
			modelID:      "claude-unknown-model-20991231",
			inputTokens:  1_000_000,
			outputTokens: 1_000_000,
			wantCost:     3.0 + 15.0, // Sonnet 4 pricing as fallback
		},
		{
			name:         "Zero tokens",
			modelID:      "claude-opus-4-5-20251101",
			inputTokens:  0,
			outputTokens: 0,
			wantCost:     0,
		},
		{
			name:         "Small token count",
			modelID:      "claude-sonnet-4-20250514",
			inputTokens:  1000,
			outputTokens: 500,
			wantCost:     0.003 + 0.0075, // $3/M * 0.001 + $15/M * 0.0005
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateCost(tt.modelID, tt.inputTokens, tt.outputTokens, tt.cacheRead, tt.cacheCreation)
			// Use approximate comparison for floating point
			if diff := got - tt.wantCost; diff > 0.0001 || diff < -0.0001 {
				t.Errorf("calculateCost(%q, %d, %d, %d, %d) = %v, want %v", tt.modelID, tt.inputTokens, tt.outputTokens, tt.cacheRead, tt.cacheCreation, got, tt.wantCost)
			}
		})
	}
}

// TestFormatModelName tests the model name formatting logic
func TestFormatModelName(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		want    string
	}{
		{
			name:    "Known model - Opus 4.5",
			modelID: "claude-opus-4-5-20251101",
			want:    "Opus 4.5",
		},
		{
			name:    "Known model - Sonnet 4.5",
			modelID: "claude-sonnet-4-5-20241022",
			want:    "Sonnet 4.5",
		},
		{
			name:    "Known model - Sonnet 4",
			modelID: "claude-sonnet-4-20250514",
			want:    "Sonnet 4",
		},
		{
			name:    "Known model - Haiku 4.5",
			modelID: "claude-haiku-4-5-20241022",
			want:    "Haiku 4.5",
		},
		{
			name:    "Newer model - version derived from ID",
			modelID: "claude-opus-5-20260101",
			want:    "Opus 5",
		},
		{
			name:    "Qualified 1M-context model ID",
			modelID: "claude-opus-4-8[1m]",
			want:    "Opus 4.8",
		},
		{
			name:    "Newer model - unversioned ID",
			modelID: "claude-sonnet-5",
			want:    "Sonnet 5",
		},
		{
			name:    "Newer haiku model",
			modelID: "claude-haiku-5-20260101",
			want:    "Haiku 5",
		},
		{
			name:    "Tier only, no parseable version",
			modelID: "claude-opus",
			want:    "Opus",
		},
		{
			name:    "Completely unknown model - defaults to Claude",
			modelID: "some-unknown-model",
			want:    "Claude",
		},
		{
			name:    "Empty model ID",
			modelID: "",
			want:    "Claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatModelName(tt.modelID); got != tt.want {
				t.Errorf("formatModelName(%q) = %q, want %q", tt.modelID, got, tt.want)
			}
		})
	}
}

// TestFormatNumber tests the number formatting logic
func TestFormatNumber(t *testing.T) {
	tests := []struct {
		name string
		n    int64
		want string
	}{
		{name: "Zero", n: 0, want: "0"},
		{name: "Small number", n: 123, want: "123"},
		{name: "999 - no suffix", n: 999, want: "999"},
		{name: "1000 - K suffix", n: 1000, want: "1.0K"},
		{name: "1500 - K suffix", n: 1500, want: "1.5K"},
		{name: "10000 - K suffix", n: 10000, want: "10.0K"},
		{name: "999949 - K suffix", n: 999949, want: "999.9K"},
		{name: "999999 - rounds to M", n: 999999, want: "1.0M"},
		{name: "1000000 - M suffix", n: 1_000_000, want: "1.0M"},
		{name: "1500000 - M suffix", n: 1_500_000, want: "1.5M"},
		{name: "10000000 - M suffix", n: 10_000_000, want: "10.0M"},
		{name: "Large number", n: 150_000_000, want: "150.0M"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatNumber(tt.n); got != tt.want {
				t.Errorf("formatNumber(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

// TestReadStatusLineData tests reading and parsing statusline JSON
func TestReadStatusLineData(t *testing.T) {
	// Save original values
	origDataFilePath := dataFilePath
	origSessionStartTime := sessionStartTime
	defer func() {
		dataFilePath = origDataFilePath
		sessionStartTime = origSessionStartTime
	}()

	// Use a fixed start time for testing
	sessionStartTime = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "cc-discord-presence-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name        string
		fileContent string
		wantNil     bool
		wantProject string
		wantModel   string
		wantTokens  int64
	}{
		{
			name: "Valid statusline data",
			fileContent: `{
				"session_id": "abc123",
				"cwd": "/Users/test/myproject",
				"model": {"id": "claude-opus-4-5-20251101", "display_name": "Opus 4.5"},
				"workspace": {"current_dir": "/Users/test/myproject", "project_dir": "/Users/test/myproject"},
				"cost": {"total_cost_usd": 0.50, "total_duration_ms": 1000, "total_api_duration_ms": 500},
				"context_window": {"total_input_tokens": 10000, "total_output_tokens": 5000}
			}`,
			wantNil:     false,
			wantProject: "myproject",
			wantModel:   "Opus 4.5",
			wantTokens:  10000, // context size only, output excluded
		},
		{
			name: "Missing session_id - should return nil",
			fileContent: `{
				"session_id": "",
				"cwd": "/Users/test/myproject",
				"model": {"id": "claude-opus-4-5-20251101", "display_name": "Opus 4.5"}
			}`,
			wantNil: true,
		},
		{
			name:        "Invalid JSON",
			fileContent: `{invalid json`,
			wantNil:     true,
		},
		{
			name: "Missing project_dir uses cwd",
			fileContent: `{
				"session_id": "abc123",
				"cwd": "/Users/test/fallback-project",
				"model": {"id": "claude-sonnet-4-20250514", "display_name": "Sonnet 4"},
				"workspace": {},
				"cost": {},
				"context_window": {}
			}`,
			wantNil:     false,
			wantProject: "fallback-project",
			wantModel:   "Sonnet 4",
		},
		{
			name: "Missing display_name derives a readable name",
			fileContent: `{
				"session_id": "abc123",
				"cwd": "/Users/test/derived-project",
				"model": {"id": "claude-opus-5"},
				"workspace": {},
				"cost": {},
				"context_window": {}
			}`,
			wantNil:     false,
			wantProject: "derived-project",
			wantModel:   "Opus 5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write test file
			testFile := filepath.Join(tmpDir, "test-statusline.json")
			if err := os.WriteFile(testFile, []byte(tt.fileContent), 0644); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}
			dataFilePath = testFile

			got := readStatusLineData()

			if tt.wantNil {
				if got != nil {
					t.Errorf("readStatusLineData() = %+v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatal("readStatusLineData() = nil, want non-nil")
			}

			if got.ProjectName != tt.wantProject {
				t.Errorf("ProjectName = %q, want %q", got.ProjectName, tt.wantProject)
			}
			if got.ModelName != tt.wantModel {
				t.Errorf("ModelName = %q, want %q", got.ModelName, tt.wantModel)
			}
			if got.TotalTokens != tt.wantTokens {
				t.Errorf("TotalTokens = %d, want %d", got.TotalTokens, tt.wantTokens)
			}
		})
	}

	// Test file not found
	t.Run("File not found", func(t *testing.T) {
		dataFilePath = filepath.Join(tmpDir, "nonexistent.json")
		if got := readStatusLineData(); got != nil {
			t.Errorf("readStatusLineData() with missing file = %+v, want nil", got)
		}
	})
}

// TestParseJSONLSession tests parsing JSONL transcript files
func TestParseJSONLSession(t *testing.T) {
	// Save and restore original sessionStartTime
	origSessionStartTime := sessionStartTime
	defer func() { sessionStartTime = origSessionStartTime }()
	sessionStartTime = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	tmpDir, err := os.MkdirTemp("", "cc-discord-presence-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name        string
		content     string
		wantNil     bool
		wantTokens  int64
		wantModel   string
		wantProject string
	}{
		{
			name: "Valid session with multiple messages",
			content: `{"type":"user","cwd":"/Users/test/project"}
{"type":"assistant","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":1000,"output_tokens":500}}}
{"type":"user"}
{"type":"assistant","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":2000,"output_tokens":1000}}}`,
			wantNil:     false,
			wantTokens:  2000, // last request's input: context size
			wantModel:   "Sonnet 4",
			wantProject: "project",
		},
		{
			name: "Cached tokens count toward context size",
			content: `{"type":"user","cwd":"/Users/test/cached"}
{"type":"assistant","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":2,"cache_creation_input_tokens":446,"cache_read_input_tokens":53664,"output_tokens":294}}}`,
			wantNil:     false,
			wantTokens:  54112, // 2+446+53664
			wantModel:   "Sonnet 4",
			wantProject: "cached",
		},
		{
			name:    "Empty file",
			content: "",
			wantNil: true,
		},
		{
			name: "No assistant messages",
			content: `{"type":"user","cwd":"/Users/test/project"}
{"type":"user"}`,
			wantNil: true,
		},
		{
			name: "Mixed valid and invalid JSON lines",
			content: `{"type":"user","cwd":"/Users/test/myapp"}
invalid json line
{"type":"assistant","message":{"model":"claude-haiku-4-5-20241022","usage":{"input_tokens":500,"output_tokens":100}}}`,
			wantNil:     false,
			wantTokens:  500,
			wantModel:   "Haiku 4.5",
			wantProject: "myapp",
		},
		{
			name: "Multiple models uses last one",
			content: `{"type":"user","cwd":"/Users/test/multimodel"}
{"type":"assistant","message":{"model":"claude-haiku-4-5-20241022","usage":{"input_tokens":100,"output_tokens":50}}}
{"type":"assistant","message":{"model":"claude-opus-4-5-20251101","usage":{"input_tokens":200,"output_tokens":100}}}`,
			wantNil:     false,
			wantTokens:  200,
			wantModel:   "Opus 4.5", // Last model used
			wantProject: "multimodel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testFile := filepath.Join(tmpDir, "test.jsonl")
			if err := os.WriteFile(testFile, []byte(tt.content), 0644); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			got := parseJSONLSession(testFile, "")

			if tt.wantNil {
				if got != nil {
					t.Errorf("parseJSONLSession() = %+v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatal("parseJSONLSession() = nil, want non-nil")
			}

			if got.TotalTokens != tt.wantTokens {
				t.Errorf("TotalTokens = %d, want %d", got.TotalTokens, tt.wantTokens)
			}
			if got.ModelName != tt.wantModel {
				t.Errorf("ModelName = %q, want %q", got.ModelName, tt.wantModel)
			}
			if got.ProjectName != tt.wantProject {
				t.Errorf("ProjectName = %q, want %q", got.ProjectName, tt.wantProject)
			}
		})
	}
}

func TestParseJSONLSessionCostPerModel(t *testing.T) {
	tmpDir := t.TempDir()
	content := `{"type":"user","cwd":"/Users/test/mixed"}
{"type":"assistant","message":{"model":"claude-haiku-4-5-20241022","usage":{"input_tokens":1000000}}}
{"type":"assistant","message":{"model":"claude-opus-4-5-20251101","usage":{"input_tokens":1000000}}}`
	testFile := filepath.Join(tmpDir, "mixed.jsonl")
	if err := os.WriteFile(testFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := parseJSONLSession(testFile, "")
	if got == nil {
		t.Fatal("parseJSONLSession() = nil")
	}
	// $1/M for Haiku 4.5 plus $15/M for Opus 4.5. Pricing everything at the
	// last model would report $30.
	if diff := got.TotalCost - 16.0; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("TotalCost = %v, want 16.0", got.TotalCost)
	}
}

// TestParseJSONLSessionLargeLine ensures a transcript line larger than any
// scanner buffer (pasted image, big file dump) does not stop the parse and
// freeze the token total — messages after it must still be counted.
func TestParseJSONLSessionLargeLine(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cc-discord-presence-bigline")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	huge := make([]byte, 3<<20) // 3 MiB, well past the old 1 MiB cap
	for i := range huge {
		huge[i] = 'x'
	}

	content := `{"type":"user","cwd":"/Users/test/big"}
{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":50}}}
{"type":"user","note":"` + string(huge) + `"}
{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tokens":200,"output_tokens":75,"cache_read_input_tokens":1000}}}
`
	testFile := filepath.Join(tmpDir, "big.jsonl")
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := parseJSONLSession(testFile, "")
	if got == nil {
		t.Fatal("parseJSONLSession() = nil")
	}
	// Context size from the last assistant message: input 200 + cache read 1000.
	// The message after the huge line must still be parsed.
	if got.TotalTokens != 1200 {
		t.Errorf("TotalTokens = %d, want 1200 (message after the huge line was dropped)", got.TotalTokens)
	}
	// Cache tokens still feed the cost estimate even though they aren't displayed.
	if got.TotalCost <= 0 {
		t.Errorf("TotalCost = %v, want > 0 (cache-read tokens should be priced)", got.TotalCost)
	}
	if got.ProjectName != "big" {
		t.Errorf("ProjectName = %q, want %q", got.ProjectName, "big")
	}
}

// TestPathDecoding tests the path decoding logic used in findMostRecentJSONL
func TestPathDecoding(t *testing.T) {
	// This tests the path decoding algorithm used in findMostRecentJSONL
	// The encoding is: / becomes -, and literal - becomes --
	// Decoding: -- → placeholder → - to / → placeholder to -

	tests := []struct {
		name    string
		encoded string
		want    string
	}{
		{
			name:    "Simple path",
			encoded: "-Users-foo-project",
			want:    "/Users/foo/project",
		},
		{
			name:    "Path with literal dash",
			encoded: "-Users-foo-my--project",
			want:    "/Users/foo/my-project",
		},
		{
			name:    "Path with multiple literal dashes",
			encoded: "-Users-foo--bar-my----special----project",
			want:    "/Users/foo-bar/my--special--project",
		},
		{
			name:    "Root path",
			encoded: "-",
			want:    "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Apply the same decoding logic as findMostRecentJSONL
			decoded := tt.encoded
			decoded = replaceDoubleDash(decoded, "\x00")
			decoded = replaceSingleDash(decoded, "/")
			decoded = replacePlaceholder(decoded, "-")

			if decoded != tt.want {
				t.Errorf("decoding %q = %q, want %q", tt.encoded, decoded, tt.want)
			}
		})
	}
}

// Helper functions to match the decoding logic in findMostRecentJSONL
func replaceDoubleDash(s, replacement string) string {
	result := ""
	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i] == '-' && s[i+1] == '-' {
			result += replacement
			i += 2
		} else {
			result += string(s[i])
			i++
		}
	}
	return result
}

func replaceSingleDash(s, replacement string) string {
	result := ""
	for _, c := range s {
		if c == '-' {
			result += replacement
		} else {
			result += string(c)
		}
	}
	return result
}

func replacePlaceholder(s, replacement string) string {
	result := ""
	for _, c := range s {
		if c == '\x00' {
			result += replacement
		} else {
			result += string(c)
		}
	}
	return result
}

// TestFindMostRecentJSONL tests finding the most recent JSONL file
func TestFindMostRecentJSONL(t *testing.T) {
	// Save original projectsDir
	origProjectsDir := projectsDir
	defer func() { projectsDir = origProjectsDir }()

	tmpDir, err := os.MkdirTemp("", "cc-discord-presence-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Run("No projects directory", func(t *testing.T) {
		projectsDir = filepath.Join(tmpDir, "nonexistent")
		_, _, err := findMostRecentJSONL()
		if err == nil {
			t.Error("Expected error for nonexistent directory, got nil")
		}
	})

	t.Run("Empty projects directory", func(t *testing.T) {
		emptyDir := filepath.Join(tmpDir, "empty")
		os.MkdirAll(emptyDir, 0755)
		projectsDir = emptyDir

		_, _, err := findMostRecentJSONL()
		if err == nil {
			t.Error("Expected error for empty directory, got nil")
		}
	})

	t.Run("Single JSONL file", func(t *testing.T) {
		singleDir := filepath.Join(tmpDir, "single")
		projectDir := filepath.Join(singleDir, "-Users-test-myproject")
		os.MkdirAll(projectDir, 0755)
		projectsDir = singleDir

		jsonlFile := filepath.Join(projectDir, "session.jsonl")
		os.WriteFile(jsonlFile, []byte(`{"type":"test"}`), 0644)

		path, projectPath, err := findMostRecentJSONL()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if path != jsonlFile {
			t.Errorf("path = %q, want %q", path, jsonlFile)
		}
		if projectPath != "/Users/test/myproject" {
			t.Errorf("projectPath = %q, want %q", projectPath, "/Users/test/myproject")
		}
	})

	t.Run("Multiple JSONL files - returns most recent", func(t *testing.T) {
		multiDir := filepath.Join(tmpDir, "multi")
		projectDir := filepath.Join(multiDir, "-Users-test-project")
		os.MkdirAll(projectDir, 0755)
		projectsDir = multiDir

		// Create older file
		oldFile := filepath.Join(projectDir, "old.jsonl")
		os.WriteFile(oldFile, []byte(`{"type":"old"}`), 0644)
		oldTime := time.Now().Add(-time.Hour)
		os.Chtimes(oldFile, oldTime, oldTime)

		// Create newer file
		newFile := filepath.Join(projectDir, "new.jsonl")
		os.WriteFile(newFile, []byte(`{"type":"new"}`), 0644)

		path, _, err := findMostRecentJSONL()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if path != newFile {
			t.Errorf("Expected newest file %q, got %q", newFile, path)
		}
	})
}

// TestPickJSONLSession verifies the sticky/debounced session selection.
func TestPickJSONLSession(t *testing.T) {
	origProjectsDir := projectsDir
	origShownKey := shownKey
	defer func() {
		projectsDir = origProjectsDir
		shownKey = origShownKey
	}()

	tmpDir, err := os.MkdirTemp("", "cc-discord-presence-pick")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	projectsDir = tmpDir

	mkSession := func(project, name string, age time.Duration) string {
		dir := filepath.Join(tmpDir, project)
		os.MkdirAll(dir, 0755)
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(`{"type":"user"}`), 0644)
		mt := time.Now().Add(-age)
		os.Chtimes(p, mt, mt)
		return p
	}

	sessionA := mkSession("-Users-test-projA", "a.jsonl", 2*time.Second)
	sessionB := mkSession("-Users-test-projB", "b.jsonl", 30*time.Second)

	// Nothing shown yet -> newest (A) wins.
	shownKey = ""
	if got, _, _ := pickJSONLSession(); got != sessionA {
		t.Fatalf("fresh pick = %q, want %q", got, sessionA)
	}

	// A is shown and still recently active -> stays on A even though we now make
	// B the most recent.
	os.Chtimes(sessionB, time.Now(), time.Now())
	shownKey = "jl:" + sessionA
	if got, _, _ := pickJSONLSession(); got != sessionA {
		t.Fatalf("sticky pick = %q, want %q (should not flip to newer B)", got, sessionA)
	}

	// A goes quiet past the threshold -> switch to the now-active B.
	stale := time.Now().Add(-2 * StickyQuietPeriod)
	os.Chtimes(sessionA, stale, stale)
	os.Chtimes(sessionB, time.Now(), time.Now())
	shownKey = "jl:" + sessionA
	if got, _, _ := pickJSONLSession(); got != sessionB {
		t.Fatalf("stale pick = %q, want %q (should flip after quiet period)", got, sessionB)
	}
}

// TestModelPricingConsistency ensures every priced model resolves to a specific
// display name (not the generic "Claude" fallback), and that pricing keys are
// already in normalized (date-stripped) form.
func TestModelPricingConsistency(t *testing.T) {
	for modelID := range modelPricing {
		if got := normalizeModelID(modelID); got != modelID {
			t.Errorf("pricing key %q is not normalized (got %q)", modelID, got)
		}
		if name := formatModelName(modelID); name == "Claude" {
			t.Errorf("priced model %q resolves to generic display name", modelID)
		}
	}

	// Display-name overrides must also have pricing.
	for modelID := range modelDisplayNames {
		if _, ok := modelPricing[modelID]; !ok {
			t.Errorf("Model %q has display name override but no pricing", modelID)
		}
	}
}

// TestReadSessionDataPriority tests that the hooks module's file wins over the
// statusline file, and that a blanked module file falls back to it.
func TestReadSessionDataPriority(t *testing.T) {
	origData, origModule, origProjects := dataFilePath, moduleFilePath, projectsDir
	origSource, origNudge := currentSource, nudgeShown
	origClouds := cloudSessionsSource
	origFileSession, origShownKey, origShownSince := fileSession, shownKey, shownSince
	defer func() {
		dataFilePath, moduleFilePath, projectsDir = origData, origModule, origProjects
		currentSource, nudgeShown = origSource, origNudge
		cloudSessionsSource = origClouds
		fileSession, shownKey, shownSince = origFileSession, origShownKey, origShownSince
	}()
	cloudSessionsSource = func() ([]cloudSession, error) { return nil, nil }
	fileSession = fileSessionCache{}
	shownKey = ""
	shownSince = time.Time{}

	tmpDir := t.TempDir()
	dataFilePath = filepath.Join(tmpDir, "discord-presence-data.json")
	moduleFilePath = filepath.Join(tmpDir, "discord-presence-module.json")
	projectsDir = filepath.Join(tmpDir, "projects") // empty: no JSONL fallback
	nudgeShown = true

	presence := func(project string) string {
		return `{"session_id":"s-` + project + `","cwd":"/work/` + project + `",` +
			`"model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"},` +
			`"workspace":{"current_dir":"/work/` + project + `","project_dir":"/work/` + project + `"},` +
			`"context_window":{"total_input_tokens":1234}}`
	}
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	tests := []struct {
		name        string
		module      string // "" means no file
		statusLine  string
		wantProject string
		wantSource  string
	}{
		{"module wins over statusline", presence("from-module"), presence("from-statusline"), "from-module", sourceModule},
		{"blanked module falls back", "{}", presence("from-statusline"), "from-statusline", sourceStatusLine},
		{"missing module falls back", "", presence("from-statusline"), "from-statusline", sourceStatusLine},
		{"module alone", presence("from-module"), "", "from-module", sourceModule},
		{"neither", "", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Remove(moduleFilePath)
			os.Remove(dataFilePath)
			// Write statusline first so the module file is the newer one.
			if tt.statusLine != "" {
				write(dataFilePath, tt.statusLine)
			}
			if tt.module != "" {
				write(moduleFilePath, tt.module)
			}
			currentSource = ""
			fileSession = fileSessionCache{}
			shownKey = ""
			shownSince = time.Time{}

			got := readSessionData()
			if tt.wantProject == "" {
				if got != nil {
					t.Fatalf("readSessionData() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("readSessionData() = nil, want non-nil")
			}
			if got.ProjectName != tt.wantProject {
				t.Errorf("ProjectName = %q, want %q", got.ProjectName, tt.wantProject)
			}
			if currentSource != tt.wantSource {
				t.Errorf("currentSource = %q, want %q", currentSource, tt.wantSource)
			}
			if got.TotalTokens != 1234 {
				t.Errorf("TotalTokens = %d, want 1234", got.TotalTokens)
			}
		})
	}
}

// TestPresenceText tests the activity lines Discord shows.
func TestReadSessionDataPrefersNewerStatusline(t *testing.T) {
	origData, origModule, origProjects := dataFilePath, moduleFilePath, projectsDir
	origSource, origNudge := currentSource, nudgeShown
	origClouds := cloudSessionsSource
	origDesktop := desktopAppRunningFn
	origShownKey, origShownSince := shownKey, shownSince
	origFileSession := fileSession
	defer func() {
		dataFilePath, moduleFilePath, projectsDir = origData, origModule, origProjects
		currentSource, nudgeShown = origSource, origNudge
		cloudSessionsSource = origClouds
		desktopAppRunningFn = origDesktop
		shownKey, shownSince = origShownKey, origShownSince
		fileSession = origFileSession
	}()
	fileSession = fileSessionCache{}
	shownKey = ""
	shownSince = time.Time{}
	cloudSessionsSource = func() ([]cloudSession, error) { return nil, nil }
	desktopAppRunningFn = func() bool { return false }
	nudgeShown = true

	tmpDir := t.TempDir()
	dataFilePath = filepath.Join(tmpDir, "discord-presence-data.json")
	moduleFilePath = filepath.Join(tmpDir, "discord-presence-module.json")
	projectsDir = filepath.Join(tmpDir, "projects")

	write := func(path, project string) {
		content := `{"session_id":"s-` + project + `","cwd":"/work/` + project + `",` +
			`"model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"},` +
			`"workspace":{"project_dir":"/work/` + project + `"}}`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(moduleFilePath, "stale-module")
	write(dataFilePath, "fresh-statusline")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(moduleFilePath, old, old); err != nil {
		t.Fatal(err)
	}

	got := readSessionData()
	if got == nil || got.ProjectName != "fresh-statusline" {
		t.Fatalf("readSessionData() = %+v, want fresh-statusline", got)
	}
	if currentSource != sourceStatusLine {
		t.Errorf("currentSource = %q, want %q", currentSource, sourceStatusLine)
	}
}

func TestReadSessionDataHoldsPreviousFileSession(t *testing.T) {
	origData, origModule, origProjects := dataFilePath, moduleFilePath, projectsDir
	origSource, origNudge := currentSource, nudgeShown
	origClouds := cloudSessionsSource
	origDesktop := desktopAppRunningFn
	origFileSession, origShownKey, origShownSince := fileSession, shownKey, shownSince
	defer func() {
		dataFilePath, moduleFilePath, projectsDir = origData, origModule, origProjects
		currentSource, nudgeShown = origSource, origNudge
		cloudSessionsSource = origClouds
		desktopAppRunningFn = origDesktop
		fileSession, shownKey, shownSince = origFileSession, origShownKey, origShownSince
	}()
	cloudSessionsSource = func() ([]cloudSession, error) { return nil, nil }
	desktopAppRunningFn = func() bool { return false }
	nudgeShown = true
	fileSession = fileSessionCache{}
	shownKey = ""
	shownSince = time.Time{}

	tmpDir := t.TempDir()
	dataFilePath = filepath.Join(tmpDir, "missing-statusline.json")
	moduleFilePath = filepath.Join(tmpDir, "discord-presence-module.json")
	projectsDir = filepath.Join(tmpDir, "projects")

	write := func(project string) {
		content := `{"session_id":"s-` + project + `","cwd":"/work/` + project + `",` +
			`"model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"},` +
			`"workspace":{"project_dir":"/work/` + project + `"}}`
		if err := os.WriteFile(moduleFilePath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("session-a")
	got := readSessionData()
	if got == nil || got.ProjectName != "session-a" {
		t.Fatalf("first read = %+v, want session-a", got)
	}

	// Another session overwrites the single module file immediately. The
	// displayed session should not flicker to it yet.
	write("session-b")
	got = readSessionData()
	if got == nil || got.ProjectName != "session-a" {
		t.Fatalf("sticky read = %+v, want session-a", got)
	}

	// Once the previous session has been quiet for the sticky period, switch.
	fileSession.lastSeen = time.Now().Add(-StickyQuietPeriod - time.Second)
	got = readSessionData()
	if got == nil || got.ProjectName != "session-b" {
		t.Fatalf("after quiet period = %+v, want session-b", got)
	}
}

func TestReadSessionDataSkipsUnusableJSONL(t *testing.T) {
	origData, origModule, origProjects := dataFilePath, moduleFilePath, projectsDir
	origSource, origNudge := currentSource, nudgeShown
	origClouds := cloudSessionsSource
	origDesktop := desktopAppRunningFn
	origFileSession, origShownKey, origShownSince := fileSession, shownKey, shownSince
	defer func() {
		dataFilePath, moduleFilePath, projectsDir = origData, origModule, origProjects
		currentSource, nudgeShown = origSource, origNudge
		cloudSessionsSource = origClouds
		desktopAppRunningFn = origDesktop
		fileSession, shownKey, shownSince = origFileSession, origShownKey, origShownSince
	}()
	cloudSessionsSource = func() ([]cloudSession, error) { return nil, nil }
	desktopAppRunningFn = func() bool { return false }
	nudgeShown = true
	fileSession = fileSessionCache{}
	shownKey = ""
	shownSince = time.Time{}

	tmpDir := t.TempDir()
	dataFilePath = filepath.Join(tmpDir, "missing-statusline.json")
	moduleFilePath = filepath.Join(tmpDir, "missing-module.json")
	projectsDir = filepath.Join(tmpDir, "projects")

	writeSession := func(project, name, content string, age time.Duration) {
		dir := filepath.Join(projectsDir, "-Users-test-"+project)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	writeSession("bad", "newest.jsonl", `{"type":"user","cwd":"/Users/test/bad"}`, 0)
	writeSession("good", "older.jsonl", `{"type":"user","cwd":"/Users/test/good"}
{"type":"assistant","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":1}}}`, time.Minute)

	got := readSessionData()
	if got == nil || got.ProjectName != "good" {
		t.Fatalf("readSessionData() = %+v, want good", got)
	}
}

func TestShouldSendPresence(t *testing.T) {
	now := time.Now()

	if !shouldSendPresence("new", "old", now.Add(-time.Second), now) {
		t.Error("changed presence should be sent")
	}
	if shouldSendPresence("same", "same", now.Add(-time.Second), now) {
		t.Error("unchanged presence was sent before the heartbeat interval")
	}
	if !shouldSendPresence("same", "same", now.Add(-PresenceHeartbeatInterval), now) {
		t.Error("unchanged presence should be re-sent at the heartbeat interval")
	}
	if !shouldSendPresence("same", "same", time.Time{}, now) {
		t.Error("presence should be sent when no previous send time is recorded")
	}
}

func TestPresenceText(t *testing.T) {
	details, state := presenceText(&SessionData{
		ProjectName: "kadens",
		GitBranch:   "main",
		ModelName:   "Opus 5.5",
		TotalTokens: 54112,
		TotalCost:   1.27,
	})
	if details != "Working on: kadens (main)" {
		t.Errorf("details = %q", details)
	}
	if state != "Opus 5.5 | 54.1K tokens | $1.2700" {
		t.Errorf("state = %q", state)
	}
}
