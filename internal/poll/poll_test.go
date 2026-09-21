package poll

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"

	"github.com/orlandoburli/apiary-slack/internal/config"
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

func channels(mode string) map[string]any {
	return map[string]any{"channels": []any{map[string]any{"id": "C1", "mode": mode, "labels": []any{"team:eng"}}}}
}

func TestFirstPollNeverBackfills(t *testing.T) {
	fake := slacktest.New(t)
	fake.Add("C1", slack.Message{User: "U1", Text: "<@UBOT> old question", TS: ts(-60)})
	t.Setenv(config.TokenEnv, slacktest.Token)
	in := channels("mentions")
	in["state_file"] = filepath.Join(t.TempDir(), "s.json")
	in["api_url"] = fake.URL
	cfg, err := config.Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, fake: fake, cfg: cfg}
	for i := 0; i < 2; i++ {
		if items := h.poll(); len(items) != 0 {
			t.Fatalf("poll %d emitted %d items for a message older than install", i, len(items))
		}
	}
}

func TestMentionsModeEmitsOnlyMentionsOnce(t *testing.T) {
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
	if want := "slack:C1:" + ts(2) + ":" + ts(2); it.ID != want {
		t.Errorf("id = %q, want %q", it.ID, want)
	}
	if it.Title != "deploy & verify" {
		t.Errorf("title = %q", it.Title)
	}
	for _, l := range []string{"slack", "channel:C1", "kind:mention", "turn:first", "team:eng"} {
		if !contains(it.Labels, l) {
			t.Errorf("labels %v missing %q", it.Labels, l)
		}
	}
	if !strings.HasPrefix(it.URL, "https://acme.slack.com/archives/C1/p") {
		t.Errorf("url = %q", it.URL)
	}
	if again := h.poll(); len(again) != 0 {
		t.Fatalf("second poll re-emitted %d items", len(again))
	}
}

func TestThreadReplyCarriesTranscript(t *testing.T) {
	h := newHarness(t, channels("mentions"))
	h.fake.Add("C1", slack.Message{User: "U1", Text: "<@UBOT> what broke?", TS: ts(1)})
	h.poll()
	h.fake.Add("C1", slack.Message{User: slacktest.BotUserID, BotID: "BSELF", Text: "The migration.", TS: ts(2), ThreadTS: ts(1)})
	h.fake.Add("C1", slack.Message{User: "U2", Text: "told you", TS: ts(3), ThreadTS: ts(1)}) // no mention: people talking
	h.fake.Add("C1", slack.Message{User: "U1", Text: "<@UBOT> fix it", TS: ts(4), ThreadTS: ts(1)})

	items := h.poll()
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1: %+v", len(items), items)
	}
	it := items[0]
	if want := "slack:C1:" + ts(1) + ":" + ts(4); it.ID != want {
		t.Errorf("id = %q, want %q", it.ID, want)
	}
	if !contains(it.Labels, "turn:reply") {
		t.Errorf("labels = %v, want turn:reply", it.Labels)
	}
	for _, want := range []string{"## Conversation so far", "**<@U1>**: what broke?", "**assistant (you)**: The migration.", "**<@U2>**: told you", "## Latest message\n\nfix it"} {
		if !strings.Contains(it.Description, want) {
			t.Errorf("description missing %q:\n%s", want, it.Description)
		}
	}
	if again := h.poll(); len(again) != 0 {
		t.Fatalf("re-emitted %d items", len(again))
	}
}

func TestThreadRepliesPolicy(t *testing.T) {
	// U1 opened the conversation; U2 never addressed the bot.
	for policy, want := range map[string][]string{"": {"U1"}, "participants": {"U1"}, "mentions": {}, "all": {"U1", "U2"}} {
		in := channels("mentions")
		if policy != "" {
			in["thread_replies"] = policy
		}
		h := newHarness(t, in)
		h.fake.Add("C1", slack.Message{User: "U1", Text: "<@UBOT> what broke?", TS: ts(1)})
		h.poll()
		h.fake.Add("C1", slack.Message{User: "U1", Text: "where is the list?", TS: ts(2), ThreadTS: ts(1)})
		h.fake.Add("C1", slack.Message{User: "U2", Text: "side chatter", TS: ts(3), ThreadTS: ts(1)})
		var got []string
		for _, it := range h.poll() {
			got = append(got, it.Metadata["user"].(string))
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("thread_replies=%q emitted %v, want %v", policy, got, want)
		}
	}
}

func TestAllModeAndAllowedUsers(t *testing.T) {
	in := channels("all")
	in["allowed_users"] = []any{"U1"}
	h := newHarness(t, in)
	h.fake.Add("C1", slack.Message{User: "U1", Text: "no mention needed", TS: ts(1)})
	h.fake.Add("C1", slack.Message{User: "UEVIL", Text: "rm -rf", TS: ts(2)})

	items := h.poll()
	if len(items) != 1 || items[0].Metadata["user"] != "U1" || !contains(items[0].Labels, "kind:message") {
		t.Fatalf("items = %+v", items)
	}
}

func TestBudgetResumesWhereItStopped(t *testing.T) {
	in := channels("all")
	in["max_per_poll"] = 2
	h := newHarness(t, in)
	for i := 1; i <= 5; i++ {
		h.fake.Add("C1", slack.Message{User: "U1", Text: "m", TS: ts(i)})
	}
	var seen []string
	for _, want := range []int{2, 2, 1, 0} {
		items := h.poll()
		if len(items) != want {
			t.Fatalf("poll emitted %d, want %d", len(items), want)
		}
		for _, it := range items {
			seen = append(seen, it.Metadata["ts"].(string))
		}
	}
	for i, got := range seen {
		if got != ts(i+1) {
			t.Fatalf("order = %v", seen)
		}
	}
}

func TestDirectMessagesCarryRunningHistory(t *testing.T) {
	fake := slacktest.New(t)
	fake.IMs = []string{"D1"}
	t.Setenv(config.TokenEnv, slacktest.Token)
	cfg, err := config.Parse(map[string]any{"direct_messages": true, "state_file": filepath.Join(t.TempDir(), "s.json"), "api_url": fake.URL})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, fake: fake, cfg: cfg}
	h.poll() // prime
	fake.Add("D1", slack.Message{User: "U1", Text: "hello", TS: ts(1)})
	if n := len(h.poll()); n != 1 {
		t.Fatalf("first dm: %d items", n)
	}
	fake.Add("D1", slack.Message{User: slacktest.BotUserID, BotID: "BSELF", Text: "hi!", TS: ts(2)})
	fake.Add("D1", slack.Message{User: "U1", Text: "and now?", TS: ts(3)})

	items := h.poll()
	if len(items) != 1 || !contains(items[0].Labels, "kind:dm") {
		t.Fatalf("items = %+v", items)
	}
	for _, want := range []string{"**<@U1>**: hello", "**assistant (you)**: hi!", "## Latest message\n\nand now?"} {
		if !strings.Contains(items[0].Description, want) {
			t.Errorf("description missing %q:\n%s", want, items[0].Description)
		}
	}
}

func TestOneBadChannelDoesNotSilenceTheRest(t *testing.T) {
	h := newHarness(t, map[string]any{"channels": []any{
		map[string]any{"id": "C1", "mode": "all"}, map[string]any{"id": "C2", "mode": "all"}}})
	h.fake.Add("C2", slack.Message{User: "U1", Text: "still here", TS: ts(1)})
	h.fake.Fail["conversations.history"] = "" // healthy
	items := h.poll()
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}

	h.fake.Fail["conversations.history"] = "not_in_channel"
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
