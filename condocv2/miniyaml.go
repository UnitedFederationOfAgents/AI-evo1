package main

// A small YAML subset, enough for slopspace pointer specs and the agent's
// propose-PR template (see slopspace.go and docs/SlopspaceControlScheme.md
// D4/D10) without pulling in a YAML dependency:
//
//   - block mappings ("key: value", nested by indentation)
//   - block sequences ("- item"), of scalars or of mappings ("- key: v")
//   - plain, 'single'- and "double"-quoted scalars, with " #" comments
//   - literal block scalars ("key: |" / "key: |-")
//   - the empty flow collections "[]" and "{}"
//
// Every scalar parses as a string. Anchors, tags, flow collections with
// content, folded (">") scalars and multi-document streams are not
// supported.

import (
	"fmt"
	"strconv"
	"strings"
)

type yamlParser struct {
	lines []string
	i     int
}

// parseMiniYAML parses src, whose top level must be a mapping. Values are
// string, map[string]any or []any.
func parseMiniYAML(src string) (map[string]any, error) {
	p := &yamlParser{lines: strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")}
	ind, ok := p.peek()
	if !ok {
		return map[string]any{}, nil
	}
	if strings.HasPrefix(p.text(), "-") {
		return nil, fmt.Errorf("yaml: top level is a sequence, want a mapping")
	}
	m, err := p.parseMap(ind)
	if err != nil {
		return nil, err
	}
	if _, more := p.peek(); more {
		return nil, fmt.Errorf("yaml: line %d: unexpected indentation", p.i+1)
	}
	return m, nil
}

// peek skips blank and comment-only lines and returns the indent of the next
// meaningful one.
func (p *yamlParser) peek() (int, bool) {
	for p.i < len(p.lines) {
		t := strings.TrimSpace(p.lines[p.i])
		if t == "" || strings.HasPrefix(t, "#") || t == "---" {
			p.i++
			continue
		}
		return indentOf(p.lines[p.i]), true
	}
	return 0, false
}

func (p *yamlParser) text() string {
	return strings.TrimSpace(p.lines[p.i])
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

func isSeqItem(t string) bool {
	return t == "-" || strings.HasPrefix(t, "- ")
}

func (p *yamlParser) parseMap(ind int) (map[string]any, error) {
	m := map[string]any{}
	for {
		lineInd, ok := p.peek()
		if !ok || lineInd < ind {
			return m, nil
		}
		if lineInd > ind {
			return nil, fmt.Errorf("yaml: line %d: unexpected indentation", p.i+1)
		}
		t := p.text()
		if isSeqItem(t) {
			return m, nil
		}
		key, rest, ok := splitMapEntry(t)
		if !ok {
			return nil, fmt.Errorf("yaml: line %d: expected \"key: value\"", p.i+1)
		}
		p.i++
		val, err := p.parseValue(ind, rest)
		if err != nil {
			return nil, err
		}
		m[key] = val
	}
}

// parseValue parses what follows "key:" (or a bare "-") on a line whose
// collection sits at indent ind.
func (p *yamlParser) parseValue(ind int, rest string) (any, error) {
	if strings.HasPrefix(rest, "#") {
		rest = "" // "key: # comment"
	}
	switch {
	case rest == "|" || rest == "|-" || rest == "|+":
		return p.parseBlockScalar(ind, rest), nil
	case rest == "[]":
		return []any{}, nil
	case rest == "{}":
		return map[string]any{}, nil
	case rest != "":
		return parseYAMLScalar(rest)
	}
	// Nested collection, or an empty value.
	childInd, ok := p.peek()
	if !ok {
		return "", nil
	}
	t := p.text()
	switch {
	case childInd > ind && isSeqItem(t):
		return p.parseSeq(childInd)
	case childInd == ind && isSeqItem(t):
		return p.parseSeq(childInd) // "key:\n- item": a sequence may sit at its key's indent
	case childInd > ind:
		return p.parseMap(childInd)
	}
	return "", nil
}

func (p *yamlParser) parseSeq(ind int) ([]any, error) {
	seq := []any{}
	for {
		lineInd, ok := p.peek()
		if !ok || lineInd != ind || !isSeqItem(p.text()) {
			return seq, nil
		}
		line := p.lines[p.i]
		item := strings.TrimPrefix(strings.TrimSpace(line), "-")
		itemText := strings.TrimLeft(item, " ")
		if itemText == "" || strings.HasPrefix(itemText, "#") {
			p.i++
			v, err := p.parseValue(ind, "")
			if err != nil {
				return nil, err
			}
			seq = append(seq, v)
			continue
		}
		if _, _, isEntry := splitMapEntry(itemText); isEntry && !strings.HasPrefix(itemText, `"`) && !strings.HasPrefix(itemText, "'") {
			// "- key: v": a mapping whose first key sits after the dash.
			// Re-indent this line so the mapping parses at that column.
			mapInd := ind + 1 + (len(item) - len(itemText))
			p.lines[p.i] = strings.Repeat(" ", mapInd) + itemText
			v, err := p.parseMap(mapInd)
			if err != nil {
				return nil, err
			}
			seq = append(seq, v)
			continue
		}
		p.i++
		v, err := p.parseValue(ind, itemText)
		if err != nil {
			return nil, err
		}
		seq = append(seq, v)
	}
}

// parseBlockScalar consumes the lines of a literal block scalar belonging to
// a key at indent ind: every following line that is blank or more indented.
func (p *yamlParser) parseBlockScalar(ind int, indicator string) string {
	var raw []string
	for p.i < len(p.lines) {
		line := p.lines[p.i]
		if strings.TrimSpace(line) != "" && indentOf(line) <= ind {
			break
		}
		raw = append(raw, line)
		p.i++
	}
	// The block's indent is its least-indented line (YAML proper takes the
	// first line's, which loses a leading indent on the first line of text).
	blockInd := -1
	for _, l := range raw {
		if strings.TrimSpace(l) != "" && (blockInd < 0 || indentOf(l) < blockInd) {
			blockInd = indentOf(l)
		}
	}
	out := make([]string, len(raw))
	for i, l := range raw {
		if len(l) >= blockInd && blockInd >= 0 {
			out[i] = l[blockInd:]
		} else {
			out[i] = strings.TrimLeft(l, " ")
		}
	}
	s := strings.Join(out, "\n")
	switch indicator {
	case "|-":
		return strings.TrimRight(s, "\n")
	case "|+":
		return s + "\n"
	default:
		s = strings.TrimRight(s, "\n")
		if s == "" {
			return ""
		}
		return s + "\n"
	}
}

// splitMapEntry splits "key: value" / "key:" into its key and the rest.
func splitMapEntry(t string) (string, string, bool) {
	if strings.HasPrefix(t, `"`) || strings.HasPrefix(t, "'") {
		q := t[0]
		end := strings.IndexByte(t[1:], q)
		if end < 0 {
			return "", "", false
		}
		key := t[1 : end+1]
		after := t[end+2:]
		if after == ":" {
			return key, "", true
		}
		if strings.HasPrefix(after, ": ") {
			return key, strings.TrimSpace(after[2:]), true
		}
		return "", "", false
	}
	if strings.HasSuffix(t, ":") && !strings.Contains(t, ": ") {
		return strings.TrimSpace(strings.TrimSuffix(t, ":")), "", true
	}
	i := strings.Index(t, ": ")
	if i <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(t[:i]), strings.TrimSpace(t[i+2:]), true
}

// parseYAMLScalar parses a one-line scalar, dropping a trailing " #" comment
// from unquoted values.
func parseYAMLScalar(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, `"`):
		end := closingDoubleQuote(s)
		if end < 0 {
			return "", fmt.Errorf("yaml: unterminated string %s", s)
		}
		v, err := strconv.Unquote(s[:end+1])
		if err != nil {
			return s[1:end], nil
		}
		return v, nil
	case strings.HasPrefix(s, "'"):
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				return b.String(), nil
			}
			b.WriteByte(s[i])
		}
		return "", fmt.Errorf("yaml: unterminated string %s", s)
	}
	if j := strings.Index(s, " #"); j >= 0 {
		s = strings.TrimSpace(s[:j])
	}
	if s == "~" || s == "null" {
		return "", nil
	}
	return s, nil
}

func closingDoubleQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// ---- emitting ----

// yamlNeedsQuote reports whether s must be quoted to read back as the same
// string.
func yamlNeedsQuote(s string) bool {
	if s == "" || strings.TrimSpace(s) != s {
		return true
	}
	switch strings.ToLower(s) {
	case "~", "null", "true", "false", "yes", "no", "on", "off":
		return true
	}
	if strings.ContainsAny(s[:1], "-?:,[]{}#&*!|>'\"%@`") {
		return true
	}
	return strings.Contains(s, ": ") || strings.Contains(s, " #") || strings.HasSuffix(s, ":") ||
		strings.ContainsAny(s, "\n\r\t\\")
}

// yamlScalar renders a single-line value.
func yamlScalar(s string) string {
	if yamlNeedsQuote(s) {
		return strconv.Quote(s)
	}
	return s
}

// yamlWriter builds a document line by line.
type yamlWriter struct {
	b strings.Builder
}

func (w *yamlWriter) line(indent int, s string) {
	w.b.WriteString(strings.Repeat(" ", indent))
	w.b.WriteString(s)
	w.b.WriteByte('\n')
}

// field writes "key: value", as a literal block when value spans lines.
// Empty values are skipped unless always is set.
func (w *yamlWriter) field(indent int, key, value string, always bool) {
	if value == "" && !always {
		return
	}
	if strings.Contains(value, "\n") {
		indicator := "|"
		if !strings.HasSuffix(value, "\n") {
			indicator = "|-"
		}
		w.line(indent, key+": "+indicator)
		for _, l := range strings.Split(strings.TrimSuffix(value, "\n"), "\n") {
			if l == "" {
				w.b.WriteByte('\n')
				continue
			}
			w.line(indent+2, l)
		}
		return
	}
	w.line(indent, key+": "+yamlScalar(value))
}

func (w *yamlWriter) String() string { return w.b.String() }

// ---- reading parsed trees ----

func yamlString(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func yamlMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func yamlList(m map[string]any, key string) []any {
	if v, ok := m[key].([]any); ok {
		return v
	}
	return nil
}
