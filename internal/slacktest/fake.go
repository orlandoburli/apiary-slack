// Package slacktest is an in-memory Slack Web API for tests: just enough of
// history/replies/post semantics to exercise the plugin end to end.
package slacktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"testing"

	"github.com/orlandoburli/apiary-slack/internal/slack"
)

const (
	BotUserID = "UBOT"
	TeamURL   = "https://acme.slack.com/"
	Token     = "xoxb-test"
)

// Posted is one chat.postMessage call.
type Posted struct {
	Channel, ThreadTS, Text string
	// Blocks is the raw blocks JSON when the post used Block Kit.
	Blocks string
}

// Reaction is one reactions.add call.
type Reaction struct{ Channel, TS, Name string }

// Fake is the server. Seed Messages per channel; read Posted / Reactions.
type Fake struct {
	*httptest.Server

	mu        sync.Mutex
	Messages  map[string][]slack.Message
	IMs       []string
	Posted    []Posted
	Reactions []Reaction
	Calls     map[string]int
	// Fail makes a method answer {"ok":false,"error":...}; "429" rate-limits.
	Fail map[string]string
}

func New(t *testing.T) *Fake {
	t.Helper()
	f := &Fake{Messages: map[string][]slack.Message{}, Calls: map[string]int{}, Fail: map[string]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

// Add appends a message to a channel.
func (f *Fake) Add(channel string, m slack.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Messages[channel] = append(f.Messages[channel], m)
}

func (f *Fake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	method := r.URL.Path[1:]
	f.Calls[method]++
	_ = r.ParseForm()

	if r.Header.Get("Authorization") != "Bearer "+Token {
		write(w, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	if e := f.Fail[method]; e == "429" {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	} else if e != "" {
		write(w, map[string]any{"ok": false, "error": e})
		return
	}

	channel := r.Form.Get("channel")
	switch method {
	case "auth.test":
		write(w, map[string]any{"ok": true, "user_id": BotUserID, "url": TeamURL})
	case "users.conversations":
		chans := []map[string]string{}
		for _, id := range f.IMs {
			chans = append(chans, map[string]string{"id": id})
		}
		write(w, map[string]any{"ok": true, "channels": chans})
	case "conversations.history":
		var out []slack.Message
		for _, m := range f.Messages[channel] {
			// Like Slack: top-level messages plus thread broadcasts.
			if m.IsReply() && m.Subtype != "thread_broadcast" {
				continue
			}
			if o := r.Form.Get("oldest"); o != "" && slack.CompareTS(m.TS, o) <= 0 {
				continue
			}
			if l := r.Form.Get("latest"); l != "" && slack.CompareTS(m.TS, l) >= 0 {
				continue
			}
			out = append(out, m)
		}
		sort.Slice(out, func(i, j int) bool { return slack.CompareTS(out[i].TS, out[j].TS) > 0 }) // newest first
		if limit, _ := strconv.Atoi(r.Form.Get("limit")); limit > 0 && len(out) > limit {
			out = out[:limit]
		}
		write(w, map[string]any{"ok": true, "messages": out})
	case "conversations.replies":
		root := r.Form.Get("ts")
		var out []slack.Message
		for _, m := range f.Messages[channel] {
			if m.TS == root || m.ThreadTS == root {
				out = append(out, m)
			}
		}
		write(w, map[string]any{"ok": true, "messages": out})
	case "chat.postMessage":
		f.Posted = append(f.Posted, Posted{Channel: channel, ThreadTS: r.Form.Get("thread_ts"), Text: r.Form.Get("text"), Blocks: r.Form.Get("blocks")})
		write(w, map[string]any{"ok": true})
	case "reactions.add":
		f.Reactions = append(f.Reactions, Reaction{channel, r.Form.Get("timestamp"), r.Form.Get("name")})
		write(w, map[string]any{"ok": true})
	default:
		write(w, map[string]any{"ok": false, "error": fmt.Sprintf("unknown_method %s", method)})
	}
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
