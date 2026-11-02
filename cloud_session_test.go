package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/syndtr/goleveldb/leveldb"
)

func chromiumValue(s string) []byte {
	return append([]byte{1}, []byte(s)...)
}

func ms(v int64) string {
	return strconv.FormatInt(v, 10)
}

func TestParseCloudSessions(t *testing.T) {
	now := time.Now()
	store := map[string]string{
		"epitaxy:code-session-durability:v1": `{
			"session_01new":{"uuid":"a","at":` + ms(now.Add(-2*time.Minute).UnixMilli()) + `},
			"session_01old":{"uuid":"b","at":` + ms(now.Add(-20*time.Minute).UnixMilli()) + `},
			"local_abc":{"uuid":"c","at":` + ms(now.UnixMilli()) + `}
		}`,
		"code-session-pills": `[
			["session_01new","claude-opus-5-5","high"],
			["session_01old","claude-sonnet-5","medium"],
			["local_abc","claude-opus-5","high"]
		]`,
		"epitaxy-session-result:session_01new": `{"v":2,"costUSD":12.5,"perModel":{"claude-opus-5-5":{"costUSD":12.5}}}`,
		"epitaxy-session-result:session_01old": `{"v":2,"costUSD":1.25,"perModel":{"claude-sonnet-5":{"costUSD":1.25}}}`,
	}

	sessions := parseCloudSessions(store)
	if len(sessions) != 2 {
		t.Fatalf("parseCloudSessions() returned %d sessions, want 2", len(sessions))
	}
	// Sorted most-recently-active first.
	if sessions[0].ID != "session_01new" {
		t.Errorf("first session = %q, want session_01new", sessions[0].ID)
	}
	if sessions[0].ModelID != "claude-opus-5-5" {
		t.Errorf("new model = %q, want claude-opus-5-5", sessions[0].ModelID)
	}
	if sessions[0].CostUSD != 12.5 {
		t.Errorf("new cost = %v, want 12.5", sessions[0].CostUSD)
	}
	wantActive := time.UnixMilli(now.Add(-2 * time.Minute).UnixMilli())
	if !sessions[0].LastActive.Equal(wantActive) {
		t.Errorf("new LastActive = %v, want %v", sessions[0].LastActive, wantActive)
	}
	if sessions[1].ID != "session_01old" {
		t.Errorf("second session = %q, want session_01old", sessions[1].ID)
	}
}

func TestParseCloudSessionsFallsBackToResultModel(t *testing.T) {
	now := time.Now()
	store := map[string]string{
		"epitaxy:code-session-durability:v1": `{"session_01a":{"uuid":"a","at":` + ms(now.UnixMilli()) + `}}`,
		"epitaxy-session-result:session_01a": `{
			"v":2,
			"costUSD":3.5,
			"perModel":{
				"claude-haiku-5-5":{"costUSD":0.5},
				"claude-opus-5-5":{"costUSD":3.0}
			}
		}`,
	}

	sessions := parseCloudSessions(store)
	if len(sessions) != 1 {
		t.Fatalf("parseCloudSessions() returned %d sessions, want 1", len(sessions))
	}
	// Without a pill, use the model that contributed the most cost.
	if sessions[0].ModelID != "claude-opus-5-5" {
		t.Errorf("model = %q, want claude-opus-5-5", sessions[0].ModelID)
	}
	if sessions[0].CostUSD != 3.5 {
		t.Errorf("cost = %v, want 3.5", sessions[0].CostUSD)
	}
}

func TestParseCloudSessionsRejectsBadDurability(t *testing.T) {
	for name, store := range map[string]map[string]string{
		"no durability key":    {},
		"malformed durability": {"epitaxy:code-session-durability:v1": `{not json`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := parseCloudSessions(store); len(got) != 0 {
				t.Errorf("parseCloudSessions() = %v, want no sessions", got)
			}
		})
	}
}

