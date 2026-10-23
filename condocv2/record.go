package main

// The timeline record: the visual tab's model, saved into the condoc branch
// when the condoc is completed, as Record.html beside its step file. It
// renders the swimlanes as a standalone page and carries the model itself as
// JSON, so once Merge has deleted the slopspace -- the only place the condoc
// and target lanes could be read from -- the visual tab is drawn from the
// record instead, in the same detail.
//
// Nothing the visual tab draws happens after completion (the complete,
// drafted, prs and merged commits aren't drawn), so the record is the whole
// picture.

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	dktypes "dungeon-keeper/pkg/types"
	prkeeper "pr-keeper"
)

const (
	recordFileName = "Record.html"
	recordVersion  = 1
	recordRetry    = time.Minute // how soon a record that couldn't be read is fetched again
)

// TimelineRecord is the saved model.
type TimelineRecord struct {
	Version    int           `json:"version"`
	Condoc     string        `json:"condoc"`
	RecordedAt int64         `json:"recordedAt"`
	Timeline   Timeline      `json:"timeline"`
	Prompts    []Interaction `json:"prompts"` // each interaction's n and prompt: the visual's prompt column
}

type recordCache struct {
	rec *TimelineRecord
	err error
	at  time.Time
}

// recordPath is the record beside the condoc's step file.
func recordPath(mainPath string) string {
	return filepath.Join(implDir(mainPath), recordFileName)
}

func recordRepoPath(spec *PointerSpec) string {
	return filepath.ToSlash(recordPath(filepath.FromSlash(spec.CondocPath)))
}

func recordURL(spec *PointerSpec) string {
	return fmt.Sprintf("https://github.com/%s/blob/%s/%s", spec.CondocRepo, spec.Branch, recordRepoPath(spec))
}

func newTimelineRecord(spec *PointerSpec, tl Timeline, interactions []Interaction, at time.Time) TimelineRecord {
	prompts := make([]Interaction, len(interactions))
	for i, it := range interactions {
		prompts[i] = Interaction{N: it.N, Prompt: it.Prompt}
	}
	return TimelineRecord{Version: recordVersion, Condoc: spec.Name, RecordedAt: at.Unix(), Timeline: tl, Prompts: prompts}
}

// writeTimelineRecord saves the record into the condoc repo's writespace,
// for the caller's next condoc commit.
func (s *Server) writeTimelineRecord(spec *PointerSpec, meta *dktypes.SlopspaceMetadata, main string) error {
	step, err := readFileString(stepFilePath(main))
	if err != nil {
		return err
	}
	rec := newTimelineRecord(spec, s.buildTimeline(spec, meta), parseInteractions(step), time.Now())
	page, err := renderRecordHTML(rec)
	if err != nil {
		return err
	}
	return writeFileString(recordPath(main), page)
}

// timelineRecord reads a merged condoc's record from GitHub -- its kept
// branch, else the default branch it was merged into. A record never changes
// once merged, so it is fetched once.
func (s *Server) timelineRecord(spec *PointerSpec) (*TimelineRecord, error) {
	stem := spec.Stem()
	s.slop.mu.Lock()
	c, ok := s.slop.records[stem]
	s.slop.mu.Unlock()
	if ok && (c.err == nil || time.Since(c.at) < recordRetry) {
		return c.rec, c.err
	}
	rec, err := fetchTimelineRecord(spec)
	s.slop.mu.Lock()
	s.slop.records[stem] = recordCache{rec: rec, err: err, at: time.Now()}
	s.slop.mu.Unlock()
	return rec, err
}

func fetchTimelineRecord(spec *PointerSpec) (*TimelineRecord, error) {
	client, err := prClient()
	if err != nil {
		return nil, err
	}
	path := recordRepoPath(spec)
	page, err := client.FileContent(spec.CondocRepo, spec.Branch, path)
	if prkeeper.IsStatus(err, http.StatusNotFound) {
		page, err = client.FileContent(spec.CondocRepo, "", path)
	}
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %v", spec.CondocRepo, path, err)
	}
	rec, err := parseRecordHTML(string(page))
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %v", spec.CondocRepo, path, err)
	}
	return &rec, nil
}

// ---- the page ----

const recordModelOpen = `<script type="application/json" id="condocv2-record">`

