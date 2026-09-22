// Package reply posts a run's outcome back into the Slack conversation the
// item came from.
//
// The host calls write_result for every comment a workflow publishes —
// APIARY_PUBLISH blocks, result_comment, post_comment steps — so one item can
// produce several replies, and they all land in the same thread.
package reply

import (
	"context"
	"regexp"
	"strings"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"

	"github.com/orlandoburli/apiary-slack/internal/config"
	"github.com/orlandoburli/apiary-slack/internal/item"
	"github.com/orlandoburli/apiary-slack/internal/slack"
)

// maxChunk keeps each message under the ~4000 characters Slack recommends;
// longer text is accepted but truncated in some clients.
const maxChunk = 3500

// Write posts the outcome. Success posts the output; failure posts a short
// notice when post_failures is on. An empty output posts nothing.
func Write(ctx context.Context, cfg *config.Config, client *slack.Client, req pluginsdk.SourceWriteResultRequest) error {
	ref, err := item.ParseID(req.Item.ID)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(req.Output)
	if !req.Success {
		if !cfg.PostFailures {
			return nil
		}
		text = ":warning: The run failed."
		if detail := strings.TrimSpace(req.Error); detail != "" {
			text += "\n```\n" + detail + "\n```"
		}
	} else {
		text = Mrkdwn(text)
	}
	if text == "" {
		return nil
	}
	for _, chunk := range Chunks(text, maxChunk) {
		if err := client.PostMessage(ctx, ref.Channel, threadFor(ref), chunk); err != nil {
			return err
		}
	}
	return nil
}

// threadFor picks where the answer goes: a DM is one running conversation,
// answered in line; a thread gets a reply.
func threadFor(ref item.Ref) string { return ref.ThreadTS }

// Acknowledge marks the turn Apiary picked up with the configured reaction.
// The item's metadata names that turn; without it there is nothing to react to.
func Acknowledge(ctx context.Context, cfg *config.Config, client *slack.Client, req pluginsdk.SourceAckRequest) error {
	if cfg.AckReaction == "" {
		return nil
	}
	ref, err := item.ParseID(req.Item.ID)
	if err != nil {
		return err
	}
	ts, _ := req.Item.Metadata["ts"].(string)
	if ts == "" {
		return nil
	}
	return client.AddReaction(ctx, ref.Channel, ts, cfg.AckReaction)
}

var (
	mdLink    = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	mdBold    = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	mdHeading = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.+?)[ \t]*#*$`)
	mdQuote   = regexp.MustCompile(`(?m)^&gt;[ \t]?`)
	mdBullet  = regexp.MustCompile(`(?m)^([ \t]*)[*-][ \t]+`)
)

// Mrkdwn converts the Markdown agents write into Slack's mrkdwn: escapes the
// three control characters, then rewrites links, bold, headings and bullets.
// Fenced code is left untouched apart from escaping.
func Mrkdwn(md string) string {
	md = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(md)
	parts := strings.Split(md, "```")
	for i := 0; i < len(parts); i += 2 { // even parts are outside fences
		p := parts[i]
		p = mdQuote.ReplaceAllString(p, "> ") // a leading > is a blockquote, not text
		p = mdBullet.ReplaceAllString(p, "$1• ")
		p = mdLink.ReplaceAllString(p, "<$2|$1>")
		p = mdBold.ReplaceAllString(p, "*$1*")
		p = mdHeading.ReplaceAllString(p, "*$1*")
		parts[i] = p
	}
	return strings.Join(parts, "```")
}

// Chunks splits text into pieces of at most max bytes, preferring line
// boundaries. A single line longer than max is cut hard, on a rune boundary.
func Chunks(text string, max int) []string {
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimRight(cur.String(), "\n"); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		for len(line) > max {
			flush()
			cut := max
			for cut > 0 && !isRuneStart(line[cut]) {
				cut--
			}
			chunks = append(chunks, line[:cut])
			line = line[cut:]
		}
		if cur.Len()+len(line) > max {
			flush()
		}
		cur.WriteString(line)
	}
	flush()
	return chunks
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