func TestParseCloudSessionsSkipsMalformedEntry(t *testing.T) {
	now := time.Now()
	store := map[string]string{
		"epitaxy:code-session-durability:v1": `{
			"session_01good":{"uuid":"a","at":` + ms(now.UnixMilli()) + `},
			"session_01bad":{"uuid":"b","at":"not-a-number"}
		}`,
	}

	sessions := parseCloudSessions(store)
	if len(sessions) != 1 {
		t.Fatalf("parseCloudSessions() returned %d sessions, want 1", len(sessions))
	}
	if sessions[0].ID != "session_01good" {
		t.Errorf("session ID = %q, want session_01good", sessions[0].ID)
	}
}

func TestParseCloudSessionPillsSkipsMalformedRow(t *testing.T) {
	store := map[string]string{
		"epitaxy:code-session-durability:v1": `{"session_01a":{"uuid":"a","at":` + ms(time.Now().UnixMilli()) + `}}`,
		"code-session-pills":                 `[["session_01a","claude-opus-5-5","high"], 42]`,
	}

	sessions := parseCloudSessions(store)
	if len(sessions) != 1 {
		t.Fatalf("parseCloudSessions() returned %d sessions, want 1", len(sessions))
	}
	if sessions[0].ModelID != "claude-opus-5-5" {
		t.Errorf("model = %q, want claude-opus-5-5", sessions[0].ModelID)
	}
}

func TestParseCloudSessionsKeepsSessionWithBadExtras(t *testing.T) {
	now := time.Now()
	durability := `{"session_01a":{"uuid":"a","at":` + ms(now.UnixMilli()) + `}}`

	t.Run("malformed pills", func(t *testing.T) {
		sessions := parseCloudSessions(map[string]string{
			"epitaxy:code-session-durability:v1": durability,
			"code-session-pills":                 `nope`,
		})
		if len(sessions) != 1 {
			t.Fatalf("parseCloudSessions() returned %d sessions, want 1", len(sessions))
		}
		if sessions[0].ID != "session_01a" {
			t.Errorf("session ID = %q, want session_01a", sessions[0].ID)
		}
	})

	t.Run("malformed session result", func(t *testing.T) {
		sessions := parseCloudSessions(map[string]string{
			"epitaxy:code-session-durability:v1": durability,
			"epitaxy-session-result:session_01a": `{]`,
		})
		if len(sessions) != 1 {
			t.Fatalf("parseCloudSessions() returned %d sessions, want 1", len(sessions))
		}
		if sessions[0].CostUSD != 0 {
			t.Errorf("cost = %v, want 0", sessions[0].CostUSD)
		}
	})
}