// parseRecordHTML reads the model back out of a rendered record.
func parseRecordHTML(page string) (TimelineRecord, error) {
	var rec TimelineRecord
	i := strings.Index(page, recordModelOpen)
	if i < 0 {
		return rec, fmt.Errorf("no condocv2 record model in the page")
	}
	body := page[i+len(recordModelOpen):]
	j := strings.Index(body, "</script>")
	if j < 0 {
		return rec, fmt.Errorf("the record model is unterminated")
	}
	if err := json.Unmarshal([]byte(body[:j]), &rec); err != nil {
		return rec, fmt.Errorf("the record model: %v", err)
	}
	if rec.Version != recordVersion {
		return rec, fmt.Errorf("record version %d, expected %d", rec.Version, recordVersion)
	}
	return rec, nil
}

// renderRecordHTML is the record page: the swimlanes, newest on top, and the
// model. json.Marshal escapes <, > and &, so the model can't close its
// script tag.
func renderRecordHTML(rec TimelineRecord) (string, error) {
	model, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n")
	fmt.Fprintf(&b, "<title>%s — condocv2 timeline record</title>\n", esc(rec.Condoc))
	b.WriteString(recordCSS)
	b.WriteString("</head>\n<body>\n<div class=\"swim\">\n")
	fmt.Fprintf(&b, "<h1>%s</h1>\n<p class=\"swim-note\">The condoc's timeline as condocv2 recorded it on completion, %s.</p>\n",
		esc(rec.Condoc), esc(time.Unix(rec.RecordedAt, 0).UTC().Format("2006-01-02 15:04 UTC")))
	fmt.Fprintf(&b, "<div class=\"swim-legend\"><span style=\"color:%s\">● structure</span><span style=\"color:%s\">● interaction</span>"+
		"<span style=\"color:%s\">◌ pending</span><span>click a node for its commit</span></div>\n", swimBlue, swimGreen, swimPending)
	for _, e := range rec.Timeline.Errors {
		fmt.Fprintf(&b, "<div class=\"swim-error\">%s</div>\n", esc(e))
	}
	b.WriteString("<div class=\"swim-scroll\">\n")
	b.WriteString(swimlaneSVG(rec.Timeline, rec.Prompts))
	b.WriteString("</div>\n")
	b.WriteString(`<ol class="swim-sequence">
<li><b>propose</b> — the proposal's pointer commit</li>
<li><b>begin</b> — the slopspace, a branch in every repo, the condoc's first commit</li>
<li><b>lock</b> — the pointer locks the condoc for one interaction</li>
<li><b>prompt</b> — the prompt commit (auto-submits once if asked)</li>
<li><b>response</b> — the agent's reply, committed to the condoc, with a commit in each target it changed</li>
<li><b>record</b> — those target commits mapped into the condoc</li>
<li><b>unlock</b> — the pointer releases the condoc</li>
</ol>
</div>
`)
	b.WriteString(recordModelOpen)
	b.Write(model)
	b.WriteString("</script>\n</body>\n</html>\n")
	return b.String(), nil
}

const recordCSS = `<style>
body { margin: 0; background: #1a1a1a; color: #ccc; font-family: system-ui, sans-serif; }
h1 { font-size: 16px; margin: 0; }
.swim { display: flex; flex-direction: column; gap: 8px; padding: 12px 24px 24px; }
.swim-note { margin: 0; font-size: 12px; color: #888; }
.swim-error { font-size: 12px; color: #e88; }
.swim-legend { display: flex; gap: 14px; font-size: 11px; color: #777; }
.swim-scroll { overflow: auto; }
.swim-svg { display: block; font-family: inherit; }
.swim-band { fill: #1d1d20; }
.swim-band-odd { fill: #212126; }
.swim-lane-title { fill: #cccccc; font-size: 12px; font-weight: 600; }
.swim-lane-repo { fill: #666; font-size: 10px; }
.swim-prompt-n { font-size: 11px; font-weight: 600; }
.swim-prompt-text { fill: #999; font-size: 11px; }
.swim-node { cursor: pointer; }
.swim-node:hover circle { stroke-width: 3; }
.swim-node-pending { cursor: default; }
.swim-node-label { font-size: 10px; }
.swim-sequence { margin: 4px 0 0; padding-left: 22px; font-size: 11px; color: #888; line-height: 1.7; }
.swim-sequence b { color: #bbb; font-weight: 600; }
</style>
`

