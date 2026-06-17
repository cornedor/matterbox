package jira

import (
	"encoding/json"
	"strings"
)

// adfNode is one node in an Atlassian Document Format tree. Block and inline
// nodes share the shape; Text is set on leaf "text" nodes, Content holds
// children, Marks decorate text, Attrs carries node-specific fields (heading
// level, link href, code language, …).
type adfNode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text"`
	Content []adfNode      `json:"content"`
	Marks   []adfMark      `json:"marks"`
	Attrs   map[string]any `json:"attrs"`
}

type adfMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs"`
}

// adfToMarkdown flattens an ADF document into markdown the TUI's renderer
// understands (see internal/ui/markdown.go). It handles the common node types;
// anything unrecognised degrades to its text content rather than vanishing, so
// an exotic issue still reads sensibly. A nil/empty/garbage document yields "".
func adfToMarkdown(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var doc adfNode
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	var b strings.Builder
	writeBlocks(&b, doc.Content, "")
	return strings.TrimSpace(b.String())
}

// writeBlocks renders a slice of block-level nodes, each separated by a blank
// line. indent is the running left margin for nested list items.
func writeBlocks(b *strings.Builder, nodes []adfNode, indent string) {
	for _, n := range nodes {
		writeBlock(b, n, indent)
	}
}

func writeBlock(b *strings.Builder, n adfNode, indent string) {
	switch n.Type {
	case "paragraph":
		if line := inline(n.Content); strings.TrimSpace(line) != "" {
			b.WriteString(indent + line + "\n\n")
		}
	case "heading":
		level := 2
		if v, ok := n.Attrs["level"].(float64); ok && v >= 1 && v <= 6 {
			level = int(v)
		}
		b.WriteString(strings.Repeat("#", level) + " " + inline(n.Content) + "\n\n")
	case "bulletList":
		writeList(b, n.Content, indent, "- ")
	case "orderedList":
		writeOrderedList(b, n.Content, indent)
	case "codeBlock":
		lang, _ := n.Attrs["language"].(string)
		b.WriteString("```" + lang + "\n" + codeText(n.Content) + "\n```\n\n")
	case "blockquote":
		var inner strings.Builder
		writeBlocks(&inner, n.Content, "")
		for _, line := range strings.Split(strings.TrimRight(inner.String(), "\n"), "\n") {
			b.WriteString("> " + line + "\n")
		}
		b.WriteString("\n")
	case "rule":
		b.WriteString("---\n\n")
	case "mediaGroup", "mediaSingle":
		b.WriteString(indent + "_[attachment]_\n\n")
	default:
		// Unknown block: recurse so nested text isn't lost.
		if len(n.Content) > 0 {
			writeBlocks(b, n.Content, indent)
		}
	}
}

// writeList renders bullet list items, recursing into nested lists with a
// deeper indent.
func writeList(b *strings.Builder, items []adfNode, indent, marker string) {
	for _, item := range items {
		writeListItem(b, item, indent, marker)
	}
	// A blank line after the list only when at the top level, so nested lists
	// stay attached to their parent item.
	if indent == "" {
		b.WriteString("\n")
	}
}

func writeOrderedList(b *strings.Builder, items []adfNode, indent string) {
	for i, item := range items {
		writeListItem(b, item, indent, itoa(i+1)+". ")
	}
	if indent == "" {
		b.WriteString("\n")
	}
}

// writeListItem renders one listItem: its first paragraph on the marker line,
// then any nested lists indented beneath it.
func writeListItem(b *strings.Builder, item adfNode, indent, marker string) {
	for i, child := range item.Content {
		switch {
		case child.Type == "paragraph" && i == 0:
			b.WriteString(indent + marker + inline(child.Content) + "\n")
		case child.Type == "bulletList":
			writeList(b, child.Content, indent+"  ", "- ")
		case child.Type == "orderedList":
			writeOrderedList(b, child.Content, indent+"  ")
		case child.Type == "paragraph":
			b.WriteString(indent + strings.Repeat(" ", len(marker)) + inline(child.Content) + "\n")
		default:
			writeBlock(b, child, indent+"  ")
		}
	}
}

// codeText concatenates the raw text of a code block's children, ignoring marks
// (code is already verbatim).
func codeText(nodes []adfNode) string {
	var b strings.Builder
	for _, n := range nodes {
		if n.Type == "hardBreak" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(n.Text)
	}
	return b.String()
}

