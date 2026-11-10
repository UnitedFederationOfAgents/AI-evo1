package main

// The timeline record: the condoc view's model -- the visual tab's timeline
// and the text tab's interactions -- saved into the condoc branch when the
// condoc is completed, as Record.html beside its step file. It renders the
// swimlanes and the text as a standalone page and carries the model itself
// as JSON, so once Merge has deleted the slopspace -- the only place the
// condoc's files and lanes could be read from -- both tabs are drawn from the
// record instead, in the same detail.
//
// The record is saved at complete, so it ends there: the completion commits
// the visual tab draws after it (drafted, prs, merged) aren't in it, and
// neither is the visual's column of local operations, which is never part of
// the model (see ops.go).

import (
	"encoding/json"
	"fmt"
	"html"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"dungeon-keeper/pkg/gitauth"
	dktypes "dungeon-keeper/pkg/types"
	prkeeper "pr-keeper"
)

const (
	recordFileName = "Record.html"
	recordVersion  = 1
	recordRetry    = time.Minute // how soon a record that couldn't be read is fetched again
)

// TimelineRecord is the saved model: the visual side (Timeline) and the text
// side (Interactions, each Prompt/Response/Record in full).
type TimelineRecord struct {
	Version      int           `json:"version"`
	Condoc       string        `json:"condoc"`
	RecordedAt   int64         `json:"recordedAt"`
	Timeline     Timeline      `json:"timeline"`
	Interactions []Interaction `json:"interactions,omitempty"`
	// Prompts is what records written before Interactions carry instead:
	// each interaction's n and prompt only (see fetchTimelineRecord).
	Prompts []Interaction `json:"prompts,omitempty"`
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
	return repoBlobURL(spec.CondocRepo, spec.Branch, recordRepoPath(spec))
}

func newTimelineRecord(spec *PointerSpec, tl Timeline, interactions []Interaction, at time.Time) TimelineRecord {
	return TimelineRecord{Version: recordVersion, Condoc: spec.Name, RecordedAt: at.Unix(), Timeline: tl, Interactions: interactions}
}