// ---- the swimlanes, as frontend/src/Swimlanes.tsx draws them ----
//
// Keep the two in step: the layout, shapes and connectors are the same, with
// newest on top.

const (
	swimBlue     = "#4da6ff"
	swimGreen    = "#4ec94e"
	swimPending  = "#777"
	swimLaneLine = "#2f4f6f"

	swimGutter    = 210.0
	swimColW      = 150.0
	swimRowH      = 54.0
	swimHeaderH   = 44.0
	swimPadBottom = 24.0
	swimR         = 9.0
)

func swimRowOf(kind string, n int) int {
	switch kind {
	case "propose":
		return 0
	case "begin":
		return 1
	}
	if n == 0 {
		n = 1
	}
	base := 2 + (n-1)*3
	switch kind {
	case "lock", "prompt":
		return base
	case "response", "commit":
		return base + 1
	}
	return base + 2 // record, unlock
}

func swimIsGreen(kind string) bool {
	return kind == "lock" || kind == "prompt" || kind == "record" || kind == "unlock"
}

func swimShortRepo(repo string) string {
	return repo[strings.LastIndex(repo, "/")+1:]
}

func swimSnippet(text string, n int) string {
	one := strings.Join(strings.Fields(text), " ")
	if r := []rune(one); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return one
}

