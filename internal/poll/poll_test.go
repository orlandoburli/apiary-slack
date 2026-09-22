package poll

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"

	"github.com/orlandoburli/apiary-slack/internal/config"
	"github.com/orlandoburli/apiary-slack/internal/item"
	"github.com/orlandoburli/apiary-slack/internal/slack"
	"github.com/orlandoburli/apiary-slack/internal/slacktest"
)

// t0 is "install time": the first poll pins every cursor here, so messages
// must be stamped after it to be seen.
var t0 = time.Unix(1_800_000_000, 0)

func ts(offset int) string { return slack.NowTS(t0.Add(time.Duration(offset) * time.Second)) }

type harness struct {
	t    *testing.T
	fake *slacktest.Fake
	cfg  *config.Config
}

func newHarness(t *testing.T, in map[string]any) *harness {
	t.Helper()
	fake := slacktest.New(t)
	return newHarnessWith(t, fake, in)
}

func newHarnessWith(t *testing.T, fake *slacktest.Fake, in map[string]any) *harness {
	t.Helper()
	t.Setenv(config.TokenEnv, slacktest.Token)
	in["state_file"] = filepath.Join(t.TempDir(), "slack.state.json")
	in["api_url"] = fake.URL
	cfg, err := config.Parse(in)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	h := &harness{t: t, fake: fake, cfg: cfg}
	if items := h.poll(); len(items) != 0 { // the priming poll
		t.Fatalf("first poll emitted %d items, want 0", len(items))
	}
	return h
}

func (h *harness) poll() []pluginsdk.SourceItem {
	h.t.Helper()
	items, err := h.tryPoll()
	if err != nil {
		h.t.Fatalf("poll: %v", err)
	}
	return items
}

func (h *harness) tryPoll() ([]pluginsdk.SourceItem, error) {
	p := &Poller{Config: h.cfg, Client: slack.New(h.cfg.APIURL, h.cfg.Token), Now: func() time.Time { return t0 }, Logf: h.t.Logf}
	res, err := p.Poll(context.Background())
	if err == nil && res.Items == nil {
		h.t.Fatal("items is nil: it must encode as [] on the wire, never null")
	}
	return res.Items, err
}

// ack is what the host does after dispatching an item.
func (h *harness) ack(it pluginsdk.SourceItem) {
	h.t.Helper()
	if err := Acknowledge(h.cfg, pluginsdk.SourceAckRequest{Item: it, Action: "dispatched"}); err != nil {
		h.t.Fatal(err)
	}
}

func channels(mode string) map[string]any {
	return map[string]any{"channels": []any{map[string]any{"id": "C1", "mode": mode, "labels": []any{"team:eng"}}}}
}

func TestFirstPollNeverBackfills(t *testing.T) {
	fake := slacktest.New(t)
	fake.Add("C1", slack.Message{User: "U1", Text: "<@UBOT> old question", TS: ts(-60)})
	h := newHarnessWith(t, fake, channels("mentions"))
	if items := h.poll(); len(items) != 0 {
		t.Fatalf("emitted %d items for a message older than install", len(items))
	}
}

func TestConversationIsOneItemAcrossTurns(t *testing.T) {
	h := newHarness(t, channels("mentions"))
	h.fake.Add("C1", slack.Message{User: "U1", Text: "just chatting", TS: ts(1)})
	h.fake.Add("C1", slack.Message{User: "U1", Text: "<@UBOT> deploy &amp; verify\nplease", TS: ts(2)})
	h.fake.Add("C1", slack.Message{User: "U2", BotID: "B9", Text: "<@UBOT> from a bot", TS: ts(3)})
	h.fake.Add("C1", slack.Message{User: "U1", Subtype: "channel_join", Text: "<@UBOT> joined", TS: ts(4)})

	items := h.poll()
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1: %+v", len(items), items)
	}
	it := items[0]
	if it.ID != "slack:C1:"+ts(2) || it.State != item.StatePending || it.Title != "deploy & verify" || it.Metadata["ts"] != ts(2) {
		t.Errorf("item = %+v", it)
	}
	for _, l := range []string{"slack", "channel:C1", "kind:mention", "turn:first", "team:eng"} {
		if !contains(it.Labels, l) {
			t.Errorf("labels %v missing %q", it.Labels, l)
		}
	}
	if !strings.HasPrefix(it.URL, "https://acme.slack.com/archives/C1/p") {
		t.Errorf("url = %q", it.URL)
	}

	// Pending until acknowledged: the same item comes back, unchanged.
	again := h.poll()
	if len(again) != 1 || again[0].ID != it.ID || again[0].Metadata["ts"] != ts(2) {
		t.Fatalf("second poll = %+v", again)
	}
	h.ack(it)
	if n := len(h.poll()); n != 0 {
		t.Fatalf("emitted %d items after ack", n)
	}

	// A reply from the participant reopens the SAME item, now with transcript.
	h.fake.Add("C1", slack.Message{User: slacktest.BotUserID, BotID: "BSELF", Text: "Deploying.", TS: ts(5), ThreadTS: ts(2)})
	h.fake.Add("C1", slack.Message{User: "U2", Text: "side chatter", TS: ts(6), ThreadTS: ts(2)}) // never addressed the bot
	h.fake.Add("C1", slack.Message{User: "U1", Text: "status?", TS: ts(7), ThreadTS: ts(2)})
	items = h.poll()
	if len(items) != 1 || items[0].ID != it.ID {
		t.Fatalf("reply poll = %+v", items)
	}
	rep := items[0]
	if rep.State != item.StatePending || rep.Metadata["ts"] != ts(7) || rep.Metadata["turns"] != "2" || !contains(rep.Labels, "turn:reply") {
		t.Errorf("reply item = %+v", rep)
	}
	for _, want := range []string{"## Conversation so far", "**<@U1>**: deploy & verify", "**assistant (you)**: Deploying.", "## Latest messages (unanswered)\n\n**<@U2>**: side chatter\n\n**<@U1>**: status?"} {
		if !strings.Contains(rep.Description, want) {
			t.Errorf("description missing %q:\n%s", want, rep.Description)
		}
	}
	h.ack(rep)
	if n := len(h.poll()); n != 0 {
		t.Fatalf("emitted %d items after second ack", n)
	}
}

