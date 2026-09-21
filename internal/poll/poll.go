// Package poll reads new Slack messages and turns each human turn into a work
// item.
//
// One poll makes two passes. The channel pass reads every listened
// conversation's top-level messages past its cursor; a message that qualifies
// opens a conversation and its thread starts being watched. The thread pass
// re-reads each watched thread and emits the human replies it has not seen,
// each carrying the thread so far as a transcript. Direct messages skip the
// thread pass: a DM is one running conversation, so each message carries the
// DM's recent history instead.
//
// Cursors advance at emission, not on acknowledge: the item ids are stable, so
// the host's dedup already covers a re-read, while waiting for an ack would
// re-emit the same message on every poll until the run was dispatched.
package poll

import (
	"context"
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

// Poll returns the work items for every human turn not yet emitted.
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
		p.Logf("stopped watching %d thread(s): idle past thread_ttl, channel no longer configured, or over max_threads", n)
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
		if stop := run.guard(run.thread(key, t, targets[t.Channel])); stop {
			break
		}
	}

	// Saving is not optional: items about to be returned are behind the
	// cursors being written. Returning them without the save would be
	// harmless (stable ids), but returning an error instead keeps a broken
	// state path loud rather than letting every poll re-read Slack from the
	// same spot.
	if err := st.Save(p.Config.StateFile); err != nil {
		return empty, err
	}
	if len(run.items) == 0 && run.firstErr != nil {
		return empty, run.firstErr
	}
	return pluginsdk.SourcePollResult{Items: run.items}, nil
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
		if r.budget <= 0 {
			break // leave the cursor before m; next poll resumes here
		}
		cur.Cursor = m.TS
		// Replies are the thread pass's business; history only shows the
		// ones broadcast to the channel, and those would be emitted twice.
		if m.IsReply() || !r.qualifies(m, t) {
			continue
		}
		if t.kind == item.KindDM {
			// A DM is one running conversation, answered in line rather
			// than in a thread, so its context is the DM's recent history.
			earlier, err := r.p.Client.Before(r.ctx, id, m.TS, r.p.Config.TranscriptLimit)
			if err != nil {
				r.p.Logf("dm %s: no transcript for %s: %v", id, m.TS, err)
			}
			r.emit(id, t, m, earlier)
			continue
		}
		r.emit(id, t, m, nil)
		r.st.Threads[state.ThreadKey(id, m.TS)] = state.Thread{Channel: id, ThreadTS: m.TS, LastTS: m.TS, LastActivity: r.now}
	}
	r.st.Channels[id] = cur
	return nil
}

func (r *run) thread(key string, t state.Thread, tgt target) error {
	msgs, err := r.p.Client.Replies(r.ctx, t.Channel, t.ThreadTS)
	if err != nil {
		return fmt.Errorf("thread %s: %w", key, err)
	}
	for i, m := range msgs {
		if slack.CompareTS(m.TS, t.LastTS) <= 0 {
			continue
		}
		if r.budget <= 0 {
			break
		}
		t.LastTS = m.TS
		if !r.qualifies(m, tgt) {
			continue
		}
		r.emit(t.Channel, tgt, m, msgs[:i])
		t.LastActivity = r.now
	}
	r.st.Threads[key] = t
	return nil
}

// qualifies applies the listening rules to one message. A thread follows its
// channel's mode: in a mentions channel a reply must mention the bot again,
// so people can talk to each other in a thread the bot once answered in.
func (r *run) qualifies(m slack.Message, t target) bool {
	if !m.Human() || m.User == r.st.Bot.UserID || !r.p.Config.Allowed(m.User) {
		return false
	}
	return t.mode == config.ModeAll || item.Mentions(m.Text, r.st.Bot.UserID)
}

func (r *run) emit(channel string, t target, m slack.Message, earlier []slack.Message) {
	r.items = append(r.items, item.Build(item.Input{
		Message:         m,
		Channel:         channel,
		Kind:            t.kind,
		Earlier:         earlier,
		TranscriptLimit: r.p.Config.TranscriptLimit,
		BotUserID:       r.st.Bot.UserID,
		TeamURL:         r.st.Bot.TeamURL,
		Labels:          t.labels,
	}))
	r.budget--
}