// inline renders a run of inline nodes (the children of a paragraph/heading)
// into a single markdown line.
func inline(nodes []adfNode) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.Type {
		case "text":
			b.WriteString(applyMarks(n.Text, n.Marks))
		case "hardBreak":
			b.WriteString("\n")
		case "mention":
			if name, ok := n.Attrs["text"].(string); ok {
				b.WriteString(strings.TrimPrefix(name, "@"))
			}
		case "emoji":
			if short, ok := n.Attrs["shortName"].(string); ok {
				b.WriteString(short)
			} else if txt, ok := n.Attrs["text"].(string); ok {
				b.WriteString(txt)
			}
		case "inlineCard":
			if href, ok := n.Attrs["url"].(string); ok {
				b.WriteString(href)
			}
		default:
			if len(n.Content) > 0 {
				b.WriteString(inline(n.Content))
			}
		}
	}
	return b.String()
}

// applyMarks wraps text in the markdown for each of its marks. A link mark wraps
// last so the visible text keeps its other styling inside the [..](..).
func applyMarks(text string, marks []adfMark) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	var href string
	for _, mk := range marks {
		switch mk.Type {
		case "strong":
			text = "**" + text + "**"
		case "em":
			text = "*" + text + "*"
		case "code":
			text = "`" + text + "`"
		case "strike":
			text = "~~" + text + "~~"
		case "link":
			if h, ok := mk.Attrs["href"].(string); ok {
				href = h
			}
		}
	}
	if href != "" {
		text = "[" + text + "](" + href + ")"
	}
	return text
}

// textToADF builds a minimal ADF document from plain text for posting a
// comment (the inverse of adfToMarkdown, much narrower). Blank lines separate
// paragraphs; single newlines within a paragraph become hardBreaks; a run of
// lines beginning with ">" becomes a blockquote (so a quoted reply renders as
// one). When mention is non-nil a real mention node — the only form Jira
// notifies on — plus a space is prepended to the first body paragraph, or
// added as its own trailing paragraph when the body is empty or only a quote.
func textToADF(text string, mention *Mention) map[string]any {
	blocks := parseADFBlocks(text)
	if mention != nil {
		nodes := []any{
			map[string]any{
				"type":  "mention",
				"attrs": map[string]any{"id": mention.AccountID, "text": "@" + mention.DisplayName},
			},
			map[string]any{"type": "text", "text": " "},
		}
		blocks = prependMention(blocks, nodes)
	}
	if len(blocks) == 0 {
		// Jira rejects an empty doc; a single space keeps it valid.
		blocks = []any{paragraphNode([]string{" "})}
	}
	return map[string]any{"type": "doc", "version": 1, "content": blocks}
}

// parseADFBlocks splits plain text into ADF block nodes (paragraphs and
// blockquotes), grouping consecutive ">" lines into a single blockquote.
func parseADFBlocks(text string) []any {
	var blocks []any
	var para, quote []string

	flushPara := func() {
		if len(para) > 0 {
			blocks = append(blocks, paragraphNode(para))
			para = nil
		}
	}
	flushQuote := func() {
		if len(quote) > 0 {
			blocks = append(blocks, map[string]any{
				"type":    "blockquote",
				"content": []any{paragraphNode(quote)},
			})
			quote = nil
		}
	}

	for _, ln := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(ln, ">"):
			flushPara()
			quote = append(quote, strings.TrimPrefix(strings.TrimPrefix(ln, ">"), " "))
		case strings.TrimSpace(ln) == "":
			flushPara()
			flushQuote()
		default:
			flushQuote()
			para = append(para, ln)
		}
	}
	flushPara()
	flushQuote()
	return blocks
}

// paragraphNode builds a paragraph node whose lines are joined by hardBreaks.
func paragraphNode(lines []string) map[string]any {
	var content []any
	for i, ln := range lines {
		if i > 0 {
			content = append(content, map[string]any{"type": "hardBreak"})
		}
		if ln != "" {
			content = append(content, map[string]any{"type": "text", "text": ln})
		}
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": " "})
	}
	return map[string]any{"type": "paragraph", "content": content}
}

// prependMention inserts nodes at the start of the first paragraph block, so a
// reply's @mention sits with the author's own text. When there's no paragraph
// (e.g. the body is only a quote) it appends a paragraph carrying just nodes.
func prependMention(blocks []any, nodes []any) []any {
	for i, blk := range blocks {
		m, ok := blk.(map[string]any)
		if !ok || m["type"] != "paragraph" {
			continue
		}
		content, _ := m["content"].([]any)
		m["content"] = append(append([]any{}, nodes...), content...)
		blocks[i] = m
		return blocks
	}
	return append(blocks, map[string]any{"type": "paragraph", "content": nodes})
}

// itoa is a tiny strconv.Itoa to avoid the import for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
