package item

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary-slack/internal/slack"
)

func TestIDRoundTrip(t *testing.T) {
	for _, ref := range []Ref{{Channel: "C1", ThreadTS: "100.000001"}, {Channel: "D1"}} {
		got, err := ParseID(ref.ID())
		if err != nil || got != ref {
			t.Fatalf("%+v → %q → %+v, %v", ref, ref.ID(), got, err)
		}
	}
	if (Ref{Channel: "D1"}).ID() != "slack:D1:dm" {
		t.Error("dm id")
	}
	for _, bad := range []string{"", "INC-1", "slack:C1", "jira:C1:1.1", "slack::1.1", "slack:C1:1.1:1.2"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) accepted", bad)
		}
	}
}

func TestDescribeSplitsAtTheDispatchWatermark(t *testing.T) {
	msgs := []slack.Message{
		{User: "U1", Text: "<@UBOT> what broke?", TS: "1.1"},
		{User: "UBOT", BotID: "B1", Text: "The migration.", TS: "2.1", ThreadTS: "1.1"},
		{User: "U1", Text: "fix it", TS: "3.1", ThreadTS: "1.1"},
		{User: "U1", Text: "please", TS: "4.1", ThreadTS: "1.1"},
	}
	in := Input{Ref: Ref{Channel: "C1", ThreadTS: "1.1"}, Kind: KindMention, Messages: msgs, DispatchedTS: "1.1", LastHumanTS: "4.1", Turns: 3, BotUserID: "UBOT", Pending: true}
	it := Build(in)
	if it.ID != "slack:C1:1.1" || it.State != StatePending || it.Title != "what broke?" || it.Metadata["ts"] != "4.1" || it.Metadata["turns"] != "3" {
		t.Errorf("item = %+v", it)
	}
	for _, want := range []string{"## Conversation so far", "**<@U1>**: what broke?", "**assistant (you)**: The migration.", "## Latest messages (unanswered)", "**<@U1>**: fix it\n\n**<@U1>**: please"} {
		if !strings.Contains(it.Description, want) {
			t.Errorf("description missing %q:\n%s", want, it.Description)
		}
	}

	// Nothing dispatched yet and a single message: no transcript section.
	first := Build(Input{Ref: in.Ref, Kind: KindMention, Messages: msgs[:1], LastHumanTS: "1.1", Turns: 1, BotUserID: "UBOT", Pending: true})
	if strings.Contains(first.Description, "## ") || !strings.Contains(first.Description, "From <@U1>:\n\nwhat broke?") {
		t.Errorf("first turn description:\n%s", first.Description)
	}
	if !contains(first.Labels, "turn:first") || !contains(it.Labels, "turn:reply") {
		t.Errorf("turn labels: %v / %v", first.Labels, it.Labels)
	}
}

func TestTranscriptLimit(t *testing.T) {
	msgs := []slack.Message{{User: "U1", Text: "one", TS: "1.1"}, {User: "U1", Text: "two", TS: "2.1"}, {User: "U1", Text: "three", TS: "3.1"}, {User: "U1", Text: "four", TS: "4.1"}}
	it := Build(Input{Ref: Ref{Channel: "C1", ThreadTS: "1.1"}, Messages: msgs, DispatchedTS: "3.1", LastHumanTS: "4.1", TranscriptLimit: 2, Pending: true})
	if strings.Contains(it.Description, "one") || !strings.Contains(it.Description, "last 2 of 3") || !strings.Contains(it.Description, "three") {
		t.Errorf("description:\n%s", it.Description)
	}
}

func TestPermalinkAndMentions(t *testing.T) {
	if got := Permalink("https://a.slack.com/", Ref{"C1", "1.000002"}); got != "https://a.slack.com/archives/C1/p1000002" {
		t.Errorf("thread permalink = %q", got)
	}
	if got := Permalink("https://a.slack.com/", Ref{Channel: "D1"}); got != "https://a.slack.com/archives/D1" {
		t.Errorf("dm permalink = %q", got)
	}
	if !Mentions("hey <@UBOT>", "UBOT") || !Mentions("<@UBOT|apiary> hi", "UBOT") || Mentions("<@UBOTTLE>", "UBOT") || Mentions("x", "") {
		t.Error("Mentions")
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
