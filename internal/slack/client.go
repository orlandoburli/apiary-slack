// Package slack is the small slice of the Slack Web API the plugin needs.
//
// Everything is an outbound HTTPS call: reading is conversations.history /
// conversations.replies on the host's poll interval, never Socket Mode or the
// Events API. A plugin process lives for one request, so it could not hold a
// socket open — and Apiary is polling-only by design.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxHistoryPages bounds one history read (200 messages a page). A channel
// busier than that between two polls is read up to the bound and the rest is
// picked up next poll, because the cursor only advances past what was read.
const maxHistoryPages = 5

// Message is a Slack message, reduced to the fields the plugin reads.
type Message struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	User     string `json:"user"`
	BotID    string `json:"bot_id"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
}

// Human reports whether a person wrote the message: no subtype (joins, edits,
// bot_message, …), no bot id, and an author. thread_broadcast is a person
// replying in a thread with "also send to channel".
func (m Message) Human() bool {
	return (m.Subtype == "" || m.Subtype == "thread_broadcast") && m.BotID == "" && m.User != ""
}

// IsReply reports whether the message is a reply inside a thread rather than
// a top-level message (a thread's root has thread_ts == ts).
func (m Message) IsReply() bool { return m.ThreadTS != "" && m.ThreadTS != m.TS }

// Identity is the result of auth.test.
type Identity struct {
	UserID  string
	TeamURL string
}

// RateLimitedError is Slack's HTTP 429.
type RateLimitedError struct {
	Method     string
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("slack %s: rate limited, retry after %s", e.Method, e.RetryAfter)
}

// Client calls the Slack Web API with a bot token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New returns a client. Per-call timeouts stay well under the host's default
// 10s invocation budget so one slow call cannot eat the whole poll.
func New(baseURL, token string) *Client {
	return &Client{BaseURL: baseURL, Token: token, HTTP: &http.Client{Timeout: 8 * time.Second}}
}

func (c *Client) call(ctx context.Context, method string, params url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/"+method, strings.NewReader(params.Encode()))
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		secs, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return &RateLimitedError{Method: method, RetryAfter: time.Duration(secs) * time.Second}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("slack %s: reading response: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack %s: HTTP %d", method, resp.StatusCode)
	}
	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("slack %s: decoding response: %w", method, err)
	}
	if !envelope.OK {
		return fmt.Errorf("slack %s: %s", method, envelope.Error)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("slack %s: decoding result: %w", method, err)
		}
	}
	return nil
}

// AuthTest identifies the bot behind the token.
func (c *Client) AuthTest(ctx context.Context) (Identity, error) {
	var out struct {
		UserID string `json:"user_id"`
		URL    string `json:"url"`
	}
	if err := c.call(ctx, "auth.test", url.Values{}, &out); err != nil {
		return Identity{}, err
	}
	return Identity{UserID: out.UserID, TeamURL: out.URL}, nil
}

type page struct {
	Messages         []Message `json:"messages"`
	HasMore          bool      `json:"has_more"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

func (c *Client) paged(ctx context.Context, method string, params url.Values, maxPages int) ([]Message, error) {
	var all []Message
	for i := 0; i < maxPages; i++ {
		var p page
		if err := c.call(ctx, method, params, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Messages...)
		if !p.HasMore || p.ResponseMetadata.NextCursor == "" {
			break
		}
		params.Set("cursor", p.ResponseMetadata.NextCursor)
	}
	// history is newest-first, replies oldest-first; callers want one order.
	sort.SliceStable(all, func(i, j int) bool { return CompareTS(all[i].TS, all[j].TS) < 0 })
	return all, nil
}

// History returns a conversation's top-level messages strictly newer than
// oldest, oldest first.
func (c *Client) History(ctx context.Context, channel, oldest string) ([]Message, error) {
	params := url.Values{"channel": {channel}, "limit": {"200"}, "inclusive": {"false"}}
	if oldest != "" {
		params.Set("oldest", oldest)
	}
	return c.paged(ctx, "conversations.history", params, maxHistoryPages)
}

// Before returns up to limit messages strictly older than latest, oldest first.
func (c *Client) Before(ctx context.Context, channel, latest string, limit int) ([]Message, error) {
	params := url.Values{"channel": {channel}, "latest": {latest}, "inclusive": {"false"}, "limit": {strconv.Itoa(limit)}}
	return c.paged(ctx, "conversations.history", params, 1)
}

// Replies returns a whole thread, root included, oldest first.
func (c *Client) Replies(ctx context.Context, channel, threadTS string) ([]Message, error) {
	params := url.Values{"channel": {channel}, "ts": {threadTS}, "limit": {"200"}}
	return c.paged(ctx, "conversations.replies", params, maxHistoryPages)
}

// DirectMessageChannels lists the bot's open 1:1 conversations.
func (c *Client) DirectMessageChannels(ctx context.Context) ([]string, error) {
	params := url.Values{"types": {"im"}, "limit": {"200"}, "exclude_archived": {"true"}}
	var ids []string
	for i := 0; i < maxHistoryPages; i++ {
		var out struct {
			Channels []struct {
				ID string `json:"id"`
			} `json:"channels"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := c.call(ctx, "users.conversations", params, &out); err != nil {
			return nil, err
		}
		for _, ch := range out.Channels {
			ids = append(ids, ch.ID)
		}
		if out.ResponseMetadata.NextCursor == "" {
			break
		}
		params.Set("cursor", out.ResponseMetadata.NextCursor)
	}
	sort.Strings(ids)
	return ids, nil
}

// PostMessage posts mrkdwn text into a thread.
func (c *Client) PostMessage(ctx context.Context, channel, threadTS, text string) error {
	params := url.Values{"channel": {channel}, "text": {text}, "mrkdwn": {"true"}, "unfurl_links": {"false"}}
	if threadTS != "" {
		params.Set("thread_ts", threadTS)
	}
	return c.call(ctx, "chat.postMessage", params, nil)
}

// AddReaction reacts to a message. Reacting twice is not an error worth
// surfacing: acknowledge can legitimately be retried.
func (c *Client) AddReaction(ctx context.Context, channel, ts, name string) error {
	err := c.call(ctx, "reactions.add", url.Values{"channel": {channel}, "timestamp": {ts}, "name": {name}}, nil)
	if err != nil && strings.HasSuffix(err.Error(), ": already_reacted") {
		return nil
	}
	return err
}

// CompareTS orders two Slack timestamps ("1712345678.000100"). They are
// decimal strings, not floats: float64 cannot hold the microsecond part
// exactly, and a plain string compare breaks if the widths ever differ.
func CompareTS(a, b string) int {
	as, am := splitTS(a)
	bs, bm := splitTS(b)
	switch {
	case as != bs:
		return cmp(as, bs)
	default:
		return cmp(am, bm)
	}
}

func splitTS(ts string) (int64, int64) {
	sec, micro, _ := strings.Cut(ts, ".")
	s, _ := strconv.ParseInt(sec, 10, 64)
	m, _ := strconv.ParseInt(micro, 10, 64)
	return s, m
}

func cmp(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// TSTime converts a Slack timestamp to a time.
func TSTime(ts string) time.Time {
	s, m := splitTS(ts)
	return time.Unix(s, m*1000).UTC()
}

// NowTS renders a time as a Slack timestamp.
func NowTS(t time.Time) string {
	return fmt.Sprintf("%d.%06d", t.Unix(), t.Nanosecond()/1000)
}
