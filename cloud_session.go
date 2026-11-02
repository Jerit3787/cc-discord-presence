package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

const (
	// CloudQuietPeriod is how long a cloud session may go without activity
	// before the presence falls back to a local session. Cloud transcripts
	// live on Anthropic's infrastructure, so activity is inferred from the
	// desktop app's local storage rather than from a local transcript.
	CloudQuietPeriod = 2 * time.Hour

	// cloudSessionPrefix identifies cloud-native sessions in the desktop app.
	// Local desktop sessions use the "local_" prefix.
	cloudSessionPrefix = "session_"

	// localStorageKeyPrefix prefixes every Chromium localStorage key. The NUL
	// and \x01 bytes separate the origin from the key itself.
	localStorageKeyPrefix = "_https://claude.ai\x00\x01"

	cloudDurabilityKey = "epitaxy:code-session-durability:v1"
	cloudPillsKey      = "code-session-pills"
	cloudResultKey     = "epitaxy-session-result:"
)

// cloudSession is a Claude Code cloud session as reported by the desktop app's
// local storage. The cloud VM keeps the transcript; the desktop app only keeps
// a small activity record, model pill, and per-session usage result locally.
type cloudSession struct {
	ID         string
	ModelID    string
	CostUSD    float64
	LastActive time.Time
}

type cloudDurabilityEntry struct {
	At int64 `json:"at"`
}

type cloudSessionResult struct {
	CostUSD  float64 `json:"costUSD"`
	PerModel map[string]struct {
		CostUSD float64 `json:"costUSD"`
	} `json:"perModel"`
}

// desktopLocalStorageDirs returns candidate locations for the Claude desktop
// app's Chromium Local Storage database. Only the first readable database with
// cloud-session data is used.
func desktopLocalStorageDirs() []string {
	var dirs []string
	if dir := os.Getenv("CLAUDE_USER_DATA_DIR"); dir != "" {
		dirs = append(dirs, filepath.Join(dir, "Local Storage", "leveldb"))
	}
	if base, err := os.UserConfigDir(); err == nil {
		for _, name := range []string{"Claude", "Claude-3p"} {
			dirs = append(dirs, filepath.Join(base, name, "Local Storage", "leveldb"))
		}
	}
	return dirs
}

// readCloudSessions reads cloud sessions from the desktop app's Local Storage.
// It returns nil when the desktop app is not installed, its database is
// unavailable, or no cloud session has ever been recorded.
func readCloudSessions() []cloudSession {
	for _, dir := range desktopLocalStorageDirs() {
		store, err := readLocalStorage(dir)
		if err != nil {
			continue
		}
		if sessions := parseCloudSessions(store); len(sessions) > 0 {
			return sessions
		}
	}
	return nil
}

// readLocalStorage reads a Chromium Local Storage LevelDB. It first tries to
// open the live database read-only; if that fails (Chromium's LOCK file is not
// shareable on Windows), it reads a copy.
func readLocalStorage(dir string) (map[string]string, error) {
	store, err := readLocalStorageDirect(dir)
	if err == nil {
		return store, nil
	}
	return readLocalStorageCopy(dir)
}

// readLocalStorageCopy copies the database to a temporary directory and reads
// the copy. The copy may miss a write racing with it, but is good enough for
// the activity timestamps used here.
func readLocalStorageCopy(dir string) (map[string]string, error) {
	tmp, err := os.MkdirTemp("", "cc-discord-presence-leveldb-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	if err := os.CopyFS(tmp, os.DirFS(dir)); err != nil {
		return nil, err
	}
	return readLocalStorageDirect(tmp)
}

