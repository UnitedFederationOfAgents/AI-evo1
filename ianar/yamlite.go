package main

import (
	"fmt"
	"strconv"
	"strings"
)

// This file reads and writes the subset of YAML the sequence-v2 library is
// exchanged in (see seqv2.go). It is hand-rolled rather than a YAML library
// so the build needs no new module (go.mod/go.sum are pinned for this host's
// toolchain -- see the Makefile).
//
// Supported: block mappings and sequences (including "- key: value" items
// and a sequence at the same indent as its key), single-line flow mappings
// and sequences ({op: key, keys: right}, [a, b]), plain, 'single' and
// "double" quoted scalars (double quotes take Go/YAML escapes such as \n and
// °), literal blocks (| and |-), comments, and a leading "---".
// Anchors, tags, multi-document streams, folded (>) blocks and multi-line
// plain scalars are not. Every scalar is read as a string.

// yNode is a parsed YAML value.
type yNode struct {
	kind  yKind
	str   string   // yScalar
	keys  []string // yMap, in document order
	vals  []*yNode // yMap, parallel to keys
	items []*yNode // ySeq
	line  int      // 1-based source line, for errors
}

type yKind int

const (
	yScalar yKind = iota
	yMap
	ySeq
)

func (k yKind) String() string {
	return [...]string{"a single value", "a mapping (key: value lines)", "a list (- item lines)"}[k]
}

// get returns the value for key in a mapping, or nil.
func (n *yNode) get(key string) *yNode {
	for i, k := range n.keys {
		if k == key {
			return n.vals[i]
		}
	}
	return nil
}

type yLine struct {
	raw    string // the line as written
	indent int    // leading spaces
	text   string // content after the indent, comment stripped and trimmed; "" for a blank/comment line
	num    int    // 1-based
}

type yParser struct {
	lines []yLine
}

// parseYAML parses a document in the subset described above.
func parseYAML(src string) (*yNode, error) {
	p := &yParser{}
	for i, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimLeft(raw, " "), "\t") {
			return nil, fmt.Errorf("line %d: indent with spaces, not tabs", i+1)
		}
		ind := len(raw) - len(strings.TrimLeft(raw, " "))
		text := strings.TrimSpace(stripComment(raw[ind:]))
		if text == "---" && p.skipBlank(0) == len(p.lines) {
			text = "" // document start marker
		}
		p.lines = append(p.lines, yLine{raw: raw, indent: ind, text: text, num: i + 1})
	}
	i := p.skipBlank(0)
	if i >= len(p.lines) {
		return &yNode{kind: yMap, line: 1}, nil
	}
	n, next, err := p.parseBlock(i, p.lines[i].indent)
	if err != nil {
		return nil, err
	}
	if next = p.skipBlank(next); next < len(p.lines) {
		return nil, fmt.Errorf("line %d: unexpected indentation or content %q", p.lines[next].num, p.lines[next].text)
	}
	return n, nil
}

// stripComment drops a "#" comment that isn't inside quotes: one at the
// start of the content, or preceded by whitespace.
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				if quote == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					quote = 0
				}
			}
		case c == '"' || c == '\'':
			if i == 0 || strings.ContainsRune(" \t:,[{-", rune(s[i-1])) {
				quote = c
			}
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return s[:i]
		}
	}
	return s
}

func (p *yParser) skipBlank(i int) int {
	for i < len(p.lines) && p.lines[i].text == "" {
		i++
	}
	return i
}

func isSeqItem(text string) bool { return text == "-" || strings.HasPrefix(text, "- ") }

// parseBlock parses the block (mapping, sequence or lone scalar) whose first
// line is i, at indent ind.
func (p *yParser) parseBlock(i, ind int) (*yNode, int, error) {
	l := p.lines[i]
	if isSeqItem(l.text) {
		return p.parseSeq(i, ind)
	}
	if _, _, ok, err := splitKey(l.text); err != nil {
		return nil, 0, fmt.Errorf("line %d: %v", l.num, err)
	} else if ok {
		return p.parseMap(i, ind)
	}
	n, err := parseInline(l.text, l.num)
	return n, i + 1, err
}

func (p *yParser) parseSeq(i, ind int) (*yNode, int, error) {
	n := &yNode{kind: ySeq, line: p.lines[i].num}
	for {
		i = p.skipBlank(i)
		if i >= len(p.lines) || p.lines[i].indent != ind || !isSeqItem(p.lines[i].text) {
			if i < len(p.lines) && p.lines[i].indent > ind {
				return nil, 0, fmt.Errorf("line %d: unexpected indentation", p.lines[i].num)
			}
			return n, i, nil
		}
		l := p.lines[i]
		rest := strings.TrimSpace(strings.TrimPrefix(l.text, "-"))
		var item *yNode
		var err error
		switch {
		case rest == "":
			next := p.skipBlank(i + 1)
			if next < len(p.lines) && p.lines[next].indent > ind {
				item, i, err = p.parseBlock(next, p.lines[next].indent)
			} else {
				item, i = &yNode{kind: yScalar, line: l.num}, i+1
			}
		default:
			if _, _, isKey, kerr := splitKey(rest); kerr == nil && isKey && rest[0] != '{' && rest[0] != '[' {
				// "- key: value": a mapping whose first line starts where
				// rest does. Re-read this line as that mapping's first.
				off := len(l.raw) - len(strings.TrimLeft(l.raw[l.indent+1:], " "))
				p.lines[i].indent, p.lines[i].text = off, rest
				item, i, err = p.parseMap(i, off)
			} else {
				item, err = parseInline(rest, l.num)
				i++
			}
		}
		if err != nil {
			return nil, 0, err
		}
		n.items = append(n.items, item)
	}
}

