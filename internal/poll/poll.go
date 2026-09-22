// Package poll reads new Slack messages and reports each conversation that is
// waiting for an answer as a work item.
//
// One poll makes two passes. The channel pass reads every listened
// conversation's new top-level messages: a qualifying one opens a conversation
// (its thread starts being watched; in a DM the channel itself is the
// conversation). The thread pass re-reads each watched thread, records the
// human turns it has not seen, and emits an item for every conversation whose
// latest human turn is newer than the one Apiary last acknowledged. The item's
// id names the conversation, so every turn lands on the same Apiary task.
//
// Emission is idempotent: a pending conversation is returned on every poll
// until acknowledge records its dispatch, and Apiary's own guards (a live
// instance, the trigger's `states: [pending]`) decide whether it runs.
package poll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"

	"github.com/orlandoburli/apiary-slack/internal/config"
	"github.com/orlandoburli/apiary-slack/internal/item"
	"github.com/orlandoburli/apiary-slack/internal/slack"
	"github.com/orlandoburli/apiary-slack/internal/state"
)

// Poller runs one poll.
type Poller struct {
	Config *config.Config
	Client *slack.Client
	Now    func() time.Time
	Logf   func(format string, args ...any)
}

// target is one conversation being listened to.
type target struct {
	mode   string
	kind   string
	labels []string
}

// Poll returns a work item for every conversation awaiting an answer.
func (p *Poller) Poll(ctx context.Context) (pluginsdk.SourcePollResult, error) {
	empty := pluginsdk.SourcePollResult{Items: []pluginsdk.SourceItem{}}
	now := p.Now()

	st, err := state.Load(p.Config.StateFile)
	if err != nil {
		return empty, err
	}
	if st.Bot.UserID == "" {
		id, err := p.Client.AuthTest(ctx)
		if err != nil {
			return empty, err
		}
		st.Bot = state.Bot{UserID: id.UserID, TeamURL: id.TeamURL}
	}

	targets, order, err := p.targets(ctx)
	if err != nil {
		return empty, err
	}
	if n := st.PruneThreads(now, p.Config.ThreadTTL, p.Config.MaxThreads, func(ch string) bool { _, ok := targets[ch]; return ok }); n > 0 {
		p.Logf("stopped watching %d conversation(s): idle past thread_ttl, channel no longer configured, or over max_threads", n)
	}

	run := &run{p: p, ctx: ctx, st: st, now: now, budget: p.Config.MaxPerPoll, items: empty.Items}
	for _, ch := range order {
		if stop := run.guard(run.channel(ch, targets[ch])); stop {
			break
		}
	}
	for _, key := range st.ThreadKeys() {
		if run.stopped {
			break
		}
		t := st.Threads[key]
		if stop := run.guard(run.conversation(key, t, targets[t.Channel])); stop {
			break
		}
	}

	// Saving is not optional: what was read is behind the cursors being
	// written, and a broken state path must stay loud rather than let every
	// poll re-read Slack from the same spot.
	if err := st.Save(p.Config.StateFile); err != nil {
		return empty, err
	}
	if len(run.items) == 0 && run.firstErr != nil {
		return empty, run.firstErr
	}
	return pluginsdk.SourcePollResult{Items: run.items}, nil
}

// Acknowledge records that Apiary dispatched a conversation's item: every
// human turn up to the one the item carried counts as handed over, so the
// conversation stops being pending until someone writes again.
func Acknowledge(cfg *config.Config, req pluginsdk.SourceAckRequest) error {
	ref, err := item.ParseID(req.Item.ID)
	if err != nil {
		return err
	}
	st, err := state.Load(cfg.StateFile)
	if err != nil {
		return err
	}
	key := state.ThreadKey(ref.Channel, ref.ThreadTS)
	t, ok := st.Threads[key]
	if !ok {
		return nil // pruned meanwhile; nothing to mark
	}
	// The item says which turn it carried; that — not whatever arrived since —
	// is what the agent will answer.
	ts, _ := req.Item.Metadata["ts"].(string)
	if ts == "" || slack.CompareTS(ts, t.LastHumanTS) > 0 {
		ts = t.LastHumanTS
	}
	if slack.CompareTS(ts, t.DispatchedTS) > 0 {
		t.DispatchedTS = ts
		st.Threads[key] = t
	}
	return st.Save(cfg.StateFile)
}

// DecodeAck is a convenience for main: it decodes the payload and applies it.
func DecodeAck(cfg *config.Config, payload json.RawMessage) (pluginsdk.SourceAckRequest, error) {
	var req pluginsdk.SourceAckRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return req, err
	}
	return req, Acknowledge(cfg, req)
}

