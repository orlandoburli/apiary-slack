// Package state persists what the plugin has already read from Slack.
//
// A plugin process is single-shot: Apiary spawns it, writes one request, reads
// one response, and the process exits. Nothing survives in memory between
// calls, so per-channel cursors and the set of watched threads live on disk —
// without them every poll would re-read the same messages. The host's poll
// `since` cannot stand in: the daemon keeps it in memory and resets it on
// restart.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Version is the state file's schema version, so a future format change can be
// migrated rather than guessed at.
const Version = 1

// File is the whole on-disk state.
type File struct {
	Version int `json:"version"`
	// Bot caches auth.test, which never changes for a given token.
	Bot      Bot                `json:"bot"`
	Channels map[string]Channel `json:"channels"`
	Threads  map[string]Thread  `json:"threads"`
}

// Bot is the identity behind the token.
type Bot struct {
	UserID string `json:"user_id,omitempty"`
	// TeamURL is the workspace base URL, used to build message permalinks
	// without spending an API call per item.
	TeamURL string `json:"team_url,omitempty"`
}

// Channel is one conversation's cursor.
type Channel struct {
	// Cursor is the ts of the last message read. It is the exclusive lower
	// bound of the next history call.
	Cursor string `json:"cursor"`
}

// Thread is a thread the bot was pulled into and still watches for replies.
type Thread struct {
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts"`
	// LastTS is the ts of the last thread message read.
	LastTS       string    `json:"last_ts"`
	LastActivity time.Time `json:"last_activity"`
}

// ThreadKey names a thread in File.Threads.
func ThreadKey(channel, threadTS string) string { return channel + ":" + threadTS }

// Load reads the state file. A missing file is not an error — it is the first
// run — and yields an empty, usable state.
//
// A corrupt file IS an error: silently starting from zero would reset every
// cursor to "now" and drop every watched thread, hiding the corruption behind
// conversations that quietly stop getting answers.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return empty(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state file %s: %w", path, err)
	}
	f := empty()
	if err := json.Unmarshal(raw, f); err != nil {
		return nil, fmt.Errorf("state file %s is corrupt (%w) — inspect it; deleting it restarts every channel from now and forgets watched threads", path, err)
	}
	if f.Version != Version {
		return nil, fmt.Errorf("state file %s has version %d, this plugin writes version %d", path, f.Version, Version)
	}
	if f.Channels == nil {
		f.Channels = map[string]Channel{}
	}
	if f.Threads == nil {
		f.Threads = map[string]Thread{}
	}
	return f, nil
}

func empty() *File {
	return &File{Version: Version, Channels: map[string]Channel{}, Threads: map[string]Thread{}}
}

// Save writes the state file atomically: a temp file in the same directory
// followed by a rename, so a crash mid-write cannot leave a truncated file
// behind. Same-directory matters — rename is only atomic within a filesystem.
func (f *File) Save(path string) error {
	f.Version = Version
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding state: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating state directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".slack-state-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp state file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp state file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing state file %s: %w", path, err)
	}
	return nil
}

// PruneThreads drops threads idle for longer than ttl, threads in channels no
// longer listened to, and — beyond max — the least recently active. Returns
// how many were dropped.
func (f *File) PruneThreads(now time.Time, ttl time.Duration, max int, listening func(channel string) bool) int {
	dropped := 0
	for key, t := range f.Threads {
		if now.Sub(t.LastActivity) > ttl || !listening(t.Channel) {
			delete(f.Threads, key)
			dropped++
		}
	}
	if len(f.Threads) <= max {
		return dropped
	}
	keys := f.ThreadKeys()
	sort.SliceStable(keys, func(i, j int) bool {
		return f.Threads[keys[i]].LastActivity.After(f.Threads[keys[j]].LastActivity)
	})
	for _, key := range keys[max:] {
		delete(f.Threads, key)
		dropped++
	}
	return dropped
}

// ThreadKeys returns the watched threads' keys in a stable order, so a poll
// that runs out of budget resumes predictably.
func (f *File) ThreadKeys() []string {
	keys := make([]string, 0, len(f.Threads))
	for key := range f.Threads {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
