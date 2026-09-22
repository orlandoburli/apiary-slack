// Package item turns a Slack conversation into an Apiary work item and back.
//
// The item is the conversation — a thread, or a DM channel — not a message.
// Its id is stable for the life of the conversation, so Apiary binds every
// turn to the same task and the dashboard shows one task per thread with one
// workflow instance per turn. What changes between turns is the item's state
// (pending while a human turn awaits an answer, answered otherwise) and its
// description, which carries the whole thread as a transcript. Plugin sources
// cannot resume a parked task, so this is how a conversation spans turns.
package item

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"

	"github.com/orlandoburli/apiary-slack/internal/slack"
)

const (
	KindMention = "mention"
	KindMessage = "message"
	KindDM      = "dm"

	StatePending  = "pending"
	StateAnswered = "answered"

	idPrefix = "slack"
	// dmMarker stands in for the thread ts in a DM's id: a DM is one running
	// conversation, not a thread.
	dmMarker = "dm"
	// maxMessageChars bounds one message inside a transcript; a pasted log
	// should not crowd out the rest of the conversation.
	maxMessageChars = 4000
	maxTitleChars   = 80
)

// Ref locates a conversation: the channel and, for a thread, its root.
// ThreadTS is empty for a DM.
type Ref struct {
	Channel  string
	ThreadTS string
}

// ID renders the dedup key, "slack:<channel>:<thread_ts>" (or "slack:<dm>:dm").
// It is self-sufficient on purpose: write_result finds the conversation from
// the id alone, without depending on metadata surviving the round trip.
func (r Ref) ID() string {
	ts := r.ThreadTS
	if ts == "" {
		ts = dmMarker
	}
	return strings.Join([]string{idPrefix, r.Channel, ts}, ":")
}

// ParseID is the inverse of Ref.ID.
func ParseID(id string) (Ref, error) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != idPrefix || parts[1] == "" || parts[2] == "" {
		return Ref{}, fmt.Errorf("item id %q is not slack:<channel>:<thread_ts>", id)
	}
	ref := Ref{Channel: parts[1], ThreadTS: parts[2]}
	if ref.ThreadTS == dmMarker {
		ref.ThreadTS = ""
	}
	return ref, nil
}

// Input is everything needed to render one conversation.
type Input struct {
	Ref  Ref
	Kind string
	// Messages is the conversation so far, oldest first.
	Messages []slack.Message
	// DispatchedTS is the ts of the last human turn Apiary was already handed;
	// human messages after it are the ones awaiting an answer.
	DispatchedTS string
	// LastHumanTS is the ts of the latest turn that qualifies for an answer.
	LastHumanTS string
	// Turns is how many human turns the conversation has had.
	Turns int
	// TranscriptLimit caps how many earlier messages are carried.
	TranscriptLimit int
	BotUserID       string
	TeamURL         string
	Labels          []string
	// Pending is whether the conversation awaits an answer.
	Pending bool
}

// Build renders the work item for one conversation.
func Build(in Input) pluginsdk.SourceItem {
	state := StateAnswered
	if in.Pending {
		state = StatePending
	}
	turn := "reply"
	if in.Turns <= 1 {
		turn = "first"
	}
	root, latest := bounds(in)
	title := "Slack conversation"
	if root != nil {
		title = titleOf(Clean(root.Text, in.BotUserID))
	}
	created := time.Now().UTC().Format(time.RFC3339)
	if root != nil {
		created = slack.TSTime(root.TS).Format(time.RFC3339)
	}
	updated := created
	if latest != nil {
		updated = slack.TSTime(latest.TS).Format(time.RFC3339)
	}

	labels := append([]string{"slack", "channel:" + in.Ref.Channel, "kind:" + in.Kind, "turn:" + turn}, in.Labels...)
	number := in.Ref.Channel
	if in.Ref.ThreadTS != "" {
		number += "/" + in.Ref.ThreadTS
	}
	return pluginsdk.SourceItem{
		ID:          in.Ref.ID(),
		Number:      number,
		Title:       title,
		Description: describe(in),
		Labels:      labels,
		Type:        "conversation",
		State:       state,
		URL:         Permalink(in.TeamURL, in.Ref),
		Metadata: map[string]any{
			"channel":   in.Ref.Channel,
			"thread_ts": in.Ref.ThreadTS,
			"ts":        in.LastHumanTS,
			"kind":      in.Kind,
			"turns":     strconv.Itoa(in.Turns),
		},
		CreatedAt: created,
		UpdatedAt: updated,
	}
}

// bounds returns the first and last message, or nil when there are none.
func bounds(in Input) (root, latest *slack.Message) {
	if len(in.Messages) == 0 {
		return nil, nil
	}
	return &in.Messages[0], &in.Messages[len(in.Messages)-1]
}

func describe(in Input) string {
	var b strings.Builder
	where := "thread"
	if in.Kind == KindDM {
		where = "direct message"
	}
	fmt.Fprintf(&b, "Slack %s (%s). Your published output is posted back as a reply in the same Slack %s.\n\n", in.Kind, in.Ref.ID(), where)

	// Split at the dispatch watermark: everything up to and including the
	// last dispatched human turn (and any answers to it) is context; what
	// follows is what the person is waiting on now.
	split := len(in.Messages)
	for i, m := range in.Messages {
		if m.Human() && in.DispatchedTS != "" && slack.CompareTS(m.TS, in.DispatchedTS) > 0 {
			split = i
			break
		}
	}
	if in.DispatchedTS == "" && len(in.Messages) > 0 {
		// Nothing dispatched yet: the whole conversation is new. Keep the
		// latest human turn as the message to answer and the rest as context.
		split = 0
		for i := len(in.Messages) - 1; i >= 0; i-- {
			if in.Messages[i].Human() {
				split = i
				break
			}
		}
	}
	earlier, latest := in.Messages[:split], in.Messages[split:]

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
		if len(latest) > 1 {
			b.WriteString("## Latest messages (unanswered)\n\n")
		} else {
			b.WriteString("## Latest message\n\n")
		}
	}
	for i, m := range latest {
		text := truncate(Clean(m.Text, in.BotUserID), maxMessageChars)
		if len(latest) == 1 && len(earlier) == 0 {
			fmt.Fprintf(&b, "From <@%s>:\n\n%s\n", m.User, text)
			continue
		}
		fmt.Fprintf(&b, "**%s**: %s\n", speaker(m, in.BotUserID), text)
		if i < len(latest)-1 {
			b.WriteString("\n")
		}
	}
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

func titleOf(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "Slack conversation"
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

// Permalink builds a link to the conversation from the workspace URL, saving
// a chat.getPermalink call per item. Empty when the workspace URL is unknown;
// a DM links to the channel.
func Permalink(teamURL string, ref Ref) string {
	if teamURL == "" {
		return ""
	}
	link := strings.TrimRight(teamURL, "/") + "/archives/" + ref.Channel
	if ref.ThreadTS != "" {
		link += "/p" + strings.ReplaceAll(ref.ThreadTS, ".", "")
	}
	return link
}