// targets resolves what to listen to: configured channels, plus the bot's
// open DMs when direct_messages is on.
func (p *Poller) targets(ctx context.Context) (map[string]target, []string, error) {
	targets := map[string]target{}
	var order []string
	for _, ch := range p.Config.Channels {
		kind := item.KindMention
		if ch.Mode == config.ModeAll {
			kind = item.KindMessage
		}
		targets[ch.ID] = target{mode: ch.Mode, kind: kind, labels: ch.Labels}
		order = append(order, ch.ID)
	}
	if p.Config.DirectMessages {
		ims, err := p.Client.DirectMessageChannels(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, id := range ims {
			if _, dup := targets[id]; dup {
				continue
			}
			targets[id] = target{mode: config.ModeAll, kind: item.KindDM}
			order = append(order, id)
		}
	}
	return targets, order, nil
}

type run struct {
	p        *Poller
	ctx      context.Context
	st       *state.File
	now      time.Time
	budget   int
	items    []pluginsdk.SourceItem
	firstErr error
	stopped  bool
	// replies caches each thread's messages read this poll, so emission does
	// not read a thread twice.
	replies map[string][]slack.Message
}

// guard records a failed read and decides whether the poll goes on. One
// unreadable channel (bot not invited, say) must not silence the others; a
// rate limit or an expired deadline means every further call fails too.
func (r *run) guard(err error) (stop bool) {
	if err == nil {
		r.stopped = r.stopped || r.budget <= 0
		return r.stopped
	}
	r.p.Logf("%v", err)
	if r.firstErr == nil {
		r.firstErr = err
	}
	var limited *slack.RateLimitedError
	if errors.As(err, &limited) || r.ctx.Err() != nil {
		r.stopped = true
	}
	return r.stopped
}

// channel reads a conversation's new top-level messages. In a channel a
// qualifying message opens a thread conversation; in a DM every message
// belongs to the one running conversation.
func (r *run) channel(id string, t target) error {
	cur, known := r.st.Channels[id]
	if !known {
		// First sight of a conversation starts from now: installing the
		// plugin must not answer last month's messages.
		r.st.Channels[id] = state.Channel{Cursor: slack.NowTS(r.now)}
		return nil
	}
	msgs, err := r.p.Client.History(r.ctx, id, cur.Cursor)
	if err != nil {
		return fmt.Errorf("channel %s: %w", id, err)
	}
	for _, m := range msgs {
		if slack.CompareTS(m.TS, cur.Cursor) <= 0 {
			continue
		}
		cur.Cursor = m.TS
		if t.kind == item.KindDM {
			key := state.ThreadKey(id, "")
			c := r.st.Threads[key]
			c.Channel, c.LastTS, c.LastActivity = id, m.TS, r.now
			if r.qualifies(m, t) {
				c.LastHumanTS = m.TS
				c.Turns++
			}
			r.st.Threads[key] = c
			continue
		}
		// Replies are the thread pass's business; history only shows the
		// ones broadcast to the channel.
		if m.IsReply() || !r.qualifies(m, t) {
			continue
		}
		r.st.Threads[state.ThreadKey(id, m.TS)] = state.Thread{
			Channel: id, ThreadTS: m.TS, LastTS: m.TS, LastHumanTS: m.TS, Turns: 1, LastActivity: r.now,
		}
	}
	r.st.Channels[id] = cur
	return nil
}

// conversation catches up one watched conversation and emits it if pending.
func (r *run) conversation(key string, t state.Thread, tgt target) error {
	var msgs []slack.Message
	var err error
	if t.ThreadTS != "" {
		msgs, err = r.p.Client.Replies(r.ctx, t.Channel, t.ThreadTS)
		if err != nil {
			return fmt.Errorf("thread %s: %w", key, err)
		}
		for i, m := range msgs {
			if slack.CompareTS(m.TS, t.LastTS) <= 0 {
				continue
			}
			t.LastTS = m.TS
			if r.qualifiesReply(m, tgt, msgs[:i]) {
				t.LastHumanTS = m.TS
				t.Turns++
				t.LastActivity = r.now
			}
		}
		r.st.Threads[key] = t
	}
	if !t.Pending() || r.budget <= 0 {
		return nil
	}
	if t.ThreadTS == "" {
		// A DM's transcript is its recent history.
		msgs, err = r.p.Client.Recent(r.ctx, t.Channel, transcriptWindow(r.p.Config))
		if err != nil {
			return fmt.Errorf("dm %s: %w", key, err)
		}
	}
	r.items = append(r.items, item.Build(item.Input{
		Ref:             item.Ref{Channel: t.Channel, ThreadTS: t.ThreadTS},
		Kind:            tgt.kind,
		Messages:        msgs,
		DispatchedTS:    t.DispatchedTS,
		LastHumanTS:     t.LastHumanTS,
		Turns:           t.Turns,
		TranscriptLimit: r.p.Config.TranscriptLimit,
		BotUserID:       r.st.Bot.UserID,
		TeamURL:         r.st.Bot.TeamURL,
		Labels:          tgt.labels,
		Pending:         true,
	}))
	r.budget--
	return nil
}

// transcriptWindow is how many DM messages to read: the transcript limit plus
// room for the unanswered turns after it.
func transcriptWindow(cfg *config.Config) int { return cfg.TranscriptLimit + 20 }

// qualifies applies the channel's listening rules to one message.
func (r *run) qualifies(m slack.Message, t target) bool {
	if !m.Human() || m.User == r.st.Bot.UserID || !r.p.Config.Allowed(m.User) {
		return false
	}
	return t.mode == config.ModeAll || item.Mentions(m.Text, r.st.Bot.UserID)
}

// qualifiesReply applies thread_replies on top of the channel's rules. In a
// mentions channel the default lets whoever already addressed the bot in this
// thread carry on without mentioning it again — that is how people talk —
// while everyone else still has to, so a side conversation in the same thread
// does not start a run per line.
func (r *run) qualifiesReply(m slack.Message, t target, earlier []slack.Message) bool {
	if r.qualifies(m, t) {
		return true
	}
	if !r.qualifies(m, target{mode: config.ModeAll}) { // not a person, or not allowed
		return false
	}
	switch r.p.Config.ThreadReplies {
	case config.RepliesAll:
		return true
	case config.RepliesParticipants:
		for _, e := range earlier {
			if e.User == m.User && r.qualifies(e, t) {
				return true
			}
		}
	}
	return false
}