func (p *yParser) parseMap(i, ind int) (*yNode, int, error) {
	n := &yNode{kind: yMap, line: p.lines[i].num}
	for {
		i = p.skipBlank(i)
		if i >= len(p.lines) || p.lines[i].indent != ind || isSeqItem(p.lines[i].text) {
			if i < len(p.lines) && p.lines[i].indent > ind {
				return nil, 0, fmt.Errorf("line %d: unexpected indentation", p.lines[i].num)
			}
			return n, i, nil
		}
		l := p.lines[i]
		key, rest, ok, err := splitKey(l.text)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("expected \"key: value\", got %q", l.text)
			}
			return nil, 0, fmt.Errorf("line %d: %v", l.num, err)
		}
		if n.get(key) != nil {
			return nil, 0, fmt.Errorf("line %d: %q appears twice", l.num, key)
		}
		var val *yNode
		switch {
		case rest == "|" || rest == "|-" || rest == "|+":
			val, i = p.parseLiteral(i, ind, rest)
		case rest == "":
			next := p.skipBlank(i + 1)
			switch {
			case next < len(p.lines) && p.lines[next].indent > ind:
				val, i, err = p.parseBlock(next, p.lines[next].indent)
			case next < len(p.lines) && p.lines[next].indent == ind && isSeqItem(p.lines[next].text):
				val, i, err = p.parseSeq(next, ind)
			default:
				val, i = &yNode{kind: yScalar, line: l.num}, i+1
			}
		default:
			val, err = parseInline(rest, l.num)
			i++
		}
		if err != nil {
			return nil, 0, err
		}
		n.keys = append(n.keys, key)
		n.vals = append(n.vals, val)
	}
}

// parseLiteral reads a | block: the following lines indented past ind,
// kept verbatim (comments included) less their common indent.
func (p *yParser) parseLiteral(i, ind int, header string) (*yNode, int) {
	start := p.lines[i].num
	blockInd := -1
	last := i // the block's last non-blank line; blank lines after it belong to whatever follows
	for j := i + 1; j < len(p.lines); j++ {
		raw := p.lines[j].raw
		if strings.TrimSpace(raw) == "" {
			continue
		}
		in := len(raw) - len(strings.TrimLeft(raw, " "))
		if in <= ind || (blockInd >= 0 && in < blockInd) {
			break
		}
		if blockInd < 0 {
			blockInd = in
		}
		last = j
	}
	var body []string
	for j := i + 1; j <= last; j++ {
		raw := p.lines[j].raw
		if strings.TrimSpace(raw) == "" {
			body = append(body, "")
		} else {
			body = append(body, raw[blockInd:])
		}
	}
	s := strings.Join(body, "\n")
	if header != "|-" && s != "" {
		s += "\n"
	}
	return &yNode{kind: yScalar, str: s, line: start}, last + 1
}

// splitKey splits "key: rest" (or "key:"), reporting ok false if text isn't
// a key line. A quoted key may hold any characters.
func splitKey(text string) (key, rest string, ok bool, err error) {
	if text == "" {
		return "", "", false, nil
	}
	if text[0] == '"' || text[0] == '\'' {
		q, n, qerr := readQuoted(text)
		if qerr != nil {
			return "", "", false, nil // a quoted scalar, not a key
		}
		after := text[n:]
		if after == ":" || strings.HasPrefix(after, ": ") {
			return q, strings.TrimSpace(after[1:]), true, nil
		}
		return "", "", false, nil
	}
	if text[0] == '{' || text[0] == '[' {
		return "", "", false, nil
	}
	for i := 0; i < len(text); i++ {
		if text[i] == ':' && (i+1 == len(text) || text[i+1] == ' ') {
			return strings.TrimSpace(text[:i]), strings.TrimSpace(text[i+1:]), true, nil
		}
	}
	return "", "", false, nil
}

// readQuoted reads a quoted scalar at the start of s, returning its value
// and how many bytes it took.
func readQuoted(s string) (string, int, error) {
	q := s[0]
	for i := 1; i < len(s); i++ {
		switch {
		case q == '"' && s[i] == '\\':
			i++
		case s[i] == q:
			if q == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				return strings.ReplaceAll(s[1:i], "''", "'"), i + 1, nil
			}
			v, err := strconv.Unquote(s[:i+1])
			if err != nil {
				return "", 0, fmt.Errorf("bad escape in %s", s[:i+1])
			}
			return v, i + 1, nil
		}
	}
	return "", 0, fmt.Errorf("unterminated quote in %s", s)
}

