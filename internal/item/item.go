// Package item turns Slack messages into Apiary work items and back.
//
// Plugin sources cannot resume a parked task, so a conversation is not one
// long-lived task: every human turn is its own work item. Continuity comes
// from the item itself — a reply carries the thread so far as a transcript —
// and from the id, which names the thread the answer must be posted into.
package item

import (
	"fmt"
	"html"
	"strings"
	"time"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"

	"github.com/orlandoburli/apiary-slack/internal/slack"
)

const (
	KindMention = "mention"
	KindMessage = "message"
	KindDM      = "dm"

	idPrefix = "slack"
	// maxMessageChars bounds one message inside a transcript; a pasted log
	// should not crowd out the rest of the conversation.
	maxMessageChars = 4000
	maxTitleChars   = 80
)

// Ref locates a message: where it is and which thread an answer belongs in.
type Ref struct {
	Channel  string
	ThreadTS string
	TS       string
}

// ID renders the dedup key, "slack:<channel>:<thread_ts>:<ts>". It is
// self-sufficient on purpose: write_result can find the thread from the id
// alone, without depending on metadata surviving the round trip.
func (r Ref) ID() string {
	return strings.Join([]string{idPrefix, r.Channel, r.ThreadTS, r.TS}, ":")
}

// ParseID is the inverse of Ref.ID.
func ParseID(id string) (Ref, error) {
	parts := strings.Split(id, ":")
	if len(parts) != 4 || parts[0] != idPrefix || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return Ref{}, fmt.Errorf("item id %q is not slack:<channel>:<thread_ts>:<ts>", id)
	}
	return Ref{Channel: parts[1], ThreadTS: parts[2], TS: parts[3]}, nil
}

// Input is everything needed to build one work item.
type Input struct {
	Message slack.Message
	Channel string
	Kind    string
	// Earlier is the thread before Message, oldest first; empty for the
	// message that opens a conversation.
	Earlier []slack.Message
	// TranscriptLimit caps how many Earlier messages are carried.
	TranscriptLimit int
	BotUserID       string
	TeamURL         string
	Labels          []string
}

// Build renders the work item for one human turn.
func Build(in Input) pluginsdk.SourceItem {
	threadTS := in.Message.ThreadTS
	if threadTS == "" {
		threadTS = in.Message.TS
	}
	ref := Ref{Channel: in.Channel, ThreadTS: threadTS, TS: in.Message.TS}
	text := Clean(in.Message.Text, in.BotUserID)
	turn := "first"
	if len(in.Earlier) > 0 {
		turn = "reply"
	}
	at := slack.TSTime(in.Message.TS).Format(time.RFC3339)

	labels := append([]string{"slack", "channel:" + in.Channel, "kind:" + in.Kind, "turn:" + turn}, in.Labels...)
	return pluginsdk.SourceItem{
		ID:          ref.ID(),
		Number:      in.Channel + "/" + in.Message.TS,
		Title:       title(text),
		Description: describe(in, text),
		Labels:      labels,
		Type:        "conversation",
		URL:         Permalink(in.TeamURL, ref),
		Metadata: map[string]any{
			"channel":   ref.Channel,
			"thread_ts": ref.ThreadTS,
			"ts":        ref.TS,
			"user":      in.Message.User,
			"kind":      in.Kind,
			"turn":      turn,
		},
		CreatedAt: at,
		UpdatedAt: at,
	}
}

func describe(in Input, text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Slack %s from <@%s>. Your published output is posted back as a reply in the same Slack thread.\n\n", in.Kind, in.Message.User)

	earlier := in.Earlier
	if in.TranscriptLimit > 0 && len(earlier) > in.TranscriptLimit {
		fmt.Fprintf(&b, "## Conversation so far (last %d of %d messages)\n\n", in.TranscriptLimit, len(earlier))
		earlier = earlier[len(earlier)-in.TranscriptLimit:]
	} else if len(earlier) > 0 {
		b.WriteString("## Conversation so far\n\n")
	}
	for _, m := range earlier {
		fmt.Fprintf(&b, "**%s**: %s\n\n", speaker(m, in.BotUserID), truncate(Clean(m.Text, in.BotUserID), maxMessageChars))
	}
	if len(earlier) > 0 {
		b.WriteString("## Latest message\n\n")
	}
	b.WriteString(text)
	b.WriteString("\n")
	return b.String()
}

func speaker(m slack.Message, botUserID string) string {
	switch {
	case m.User != "" && m.User == botUserID:
		return "assistant (you)"
	case m.User != "":
		return "<@" + m.User + ">"
	default:
		return "bot"
	}
}

// Clean makes Slack's wire text readable: the bot's own mention is dropped
// (it is addressing, not content) and the three HTML entities Slack escapes
// are restored.
func Clean(text, botUserID string) string {
	if botUserID != "" {
		text = strings.ReplaceAll(text, "<@"+botUserID+">", "")
	}
	return strings.TrimSpace(html.UnescapeString(text))
}

// Mentions reports whether text @mentions the user.
func Mentions(text, userID string) bool {
	return userID != "" && (strings.Contains(text, "<@"+userID+">") || strings.Contains(text, "<@"+userID+"|"))
}

func title(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "Slack message"
	}
	return truncate(line, maxTitleChars)
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// Permalink builds a message link from the workspace URL, saving a
// chat.getPermalink call per item. Empty when the workspace URL is unknown.
func Permalink(teamURL string, ref Ref) string {
	if teamURL == "" {
		return ""
	}
	link := strings.TrimRight(teamURL, "/") + "/archives/" + ref.Channel + "/p" + strings.ReplaceAll(ref.TS, ".", "")
	if ref.ThreadTS != ref.TS {
		link += "?thread_ts=" + ref.ThreadTS + "&cid=" + ref.Channel
	}
	return link
}