func TestBotReplyMarksTheTurnAnsweredWithoutAck(t *testing.T) {
	// Apiary only acknowledges when settings.state_lock is on, so the plugin
	// must read answered-ness from the thread: the bot's reply after the turn.
	h := newHarness(t, channels("all"))
	h.fake.Add("C1", slack.Message{User: "U1", Text: "one", TS: ts(1)})
	if n := len(h.poll()); n != 1 {
		t.Fatalf("emitted %d", n)
	}
	h.poll() // still pending: re-emitted, no ack ever arrives
	h.fake.Add("C1", slack.Message{User: slacktest.BotUserID, BotID: "BSELF", Text: "answer", TS: ts(2), ThreadTS: ts(1)})
	if items := h.poll(); len(items) != 0 {
		t.Fatalf("still pending after the bot replied: %+v", items)
	}
	h.fake.Add("C1", slack.Message{User: "U1", Text: "two", TS: ts(3), ThreadTS: ts(1)})
	items := h.poll()
	if len(items) != 1 || items[0].Metadata["ts"] != ts(3) {
		t.Fatalf("follow-up = %+v", items)
	}
}

func TestAlreadyRepliedThreadIsNotReDispatchedOnUpgrade(t *testing.T) {
	// A thread answered before the plugin tracked turns (state v1, or a poll
	// that missed the reply): the bot's reply is already newer than the last
	// human turn, so nothing is pending.
	h := newHarness(t, channels("all"))
	h.fake.Add("C1", slack.Message{User: "U1", Text: "one", TS: ts(1)})
	h.fake.Add("C1", slack.Message{User: slacktest.BotUserID, BotID: "BSELF", Text: "answer", TS: ts(2), ThreadTS: ts(1)})
	if items := h.poll(); len(items) != 0 {
		t.Fatalf("re-dispatched an answered thread: %+v", items)
	}
}

func TestTurnArrivingDuringARunStaysPendingAfterTheReply(t *testing.T) {
	h := newHarness(t, channels("all"))
	h.fake.Add("C1", slack.Message{User: "U1", Text: "one", TS: ts(1)})
	h.poll() // dispatched: the run is answering ts(1)
	h.fake.Add("C1", slack.Message{User: "U1", Text: "two", TS: ts(2), ThreadTS: ts(1)})
	h.poll()
	h.fake.Add("C1", slack.Message{User: slacktest.BotUserID, BotID: "BSELF", Text: "answer to one", TS: ts(3), ThreadTS: ts(1)})
	items := h.poll()
	if len(items) != 1 || items[0].Metadata["ts"] != ts(2) || !strings.Contains(items[0].Description, "(unanswered)\n\n**<@U1>**: two") {
		t.Fatalf("turn two lost after the reply to one: %+v", items)
	}
}

func TestTurnArrivingDuringARunStaysPending(t *testing.T) {
	h := newHarness(t, channels("all"))
	h.fake.Add("C1", slack.Message{User: "U1", Text: "one", TS: ts(1)})
	it := h.poll()[0]
	// A second turn lands before the host acknowledges the first item.
	h.fake.Add("C1", slack.Message{User: "U1", Text: "two", TS: ts(2), ThreadTS: ts(1)})
	h.poll()
	h.ack(it) // carries ts(1): only that turn is handed over
	items := h.poll()
	if len(items) != 1 || items[0].Metadata["ts"] != ts(2) || !strings.Contains(items[0].Description, "## Latest message\n\n**<@U1>**: two") {
		t.Fatalf("second turn lost: %+v", items)
	}
}