// parseInline parses a value written on one line: a quoted or plain scalar,
// or a flow mapping/sequence.
func parseInline(s string, line int) (*yNode, error) {
	n, rest, err := parseFlowValue(s, line, false)
	if err != nil {
		return nil, fmt.Errorf("line %d: %v", line, err)
	}
	if strings.TrimSpace(rest) != "" {
		return nil, fmt.Errorf("line %d: unexpected %q after the value", line, strings.TrimSpace(rest))
	}
	return n, nil
}

// parseFlowValue parses one value at the start of s. inFlow is set inside
// {...} or [...], where a plain scalar ends at ',', '}' or ']'.
func parseFlowValue(s string, line int, inFlow bool) (*yNode, string, error) {
	s = strings.TrimLeft(s, " ")
	if s == "" {
		return &yNode{kind: yScalar, line: line}, "", nil
	}
	switch s[0] {
	case '"', '\'':
		v, n, err := readQuoted(s)
		if err != nil {
			return nil, "", err
		}
		return &yNode{kind: yScalar, str: v, line: line}, s[n:], nil
	case '{':
		n := &yNode{kind: yMap, line: line}
		s = strings.TrimLeft(s[1:], " ")
		for {
			if strings.HasPrefix(s, "}") {
				return n, s[1:], nil
			}
			var key string
			if s != "" && (s[0] == '"' || s[0] == '\'') {
				k, m, err := readQuoted(s)
				if err != nil {
					return nil, "", err
				}
				key, s = k, s[m:]
			} else {
				end := strings.IndexAny(s, ":,}")
				if end < 0 {
					return nil, "", fmt.Errorf("unterminated {")
				}
				key, s = strings.TrimSpace(s[:end]), s[end:]
			}
			s = strings.TrimLeft(s, " ")
			if !strings.HasPrefix(s, ":") {
				return nil, "", fmt.Errorf("expected \":\" after %q in {...}", key)
			}
			if n.get(key) != nil {
				return nil, "", fmt.Errorf("%q appears twice", key)
			}
			val, rest, err := parseFlowValue(s[1:], line, true)
			if err != nil {
				return nil, "", err
			}
			n.keys, n.vals = append(n.keys, key), append(n.vals, val)
			s = strings.TrimLeft(rest, " ")
			if strings.HasPrefix(s, ",") {
				s = strings.TrimLeft(s[1:], " ")
			} else if !strings.HasPrefix(s, "}") {
				return nil, "", fmt.Errorf("expected \",\" or \"}\" in {...}")
			}
		}
	case '[':
		n := &yNode{kind: ySeq, line: line}
		s = strings.TrimLeft(s[1:], " ")
		for {
			if strings.HasPrefix(s, "]") {
				return n, s[1:], nil
			}
			val, rest, err := parseFlowValue(s, line, true)
			if err != nil {
				return nil, "", err
			}
			n.items = append(n.items, val)
			s = strings.TrimLeft(rest, " ")
			if strings.HasPrefix(s, ",") {
				s = strings.TrimLeft(s[1:], " ")
			} else if !strings.HasPrefix(s, "]") {
				return nil, "", fmt.Errorf("expected \",\" or \"]\" in [...]")
			}
		}
	}
	v, rest := s, ""
	if inFlow {
		if end := strings.IndexAny(s, ",}]"); end >= 0 {
			v, rest = s[:end], s[end:]
		}
	}
	v = strings.TrimSpace(v)
	if v == "~" || v == "null" {
		v = ""
	}
	return &yNode{kind: yScalar, str: v, line: line}, rest, nil
}

// ---- Writing ----

// yamlScalar renders s as a scalar: plain when that reads back unchanged in
// any YAML parser, double-quoted otherwise.
func yamlScalar(s string) string {
	if yamlPlainSafe(s) {
		return s
	}
	return strconv.Quote(s)
}

func yamlPlainSafe(s string) bool {
	if s == "" || s != strings.TrimSpace(s) {
		return false
	}
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~", "y", "n":
		return false
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return false
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' ||
			(i > 0 && strings.ContainsRune(" .-/()", r))
		if !ok {
			return false
		}
	}
	return true
}

// yamlWriter builds a block-style document.
type yamlWriter struct {
	b strings.Builder
}

func (w *yamlWriter) line(indent int, s string) {
	w.b.WriteString(strings.Repeat(" ", indent))
	w.b.WriteString(s)
	w.b.WriteString("\n")
}

// field writes "key: value" unless value is empty.
func (w *yamlWriter) field(indent int, key, value string) {
	if value != "" {
		w.line(indent, key+": "+yamlScalar(value))
	}
}

// flowMap renders keys/values as a one-line {k: v, ...} mapping.
func flowMap(keys, values []string) string {
	parts := make([]string, len(keys))
	for i := range keys {
		parts[i] = yamlScalar(keys[i]) + ": " + yamlScalar(values[i])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
