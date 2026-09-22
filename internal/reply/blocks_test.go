package reply

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderTableBecomesTableBlock(t *testing.T) {
	md := "Here are the open issues:\n\n| Issue | Status | Title |\n|---|:---:|---|\n| [#5661](https://x/5661) | open · `workflow:implementation` | fix(e2e): a \\| b |\n| #5650 | **open** | bug |\n\nTwo in flight."
	msgs := Render(md)
	if len(msgs) != 1 {
		t.Fatalf("messages = %d", len(msgs))
	}
	b := msgs[0].Blocks
	if len(b) != 3 || b[0]["type"] != "section" || b[1]["type"] != "table" || b[2]["type"] != "section" {
		t.Fatalf("blocks = %v", types(b))
	}
	rows := b[1]["rows"].([][]any)
	if len(rows) != 3 || len(rows[0]) != 3 {
		t.Fatalf("rows = %d x %d", len(rows), len(rows[0]))
	}
	if rows[0][0].(map[string]any)["type"] != "raw_text" || rows[1][0].(map[string]any)["type"] != "rich_text" {
		t.Errorf("cell types: header %v, link cell %v", rows[0][0], rows[1][0])
	}
	link := rows[1][0].(map[string]any)["elements"].([]any)[0].(map[string]any)["elements"].([]any)[0].(map[string]any)
	if link["type"] != "link" || link["url"] != "https://x/5661" || link["text"] != "#5661" {
		t.Errorf("link element = %v", link)
	}
	if got := rows[1][2].(map[string]any)["text"]; got != "fix(e2e): a | b" {
		t.Errorf("escaped pipe cell = %q", got)
	}
	if !strings.Contains(msgs[0].Text, "#5650 · open · bug") {
		t.Errorf("fallback = %q", msgs[0].Text)
	}
	if _, err := MarshalBlocks(b); err != nil {
		t.Fatal(err)
	}
	if b[0]["text"].(map[string]any)["text"] != "Here are the open issues:" {
		t.Errorf("section = %v", b[0])
	}
}

func TestRenderWithoutTableIsSectionsOnly(t *testing.T) {
	msgs := Render("**done**\n- one\n- two")
	if len(msgs) != 1 || len(msgs[0].Blocks) != 1 || msgs[0].Blocks[0]["type"] != "section" {
		t.Fatalf("msgs = %+v", msgs)
	}
	if got := msgs[0].Blocks[0]["text"].(map[string]any)["text"]; got != "*done*\n• one\n• two" {
		t.Errorf("mrkdwn = %q", got)
	}
}

func TestRenderSplitsOversizedTables(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("| n | v |\n|---|---|\n")
	for i := 0; i < 250; i++ {
		sb.WriteString("| r | " + strings.Repeat("x", 30) + " |\n")
	}
	msgs := Render(sb.String())
	tables, rows := 0, 0
	for _, m := range msgs {
		if len(m.Blocks) > maxBlocksPerMessage {
			t.Errorf("message with %d blocks", len(m.Blocks))
		}
		chars := 0
		for _, b := range m.Blocks {
			if b["type"] == "table" {
				tables++
				r := b["rows"].([][]any)
				if len(r) > maxTableRows {
					t.Errorf("table with %d rows", len(r))
				}
				rows += len(r) - 1 // minus the repeated header
				chars += tableTextLen(b)
			}
		}
		if chars > maxTableChars {
			t.Errorf("message with %d table chars", chars)
		}
	}
	if rows != 250 || tables < 3 {
		t.Errorf("rows=%d tables=%d", rows, tables)
	}
	raw, _ := json.Marshal(msgs[0].Blocks)
	if !strings.Contains(string(raw), `"is_wrapped":true`) {
		t.Error("column_settings missing")
	}
}

func types(b []Block) []string {
	var out []string
	for _, x := range b {
		out = append(out, x["type"].(string))
	}
	return out
}