func TestThreadRepliesPolicy(t *testing.T) {
	// U1 opened the conversation; U2 never addressed the bot.
	for policy, want := range map[string]string{"": ts(2), "participants": ts(2), "mentions": "", "all": ts(3)} {
		in := channels("mentions")
		if policy != "" {
			in["thread_replies"] = policy
		}
		h := newHarness(t, in)
		h.fake.Add("C1", slack.Message{User: "U1", Text: "<@UBOT> what broke?", TS: ts(1)})
		h.ack(h.poll()[0])
		h.fake.Add("C1", slack.Message{User: "U1", Text: "where is the list?", TS: ts(2), ThreadTS: ts(1)})
		h.fake.Add("C1", slack.Message{User: "U2", Text: "side chatter", TS: ts(3), ThreadTS: ts(1)})
		items := h.poll()
		got := ""
		if len(items) == 1 {
			got = items[0].Metadata["ts"].(string)
		}
		if got != want {
			t.Errorf("thread_replies=%q: latest turn = %q, want %q (%d items)", policy, got, want, len(items))
		}
	}
}

func TestAllowedUsers(t *testing.T) {
	in := channels("all")
	in["allowed_users"] = []any{"U1"}
	h := newHarness(t, in)
	h.fake.Add("C1", slack.Message{User: "UEVIL", Text: "rm -rf", TS: ts(1)})
	h.fake.Add("C1", slack.Message{User: "U1", Text: "hello", TS: ts(2)})
	items := h.poll()
	if len(items) != 1 || items[0].ID != "slack:C1:"+ts(2) || !contains(items[0].Labels, "kind:message") {
		t.Fatalf("items = %+v", items)
	}
}

func TestBudgetCapsEmissionNotReading(t *testing.T) {
	in := channels("all")
	in["max_per_poll"] = 2
	h := newHarness(t, in)
	for i := 1; i <= 3; i++ {
		h.fake.Add("C1", slack.Message{User: "U1", Text: "m", TS: ts(i)})
	}
	first := h.poll()
	if len(first) != 2 {
		t.Fatalf("emitted %d, want 2", len(first))
	}
	for _, it := range first {
		h.ack(it)
	}
	second := h.poll()
	if len(second) != 1 || second[0].ID != "slack:C1:"+ts(3) {
		t.Fatalf("second poll = %+v", second)
	}
}

func TestDirectMessageIsOneRunningConversation(t *testing.T) {
	fake := slacktest.New(t)
	fake.IMs = []string{"D1"}
	h := newHarnessWith(t, fake, map[string]any{"direct_messages": true})
	fake.Add("D1", slack.Message{User: "U1", Text: "hello", TS: ts(1)})
	items := h.poll()
	if len(items) != 1 || items[0].ID != "slack:D1:dm" || !contains(items[0].Labels, "kind:dm") {
		t.Fatalf("first dm: %+v", items)
	}
	h.ack(items[0])
	fake.Add("D1", slack.Message{User: slacktest.BotUserID, BotID: "BSELF", Text: "hi!", TS: ts(2)})
	fake.Add("D1", slack.Message{User: "U1", Text: "and now?", TS: ts(3)})

	items = h.poll()
	if len(items) != 1 || items[0].ID != "slack:D1:dm" || items[0].Metadata["turns"] != "2" {
		t.Fatalf("items = %+v", items)
	}
	for _, want := range []string{"**<@U1>**: hello", "**assistant (you)**: hi!", "## Latest message\n\n**<@U1>**: and now?"} {
		if !strings.Contains(items[0].Description, want) {
			t.Errorf("description missing %q:\n%s", want, items[0].Description)
		}
	}
}

func TestOneBadChannelDoesNotSilenceTheRest(t *testing.T) {
	h := newHarness(t, map[string]any{"channels": []any{
		map[string]any{"id": "C1", "mode": "all"}, map[string]any{"id": "C2", "mode": "all"}}})
	h.fake.Add("C2", slack.Message{User: "U1", Text: "still here", TS: ts(1)})
	if items := h.poll(); len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	h.fake.Fail["conversations.history"] = "not_in_channel"
	h.fake.Fail["conversations.replies"] = "not_in_channel"
	if _, err := h.tryPoll(); err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Fatalf("err = %v, want not_in_channel surfaced when nothing could be read", err)
	}
}

func TestRateLimitStopsThePoll(t *testing.T) {
	h := newHarness(t, map[string]any{"channels": []any{
		map[string]any{"id": "C1", "mode": "all"}, map[string]any{"id": "C2", "mode": "all"}}})
	before := h.fake.Calls["conversations.history"]
	h.fake.Fail["conversations.history"] = "429"
	if _, err := h.tryPoll(); err == nil {
		t.Fatal("want a rate-limit error")
	}
	if got := h.fake.Calls["conversations.history"] - before; got != 1 {
		t.Fatalf("made %d history calls after a 429, want 1", got)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