func swimEventTitle(e TimelineEvent) string {
	head := e.Kind
	if e.Interaction > 0 {
		head += " " + strconv.Itoa(e.Interaction)
	}
	parts := []string{head}
	if e.Pending {
		p := "(pending"
		if e.Note != "" {
			p += ": " + e.Note
		}
		parts = append(parts, p+")")
	} else if e.Note != "" {
		parts = append(parts, "("+e.Note+")")
	}
	if e.Subject != "" {
		parts = append(parts, e.Subject)
	}
	if e.SHA != "" {
		parts = append(parts, e.SHA[:min(12, len(e.SHA))])
	}
	if e.Time != 0 {
		parts = append(parts, time.Unix(e.Time, 0).UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	return strings.Join(parts, "\n")
}

func swimNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func swimArrow(b *strings.Builder, x1, y1, x2, y2 float64, color string, dashed bool) {
	marker := "blue"
	switch color {
	case swimGreen:
		marker = "green"
	case swimPending:
		marker = "grey"
	}
	dash := ""
	if dashed {
		dash = ` stroke-dasharray="2 4"`
	}
	fmt.Fprintf(b, "<line x1=\"%s\" y1=\"%s\" x2=\"%s\" y2=\"%s\" stroke=\"%s\" stroke-width=\"1.5\"%s marker-end=\"url(#swim-arrow-%s)\"/>\n",
		swimNum(x1), swimNum(y1), swimNum(x2), swimNum(y2), color, dash, marker)
}

func swimNode(b *strings.Builder, e TimelineEvent, x, y float64) {
	esc := html.EscapeString
	color := swimBlue
	if e.Pending {
		color = swimPending
	} else if swimIsGreen(e.Kind) {
		color = swimGreen
	}
	dash := ""
	if e.Pending {
		dash = ` stroke-dasharray="3 3"`
	}
	circle := func(r float64, fill string, width string) {
		fmt.Fprintf(b, "<circle cx=\"%s\" cy=\"%s\" r=\"%s\" fill=\"%s\" stroke=\"%s\" stroke-width=\"%s\"%s/>",
			swimNum(x), swimNum(y), swimNum(r), fill, color, width, dash)
	}
	if e.URL != "" {
		fmt.Fprintf(b, "<a href=\"%s\" target=\"_blank\" rel=\"noreferrer\">", esc(e.URL))
	}
	class := "swim-node"
	if e.Pending {
		class += " swim-node-pending"
	}
	fmt.Fprintf(b, "<g class=\"%s\"><title>%s</title>", class, esc(swimEventTitle(e)))
	switch e.Kind {
	case "begin":
		circle(swimR+3, "none", "1.5")
		circle(swimR-2, "none", "1.5")
	case "record":
		circle(swimR, "#1a1a1a", "2")
		fmt.Fprintf(b, "<circle cx=\"%s\" cy=\"%s\" r=\"2.5\" fill=\"%s\"/>", swimNum(x), swimNum(y), color)
	default:
		r, fill := swimR, "#1a1a1a"
		if e.Kind == "lock" || e.Kind == "unlock" {
			r = swimR - 4
		}
		if e.Pending {
			fill = "none"
		}
		circle(r, fill, "2")
	}
	label := ""
	if e.Kind == "lock" || e.Kind == "unlock" {
		label = e.Kind
	} else if e.Pending {
		label = e.Note
	}
	if label != "" {
		fmt.Fprintf(b, "<text x=\"%s\" y=\"%s\" class=\"swim-node-label\" fill=\"%s\">%s</text>",
			swimNum(x+swimR+4), swimNum(y+4), color, esc(label))
	}
	b.WriteString("</g>")
	if e.URL != "" {
		b.WriteString("</a>")
	}
	b.WriteString("\n")
}

// swimlaneSVG draws the timeline with prompts in the left-hand column.
func swimlaneSVG(tl Timeline, prompts []Interaction) string {
	esc := html.EscapeString
	laneIndex := map[string]int{}
	for i, l := range tl.Lanes {
		laneIndex[l.ID] = i
	}
	maxRow := 1
	for _, e := range tl.Events {
		maxRow = max(maxRow, swimRowOf(e.Kind, e.Interaction))
	}
	for _, p := range prompts {
		maxRow = max(maxRow, swimRowOf("lock", p.N))
	}

	width := swimGutter + float64(len(tl.Lanes))*swimColW + 40
	height := swimHeaderH + float64(maxRow+1)*swimRowH + swimPadBottom
	colX := func(lane string) float64 { return swimGutter + float64(laneIndex[lane])*swimColW + swimColW/2 }
	rowY := func(row int) float64 { return swimHeaderH + swimRowH/2 + float64(maxRow-row)*swimRowH }

	// Several events can land in one cell (a stage retried); fan them out.
	type placedEvent struct {
		e    TimelineEvent
		x, y float64
	}
	cellCount := map[string]int{}
	placed := make([]placedEvent, len(tl.Events))
	for i, e := range tl.Events {
		row := swimRowOf(e.Kind, e.Interaction)
		key := e.Lane + "|" + strconv.Itoa(row)
		k := cellCount[key]
		cellCount[key] = k + 1
		placed[i] = placedEvent{e: e, x: colX(e.Lane) + float64(k)*(2*swimR+6), y: rowY(row)}
	}
	find := func(lane, kind string, n int) *placedEvent {
		for i := range placed {
			if p := &placed[i]; p.e.Lane == lane && p.e.Kind == kind && (n < 0 || p.e.Interaction == n) {
				return p
			}
		}
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<svg class=\"swim-svg\" xmlns=\"http://www.w3.org/2000/svg\" width=\"%s\" height=\"%s\" viewBox=\"0 0 %s %s\">\n",
		swimNum(width), swimNum(height), swimNum(width), swimNum(height))
	b.WriteString("<defs>")
	for _, m := range [][2]string{{"blue", swimBlue}, {"green", swimGreen}, {"grey", swimPending}} {
		fmt.Fprintf(&b, "<marker id=\"swim-arrow-%s\" viewBox=\"0 0 10 10\" refX=\"9\" refY=\"5\" markerWidth=\"6\" markerHeight=\"6\" orient=\"auto-start-reverse\">"+
			"<path d=\"M 0 0 L 10 5 L 0 10 z\" fill=\"%s\"/></marker>", m[0], m[1])
	}
	b.WriteString("</defs>\n")

	// Lane bands and headings.
	for i, l := range tl.Lanes {
		band := "swim-band"
		if i%2 == 1 {
			band += " swim-band-odd"
		}
		title, sub := swimShortRepo(l.Repo), swimShortRepo(l.Repo)
		switch l.Role {
		case "pointer":
			title = "Pointer"
		case "condoc":
			title = "Condoc"
		default:
			sub = "target"
		}
		fmt.Fprintf(&b, "<rect x=\"%s\" y=\"0\" width=\"%s\" height=\"%s\" class=\"%s\"/>\n",
			swimNum(swimGutter+float64(i)*swimColW), swimNum(swimColW), swimNum(height), band)
		fmt.Fprintf(&b, "<text x=\"%s\" y=\"18\" text-anchor=\"middle\" class=\"swim-lane-title\">%s</text>\n", swimNum(colX(l.ID)), esc(title))
		fmt.Fprintf(&b, "<text x=\"%s\" y=\"33\" text-anchor=\"middle\" class=\"swim-lane-repo\"><title>%s</title>%s</text>\n",
			swimNum(colX(l.ID)), esc(l.Repo), esc(sub))
	}
	b.WriteString("<text x=\"12\" y=\"18\" class=\"swim-lane-title\">Prompts</text>\n")

	// Each lane's line runs from its first node to its last.
	for _, l := range tl.Lanes {
		var ys []float64
		for _, p := range placed {
			if p.e.Lane == l.ID {
				ys = append(ys, p.y)
			}
		}
		if len(ys) < 2 {
			continue
		}
		sort.Float64s(ys)
		fmt.Fprintf(&b, "<line x1=\"%s\" y1=\"%s\" x2=\"%s\" y2=\"%s\" stroke=\"%s\" stroke-width=\"2\"/>\n",
			swimNum(colX(l.ID)), swimNum(ys[0]), swimNum(colX(l.ID)), swimNum(ys[len(ys)-1]), swimLaneLine)
	}

	// The prompt gutter: each interaction's prompt arrives from the left.
	pointerX := colX("pointer")
	for _, p := range prompts {
		y := rowY(swimRowOf("lock", p.N))
		fmt.Fprintf(&b, "<g><title>%s</title>", esc(fmt.Sprintf("Prompt %d\n\n%s", p.N, p.Prompt)))
		fmt.Fprintf(&b, "<text x=\"12\" y=\"%s\" class=\"swim-prompt-n\" fill=\"%s\">prompt %d</text>", swimNum(y-4), swimGreen, p.N)
		fmt.Fprintf(&b, "<text x=\"12\" y=\"%s\" class=\"swim-prompt-text\">%s</text>", swimNum(y+11), esc(swimSnippet(p.Prompt, 26)))
		swimArrow(&b, swimGutter-12, y, pointerX-swimR-3, y, swimGreen, false)
		b.WriteString("</g>\n")
	}

	// Connectors.
	if pb := find("pointer", "begin", -1); pb != nil {
		for _, p := range placed {
			if p.e.Kind == "begin" && p.e.Lane != "pointer" {
				swimArrow(&b, pb.x+swimR+3, pb.y, p.x-swimR-5, p.y, swimBlue, false)
			}
		}
	}
	seen := map[int]bool{}
	var ns []int
	for _, p := range placed {
		if n := p.e.Interaction; n > 0 && !seen[n] {
			seen[n] = true
			ns = append(ns, n)
		}
	}
	pendingOr := func(p *placedEvent, color string) string {
		if p.e.Pending {
			return swimPending
		}
		return color
	}
	for _, n := range ns {
		lock, prompt := find("pointer", "lock", n), find("condoc", "prompt", n)
		if lock != nil && prompt != nil {
			swimArrow(&b, lock.x+swimR, lock.y, prompt.x-swimR-3, prompt.y, pendingOr(prompt, swimGreen), false)
		}
		response, record := find("condoc", "response", n), find("condoc", "record", n)
		for i := range placed {
			c := &placed[i]
			if c.e.Kind != "commit" || c.e.Interaction != n {
				continue
			}
			if response != nil {
				swimArrow(&b, response.x+swimR, response.y, c.x-swimR-3, c.y, pendingOr(c, swimBlue), false)
			}
			if record != nil && !c.e.Pending {
				swimArrow(&b, record.x+swimR, record.y, c.x-swimR-3, c.y, swimGreen, true)
			}
		}
		if unlock := find("pointer", "unlock", n); unlock != nil && record != nil {
			swimArrow(&b, unlock.x+swimR, unlock.y, record.x-swimR-3, record.y, pendingOr(unlock, swimGreen), false)
		}
	}

	for _, p := range placed {
		swimNode(&b, p.e, p.x, p.y)
	}
	b.WriteString("</svg>\n")
	return b.String()
}