// readLocalStorageDirect opens a Chromium Local Storage LevelDB read-only and
// returns the claude.ai keys and values.
func readLocalStorageDirect(dir string) (map[string]string, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}

	db, err := leveldb.OpenFile(dir, &opt.Options{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer db.Close()

	store := make(map[string]string)
	iter := db.NewIterator(nil, nil)
	defer iter.Release()

	prefix := []byte(localStorageKeyPrefix)
	for iter.Next() {
		key := iter.Key()
		if !bytes.HasPrefix(key, prefix) {
			continue
		}
		// Keep only the cloud-session keys; the rest of the app's local
		// storage is not our data.
		name := string(key[len(prefix):])
		if name != cloudDurabilityKey && name != cloudPillsKey && !strings.HasPrefix(name, cloudResultKey) {
			continue
		}
		store[name] = decodeLocalStorageValue(iter.Value())
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return store, nil
}

// decodeLocalStorageValue decodes Chromium's localStorage encoding: a leading
// 0 means UTF-16LE, a leading 1 means a single-byte string.
func decodeLocalStorageValue(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case 0:
		units := make([]uint16, 0, (len(raw)-1)/2)
		for i := 1; i+1 < len(raw); i += 2 {
			units = append(units, binary.LittleEndian.Uint16(raw[i:i+2]))
		}
		return string(utf16.Decode(units))
	case 1:
		return string(raw[1:])
	default:
		// Some Chromium versions write the string without a marker.
		return string(raw)
	}
}

// parseCloudSessions extracts cloud sessions from a decoded localStorage map.
// Malformed optional values are ignored; a malformed durability record means
// there is nothing reliable to report.
func parseCloudSessions(store map[string]string) []cloudSession {
	var durability map[string]cloudDurabilityEntry
	if err := json.Unmarshal([]byte(store[cloudDurabilityKey]), &durability); err != nil {
		return nil
	}

	models := parseCloudSessionPills(store[cloudPillsKey])

	sessions := make([]cloudSession, 0, len(durability))
	for id, entry := range durability {
		if !strings.HasPrefix(id, cloudSessionPrefix) || entry.At <= 0 {
			continue
		}

		session := cloudSession{
			ID:         id,
			ModelID:    models[id],
			LastActive: time.UnixMilli(entry.At),
		}

		if raw := store[cloudResultKey+id]; raw != "" {
			var result cloudSessionResult
			if err := json.Unmarshal([]byte(raw), &result); err == nil {
				session.CostUSD = result.CostUSD
				if session.ModelID == "" {
					session.ModelID = dominantModel(result.PerModel)
				}
			}
		}

		sessions = append(sessions, session)
	}

	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].LastActive.After(sessions[j].LastActive)
	})
	return sessions
}

// parseCloudSessionPills decodes the desktop app's [id, model, effort] rows.
func parseCloudSessionPills(raw string) map[string]string {
	var pills [][]string
	if err := json.Unmarshal([]byte(raw), &pills); err != nil {
		return nil
	}

	models := make(map[string]string, len(pills))
	for _, pill := range pills {
		if len(pill) >= 2 && pill[0] != "" && pill[1] != "" {
			models[pill[0]] = pill[1]
		}
	}
	return models
}

// dominantModel returns the model that contributed the most cost, which is the
// best local proxy for the session's primary model when no pill is stored.
func dominantModel(perModel map[string]struct {
	CostUSD float64 `json:"costUSD"`
}) string {
	var best string
	var bestCost float64
	for model, usage := range perModel {
		if best == "" || usage.CostUSD > bestCost || (usage.CostUSD == bestCost && model < best) {
			best = model
			bestCost = usage.CostUSD
		}
	}
	return best
}

// activeCloudSession returns the most recently active cloud session when it is
// newer than the local activity and still inside CloudQuietPeriod. A newer
// local transcript means the user has returned to a local session, so local
// data wins in that case.
func activeCloudSession(sessions []cloudSession, localActivity, now time.Time) *cloudSession {
	var newest *cloudSession
	for i := range sessions {
		if newest == nil || sessions[i].LastActive.After(newest.LastActive) {
			newest = &sessions[i]
		}
	}
	if newest == nil {
		return nil
	}
	if now.Sub(newest.LastActive) > CloudQuietPeriod {
		return nil
	}
	if localActivity.After(newest.LastActive) {
		return nil
	}
	return newest
}