// writeTimelineRecord saves the record into the condoc repo's writespace,
// for the caller's next condoc commit.
func (s *Server) writeTimelineRecord(spec *PointerSpec, meta *dktypes.SlopspaceMetadata, main string) error {
	interactions, _, err := readScopeInteractions(spec, main, readFileString) // every scope's (steps.go)
	if err != nil {
		return err
	}
	rec := newTimelineRecord(spec, s.buildTimeline(spec, meta), interactions, time.Now())
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
	// fileContent is path on the kept branch, else the default branch it was
	// merged into: on GitHub, or in a local repo (localrepo.go).
	var fileContent func(path string) ([]byte, error)
	if gitauth.IsLocal(spec.CondocRepo) {
		fileContent = func(path string) ([]byte, error) {
			b, err := localFileContent(spec.CondocRepo, spec.Branch, path)
			if err != nil {
				b, err = localFileContent(spec.CondocRepo, "", path)
			}
			return b, err
		}
	} else {
		client, err := prClient()
		if err != nil {
			return nil, err
		}
		fileContent = func(path string) ([]byte, error) {
			b, err := client.FileContent(spec.CondocRepo, spec.Branch, path)
			if prkeeper.IsStatus(err, http.StatusNotFound) {
				b, err = client.FileContent(spec.CondocRepo, "", path)
			}
			return b, err
		}
	}
	path := recordRepoPath(spec)
	page, err := fileContent(path)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %v", spec.CondocRepo, path, err)
	}
	rec, err := parseRecordHTML(string(page))
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %v", spec.CondocRepo, path, err)
	}
	if len(rec.Interactions) == 0 {
		// An older record has only the prompts: read the text side from the
		// step file it was merged beside, else make do with the prompts.
		rec.Interactions = rec.Prompts
		step := filepath.ToSlash(stepFilePath(filepath.FromSlash(spec.CondocPath)))
		content, err := fileContent(step)
		if err == nil {
			rec.Interactions = parseInteractions(string(content))
		}
	}
	if rec.Interactions == nil {
		rec.Interactions = []Interaction{}
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

// renderRecordHTML is the record page: the swimlanes, flowing from the top
// down, and the model. json.Marshal escapes <, > and &, so the model can't close its
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
		"<span style=\"color:%s\">◌ pending</span><span style=\"color:%s\">◌ external change</span><span>N.k a DTT tool call</span><span>click a node for its commit</span></div>\n",
		swimBlue, swimGreen, swimPending, swimBlue)
	for _, e := range rec.Timeline.Errors {
		fmt.Fprintf(&b, "<div class=\"swim-error\">%s</div>\n", esc(e))
	}
	b.WriteString("<div class=\"swim-scroll\">\n")
	b.WriteString(swimlaneSVG(rec.Timeline, rec.Interactions))
	b.WriteString("</div>\n")
	// The text side, as the Text tab shows it.
	for _, it := range rec.Interactions {
		scope := ""
		if it.Step != "" {
			scope = " · " + scopeTitle(it.Step)
		}
		fmt.Fprintf(&b, "<h2 class=\"text-prompt\">Prompt %d%s</h2>\n<pre>%s</pre>\n", it.N, esc(scope), esc(it.Prompt))
		if it.Response != "" {
			fmt.Fprintf(&b, "<h2 class=\"text-response\">Response %d</h2>\n<pre>%s</pre>\n", it.N, esc(it.Response))
		}
		if it.Proposals != "" {
			fmt.Fprintf(&b, "<h2 class=\"text-review\">Proposals %d</h2>\n<pre>%s</pre>\n", it.N, esc(it.Proposals))
		}
		if it.Record != "" {
			fmt.Fprintf(&b, "<h2 class=\"text-record\">Record %d</h2>\n<pre>%s</pre>\n", it.N, esc(it.Record))
		}
		if it.Review != "" {
			fmt.Fprintf(&b, "<h2 class=\"text-review\">Review %d</h2>\n<pre>%s</pre>\n", it.N, esc(it.Review))
		}
		for _, t := range it.Tools {
			if t.DTT != "" {
				fmt.Fprintf(&b, "<h2 class=\"text-review\">DTT %d.%d</h2>\n<pre>%s</pre>\n", it.N, t.K, esc(t.DTT))
			}
			if t.Result != "" {
				fmt.Fprintf(&b, "<h2 class=\"text-record\">Result %d.%d</h2>\n<pre>%s</pre>\n", it.N, t.K, esc(t.Result))
			}
		}
	}
	b.WriteString(`<ol class="swim-sequence">
<li><b>start</b> — <b>propose</b>, the proposal's pointer commit; <b>begin</b>, the slopspace, a branch in every repo, the condoc's first commit</li>
<li><b>N</b> — an interaction: <b>lock</b> the condoc, the <b>prompt</b> commit, the agent's <b>response</b> with a commit in each target it changed (a wrench when it proposed DTT tool calls), the <b>record</b> mapping those commits, <b>unlock</b></li>
<li><b>?</b> — the response proposed DTT tool calls: each was accepted or rejected (or changes were requested, the next interaction) before anything else</li>
<li><b>N.k</b> — one DTT tool call: <b>lock</b>, the proposal commit (eye a read, wrench an assert, ✗ rejected), dtt ran it once that was pushed, the image commit <b>✓</b>, <b>unlock</b></li>
<li><b>◌ read-only</b> — a read-only repo pulled at a new commit (blue dotted): at begin, or by an interaction's deploy (its response row) or return (its record row)</li>
<li><b>external</b> — commits pushed to a condoc or target branch from outside condocv2, pulled in between interactions (blue dotted): by <i>Refresh remotes</i>, or as the next prompt took the lock; the pointer's <b>external</b> commit records them</li>
<li><b>step →</b> — the condoc indented its scope into a step (step-1, a substep step-1-1, ...), whose interactions went into a step file of their own; <b>step done ←</b> — the step completed, back out to the scope it was entered from. Interaction numbers run on through every scope</li>
<li><b>resource R</b> — files added to the condoc between interactions (uploaded, from the host's files, the highlighted files, entered as text, or dictated): a dot on the pointer, a green circle with a file in the condoc, which keeps them under <code>resources/R/</code> with a <b>Resource R</b> section</li>
<li><b>complete ◆</b> — the condoc completed (and this record saved)</li>
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
h2 { font-size: 12px; margin: 10px 0 0; }
.text-prompt { color: #4ec94e; }
.text-response { color: #4da6ff; }
.text-record { color: #b48ce0; }
.text-review { color: #e0b050; }
.swim-glyph { font-size: 12px; font-weight: 700; pointer-events: none; }
.swim-pr-text { font-size: 9px; font-weight: 700; pointer-events: none; }
.swim-divider { stroke: #555; stroke-width: 1.5; }
.swim-section-label { fill: #888; font-size: 10px; font-weight: 600; }
pre { margin: 0; padding: 8px; background: #222; font-size: 12px; white-space: pre-wrap; word-break: break-word; }
</style>
`

// ---- the swimlanes, as frontend/src/Swimlanes.tsx draws them ----
//
// Keep the two in step: the layout, shapes and connectors are the same,
// flowing from the top down. The record has no operations column.

const (
	swimBlue     = "#4da6ff"
	swimGreen    = "#4ec94e"
	swimAmber    = "#e0b050"
	swimPurple   = "#b48ce0"
	swimPending  = "#777"
	swimLaneLine = "#2f4f6f"

	swimGutter    = 210.0
	swimColW      = 150.0
	swimRowH      = 54.0
	swimHeaderH   = 44.0
	swimPadBottom = 24.0
	swimR         = 9.0
)

var swimCompletionRow = map[string]int{"complete": 0, "redraft": 1, "drafted": 1, "prs": 2, "merged": 3}

// swimLayout places events in rows: start (propose, begin), each interaction
// (three rows, five when it was reviewed the older way), its sub-iterations
// (two rows each), each external change found after it (see external.go) and
// each step entered or left after it (see steps.go) and each resource added
// after it (resources.go), a row each in the order they happened, then
// completion -- with a divider above each section, labelled with its number
// ("external" for an external change, "step <path>" and "step <path> done"
// for a step, "resource R" for a resource), dashed for a sub-iteration.
//
// The record draws every scope in one picture; the visual tab draws one scope
// at a time.
type swimLayout struct {
	base           map[int]int
	legacy         map[int]bool
	external       map[[2]int]int // N.e -> its row
	steps          map[string]int // "step <path>" or "step-complete <path>" -> its row
	resources      map[int]int    // R -> its row
	completionBase int
	maxRow         int
	sections       []swimSection
}

// swimStepKey is a step event's key in swimLayout.steps.
func swimStepKey(e TimelineEvent) string {
	return e.Kind + " " + e.Path
}

// swimResourceLabel names a resource's section: "resource 2".
func swimResourceLabel(e TimelineEvent) string {
	return "resource " + strconv.Itoa(e.Seq)
}

// swimStepLabel names a step's section: "step 1", "step 1 done".
func swimStepLabel(e TimelineEvent) string {
	if e.Kind == "step-complete" {
		return "step " + e.Path + " done"
	}
	return "step " + e.Path
}

type swimSection struct {
	label string
	row   int
	solid bool
}

func swimLegacyReview(kind string) bool {
	return kind == "hold" || kind == "review" || kind == "release"
}

func newSwimLayout(events []TimelineEvent, prompts []Interaction) *swimLayout {
	maxN := 0
	l := &swimLayout{base: map[int]int{}, legacy: map[int]bool{}, external: map[[2]int]int{}, steps: map[string]int{}, resources: map[int]int{}}
	subs := map[int]int{}
	// What came after interaction N, before N+1: each external change, each
	// step entered or left and each resource added, at the time of its first
	// commit.
	type betweenItem struct {
		time     int64
		ext      [2]int // an external change's N.e
		step     string // a step's swimStepKey
		resource int    // a resource's R
		label    string
	}
	between := map[int][]betweenItem{}
	betweenAt := map[string]int{} // key -> its index in between[N]
	for _, e := range events {
		maxN = max(maxN, e.Interaction)
		if isBetweenKind(e.Kind) {
			key, item := "", betweenItem{time: e.Time}
			switch e.Kind {
			case "external":
				key = fmt.Sprintf("x %d.%d", e.Interaction, e.Seq)
				item.ext, item.label = [2]int{e.Interaction, e.Seq}, "external"
			case "resource":
				key = fmt.Sprintf("r %d", e.Seq)
				item.resource, item.label = e.Seq, swimResourceLabel(e)
			default:
				key = swimStepKey(e)
				item.step, item.label = key, swimStepLabel(e)
			}
			if i, ok := betweenAt[key]; ok {
				if t := between[e.Interaction][i].time; e.Time > 0 && (t == 0 || e.Time < t) {
					between[e.Interaction][i].time = e.Time
				}
			} else {
				betweenAt[key] = len(between[e.Interaction])
				between[e.Interaction] = append(between[e.Interaction], item)
			}
		}
		if e.Interaction > 0 && swimLegacyReview(e.Kind) {
			l.legacy[e.Interaction] = true
		}
		if e.Interaction > 0 && e.Sub > 0 {
			subs[e.Interaction] = max(subs[e.Interaction], e.Sub)
		}
	}
	for _, p := range prompts {
		maxN = max(maxN, p.N)
		for _, t := range p.Tools {
			subs[p.N] = max(subs[p.N], t.K)
		}
	}
	l.sections = []swimSection{{label: "start", row: 0, solid: true}}
	row := 2
	// Each external change, step or resource after interaction n: a section
	// of one row.
	addExternals := func(n int) {
		items := between[n]
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].time != items[j].time {
				return items[i].time < items[j].time
			}
			return items[i].ext[1]+items[i].resource < items[j].ext[1]+items[j].resource
		})
		for _, it := range items {
			switch {
			case it.step != "":
				l.steps[it.step] = row
			case it.resource > 0:
				l.resources[it.resource] = row
			default:
				l.external[it.ext] = row
			}
			l.sections = append(l.sections, swimSection{label: it.label, row: row, solid: true})
			row++
		}
	}
	addExternals(0)
	for n := 1; n <= maxN; n++ {
		l.base[n] = row
		l.sections = append(l.sections, swimSection{label: strconv.Itoa(n), row: row, solid: true})
		if l.legacy[n] {
			row += 5
		} else {
			row += 3
		}
		for k := 1; k <= subs[n]; k++ {
			l.sections = append(l.sections, swimSection{label: sectionLabel(n, k), row: row + (k-1)*2})
		}
		row += 2 * subs[n]
		addExternals(n)
	}
	l.completionBase = row
	for _, e := range events {
		if _, ok := swimCompletionRow[e.Kind]; ok {
			l.sections = append(l.sections, swimSection{row: row, solid: true})
			break
		}
	}
	l.maxRow = max(1, row-1)
	for _, e := range events {
		l.maxRow = max(l.maxRow, l.eventRow(e))
	}
	return l
}

