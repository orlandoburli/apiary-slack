package item

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary-slack/internal/slack"
)

func TestIDRoundTrip(t *testing.T) {
	ref := Ref{Channel: "C1", ThreadTS: "100.000001", TS: "100.000009"}
	got, err := ParseID(ref.ID())
	if err != nil || got != ref {
		t.Fatalf("got %+v, %v", got, err)
	}
	for _, bad := range []string{"", "INC-1", "slack:C1:1.1", "jira:C1:1.1:1.2", "slack::1.1:1.2"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) accepted", bad)
		}
	}
}

func TestTranscriptLimit(t *testing.T) {
	earlier := []slack.Message{{User: "U1", Text: "one", TS: "1.1"}, {User: "U1", Text: "two", TS: "2.1"}, {User: "U1", Text: "three", TS: "3.1"}}
	it := Build(Input{Message: slack.Message{User: "U1", Text: "four", TS: "4.1", ThreadTS: "1.1"}, Channel: "C1", Kind: KindMention, Earlier: earlier, TranscriptLimit: 2})
	if strings.Contains(it.Description, "one") || !strings.Contains(it.Description, "last 2 of 3") || !strings.Contains(it.Description, "three") {
		t.Errorf("description:\n%s", it.Description)
	}
}

func TestPermalinkAndMentions(t *testing.T) {
	if got := Permalink("https://a.slack.com/", Ref{"C1", "1.000002", "1.000002"}); got != "https://a.slack.com/archives/C1/p1000002" {
		t.Errorf("root permalink = %q", got)
	}
	if got := Permalink("https://a.slack.com/", Ref{"C1", "1.000002", "5.000006"}); !strings.HasSuffix(got, "p5000006?thread_ts=1.000002&cid=C1") {
		t.Errorf("reply permalink = %q", got)
	}
	if !Mentions("hey <@UBOT>", "UBOT") || !Mentions("<@UBOT|apiary> hi", "UBOT") || Mentions("<@UBOTTLE>", "UBOT") || Mentions("x", "") {
		t.Error("Mentions")
	}
}
