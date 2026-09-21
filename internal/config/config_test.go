package config

import (
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	t.Setenv(TokenEnv, "xoxb-1")
	cfg, err := Parse(map[string]any{
		"state_file":   "/var/lib/apiary/slack.json",
		"channels":     []any{map[string]any{"id": "C1"}, map[string]any{"id": "C2", "mode": "all"}},
		"thread_ttl":   "2h",
		"ack_reaction": ":eyes:",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Channels[0].Mode != ModeMentions || cfg.Channels[1].Mode != ModeAll {
		t.Errorf("modes = %+v", cfg.Channels)
	}
	if cfg.ThreadTTL != 2*time.Hour || cfg.MaxPerPoll != DefaultMaxPerPoll || cfg.APIURL != DefaultAPIURL || !cfg.PostFailures || cfg.AckReaction != "eyes" {
		t.Errorf("defaults = %+v", cfg)
	}
	if !cfg.Allowed("anyone") {
		t.Error("empty allowed_users must allow everyone")
	}
}

func TestParseRejects(t *testing.T) {
	ok := func() map[string]any {
		return map[string]any{"state_file": "/s.json", "channels": []any{map[string]any{"id": "C1"}}}
	}
	cases := map[string]func(map[string]any){
		"absolute":          func(m map[string]any) { m["state_file"] = "s.json" },
		"nothing to listen": func(m map[string]any) { delete(m, "channels") },
		"not a #name":       func(m map[string]any) { m["channels"] = []any{map[string]any{"id": "#general"}} },
		"listed twice":      func(m map[string]any) { m["channels"] = []any{map[string]any{"id": "C1"}, map[string]any{"id": "C1"}} },
		"must be":           func(m map[string]any) { m["channels"] = []any{map[string]any{"id": "C1", "mode": "loud"}} },
		"thread_ttl":        func(m map[string]any) { m["thread_ttl"] = "soon" },
	}
	t.Setenv(TokenEnv, "xoxb-1")
	for want, mutate := range cases {
		in := ok()
		mutate(in)
		if _, err := Parse(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
	t.Setenv(TokenEnv, "")
	if _, err := Parse(ok()); err == nil || !strings.Contains(err.Error(), TokenEnv) {
		t.Errorf("missing token: err = %v", err)
	}
}
