// Package config decodes and validates the plugin's `plugins[].config` block.
//
// Apiary validates the block against the manifest's config_schema before the
// plugin ever runs, but that validator is a JSON Schema subset (no "pattern",
// no cross-field rules), so everything the schema cannot express is enforced
// here, at the top of every invocation.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TokenEnv is the environment variable holding the bot token. It is fixed
// rather than configurable because the host only passes through variables
// named in the manifest's security.secret_env.
const TokenEnv = "SLACK_BOT_TOKEN"

const (
	ModeMentions = "mentions"
	ModeAll      = "all"

	DefaultAPIURL          = "https://slack.com/api"
	DefaultMaxPerPoll      = 10
	DefaultThreadTTL       = 24 * time.Hour
	DefaultMaxThreads      = 50
	DefaultTranscriptLimit = 50
)

// Channel is one channel the bot listens in.
type Channel struct {
	ID     string   `json:"id"`
	Mode   string   `json:"mode"`
	Labels []string `json:"labels"`
}

// Config is the validated plugin configuration, defaults applied.
type Config struct {
	StateFile       string
	Channels        []Channel
	DirectMessages  bool
	AllowedUsers    map[string]bool
	MaxPerPoll      int
	ThreadTTL       time.Duration
	MaxThreads      int
	TranscriptLimit int
	AckReaction     string
	PostFailures    bool
	APIURL          string
	Token           string
}

type raw struct {
	StateFile       string    `json:"state_file"`
	Channels        []Channel `json:"channels"`
	DirectMessages  bool      `json:"direct_messages"`
	AllowedUsers    []string  `json:"allowed_users"`
	MaxPerPoll      int       `json:"max_per_poll"`
	ThreadTTL       string    `json:"thread_ttl"`
	MaxThreads      int       `json:"max_threads"`
	TranscriptLimit int       `json:"transcript_limit"`
	AckReaction     string    `json:"ack_reaction"`
	PostFailures    *bool     `json:"post_failures"`
	APIURL          string    `json:"api_url"`
}

// Parse validates the host-supplied config map and reads the bot token from
// the environment.
func Parse(in map[string]any) (*Config, error) {
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	var r raw
	if err := json.Unmarshal(encoded, &r); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}

	if !filepath.IsAbs(r.StateFile) {
		return nil, fmt.Errorf("state_file %q must be an absolute path — a plugin's working directory is its own install directory", r.StateFile)
	}
	if len(r.Channels) == 0 && !r.DirectMessages {
		return nil, fmt.Errorf("nothing to listen to: configure channels and/or direct_messages: true")
	}

	cfg := &Config{
		StateFile:       r.StateFile,
		DirectMessages:  r.DirectMessages,
		AllowedUsers:    map[string]bool{},
		MaxPerPoll:      orDefault(r.MaxPerPoll, DefaultMaxPerPoll),
		ThreadTTL:       DefaultThreadTTL,
		MaxThreads:      orDefault(r.MaxThreads, DefaultMaxThreads),
		TranscriptLimit: orDefault(r.TranscriptLimit, DefaultTranscriptLimit),
		AckReaction:     strings.Trim(strings.TrimSpace(r.AckReaction), ":"),
		PostFailures:    r.PostFailures == nil || *r.PostFailures,
		APIURL:          strings.TrimRight(strings.TrimSpace(r.APIURL), "/"),
		Token:           strings.TrimSpace(os.Getenv(TokenEnv)),
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("%s is not set in the daemon's environment", TokenEnv)
	}
	if r.ThreadTTL != "" {
		ttl, err := time.ParseDuration(r.ThreadTTL)
		if err != nil || ttl <= 0 {
			return nil, fmt.Errorf("thread_ttl %q is not a positive Go duration (e.g. 24h)", r.ThreadTTL)
		}
		cfg.ThreadTTL = ttl
	}

	seen := map[string]bool{}
	for i, ch := range r.Channels {
		ch.ID = strings.TrimSpace(ch.ID)
		if ch.ID == "" || strings.HasPrefix(ch.ID, "#") {
			return nil, fmt.Errorf("channels[%d].id %q must be a Slack channel id (C… or G…), not a #name", i, ch.ID)
		}
		if seen[ch.ID] {
			return nil, fmt.Errorf("channels[%d].id %q is listed twice", i, ch.ID)
		}
		seen[ch.ID] = true
		switch ch.Mode {
		case "":
			ch.Mode = ModeMentions
		case ModeMentions, ModeAll:
		default:
			return nil, fmt.Errorf("channels[%d].mode %q must be %q or %q", i, ch.Mode, ModeMentions, ModeAll)
		}
		cfg.Channels = append(cfg.Channels, ch)
	}
	for _, u := range r.AllowedUsers {
		if u = strings.TrimSpace(u); u != "" {
			cfg.AllowedUsers[u] = true
		}
	}
	return cfg, nil
}

// Allowed reports whether a Slack user may start or continue a conversation.
func (c *Config) Allowed(user string) bool {
	return len(c.AllowedUsers) == 0 || c.AllowedUsers[user]
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
