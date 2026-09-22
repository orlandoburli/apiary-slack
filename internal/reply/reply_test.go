package reply

import (
	"context"
	"strings"
	"testing"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"

	"github.com/orlandoburli/apiary-slack/internal/config"
	"github.com/orlandoburli/apiary-slack/internal/slack"
	"github.com/orlandoburli/apiary-slack/internal/slacktest"
)

func TestMrkdwn(t *testing.T) {
	cases := map[string]string{
		"**bold** and [docs](https://x.dev/a?b=1)":      "*bold* and <https://x.dev/a?b=1|docs>",
		"## Summary\n- one\n  * two":                    "*Summary*\n• one\n  • two",
		"a < b && c > d":                                "a &lt; b &amp;&amp; c &gt; d",
		"> quoted":                                      "> quoted",
		"```\n**not bold** <tag>\n```":                  "```\n**not bold** &lt;tag&gt;\n```",
		"<https://x.dev/a|click here>":                  "<https://x.dev/a|click here>",
		"<https://x.dev/a>":                             "<https://x.dev/a>",
		"see <https://x.dev/a|#42> and a < b":          "see <https://x.dev/a|#42> and a &lt; b",
		"```\n<https://x.dev/a|code>\n```":              "```\n&lt;https://x.dev/a|code&gt;\n```",
	}
	for in, want := range cases {
		if got := Mrkdwn(in); got != want {
			t.Errorf("Mrkdwn(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestChunks(t *testing.T) {
	text := strings.Repeat("line of text\n", 10)
	chunks := Chunks(text, 40)
	if len(chunks) < 3 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	for _, c := range chunks {
		if len(c) > 40 {
			t.Errorf("chunk of %d bytes", len(c))
		}
	}
	if got := strings.Join(chunks, "\n") + "\n"; got != text {
		t.Errorf("chunks do not reassemble:\n%q", got)
	}
	for _, c := range Chunks(strings.Repeat("é", 50), 15) { // hard cut stays on a rune boundary
		if !strings.HasPrefix(c, "é") || !strings.HasSuffix(c, "é") {
			t.Errorf("split a rune: %q", c)
		}
	}
}

func TestWrite(t *testing.T) {
	fake := slacktest.New(t)
	cfg := &config.Config{PostFailures: true, AckReaction: "eyes"}
	client := slack.New(fake.URL, slacktest.Token)
	ctx := context.Background()
	write := func(id string, ok bool, out, errMsg string) {
		t.Helper()
		req := pluginsdk.SourceWriteResultRequest{Item: pluginsdk.SourceItem{ID: id}, Success: ok, Output: out, Error: errMsg}
		if err := Write(ctx, cfg, client, req); err != nil {
			t.Fatal(err)
		}
	}

	write("slack:C1:100.000001", true, "**done**", "")
	write("slack:D1:dm", true, "in line", "")    // a DM is answered in line
	write("slack:C1:100.000001", true, "  ", "") // nothing to say
	write("slack:C1:100.000001", false, "", "exit 1")

	want := []slacktest.Posted{
		{Channel: "C1", ThreadTS: "100.000001", Text: "*done*"},
		{Channel: "D1", ThreadTS: "", Text: "in line"},
		{Channel: "C1", ThreadTS: "100.000001", Text: ":warning: The run failed.\n```\nexit 1\n```"},
	}
	if len(fake.Posted) != len(want) {
		t.Fatalf("posted = %+v", fake.Posted)
	}
	for i := range want {
		got := fake.Posted[i]
		if got.Channel != want[i].Channel || got.ThreadTS != want[i].ThreadTS || got.Text != want[i].Text {
			t.Errorf("post %d = %+v, want %+v", i, got, want[i])
		}
	}
	if !strings.Contains(fake.Posted[0].Blocks, `"type":"section"`) || fake.Posted[2].Blocks != "" {
		t.Errorf("success posts use blocks, failures plain text: %q / %q", fake.Posted[0].Blocks, fake.Posted[2].Blocks)
	}

	cfg.PostFailures = false
	write("slack:C1:100.000001", false, "", "exit 1")
	if len(fake.Posted) != len(want) {
		t.Error("post_failures: false still posted")
	}
	if err := Write(ctx, cfg, client, pluginsdk.SourceWriteResultRequest{Item: pluginsdk.SourceItem{ID: "INC-1"}, Success: true, Output: "x"}); err == nil {
		t.Error("foreign item id accepted")
	}

	if err := Acknowledge(ctx, cfg, client, pluginsdk.SourceAckRequest{Item: pluginsdk.SourceItem{ID: "slack:C1:100.000001", Metadata: map[string]any{"ts": "100.000009"}}}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Reactions) != 1 || fake.Reactions[0] != (slacktest.Reaction{Channel: "C1", TS: "100.000009", Name: "eyes"}) {
		t.Errorf("reactions = %+v", fake.Reactions)
	}
}
