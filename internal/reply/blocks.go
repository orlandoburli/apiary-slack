package reply

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Slack's documented ceilings for a chat.postMessage payload.
const (
	maxBlocksPerMessage = 50
	maxSectionChars     = 3000
	maxTableRows        = 100
	maxTableCols        = 20
	// maxTableChars is the aggregate cell text allowed across a message's tables.
	maxTableChars = 10000
)

// Block is one Block Kit block, kept as a generic map so the wire shape is
// exactly what Slack documents.
type Block map[string]any

// Message is one chat.postMessage call: its blocks plus the plain-text
// fallback Slack shows in notifications.
type Message struct {
	Text   string
	Blocks []Block
}

// Render turns an agent's Markdown into the messages to post. Prose becomes
// mrkdwn section blocks; a GitHub-flavoured Markdown table becomes a Block Kit
// table block, which is the only way Slack renders a real table. A reply is
// split into several messages when it would exceed Slack's per-message limits.
func Render(md string) []Message {
	var blocks []Block
	var text strings.Builder
	flushText := func() {
		t := strings.TrimSpace(text.String())
		text.Reset()
		if t == "" {
			return
		}
		for _, chunk := range Chunks(Mrkdwn(t), maxSectionChars) {
			blocks = append(blocks, Block{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": chunk}})
		}
	}

	lines := strings.Split(md, "\n")
	for i := 0; i < len(lines); {
		if rows, n := parseTable(lines[i:]); n > 0 {
			flushText()
			blocks = append(blocks, tableBlocks(rows)...)
			i += n
			continue
		}
		text.WriteString(lines[i])
		text.WriteString("\n")
		i++
	}
	flushText()
	return pack(blocks)
}

// pack groups blocks into messages within the block-count and table-text
// ceilings, and gives each a plain-text fallback.
func pack(blocks []Block) []Message {
	var out []Message
	var cur Message
	tableChars := 0
	flush := func() {
		if len(cur.Blocks) > 0 {
			cur.Text = fallback(cur.Blocks)
			out = append(out, cur)
		}
		cur, tableChars = Message{}, 0
	}
	for _, b := range blocks {
		chars := tableTextLen(b)
		if len(cur.Blocks) >= maxBlocksPerMessage || (chars > 0 && tableChars+chars > maxTableChars) {
			flush()
		}
		cur.Blocks = append(cur.Blocks, b)
		tableChars += chars
	}
	flush()
	return out
}

var tableSeparator = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$`)

// parseTable recognises a GFM table at the head of lines: a header row, a
// separator row of dashes, then body rows. It returns the cell grid and how
// many lines it consumed; n is 0 when lines do not start with a table.
func parseTable(lines []string) (rows [][]string, n int) {
	if len(lines) < 2 || !isTableRow(lines[0]) || !tableSeparator.MatchString(lines[1]) {
		return nil, 0
	}
	rows = append(rows, splitRow(lines[0]))
	n = 2
	for n < len(lines) && isTableRow(lines[n]) {
		rows = append(rows, splitRow(lines[n]))
		n++
	}
	return rows, n
}

func isTableRow(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "|") && strings.Count(t, "|") >= 2
}

// splitRow splits a table row on unescaped pipes, honouring `\|` and code
// spans, and drops the outer empty cells the leading/trailing pipes produce.
func splitRow(line string) []string {
	var cells []string
	var cur strings.Builder
	inCode := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteByte('|')
			i++
		case c == '`':
			inCode = !inCode
			cur.WriteByte(c)
		case c == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	if len(cells) > 0 && cells[0] == "" {
		cells = cells[1:]
	}
	if len(cells) > 0 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	return cells
}

// tableBlocks renders a cell grid as table blocks, splitting a table longer
// than Slack allows into several that repeat the header row.
func tableBlocks(rows [][]string) []Block {
	if len(rows) == 0 {
		return nil
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols > maxTableCols {
		cols = maxTableCols
	}
	header, body := rows[0], rows[1:]
	var blocks []Block
	for start := 0; start < len(body) || start == 0; start += maxTableRows - 1 {
		end := start + maxTableRows - 1
		if end > len(body) {
			end = len(body)
		}
		block := Block{"type": "table", "rows": [][]any{cellsOf(header, cols)}}
		for _, r := range body[start:end] {
			block["rows"] = append(block["rows"].([][]any), cellsOf(r, cols))
		}
		settings := make([]map[string]any, cols)
		for i := range settings {
			settings[i] = map[string]any{"is_wrapped": true}
		}
		block["column_settings"] = settings
		blocks = append(blocks, block)
		if len(body) == 0 {
			break
		}
	}
	return blocks
}

// cellsOf renders one row to exactly cols cells: rich_text when the cell
// carries a link or emphasis, raw_text otherwise.
func cellsOf(row []string, cols int) []any {
	cells := make([]any, cols)
	for i := range cells {
		text := ""
		if i < len(row) {
			text = row[i]
		}
		cells[i] = cell(text)
	}
	return cells
}

var (
	cellLink = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	cellCode = regexp.MustCompile("`([^`]+)`")
	cellBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
)

func cell(text string) map[string]any {
	if !cellLink.MatchString(text) && !cellCode.MatchString(text) && !cellBold.MatchString(text) {
		return map[string]any{"type": "raw_text", "text": text}
	}
	return map[string]any{"type": "rich_text", "elements": []any{map[string]any{"type": "rich_text_section", "elements": inline(text)}}}
}

// inline renders Markdown links, code spans and bold as rich_text elements.
func inline(text string) []any {
	var els []any
	for text != "" {
		loc, kind := firstInline(text)
		if loc == nil {
			els = append(els, map[string]any{"type": "text", "text": text})
			break
		}
		if loc[0] > 0 {
			els = append(els, map[string]any{"type": "text", "text": text[:loc[0]]})
		}
		m := text[loc[0]:loc[1]]
		switch kind {
		case "link":
			sub := cellLink.FindStringSubmatch(m)
			els = append(els, map[string]any{"type": "link", "url": sub[2], "text": sub[1]})
		case "code":
			els = append(els, map[string]any{"type": "text", "text": cellCode.FindStringSubmatch(m)[1], "style": map[string]any{"code": true}})
		case "bold":
			els = append(els, map[string]any{"type": "text", "text": cellBold.FindStringSubmatch(m)[1], "style": map[string]any{"bold": true}})
		}
		text = text[loc[1]:]
	}
	return els
}

func firstInline(text string) (loc []int, kind string) {
	for _, c := range []struct {
		re   *regexp.Regexp
		kind string
	}{{cellLink, "link"}, {cellCode, "code"}, {cellBold, "bold"}} {
		if l := c.re.FindStringIndex(text); l != nil && (loc == nil || l[0] < loc[0]) {
			loc, kind = l, c.kind
		}
	}
	return loc, kind
}

func tableTextLen(b Block) int {
	if b["type"] != "table" {
		return 0
	}
	n := 0
	for _, row := range b["rows"].([][]any) {
		for _, c := range row {
			n += len(cellText(c.(map[string]any)))
		}
	}
	return n
}

func cellText(c map[string]any) string {
	if t, ok := c["text"].(string); ok {
		return t
	}
	var b strings.Builder
	if secs, ok := c["elements"].([]any); ok {
		for _, s := range secs {
			for _, e := range s.(map[string]any)["elements"].([]any) {
				b.WriteString(e.(map[string]any)["text"].(string))
			}
		}
	}
	return b.String()
}

// fallback is the notification text for a message: its sections' text and,
// for tables, the rows joined with " · ".
func fallback(blocks []Block) string {
	var parts []string
	for _, b := range blocks {
		switch b["type"] {
		case "section":
			parts = append(parts, b["text"].(map[string]any)["text"].(string))
		case "table":
			var rows []string
			for _, row := range b["rows"].([][]any) {
				var cells []string
				for _, c := range row {
					cells = append(cells, cellText(c.(map[string]any)))
				}
				rows = append(rows, strings.Join(cells, " · "))
			}
			parts = append(parts, strings.Join(rows, "\n"))
		}
	}
	return strings.Join(parts, "\n")
}

// MarshalBlocks encodes blocks for the chat.postMessage `blocks` parameter.
func MarshalBlocks(blocks []Block) (string, error) {
	raw, err := json.Marshal(blocks)
	return string(raw), err
}