// eventRow is e's row: rowOf, but for a read-only repo's resolve (see
// readonly.go), which goes on begin's row, or its interaction's response row
// for a deploy's and record row for a return's.
func (l *swimLayout) eventRow(e TimelineEvent) int {
	if e.Kind == "external" {
		return l.external[[2]int{e.Interaction, e.Seq}]
	}
	if isStepKind(e.Kind) {
		return l.steps[swimStepKey(e)]
	}
	if e.Kind == "resource" {
		return l.resources[e.Seq]
	}
	if e.Kind != "resolve" {
		return l.rowOf(e.Kind, e.Interaction, e.Sub)
	}
	if e.Interaction == 0 {
		return l.rowOf("begin", 0, 0)
	}
	if e.Note == readAtReturn || e.Note == readAtConfigure {
		return l.rowOf("record", e.Interaction, 0)
	}
	return l.rowOf("response", e.Interaction, 0)
}

func (l *swimLayout) rowOf(kind string, n, k int) int {
	switch kind {
	case "propose":
		return 0
	case "begin":
		return 1
	}
	if c, ok := swimCompletionRow[kind]; ok {
		return l.completionBase + c
	}
	n = max(n, 1)
	base, ok := l.base[n]
	if !ok {
		base = 2
	}
	if k > 0 {
		sb := base + 3 + (k-1)*2
		if l.legacy[n] {
			sb += 2
		}
		if kind == "lock" || kind == "dtt" {
			return sb
		}
		return sb + 1
	}
	switch kind {
	case "lock", "prompt":
		return base
	case "response", "commit":
		return base + 1
	case "hold":
		return base + 3
	case "review", "release":
		return base + 4
	}
	return base + 2 // record, unlock
}