func TestActiveCloudSession(t *testing.T) {
	now := time.Now()
	cloud := cloudSession{ID: "session_01a", LastActive: now.Add(-5 * time.Minute)}

	tests := []struct {
		name          string
		sessions      []cloudSession
		localActivity time.Time
		wantID        string
	}{
		{
			name:          "cloud newer than local",
			sessions:      []cloudSession{cloud},
			localActivity: now.Add(-10 * time.Minute),
			wantID:        "session_01a",
		},
		{
			name:          "local newer than cloud",
			sessions:      []cloudSession{cloud},
			localActivity: now.Add(-1 * time.Minute),
			wantID:        "",
		},
		{
			name:          "no local activity",
			sessions:      []cloudSession{cloud},
			localActivity: time.Time{},
			wantID:        "session_01a",
		},
		{
			name:          "cloud quiet too long",
			sessions:      []cloudSession{{ID: "session_01a", LastActive: now.Add(-3 * time.Hour)}},
			localActivity: time.Time{},
			wantID:        "",
		},
		{
			name:          "most recent cloud wins",
			sessions:      []cloudSession{{ID: "session_01old", LastActive: now.Add(-20 * time.Minute)}, cloud},
			localActivity: now.Add(-30 * time.Minute),
			wantID:        "session_01a",
		},
		{
			name:          "no cloud sessions",
			localActivity: now,
			wantID:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := activeCloudSession(tt.sessions, tt.localActivity, now)
			if tt.wantID == "" {
				if got != nil {
					t.Errorf("activeCloudSession() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("activeCloudSession() = nil, want %q", tt.wantID)
			}
			if got.ID != tt.wantID {
				t.Errorf("activeCloudSession().ID = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

func TestDecodeLocalStorageValue(t *testing.T) {
	if got := decodeLocalStorageValue(chromiumValue(`{"a":1}`)); got != `{"a":1}` {
		t.Errorf("latin1 value = %q", got)
	}

	utf16le := func(s string) []byte {
		units := utf16.Encode([]rune(s))
		buf := make([]byte, 1+2*len(units))
		for i, u := range units {
			buf[1+i*2] = byte(u)
			buf[2+i*2] = byte(u >> 8)
		}
		return buf
	}
	if got := decodeLocalStorageValue(utf16le("héllo")); got != "héllo" {
		t.Errorf("utf16 value = %q, want héllo", got)
	}
}

func TestReadLocalStorage(t *testing.T) {
	dir := t.TempDir()
	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatalf("open leveldb: %v", err)
	}
	key := localStorageKeyPrefix + "epitaxy:code-session-durability:v1"
	value := chromiumValue(`{"session_01a":{"at":123}}`)
	if err := db.Put([]byte(key), value, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := db.Put([]byte("unrelated"), []byte("x"), nil); err != nil {
		t.Fatalf("put unrelated: %v", err)
	}
	if err := db.Put([]byte(localStorageKeyPrefix+"some-secret"), chromiumValue("secret"), nil); err != nil {
		t.Fatalf("put unrelated claude.ai key: %v", err)
	}
	shortKey := "_https://claude.ai\x00code-session-pills"
	if err := db.Put([]byte(shortKey), chromiumValue(`[["session_01a","claude-sonnet-5","high"]]`), nil); err != nil {
		t.Fatalf("put old-format key: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store, err := readLocalStorage(dir)
	if err != nil {
		t.Fatalf("readLocalStorage() error: %v", err)
	}
	if got := store["epitaxy:code-session-durability:v1"]; got != `{"session_01a":{"at":123}}` {
		t.Errorf("stored value = %q", got)
	}
	if _, ok := store["unrelated"]; ok {
		t.Error("readLocalStorage() included a key from another origin")
	}
	if _, ok := store["some-secret"]; ok {
		t.Error("readLocalStorage() included an unrelated claude.ai key")
	}
	if got := store["code-session-pills"]; got != `[["session_01a","claude-sonnet-5","high"]]` {
		t.Errorf("old-format key value = %q", got)
	}

	// The copy fallback is used when the live database cannot be opened (for
	// example Chromium's LOCK file on Windows).
	copied, err := readLocalStorageCopy(dir)
	if err != nil {
		t.Fatalf("readLocalStorageCopy() error: %v", err)
	}
	if got := copied["epitaxy:code-session-durability:v1"]; got != `{"session_01a":{"at":123}}` {
		t.Errorf("copied stored value = %q", got)
	}
}

func TestDesktopLocalStorageDirs(t *testing.T) {
	dirs := desktopLocalStorageDirs()
	if len(dirs) == 0 {
		t.Fatal("desktopLocalStorageDirs() returned no paths")
	}
	for _, dir := range dirs {
		if !strings.HasSuffix(dir, filepath.Join("Local Storage", "leveldb")) {
			t.Errorf("desktopLocalStorageDirs() returned %q, want a Local Storage leveldb path", dir)
		}
	}
}

func TestLatestLocalActivityUsesNewestTranscript(t *testing.T) {
	origProjectsDir := projectsDir
	origDataFilePath := dataFilePath
	origModuleFilePath := moduleFilePath
	origShownKey := shownKey
	defer func() {
		projectsDir = origProjectsDir
		dataFilePath = origDataFilePath
		moduleFilePath = origModuleFilePath
		shownKey = origShownKey
	}()

	dir := t.TempDir()
	projectsDir = dir
	dataFilePath = filepath.Join(dir, "missing-statusline.json")
	moduleFilePath = filepath.Join(dir, "missing-module.json")

	mkSession := func(project, name string, age time.Duration) (string, time.Time) {
		projectDir := filepath.Join(dir, project)
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(projectDir, name)
		if err := os.WriteFile(path, []byte(`{"type":"user"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age).Truncate(time.Second)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
		return path, mt
	}

	// The displayed local session is still sticky, but a different local
	// session is more recently active.
	oldPath, _ := mkSession("-Users-test-old", "old.jsonl", 10*time.Second)
	_, newMod := mkSession("-Users-test-new", "new.jsonl", time.Second)
	shownKey = "jl:" + oldPath

	if got := latestLocalActivity(); !got.Equal(newMod) {
		t.Errorf("latestLocalActivity() = %v, want newest transcript %v", got, newMod)
	}
}

func TestLatestLocalActivityIgnoresBlankedModule(t *testing.T) {
	origProjectsDir := projectsDir
	origDataFilePath := dataFilePath
	origModuleFilePath := moduleFilePath
	defer func() {
		projectsDir = origProjectsDir
		dataFilePath = origDataFilePath
		moduleFilePath = origModuleFilePath
	}()

	dir := t.TempDir()
	projectsDir = dir
	dataFilePath = filepath.Join(dir, "missing-statusline.json")
	moduleFilePath = filepath.Join(dir, "module.json")
	if err := os.WriteFile(moduleFilePath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	projectDir := filepath.Join(dir, "-Users-test-old")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projectDir, "old.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(transcript, old, old); err != nil {
		t.Fatal(err)
	}

	// The module's "{}" means its session ended; its write time must not count
	// as local activity and keep a cloud session from taking over.
	if got := latestLocalActivity(); !got.Equal(old) {
		t.Errorf("latestLocalActivity() = %v, want old transcript %v", got, old)
	}
}

func TestCloudSessionData(t *testing.T) {
	origShownKey := shownKey
	origShownSince := shownSince
	defer func() {
		shownKey = origShownKey
		shownSince = origShownSince
	}()
	shownKey = ""
	shownSince = time.Time{}

	got := cloudSessionData(&cloudSession{
		ID:      "session_01a",
		ModelID: "claude-opus-5-5",
		CostUSD: 1.25,
	})

	if !got.IsCloud {
		t.Error("IsCloud = false, want true")
	}
	if got.ProjectName != "Cloud session" {
		t.Errorf("ProjectName = %q, want Cloud session", got.ProjectName)
	}
	if got.ModelName != "Opus 5.5" {
		t.Errorf("ModelName = %q, want Opus 5.5", got.ModelName)
	}
	if got.TotalCost != 1.25 {
		t.Errorf("TotalCost = %v, want 1.25", got.TotalCost)
	}
	if got.StartTime.IsZero() {
		t.Error("StartTime is zero, want presence takeover time")
	}
	if shownKey != "cloud:session_01a" {
		t.Errorf("shownKey = %q, want cloud:session_01a", shownKey)
	}
}

func TestFormatPresenceState(t *testing.T) {
	local := &SessionData{ModelName: "Sonnet 5", TotalTokens: 1_500_000, TotalCost: 0.1234}
	if got := formatPresenceState(local); got != "Sonnet 5 | 1.5M tokens | $0.1234" {
		t.Errorf("local state = %q", got)
	}

	cloud := &SessionData{ModelName: "Opus 5.5", TotalCost: 564.36, IsCloud: true}
	if got := formatPresenceState(cloud); got != "Opus 5.5 | cloud | $564.3600" {
		t.Errorf("cloud state = %q", got)
	}

	cloudNoCost := &SessionData{ModelName: "Opus 5.5", IsCloud: true}
	if got := formatPresenceState(cloudNoCost); got != "Opus 5.5 | cloud" {
		t.Errorf("cloud state without cost = %q", got)
	}
}