func swimIsGreen(kind string) bool {
	switch kind {
	case "lock", "prompt", "record", "unlock", "dtt", "result", "hold", "review", "release", "step", "step-complete", "resource", "complete", "prs":
		return true
	}
	return false
}

func swimSmall(kind string) bool {
	return kind == "lock" || kind == "unlock" || kind == "hold" || kind == "release"
}

var (
	swimDecisionGlyph = map[string]string{"accept": "✓", "reject": "✗", "modify": "↻"}
	swimDecisionWord  = map[string]string{"accept": "accepted", "reject": "rejected", "modify": "changes requested"}
)

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
	if e.Kind == "external" {
		head += " " + sectionLabel(e.Interaction, e.Seq)
	} else if e.Kind == "resource" {
		head = fmt.Sprintf("%s (after interaction %d)", swimResourceLabel(e), e.Interaction)
	} else if e.Interaction > 0 {
		head += " " + sectionLabel(e.Interaction, e.Sub)
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
	case swimAmber:
		marker = "amber"
	}
	dash := ""
	if dashed {
		dash = ` stroke-dasharray="2 4"`
	}
	fmt.Fprintf(b, "<line x1=\"%s\" y1=\"%s\" x2=\"%s\" y2=\"%s\" stroke=\"%s\" stroke-width=\"1.5\"%s marker-end=\"url(#swim-arrow-%s)\"/>\n",
		swimNum(x1), swimNum(y1), swimNum(x2), swimNum(y2), color, dash, marker)
}

func swimGlyph(b *strings.Builder, x, y float64, text, color string) {
	fmt.Fprintf(b, "<text x=\"%s\" y=\"%s\" text-anchor=\"middle\" class=\"swim-glyph\" fill=\"%s\">%s</text>",
		swimNum(x), swimNum(y+4), color, html.EscapeString(text))
}

// swimBlocked is the "?" in the condoc lane on a hold's row: the human held
// by the proposals. A record is saved at complete, which a hold blocks, so
// here every hold has been decided.
func swimBlocked(b *strings.Builder, hold TimelineEvent, x, y float64) {
	if hold.URL != "" {
		fmt.Fprintf(b, "<a href=\"%s\" target=\"_blank\" rel=\"noreferrer\">", html.EscapeString(hold.URL))
	}
	fmt.Fprintf(b, "<g class=\"swim-node\"><title>held for DTT proposals after interaction %d</title>", hold.Interaction)
	fmt.Fprintf(b, "<circle cx=\"%s\" cy=\"%s\" r=\"%s\" fill=\"#1a1a1a\" stroke=\"%s\" stroke-width=\"2\"/>",
		swimNum(x), swimNum(y), swimNum(swimR+1), swimGreen)
	swimGlyph(b, x, y, "?", swimGreen)
	b.WriteString("</g>")
	if hold.URL != "" {
		b.WriteString("</a>")
	}
	b.WriteString("\n")
}

// swimWrench and swimEye are a DTT change (an assert, or a response that
// proposed tool calls) and a DTT read.
func swimWrench(b *strings.Builder, x, y float64, color string) {
	fmt.Fprintf(b, "<path d=\"M %s %s L %s %s\" stroke=\"%s\" stroke-width=\"2.2\" stroke-linecap=\"round\"/>",
		swimNum(x-4.5), swimNum(y+4.5), swimNum(x+1), swimNum(y-1), color)
	fmt.Fprintf(b, "<path d=\"M %s %s A 3 3 0 1 0 %s %s\" fill=\"none\" stroke=\"%s\" stroke-width=\"1.8\"/>",
		swimNum(x+0.5), swimNum(y-4.5), swimNum(x+4.5), swimNum(y-0.5), color)
}

func swimEye(b *strings.Builder, x, y float64, color string) {
	fmt.Fprintf(b, "<path d=\"M %s %s Q %s %s %s %s Q %s %s %s %s Z\" fill=\"none\" stroke=\"%s\" stroke-width=\"1.5\"/>",
		swimNum(x-6), swimNum(y), swimNum(x), swimNum(y-6), swimNum(x+6), swimNum(y), swimNum(x), swimNum(y+6), swimNum(x-6), swimNum(y), color)
	fmt.Fprintf(b, "<circle cx=\"%s\" cy=\"%s\" r=\"1.8\" fill=\"%s\"/>", swimNum(x), swimNum(y), color)
}

// swimFile is a resource's file: a page with its corner folded.
func swimFile(b *strings.Builder, x, y float64, color string) {
	fmt.Fprintf(b, "<path d=\"M %s %s L %s %s L %s %s L %s %s L %s %s Z M %s %s L %s %s L %s %s\" fill=\"none\" stroke=\"%s\" stroke-width=\"1.4\" stroke-linejoin=\"round\"/>",
		swimNum(x-3.5), swimNum(y-5), swimNum(x+1.5), swimNum(y-5), swimNum(x+3.5), swimNum(y-3), swimNum(x+3.5), swimNum(y+5), swimNum(x-3.5), swimNum(y+5),
		swimNum(x+1.5), swimNum(y-5), swimNum(x+1.5), swimNum(y-3), swimNum(x+3.5), swimNum(y-3), color)
}

// swimNode draws one event. asked makes a record a "?": its interaction
// proposed DTT tool calls (all answered by the time a record is saved).
func swimNode(b *strings.Builder, e TimelineEvent, x, y float64, asked bool) {
	esc := html.EscapeString
	color := swimBlue
	switch {
	case e.Pending:
		color = swimPending
	case e.Kind == "merged":
		color = swimPurple
	case swimIsGreen(e.Kind):
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
	case "resolve", "external":
		// A read-only repo pulled at a new commit, or a branch moved from
		// outside condocv2: a blue dotted circle.
		fmt.Fprintf(b, "<circle cx=\"%s\" cy=\"%s\" r=\"%s\" fill=\"#1a1a1a\" stroke=\"%s\" stroke-width=\"2\" stroke-dasharray=\"2 3\"/>",
			swimNum(x), swimNum(y), swimNum(swimR), color)
	case "record":
		if asked && !e.Pending {
			circle(swimR+1, "#1a1a1a", "2")
			swimGlyph(b, x, y, "?", color)
			break
		}
		circle(swimR, "#1a1a1a", "2")
		fmt.Fprintf(b, "<circle cx=\"%s\" cy=\"%s\" r=\"2.5\" fill=\"%s\"/>", swimNum(x), swimNum(y), color)
	case "response":
		switch {
		case e.Pending:
			circle(swimR, "none", "2")
		case e.Note == "dtt":
			circle(swimR+1, "#1a1a1a", "2")
			swimWrench(b, x, y, color)
		default:
			circle(swimR, "#1a1a1a", "2")
		}
	case "dtt":
		// The proposal, dashed: what it asks dtt to do.
		if e.Pending {
			circle(swimR, "none", "2")
			break
		}
		fmt.Fprintf(b, "<circle cx=\"%s\" cy=\"%s\" r=\"%s\" fill=\"#1a1a1a\" stroke=\"%s\" stroke-width=\"2\" stroke-dasharray=\"3 2\"/>",
			swimNum(x), swimNum(y), swimNum(swimR+2), color)
		switch e.Note {
		case "read", "preread":
			swimEye(b, x, y, color)
		case "reject":
			swimGlyph(b, x, y, "✗", color)
		case "delete":
			swimGlyph(b, x, y, "⊘", color)
		default:
			swimWrench(b, x, y, color)
		}
	case "result":
		if e.Pending {
			circle(swimR, "none", "2")
			break
		}
		circle(swimR+1, "#1a1a1a", "2")
		glyph := "✓"
		if e.Note == "failed" {
			glyph = "✗"
		}
		swimGlyph(b, x, y, glyph, color)
	case "review":
		fill, glyph := "#1a1a1a", swimDecisionGlyph[e.Note]
		if e.Pending {
			fill, glyph = "none", "?"
		} else if glyph == "" {
			glyph = "•"
		}
		circle(swimR+1, fill, "2")
		swimGlyph(b, x, y, glyph, color)
	case "complete":
		r := swimR + 1
		fmt.Fprintf(b, "<path d=\"M %s %s L %s %s L %s %s L %s %s Z\" fill=\"#1a1a1a\" stroke=\"%s\" stroke-width=\"2\"%s/>",
			swimNum(x), swimNum(y-r), swimNum(x+r), swimNum(y), swimNum(x), swimNum(y+r), swimNum(x-r), swimNum(y), color, dash)
	case "drafted", "redraft", "prs":
		prDash := ""
		if e.Kind != "prs" {
			prDash = ` stroke-dasharray="3 2"`
		}
		fmt.Fprintf(b, "<rect x=\"%s\" y=\"%s\" width=\"24\" height=\"16\" rx=\"4\" fill=\"#1a1a1a\" stroke=\"%s\" stroke-width=\"2\"%s/>"+
			"<text x=\"%s\" y=\"%s\" text-anchor=\"middle\" class=\"swim-pr-text\" fill=\"%s\">PR</text>",
			swimNum(x-12), swimNum(y-8), color, prDash, swimNum(x), swimNum(y+4), color)
	case "step", "step-complete":
		// A step entered or left (steps.go): a dot in the pointer lane, and
		// in the condoc lane a circle with an arrow -- right going in, left
		// coming back out.
		if e.Lane != "condoc" {
			circle(swimR-4, "#1a1a1a", "2")
			break
		}
		circle(swimR+1, "#1a1a1a", "2")
		glyph := "→"
		if e.Kind == "step-complete" {
			glyph = "←"
		}
		swimGlyph(b, x, y, glyph, color)
	case "resource":
		// A resource added (resources.go): a dot in the pointer lane, and in
		// the condoc lane a circle with a file.
		if e.Lane != "condoc" {
			circle(swimR-4, "#1a1a1a", "2")
			break
		}
		circle(swimR+1, "#1a1a1a", "2")
		swimFile(b, x, y, color)
	case "merged":
		circle(swimR+2, "#1a1a1a", "2")
		fmt.Fprintf(b, "<path d=\"M %s %s L %s %s L %s %s M %s %s L %s %s\" fill=\"none\" stroke=\"%s\" stroke-width=\"1.8\"/>",
			swimNum(x-4), swimNum(y-5), swimNum(x), swimNum(y), swimNum(x+4), swimNum(y-5), swimNum(x), swimNum(y), swimNum(x), swimNum(y+6), color)
		for _, p := range [][2]float64{{x - 4, y - 5}, {x + 4, y - 5}, {x, y + 6}} {
			fmt.Fprintf(b, "<circle cx=\"%s\" cy=\"%s\" r=\"1.6\" fill=\"%s\"/>", swimNum(p[0]), swimNum(p[1]), color)
		}
	default:
		r, fill := swimR, "#1a1a1a"
		if swimSmall(e.Kind) {
			r = swimR - 4
		}
		if e.Pending {
			fill = "none"
		}
		circle(r, fill, "2")
	}
	label := ""
	if _, completion := swimCompletionRow[e.Kind]; isStepKind(e.Kind) && e.Lane == "condoc" {
		label = swimStepLabel(e)
	} else if e.Kind == "resource" && e.Lane == "condoc" {
		label = swimResourceLabel(e)
	} else if swimSmall(e.Kind) || ((completion || e.Kind == "external") && e.Lane == "pointer") {
		label = e.Kind
	} else if e.Pending {
		label = e.Note
	}
	if label != "" {
		fmt.Fprintf(b, "<text x=\"%s\" y=\"%s\" class=\"swim-node-label\" fill=\"%s\">%s</text>",
			swimNum(x+swimR+6), swimNum(y+4), color, esc(label))
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
	layout := newSwimLayout(tl.Events, prompts)
	swimRowOf := layout.rowOf

	width := swimGutter + float64(len(tl.Lanes))*swimColW + 40
	height := swimHeaderH + float64(layout.maxRow+1)*swimRowH + swimPadBottom
	colX := func(lane string) float64 { return swimGutter + float64(laneIndex[lane])*swimColW + swimColW/2 }
	rowY := func(row int) float64 { return swimHeaderH + swimRowH/2 + float64(row)*swimRowH }

	// Several events can land in one cell (a stage retried); fan them out.
	type placedEvent struct {
		e    TimelineEvent
		x, y float64
	}
	cellCount := map[string]int{}
	placed := make([]placedEvent, len(tl.Events))
	for i, e := range tl.Events {
		row := layout.eventRow(e)
		key := e.Lane + "|" + strconv.Itoa(row)
		k := cellCount[key]
		cellCount[key] = k + 1
		placed[i] = placedEvent{e: e, x: colX(e.Lane) + float64(k)*(2*swimR+6), y: rowY(row)}
	}
	// find is interaction n's event (sub-iteration n.k's, for k > 0), or the
	// first of its kind for n < 0.
	find := func(lane, kind string, n, k int) *placedEvent {
		for i := range placed {
			if p := &placed[i]; p.e.Lane == lane && p.e.Kind == kind && (n < 0 || (p.e.Interaction == n && p.e.Sub == k)) {
				return p
			}
		}
		return nil
	}
	// The interactions that proposed DTT tool calls: their record is a "?".
	proposed := map[int]bool{}
	for _, p := range placed {
		if p.e.Kind == "response" && p.e.Note == "dtt" {
			proposed[p.e.Interaction] = true
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<svg class=\"swim-svg\" xmlns=\"http://www.w3.org/2000/svg\" width=\"%s\" height=\"%s\" viewBox=\"0 0 %s %s\">\n",
		swimNum(width), swimNum(height), swimNum(width), swimNum(height))
	b.WriteString("<defs>")
	for _, m := range [][2]string{{"blue", swimBlue}, {"green", swimGreen}, {"grey", swimPending}, {"amber", swimAmber}} {
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
		case "readonly":
			sub = "read-only"
		default:
			sub = "target"
		}
		fmt.Fprintf(&b, "<rect x=\"%s\" y=\"0\" width=\"%s\" height=\"%s\" class=\"%s\"/>\n",
			swimNum(swimGutter+float64(i)*swimColW), swimNum(swimColW), swimNum(height), band)
		fmt.Fprintf(&b, "<text x=\"%s\" y=\"18\" text-anchor=\"middle\" class=\"swim-lane-title\">%s</text>\n", swimNum(colX(l.ID)), esc(title))
		fmt.Fprintf(&b, "<text x=\"%s\" y=\"33\" text-anchor=\"middle\" class=\"swim-lane-repo\"><title>%s</title>%s</text>\n",
			swimNum(colX(l.ID)), esc(l.Repo), esc(sub))
	}
	b.WriteString("<text x=\"12\" y=\"18\" class=\"swim-lane-title\">Human</text>\n")
	b.WriteString("<text x=\"12\" y=\"33\" class=\"swim-lane-repo\">prompts and decisions</text>\n")

	// The numbered dividers: solid between iterations, dashed before each
	// sub-iteration, and one under the last row.
	dividerY := func(row int) float64 { return rowY(row) - swimRowH/2 }
	divider := func(row int, solid bool) {
		dash := ""
		if !solid {
			dash = ` stroke-dasharray="8 6"`
		}
		fmt.Fprintf(&b, "<line x1=\"4\" y1=\"%s\" x2=\"%s\" y2=\"%s\" class=\"swim-divider\"%s/>\n",
			swimNum(dividerY(row)), swimNum(width-8), swimNum(dividerY(row)), dash)
	}
	for _, s := range layout.sections {
		divider(s.row, s.solid)
		if s.label != "" {
			fmt.Fprintf(&b, "<text x=\"4\" y=\"%s\" class=\"swim-section-label\">%s</text>\n", swimNum(dividerY(s.row)+11), esc(s.label))
		}
	}
	divider(layout.maxRow+1, true)

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
		stroke, dash := swimLaneLine, ""
		if l.Role == "readonly" {
			stroke, dash = swimBlue, ` stroke-dasharray="2 4"` // followed, never written
		}
		fmt.Fprintf(&b, "<line x1=\"%s\" y1=\"%s\" x2=\"%s\" y2=\"%s\" stroke=\"%s\" stroke-width=\"2\"%s/>\n",
			swimNum(colX(l.ID)), swimNum(ys[0]), swimNum(colX(l.ID)), swimNum(ys[len(ys)-1]), stroke, dash)
	}

	// The prompt gutter: each interaction's prompt arrives from the left.
	pointerX := colX("pointer")
	for _, p := range prompts {
		y := rowY(swimRowOf("lock", p.N, 0))
		fmt.Fprintf(&b, "<g><title>%s</title>", esc(fmt.Sprintf("Prompt %d\n\n%s", p.N, p.Prompt)))
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-n\" fill=\"%s\">prompt %d</text>", swimNum(y-4), swimGreen, p.N)
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-text\">%s</text>", swimNum(y+11), esc(swimSnippet(p.Prompt, 24)))
		swimArrow(&b, swimGutter-12, y, pointerX-swimR-3, y, swimGreen, false)
		b.WriteString("</g>\n")
	}
	// ...each answer to a proposal, a sub-iteration...
	for _, p := range placed {
		if p.e.Kind != "lock" || p.e.Sub == 0 {
			continue
		}
		n, k := p.e.Interaction, p.e.Sub
		word, color := "answered", swimPending
		if d := find("condoc", "dtt", n, k); d != nil && !d.e.Pending {
			word, color = "✓ accepted · dtt "+d.e.Note, swimGreen
			switch d.e.Note {
			case "reject":
				word = "✗ rejected"
			case "preread":
				word = "👁 read before deciding"
			}
		}
		proposal := ""
		for _, it := range prompts {
			for _, t := range it.Tools {
				if it.N == n && t.K == k {
					if m := backtickedRe.FindStringSubmatch(t.DTT); m != nil {
						proposal = m[1]
					}
				}
			}
		}
		fmt.Fprintf(&b, "<g><title>%s</title>", esc(fmt.Sprintf("DTT tool call %s: %s %s", sectionLabel(n, k), proposal, word)))
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-n\" fill=\"%s\">%s</text>", swimNum(p.y-4), color, esc(word))
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-text\">%s</text>", swimNum(p.y+11), esc(swimSnippet(proposal, 24)))
		swimArrow(&b, swimGutter-12, p.y, pointerX-swimR-3, p.y, color, false)
		b.WriteString("</g>\n")
	}
	// ...each external change, pushed from outside condocv2 (external.go)...
	externalKeys := slices.SortedFunc(maps.Keys(layout.external), func(a, b [2]int) int {
		return layout.external[a] - layout.external[b]
	})
	for _, key := range externalKeys {
		row := layout.external[key]
		var repos []string
		commits := 0
		for _, p := range placed {
			if p.e.Kind == "external" && p.e.Lane != "pointer" && p.e.Interaction == key[0] && p.e.Seq == key[1] {
				repos = append(repos, swimShortRepo(p.e.Lane))
				commits += p.e.Commits
			}
		}
		y := rowY(row)
		fmt.Fprintf(&b, "<g><title>%s</title>", esc(fmt.Sprintf("External change %s: %s pushed from outside condocv2, pulled in",
			sectionLabel(key[0], key[1]), commitCount(commits))))
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-n\" fill=\"%s\">external</text>", swimNum(y-4), swimBlue)
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-text\">%s</text>", swimNum(y+11),
			esc(swimSnippet(commitCount(commits)+" · "+strings.Join(repos, ", "), 24)))
		swimArrow(&b, swimGutter-12, y, pointerX-swimR-3, y, swimBlue, true)
		b.WriteString("</g>\n")
	}
	// ...each step entered or left (steps.go)...
	for _, p := range placed {
		if !isStepKind(p.e.Kind) || p.e.Lane != "pointer" {
			continue
		}
		what := fmt.Sprintf("entered after interaction %d", p.e.Interaction)
		if p.e.Kind == "step-complete" {
			what = fmt.Sprintf("completed after interaction %d", p.e.Interaction)
		}
		fmt.Fprintf(&b, "<g><title>%s</title>", esc(fmt.Sprintf("Step %s: %s", p.e.Path, what)))
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-n\" fill=\"%s\">%s</text>", swimNum(p.y-4), swimGreen, esc(swimStepLabel(p.e)))
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-text\">%s</text>", swimNum(p.y+11), esc(what))
		swimArrow(&b, swimGutter-12, p.y, pointerX-swimR-3, p.y, swimGreen, false)
		b.WriteString("</g>\n")
	}
	// ...each resource added (resources.go)...
	for _, p := range placed {
		if p.e.Kind != "resource" || p.e.Lane != "pointer" {
			continue
		}
		what := p.e.Note
		if what == "" {
			what = fmt.Sprintf("added after interaction %d", p.e.Interaction)
		}
		fmt.Fprintf(&b, "<g><title>%s</title>", esc(fmt.Sprintf("Resource %d, added after interaction %d: %s", p.e.Seq, p.e.Interaction, what)))
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-n\" fill=\"%s\">%s</text>", swimNum(p.y-4), swimGreen, esc(swimResourceLabel(p.e)))
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-text\">%s</text>", swimNum(p.y+11), esc(swimSnippet(what, 24)))
		swimArrow(&b, swimGutter-12, p.y, pointerX-swimR-3, p.y, swimGreen, false)
		b.WriteString("</g>\n")
	}
	// ...and each older review's decision.
	for _, p := range placed {
		if p.e.Kind != "review" || p.e.Pending {
			continue
		}
		n := p.e.Interaction
		count := 0
		for _, it := range prompts {
			if it.N == n {
				count = strings.Count("\n"+it.Review, "\n- `")
			}
		}
		word := swimDecisionWord[p.e.Note]
		if word == "" {
			word = "decided"
		}
		y := rowY(swimRowOf("review", n, 0))
		fmt.Fprintf(&b, "<g><title>%s</title>", esc(fmt.Sprintf("Review %d: %s", n, word)))
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-n\" fill=\"%s\">review %d</text>", swimNum(y-4), swimGreen, n)
		fmt.Fprintf(&b, "<text x=\"24\" y=\"%s\" class=\"swim-prompt-text\">%s</text>", swimNum(y+11),
			esc(strings.TrimSpace(fmt.Sprintf("%s %s · %d DTT proposal(s)", swimDecisionGlyph[p.e.Note], word, count))))
		swimArrow(&b, swimGutter-12, y, pointerX-swimR-3, y, swimGreen, false)
		b.WriteString("</g>\n")
	}

	// Connectors.
	if pb := find("pointer", "begin", -1, 0); pb != nil {
		for _, p := range placed {
			if p.e.Kind == "begin" && p.e.Lane != "pointer" {
				swimArrow(&b, pb.x+swimR+3, pb.y, p.x-swimR-5, p.y, swimBlue, false)
			}
			// Begin also adds each read-only repo, at its first commit.
			if p.e.Kind == "resolve" && p.e.Interaction == 0 {
				swimArrow(&b, pb.x+swimR+3, pb.y, p.x-swimR-3, p.y, swimBlue, true)
			}
		}
	}
	// The pointer's external commit records each lane's external change.
	for _, from := range placed {
		if from.e.Kind != "external" || from.e.Lane != "pointer" {
			continue
		}
		for _, to := range placed {
			if to.e.Kind == "external" && to.e.Lane != "pointer" && to.e.Interaction == from.e.Interaction && to.e.Seq == from.e.Seq {
				swimArrow(&b, from.x+swimR+3, from.y, to.x-swimR-3, to.y, swimBlue, true)
			}
		}
	}
	// The pointer's step commit records the condoc's.
	for _, from := range placed {
		if !isStepKind(from.e.Kind) || from.e.Lane != "pointer" {
			continue
		}
		for _, to := range placed {
			if to.e.Lane == "condoc" && to.e.Kind == from.e.Kind && to.e.Path == from.e.Path {
				swimArrow(&b, from.x+swimR-2, from.y, to.x-swimR-4, to.y, swimGreen, false)
			}
		}
	}
	// The pointer's resource commit records the condoc's.
	for _, from := range placed {
		if from.e.Kind != "resource" || from.e.Lane != "pointer" {
			continue
		}
		for _, to := range placed {
			if to.e.Lane == "condoc" && to.e.Kind == "resource" && to.e.Seq == from.e.Seq {
				swimArrow(&b, from.x+swimR-2, from.y, to.x-swimR-4, to.y, swimGreen, false)
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
		lock, prompt := find("pointer", "lock", n, 0), find("condoc", "prompt", n, 0)
		if lock != nil && prompt != nil {
			swimArrow(&b, lock.x+swimR, lock.y, prompt.x-swimR-3, prompt.y, pendingOr(prompt, swimGreen), false)
		}
		response, record := find("condoc", "response", n, 0), find("condoc", "record", n, 0)
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
		if unlock := find("pointer", "unlock", n, 0); unlock != nil && record != nil {
			swimArrow(&b, unlock.x+swimR, unlock.y, record.x-swimR-3, record.y, pendingOr(unlock, swimGreen), false)
		}
		// Each sub-iteration: its lock brings in the proposal commit, its
		// unlock the image commit.
		for k := 1; ; k++ {
			subLock, dtt := find("pointer", "lock", n, k), find("condoc", "dtt", n, k)
			if subLock == nil && dtt == nil {
				break
			}
			if subLock != nil && dtt != nil {
				swimArrow(&b, subLock.x+swimR, subLock.y, dtt.x-swimR-4, dtt.y, pendingOr(dtt, swimGreen), false)
			}
			if subUnlock, result := find("pointer", "unlock", n, k), find("condoc", "result", n, k); subUnlock != nil && result != nil {
				swimArrow(&b, subUnlock.x+swimR, subUnlock.y, result.x-swimR-3, result.y, pendingOr(subUnlock, swimGreen), false)
			}
		}
		hold := find("pointer", "hold", n, 0)
		qx := colX("condoc")
		if hold != nil {
			swimArrow(&b, hold.x+swimR, hold.y, qx-swimR-3, hold.y, swimGreen, false)
			if record != nil {
				swimArrow(&b, record.x, record.y+swimR+2, qx, hold.y-swimR-4, swimGreen, false)
			}
		}
		review := find("condoc", "review", n, 0)
		if hold != nil && review != nil {
			swimArrow(&b, qx, hold.y+swimR+2, review.x, review.y-swimR-4, pendingOr(review, swimGreen), false)
		}
		if release := find("pointer", "release", n, 0); release != nil && review != nil {
			swimArrow(&b, release.x+swimR, release.y, review.x-swimR-4, review.y, pendingOr(release, swimGreen), false)
		}
	}
	for _, kind := range []string{"complete", "prs"} {
		if from, to := find("pointer", kind, -1, 0), find("condoc", kind, -1, 0); from != nil && to != nil {
			swimArrow(&b, from.x+swimR+2, from.y, to.x-swimR-6, to.y, swimGreen, false)
		}
	}

	for _, p := range placed {
		swimNode(&b, p.e, p.x, p.y, p.e.Kind == "record" && proposed[p.e.Interaction])
	}
	for _, p := range placed {
		if p.e.Kind == "hold" && !p.e.Pending {
			swimBlocked(&b, p.e, colX("condoc"), p.y)
		}
	}
	b.WriteString("</svg>\n")
	return b.String()
}
