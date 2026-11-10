package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

// fakeLR stands in for local-representative's HTTP API
// (local-representative/controlapi.go), The Conversationalist's
// /convo/api/polly and composer's /compose/api/* behind it.
type fakeLR struct {
	mu        sync.Mutex
	node      string
	sequences map[string]string // id -> definition
	runs      []map[string]any
	files     []map[string]any
	blobs     map[string][]byte // an uploaded file's bytes, by its id
	uploads   int
	comps    map[string]map[string]any
	polly     []string // texts spoken
	pollyReqs int      // clips asked for, spoken or reused
	played    []string // the clips asked for with play, by name, in order
	clips     []map[string]any // The Conversationalist's recent clips, oldest first
	calls     []string
	acURL     string // the agent-coordinator it's connected to; "" for none
	failSeq   string // a sequence whose runs end in error
}

func newFakeLR() *fakeLR {
	return &fakeLR{node: "fake-node", sequences: map[string]string{}, blobs: map[string][]byte{}, comps: map[string]map[string]any{}}
}

func (f *fakeLR) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	body, _ := io.ReadAll(r.Body)
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/api/control/sequences/") && r.Method == "GET":
		id := strings.TrimPrefix(p, "/api/control/sequences/")
		var seq any
		if def, ok := f.sequences[id]; ok {
			seq = map[string]any{"id": id, "name": "Robot: hello world, recorded", "controls": []string{"message"}, "steps": 6, "recordings": 1, "definition": def}
		}
		reply(200, map[string]any{"node": f.node, "sequence": seq})
	case strings.HasPrefix(p, "/api/control/sequences/") && r.Method == "DELETE":
		delete(f.sequences, strings.TrimPrefix(p, "/api/control/sequences/"))
		reply(200, map[string]any{"message": "deleted"})
	case p == "/api/control/import":
		src := string(body)
		_, seqs, _ := strings.Cut(src, "\nsequences:")
		ids := regexp.MustCompile(`(?m)^  - id: (\S+)`).FindAllStringSubmatch(seqs, -1)
		if !strings.Contains(src, "format: lr-control-v1") || len(ids) == 0 {
			reply(422, map[string]string{"error": "not a control library"})
			return
		}
		def := fmt.Sprintf("def-%d", len(src))
		defined := []map[string]string{}
		for _, m := range ids {
			if r.URL.Query().Get("check") != "1" {
				f.sequences[m[1]] = def
			}
			defined = append(defined, map[string]string{"id": m[1], "definition": def})
		}
		reply(200, map[string]any{"sequences": defined})
	case p == "/api/control/runs" && r.Method == "POST":
		var req struct {
			Sequence string            `json:"sequence"`
			Key      string            `json:"key"`
			Controls map[string]string `json:"controls"`
		}
		json.Unmarshal(body, &req)
		if _, ok := f.sequences[req.Sequence]; !ok {
			reply(422, map[string]string{"error": "no sequence " + req.Sequence})
			return
		}
		n := len(f.runs) + 1
		rec := map[string]any{"who": f.node, "file_id": fmt.Sprintf("r%d_ianar-recording.webm", n), "name": "ianar-recording.webm", "video": true}
		recs := []any{rec}
		// A labels control stands for recording blocks: one labelled video each.
		for i, label := range strings.Fields(req.Controls["labels"]) {
			recs = append(recs, map[string]any{"who": f.node, "file_id": fmt.Sprintf("r%d_%d_%s.webm", n, i, label), "name": label + ".webm", "video": true, "label": label})
		}
		f.files = append(f.files, map[string]any{"id": rec["file_id"], "name": rec["name"], "uploaded_at": n})
		run := map[string]any{"id": fmt.Sprintf("run%d", n), "sequence": req.Sequence, "key": req.Key, "controls": req.Controls,
			"status": "success", "recordings": recs, "output": []any{
				map[string]any{"label": "echoed", "value": req.Controls["message"]},
				map[string]any{"label": "workspace", "value": f.node + ": on an empty workspace, ready for the demo"},
				map[string]any{"label": "demo", "value": f.node + ": " + req.Sequence + " done"},
			}}
		if req.Sequence == "dtt-demo-session-prep" || req.Sequence == "dtt-demo-condoc-prep" {
			// The session and condoc preps say which terminal they opened, for their shutdowns.
			run["output"] = append(run["output"].([]any), map[string]any{"label": "fc", "value": "federation-command#2"})
		}
		if req.Sequence == f.failSeq {
			run["status"], run["error"] = "error", "step 3: the robot gave up"
		}
		f.runs = append(f.runs, run)
		reply(200, map[string]any{"run": run})
	case p == "/api/control/runs" && r.Method == "GET":
		out := []any{}
		for _, run := range f.runs {
			if run["key"] == r.URL.Query().Get("key") {
				out = append(out, run)
			}
		}
		reply(200, map[string]any{"runs": out})
	case strings.HasPrefix(p, "/api/control/runs/"):
		for _, run := range f.runs {
			if run["id"] == strings.TrimPrefix(p, "/api/control/runs/") {
				reply(200, map[string]any{"run": run})
				return
			}
		}
		reply(404, map[string]string{"error": "no run"})
	case p == "/api/control/coordinator":
		if f.acURL == "" {
			reply(200, map[string]any{"node": f.node, "connected": false, "note": "this local-representative isn't connected to agent-coordinator"})
		} else {
			reply(200, map[string]any{"node": f.node, "connected": true, "host": "ac.lan", "http_port": "8080", "url": f.acURL})
		}
	case p == "/api/files" && r.Method == "POST":
		// An upload, as local-representative/files.go takes it: one
		// multipart "file", saved under a new id.
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		part, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).NextPart()
		if err != nil || part.FormName() != "file" {
			reply(400, map[string]string{"error": "no file provided"})
			return
		}
		data, _ := io.ReadAll(part)
		f.uploads++
		id := fmt.Sprintf("u%d_%s", f.uploads, part.FileName())
		fl := map[string]any{"id": id, "name": part.FileName(), "size": len(data), "uploaded_at": 1000 + f.uploads, "expires_at": 4600 + f.uploads, "state": "cached"}
		f.files, f.blobs[id] = append(f.files, fl), data
		reply(200, map[string]any{"files": []any{fl}})
	case p == "/api/files":
		reply(200, map[string]any{"files": f.files})
	case strings.HasPrefix(p, "/api/files/") && r.Method == "GET":
		data, ok := f.blobs[strings.TrimPrefix(p, "/api/files/")]
		if !ok {
			reply(404, map[string]string{"error": "no file"})
			return
		}
		w.Write(data)
	case strings.HasPrefix(p, "/api/files/") && r.Method == "DELETE":
		var keep []map[string]any
		for _, fl := range f.files {
			if fl["id"] != strings.TrimPrefix(p, "/api/files/") {
				keep = append(keep, fl)
			}
		}
		f.files = keep
		delete(f.blobs, strings.TrimPrefix(p, "/api/files/"))
		reply(200, map[string]any{})
	case p == "/convo/api/polly":
		// The same text in the same configuration reuses a recent clip,
		// as The Conversationalist's pollyconfig.go does.
		var req struct {
			Text, Name, Config string
			Play               bool
		}
		json.Unmarshal(body, &req)
		if req.Config == "" {
			req.Config = "default"
		}
		f.pollyReqs++
		name := "polly-" + req.Name + ".mp3"
		id := fmt.Sprintf("p%d_%s", f.pollyReqs, name)
		var clip map[string]any
		for _, k := range f.clips {
			if k["text"] == req.Text && k["config"] == req.Config {
				clip = k
				clip["uses"] = clip["uses"].(int) + 1
			}
		}
		reused := clip != nil
		if !reused {
			f.polly = append(f.polly, req.Text)
			chars := len([]rune(req.Text))
			region := "us-east-2" // the presets other than default name us-east-1 (Revision A)
			if req.Config != "default" {
				region = "us-east-1"
			}
			clip = map[string]any{"text": req.Text, "config": req.Config, "voice": "Joanna", "engine": "standard", "region": region,
				"characters": chars, "cost_usd": float64(chars) * 4 / 1e6, "cost": "0.01 cents", "uses": 1}
			f.clips = append(f.clips, clip)
		}
		clip["file_id"] = id
		f.files = append(f.files, map[string]any{"id": id, "name": name, "uploaded_at": 100 + f.pollyReqs})
		ans := map[string]any{"file_id": id, "saved_as": name, "config": req.Config, "characters": clip["characters"], "cost": clip["cost"], "reused": reused}
		if req.Play {
			// Played in a TC tab on the node (pollyplay.go, Revision B).
			f.played = append(f.played, req.Name)
			ans["played"], ans["played_on"] = true, "a hidden tab on this host"
		}
		reply(200, ans)
	case p == "/convo/api/polly/clips":
		reply(200, map[string]any{"clips": f.clips})
	case p == "/compose/api/status":
		reply(200, map[string]any{"repr_status": "connected"})
	case strings.HasPrefix(p, "/compose/api/compositions/"):
		id := strings.TrimPrefix(p, "/compose/api/compositions/")
		switch r.Method {
		case "GET":
			if c, ok := f.comps[id]; ok {
				reply(200, c)
			} else {
				reply(404, map[string]string{"error": "no composition " + id})
			}
		case "PUT":
			var c map[string]any
			json.Unmarshal(body, &c)
			c["id"], c["updated_at"] = id, time.Now().Unix()
			_, had := f.comps[id]
			f.comps[id] = c
			reply(200, map[string]any{"composition": c, "created": !had})
		case "DELETE":
			delete(f.comps, id)
			reply(200, map[string]any{"deleted": id})
		}
	case p == "/compose/api/render-jobs":
		var req struct{ Composition string }
		json.Unmarshal(body, &req)
		c := f.comps[req.Composition]
		renders, _ := c["renders"].([]any)
		// Newest first, as composer's addRender keeps them.
		c["renders"] = append([]any{map[string]any{"file_id": "x_composer.webm", "name": "composer.webm", "size": 10, "at": time.Now().Unix()}}, renders...)
		reply(200, map[string]any{"id": "job1", "renderer": "ffmpeg", "state": "done"})
	default:
		reply(404, map[string]string{"error": "unexpected " + r.Method + " " + p})
	}
}

func TestLocalrepRobotHelloExample(t *testing.T) {
	fake := newFakeLR()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "localrep-robot-hello.dtt.yaml")
	ctx := context.Background()
	engine := func(mode string, force bool, vars map[string]string) *engine {
		e := testEngine(mode, force)
		e.lr = &localrepClient{base: srv.URL, http: srv.Client()}
		e.vars = vars
		e.docDir = "examples"
		return e
	}

	// Reading changes nothing: everything would be made.
	img, err := engine("read", false, nil).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=would-change exists=would-change exists=would-change exists=would-change rendered=would-change" {
		t.Errorf("read: %s", got)
	}
	if len(fake.sequences)+len(fake.runs)+len(fake.polly)+len(fake.comps) != 0 {
		t.Fatalf("read made something: %v", fake.calls)
	}
	if img.Objects[0].ID != "var" || img.Objects[0].State["node"] != "" || img.Objects[0].State["take"] != "1" {
		t.Errorf("variables: %+v", img.Objects[0])
	}

	// Asserting imports, runs, speaks, composes and renders.
	if img, err = engine("assert", false, nil).runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=changed exists=changed exists=changed exists=changed rendered=changed" {
		t.Errorf("assert: %s", got)
	}
	made := roundTrip(t, img)
	if len(fake.runs) != 1 || fake.runs[0]["key"] != "robot-hello-2026-10-24-1" || fake.runs[0]["controls"].(map[string]string)["message"] != "hello world from fake-node!" {
		t.Fatalf("runs %+v", fake.runs)
	}
	if len(fake.polly) != 1 || !strings.Contains(fake.polly[0], "This is fake-node.") || !strings.Contains(fake.polly[0], "typed: hello world from fake-node!") {
		t.Errorf("polly %q", fake.polly)
	}
	comp := fake.comps["robot-hello-2026-10-24-1"]
	if comp == nil {
		t.Fatalf("compositions %+v", fake.comps)
	}
	video, audio := comp["video"].([]any), comp["audio"].([]any)
	if video[1].(map[string]any)["file_id"] != "r1_ianar-recording.webm" || audio[0].(map[string]any)["file_id"] != "p1_polly-robot-hello-2026-10-24-1.mp3" {
		t.Errorf("composition %+v", comp)
	}
	if renders, _ := comp["renders"].([]any); len(renders) != 1 {
		t.Errorf("renders %+v", comp["renders"])
	}

	// Asserting again changes nothing.
	if img, err = engine("assert", false, nil).runSource(ctx, src, nil); err != nil || outcomes(img) != "definition=unchanged composition=unchanged rendered=unchanged" {
		t.Errorf("a second assert: %v %s", err, outcomes(img))
	}
	if len(fake.runs) != 1 || len(fake.polly) != 1 {
		t.Errorf("a second assert ran again: %d runs, %d clips", len(fake.runs), len(fake.polly))
	}

	// Another take is another run, clip and composition.
	if _, err = engine("assert", false, map[string]string{"take": "2"}).runSource(ctx, src, nil); err != nil || len(fake.runs) != 2 || fake.comps["robot-hello-2026-10-24-2"] == nil {
		t.Fatalf("take 2: %v %d runs", err, len(fake.runs))
	}
	// Only declared variables may be given.
	if _, err = engine("read", false, map[string]string{"nod": "x"}).runSource(ctx, src, nil); err == nil || !strings.Contains(err.Error(), "only declares node, take") {
		t.Errorf("an undeclared variable: %v", err)
	}

	// Deleting with the first assert's image removes take 1's sequence, clip
	// and composition, and keeps its run.
	if img, err = engine("delete", false, nil).runSource(ctx, src, made); err != nil || outcomes(img) != "exists=changed exists=kept exists=changed exists=changed" {
		t.Fatalf("delete: %v %s", err, outcomes(img))
	}
	if len(fake.sequences) != 0 || fake.comps["robot-hello-2026-10-24-1"] != nil || fake.comps["robot-hello-2026-10-24-2"] == nil {
		t.Errorf("after delete: sequences %v, compositions %v", fake.sequences, fake.comps)
	}
	for _, fl := range fake.files {
		if fl["name"] == "polly-robot-hello-2026-10-24-1.mp3" {
			t.Errorf("the clip is still there")
		}
	}
}

// TestLocalrepDemoPrepareExample: the demo-prepare sequence is imported and
// run on the node, keyed by the assert's time, and composer puts its
// recording between a title card naming the node and the time, and a card
// with what the run reported (Step1Prompt.md, Revision A).
func TestLocalrepDemoPrepareExample(t *testing.T) {
	fake := newFakeLR()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "localrep-demo-prepare.dtt.yaml")
	ctx := context.Background()
	engine := func(mode string, vars map[string]string) *engine {
		e := testEngine(mode, false)
		e.lr = &localrepClient{base: srv.URL, http: srv.Client()}
		e.vars = vars
		e.docDir = "examples"
		return e
	}
	vars := map[string]string{"node": "fake-node"}

	img, err := engine("read", vars).runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=would-change exists=would-change exists=would-change rendered=would-change" {
		t.Errorf("read: %s", got)
	}
	if len(fake.sequences)+len(fake.runs)+len(fake.comps) != 0 {
		t.Fatalf("read made something: %v", fake.calls)
	}

	if img, err = engine("assert", vars).runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=changed exists=changed exists=changed rendered=changed" {
		t.Errorf("assert: %s", got)
	}
	made := roundTrip(t, img)
	key := fmt.Sprintf("demo-prep-%d", testNow.Unix())
	if _, ok := fake.sequences["dtt-demo-prepare"]; !ok {
		t.Fatalf("sequences %v", fake.sequences)
	}
	if len(fake.runs) != 1 || fake.runs[0]["sequence"] != "dtt-demo-prepare" || fake.runs[0]["key"] != key {
		t.Fatalf("runs %+v", fake.runs)
	}
	// The demo page is named by the assert's time too (Revision G).
	if c := fake.runs[0]["controls"].(map[string]string); c["keys"] != "super+end" ||
		c["file"] != fmt.Sprintf("~/Desktop/demo-%d.html", testNow.Unix()) || len(c) != 2 {
		t.Errorf("controls %v", c)
	}
	comp := fake.comps[key]
	if comp == nil {
		t.Fatalf("compositions %+v", fake.comps)
	}
	video := comp["video"].([]any)
	if len(video) != 3 || video[1].(map[string]any)["file_id"] != "r1_ianar-recording.webm" ||
		!strings.Contains(video[0].(map[string]any)["text"].(string), "Preparing fake-node for a demo -- "+testNow.Format(time.RFC1123)) ||
		video[2].(map[string]any)["text"] != "fake-node: on an empty workspace, ready for the demo" {
		t.Errorf("composition %+v", comp)
	}
	if renders, _ := comp["renders"].([]any); len(renders) != 1 {
		t.Errorf("renders %+v", comp["renders"])
	}

	// Deleting with the image removes the sequence and the composition, and
	// keeps the run.
	if img, err = engine("delete", vars).runSource(ctx, src, made); err != nil || outcomes(img) != "exists=changed exists=kept exists=changed" {
		t.Fatalf("delete: %v %s", err, outcomes(img))
	}
	if len(fake.sequences) != 0 || fake.comps[key] != nil {
		t.Errorf("after delete: sequences %v, compositions %v", fake.sequences, fake.comps)
	}

	// A node still holding an older version of the sequence (the window
	// counting one, Revision F) gets this file's: the run mustn't take the
	// old steps.
	fake.sequences["dtt-demo-prepare"] = "an-older-version"
	if _, err = engine("assert", vars).runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if fake.sequences["dtt-demo-prepare"] == "an-older-version" {
		t.Errorf("the older version of the sequence was kept")
	}
}

// TestLocalrepDemoCompleteExample: the complete demo calls the prepare, cool
// and shutdown examples, a phase each, all at its own time, then composes
// their three renders in a fourth phase (Step2Prompt.md, Revision A).
func TestLocalrepDemoCompleteExample(t *testing.T) {
	fake := newFakeLR()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "localrep-demo-complete.dtt.yaml")
	ctx := context.Background()
	engine := func(mode string) *engine {
		e := testEngine(mode, false)
		e.lr = &localrepClient{base: srv.URL, http: srv.Client()}
		e.vars = map[string]string{"node": "fake-node"}
		e.docDir = "examples"
		return e
	}
	phases := func(img *Image) string {
		var parts []string
		for _, p := range img.Phases {
			parts = append(parts, p.Name+"="+p.Status)
		}
		return strings.Join(parts, " ")
	}
	stamp := testNow.Unix()
	prepCall := "dtt.calls[localrep-demo-prepare.dtt.yaml]"

	// Reading reads each call: four would-changes each, and the
	// composition's two.
	img, err := engine("read").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := phases(img); got != "prepare=done cool=done shut-down=done compose=done" {
		t.Errorf("read phases: %s", got)
	}
	if img.Summary["would-change"] != 14 || img.Phases[0].Summary["would-change"] != 4 || img.Phases[3].Summary["would-change"] != 2 {
		t.Errorf("read summary %v, phases %+v", img.Summary, img.Phases)
	}
	if len(fake.sequences)+len(fake.runs)+len(fake.comps) != 0 {
		t.Fatalf("read made something: %v", fake.calls)
	}

	// Asserting gives the demo: three sequences and runs, all named by the
	// complete demo's time and sharing its page, and four compositions.
	if img, err = engine("assert").runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if got := phases(img); got != "prepare=done cool=done shut-down=done compose=done" {
		t.Errorf("assert phases: %s", got)
	}
	made := roundTrip(t, img)
	for _, id := range []string{"dtt-demo-prepare", "dtt-demo-cool", "dtt-demo-shutdown"} {
		if _, ok := fake.sequences[id]; !ok {
			t.Errorf("no sequence %s: %v", id, fake.sequences)
		}
	}
	var keys []string
	for _, r := range fake.runs {
		keys = append(keys, r["key"].(string))
		if c := r["controls"].(map[string]string); c["file"] != fmt.Sprintf("~/Desktop/demo-%d.html", stamp) {
			t.Errorf("run %s's controls %v", r["key"], c)
		}
	}
	if want := fmt.Sprintf("demo-prep-%d demo-cool-%d demo-shutdown-%d", stamp, stamp, stamp); strings.Join(keys, " ") != want {
		t.Fatalf("runs %s, want %s", strings.Join(keys, " "), want)
	}
	for _, id := range []string{"demo-prep-%d", "demo-cool-%d", "demo-shutdown-%d", "demo-complete-%d"} {
		if c := fake.comps[fmt.Sprintf(id, stamp)]; c == nil || len(asList(c["renders"])) != 1 {
			t.Errorf("composition %s: %+v", fmt.Sprintf(id, stamp), c)
		}
	}
	video := fake.comps[fmt.Sprintf("demo-complete-%d", stamp)]["video"].([]any)
	if len(video) != 5 || !strings.Contains(video[0].(map[string]any)["text"].(string), "A complete demo on fake-node") {
		t.Fatalf("complete composition %+v", video)
	}
	for i := 1; i <= 3; i++ {
		if video[i].(map[string]any)["file_id"] != "x_composer.webm" {
			t.Errorf("clip %d isn't a render: %+v", i, video[i])
		}
	}
	// Each phase has its own objects and outcomes; each call's image is its
	// state.
	if p := img.Phases[0]; p.Summary["changed"] != 4 || len(p.Objects) != 1 || p.Objects[0] != prepCall {
		t.Errorf("prepare phase %+v", p)
	}
	if p := img.Phases[3]; p.Summary["changed"] != 2 || len(p.Objects) != 1 {
		t.Errorf("compose phase %+v", p)
	}
	for _, o := range made.Objects {
		if o.ID == prepCall && (o.Phase != "prepare" || o.State["ok"] != true || o.State["image"] == nil) {
			t.Errorf("the prepare call %+v", o)
		}
	}

	// Asserting again changes nothing and runs nothing.
	if img, err = engine("assert").runSource(ctx, src, nil); err != nil || img.Summary["changed"] != 0 || len(fake.runs) != 3 {
		t.Errorf("a second assert: %v %v, %d runs", err, img.Summary, len(fake.runs))
	}

	// Deleting with the first image deletes through each call too.
	if img, err = engine("delete").runSource(ctx, src, made); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(fake.sequences) != 0 || len(fake.comps) != 0 {
		t.Errorf("after delete: sequences %v, compositions %v", fake.sequences, fake.comps)
	}
	if img.Summary["kept"] != 3 {
		t.Errorf("delete kept %v, want the three runs", img.Summary)
	}

	// A phase that fails stops the ones after it, and the image says so.
	fake = newFakeLR()
	fake.failSeq = "dtt-demo-cool"
	srv2 := httptest.NewServer(fake)
	defer srv2.Close()
	e := engine("assert")
	e.lr = &localrepClient{base: srv2.URL, http: srv2.Client()}
	img, err = e.runSource(ctx, src, nil)
	if err == nil || img == nil || img.Error == "" {
		t.Fatalf("a failed cool demo: %v", err)
	}
	if got := phases(img); got != "prepare=done cool=failed shut-down=not-run compose=not-run" {
		t.Errorf("failed phases: %s", got)
	}
	if _, ok := fake.sequences["dtt-demo-shutdown"]; ok || len(fake.runs) != 2 {
		t.Errorf("the phases after the failure ran: %v, %d runs", fake.sequences, len(fake.runs))
	}
}

// TestLocalrepDemoSessionExample: the session demo calls the prepare,
// session prepare, session shutdown and shutdown examples, a phase each,
// all at its own time; the session prep opens agent-coordinator at the URL
// this node's LR gives, and the session shutdown is handed the terminal
// the session prep opened; then a fifth phase composes their four renders
// (Step2Prompt.md, Revision C).
func TestLocalrepDemoSessionExample(t *testing.T) {
	fake := newFakeLR()
	fake.acURL = "http://ac.lan:8083"
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "localrep-demo-session.dtt.yaml")
	ctx := context.Background()
	engine := func(mode string) *engine {
		e := testEngine(mode, false)
		e.lr = &localrepClient{base: srv.URL, http: srv.Client()}
		e.vars = map[string]string{"node": "fake-node"}
		e.docDir = "examples"
		return e
	}
	phases := func(img *Image) string {
		var parts []string
		for _, p := range img.Phases {
			parts = append(parts, p.Name+"="+p.Status)
		}
		return strings.Join(parts, " ")
	}
	stamp := testNow.Unix()
	profile := fmt.Sprintf("/tmp/dtt-demo-session-%d", stamp)

	img, err := engine("read").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	const allDone = "prepare=done session-prepare=done session-shut-down=done shut-down=done compose=done"
	if got := phases(img); got != allDone {
		t.Errorf("read phases: %s", got)
	}
	if len(fake.sequences)+len(fake.runs)+len(fake.comps) != 0 {
		t.Fatalf("read made something: %v", fake.calls)
	}

	if img, err = engine("assert").runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if got := phases(img); got != allDone {
		t.Errorf("assert phases: %s", got)
	}
	made := roundTrip(t, img)
	for _, id := range []string{"dtt-demo-prepare", "dtt-demo-session-prep", "dtt-demo-session-shutdown", "dtt-demo-shutdown"} {
		if _, ok := fake.sequences[id]; !ok {
			t.Errorf("no sequence %s: %v", id, fake.sequences)
		}
	}
	var keys []string
	for _, r := range fake.runs {
		keys = append(keys, r["key"].(string))
	}
	if want := fmt.Sprintf("demo-prep-%d demo-session-prep-%d demo-session-shutdown-%d demo-shutdown-%d", stamp, stamp, stamp, stamp); strings.Join(keys, " ") != want {
		t.Fatalf("runs %s, want %s", strings.Join(keys, " "), want)
	}
	if c := fake.runs[1]["controls"].(map[string]string); c["url"] != "http://ac.lan:8083" || c["profile"] != profile || c["browser"] != "google-chrome" {
		t.Errorf("session prep's controls %v", c)
	}
	if c := fake.runs[2]["controls"].(map[string]string); c["fc"] != "federation-command#2" || c["profile"] != profile || len(c) != 2 {
		t.Errorf("session shutdown's controls %v", c)
	}
	for _, id := range []string{"demo-prep-%d", "demo-session-prep-%d", "demo-session-shutdown-%d", "demo-shutdown-%d", "demo-session-%d"} {
		if c := fake.comps[fmt.Sprintf(id, stamp)]; c == nil || len(asList(c["renders"])) != 1 {
			t.Errorf("composition %s: %+v", fmt.Sprintf(id, stamp), c)
		}
	}
	video := fake.comps[fmt.Sprintf("demo-session-%d", stamp)]["video"].([]any)
	if len(video) != 6 || !strings.Contains(video[0].(map[string]any)["text"].(string), "A session demo on fake-node") {
		t.Fatalf("session composition %+v", video)
	}
	for i := 1; i <= 4; i++ {
		if video[i].(map[string]any)["file_id"] != "x_composer.webm" {
			t.Errorf("clip %d isn't a render: %+v", i, video[i])
		}
	}

	// Deleting with the image deletes through each call too.
	if img, err = engine("delete").runSource(ctx, src, made); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(fake.sequences) != 0 || len(fake.comps) != 0 {
		t.Errorf("after delete: sequences %v, compositions %v", fake.sequences, fake.comps)
	}
	if img.Summary["kept"] != 4 {
		t.Errorf("delete kept %v, want the four runs", img.Summary)
	}

	// A failed session prep stops the rest: no terminal to close, and the
	// node stays prepared.
	fake = newFakeLR()
	fake.acURL = "http://ac.lan:8083"
	fake.failSeq = "dtt-demo-session-prep"
	srv2 := httptest.NewServer(fake)
	defer srv2.Close()
	e := engine("assert")
	e.lr = &localrepClient{base: srv2.URL, http: srv2.Client()}
	if img, err = e.runSource(ctx, src, nil); err == nil || img == nil {
		t.Fatalf("a failed session prep: %v", err)
	}
	if got := phases(img); got != "prepare=done session-prepare=failed session-shut-down=not-run shut-down=not-run compose=not-run" {
		t.Errorf("failed phases: %s", got)
	}
	if len(fake.runs) != 2 {
		t.Errorf("%d runs, want the prep's and the failed session prep's", len(fake.runs))
	}
}

// TestLocalrepDemoSessionFullExample: the full session demo calls the
// prepare, session prepare, hello, session shutdown and shutdown examples
// in turn, a phase each, all at its own time; the hello and the session
// shutdown are both handed the terminal the session prep opened, and the
// hello's session is named by the time; then a sixth phase composes their
// five renders (Step2Prompt.md, Revision F).
func TestLocalrepDemoSessionFullExample(t *testing.T) {
	fake := newFakeLR()
	fake.acURL = "http://ac.lan:8083"
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "localrep-demo-session-full.dtt.yaml")
	ctx := context.Background()
	engine := func(mode string) *engine {
		e := testEngine(mode, false)
		e.lr = &localrepClient{base: srv.URL, http: srv.Client()}
		e.vars = map[string]string{"node": "fake-node"}
		e.docDir = "examples"
		return e
	}
	phases := func(img *Image) string {
		var parts []string
		for _, p := range img.Phases {
			parts = append(parts, p.Name+"="+p.Status)
		}
		return strings.Join(parts, " ")
	}
	stamp := testNow.Unix()

	img, err := engine("read").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	const allDone = "prepare=done session-prepare=done hello=done session-shut-down=done shut-down=done compose=done"
	if got := phases(img); got != allDone {
		t.Errorf("read phases: %s", got)
	}
	if len(fake.sequences)+len(fake.runs)+len(fake.comps) != 0 {
		t.Fatalf("read made something: %v", fake.calls)
	}

	if img, err = engine("assert").runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if got := phases(img); got != allDone {
		t.Errorf("assert phases: %s", got)
	}
	made := roundTrip(t, img)
	for _, id := range []string{"dtt-demo-prepare", "dtt-demo-session-prep", "dtt-demo-session-hello", "dtt-demo-session-shutdown", "dtt-demo-shutdown"} {
		if _, ok := fake.sequences[id]; !ok {
			t.Errorf("no sequence %s: %v", id, fake.sequences)
		}
	}
	var keys []string
	for _, r := range fake.runs {
		keys = append(keys, r["key"].(string))
	}
	if want := fmt.Sprintf("demo-prep-%d demo-session-prep-%d demo-session-hello-%d demo-session-shutdown-%d demo-shutdown-%d", stamp, stamp, stamp, stamp, stamp); strings.Join(keys, " ") != want {
		t.Fatalf("runs %s, want %s", strings.Join(keys, " "), want)
	}
	if c := fake.runs[2]["controls"].(map[string]string); c["fc"] != "federation-command#2" || c["name"] != fmt.Sprintf("hello-world-%d", stamp) || len(c) != 2 {
		t.Errorf("hello's controls %v", c)
	}
	if c := fake.runs[3]["controls"].(map[string]string); c["fc"] != "federation-command#2" {
		t.Errorf("session shutdown's controls %v", c)
	}
	for _, id := range []string{"demo-prep-%d", "demo-session-prep-%d", "demo-session-hello-%d", "demo-session-shutdown-%d", "demo-shutdown-%d", "demo-session-full-%d"} {
		if c := fake.comps[fmt.Sprintf(id, stamp)]; c == nil || len(asList(c["renders"])) != 1 {
			t.Errorf("composition %s: %+v", fmt.Sprintf(id, stamp), c)
		}
	}
	video := fake.comps[fmt.Sprintf("demo-session-full-%d", stamp)]["video"].([]any)
	if len(video) != 7 || !strings.Contains(video[0].(map[string]any)["text"].(string), "A full session demo on fake-node") {
		t.Fatalf("full composition %+v", video)
	}
	for i := 1; i <= 5; i++ {
		if video[i].(map[string]any)["file_id"] != "x_composer.webm" {
			t.Errorf("clip %d isn't a render: %+v", i, video[i])
		}
	}

	if img, err = engine("delete").runSource(ctx, src, made); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(fake.sequences) != 0 || len(fake.comps) != 0 {
		t.Errorf("after delete: sequences %v, compositions %v", fake.sequences, fake.comps)
	}
	if img.Summary["kept"] != 5 {
		t.Errorf("delete kept %v, want the five runs", img.Summary)
	}

	// A failed hello stops the rest, leaving the windows open.
	fake = newFakeLR()
	fake.acURL = "http://ac.lan:8083"
	fake.failSeq = "dtt-demo-session-hello"
	srv2 := httptest.NewServer(fake)
	defer srv2.Close()
	e := engine("assert")
	e.lr = &localrepClient{base: srv2.URL, http: srv2.Client()}
	if img, err = e.runSource(ctx, src, nil); err == nil || img == nil {
		t.Fatalf("a failed hello: %v", err)
	}
	if got := phases(img); got != "prepare=done session-prepare=done hello=failed session-shut-down=not-run shut-down=not-run compose=not-run" {
		t.Errorf("failed phases: %s", got)
	}
	if len(fake.runs) != 3 {
		t.Errorf("%d runs, want the two preps' and the failed hello's", len(fake.runs))
	}
}

// TestLocalrepDemoCondocExample: the condocv2 demo is laid out as the full
// session demo -- prepare, condoc prepare, hello, condoc shutdown and
// shutdown, a phase each, all at its own time, the hello and the condoc
// shutdown handed the condoc prep's terminal and the hello's condoc named by
// the time -- then a sixth phase composes their five renders, each after a
// chapter card (Step2Prompt.md, Revision H).
func TestLocalrepDemoCondocExample(t *testing.T) {
	fake := newFakeLR()
	fake.acURL = "http://ac.lan:8083"
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "localrep-demo-condoc.dtt.yaml")
	ctx := context.Background()
	engine := func(mode string) *engine {
		e := testEngine(mode, false)
		e.lr = &localrepClient{base: srv.URL, http: srv.Client()}
		e.vars = map[string]string{"node": "fake-node"}
		e.docDir = "examples"
		return e
	}
	phases := func(img *Image) string {
		var parts []string
		for _, p := range img.Phases {
			parts = append(parts, p.Name+"="+p.Status)
		}
		return strings.Join(parts, " ")
	}
	stamp := testNow.Unix()
	profile := fmt.Sprintf("/tmp/dtt-demo-condoc-%d", stamp)

	img, err := engine("read").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	const allDone = "prepare=done condoc-prepare=done hello=done condoc-shut-down=done shut-down=done compose=done"
	if got := phases(img); got != allDone {
		t.Errorf("read phases: %s", got)
	}
	if len(fake.sequences)+len(fake.runs)+len(fake.comps) != 0 {
		t.Fatalf("read made something: %v", fake.calls)
	}

	if img, err = engine("assert").runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if got := phases(img); got != allDone {
		t.Errorf("assert phases: %s", got)
	}
	made := roundTrip(t, img)
	for _, id := range []string{"dtt-demo-prepare", "dtt-demo-condoc-prep", "dtt-demo-condoc-hello", "dtt-demo-condoc-shutdown", "dtt-demo-shutdown"} {
		if _, ok := fake.sequences[id]; !ok {
			t.Errorf("no sequence %s: %v", id, fake.sequences)
		}
	}
	var keys []string
	for _, r := range fake.runs {
		keys = append(keys, r["key"].(string))
	}
	if want := fmt.Sprintf("demo-prep-%d demo-condoc-prep-%d demo-condoc-hello-%d demo-condoc-shutdown-%d demo-shutdown-%d", stamp, stamp, stamp, stamp, stamp); strings.Join(keys, " ") != want {
		t.Fatalf("runs %s, want %s", strings.Join(keys, " "), want)
	}
	if c := fake.runs[1]["controls"].(map[string]string); c["url"] != "http://ac.lan:8083" || c["profile"] != profile || c["browser"] != "google-chrome" {
		t.Errorf("condoc prep's controls %v", c)
	}
	if c := fake.runs[2]["controls"].(map[string]string); c["fc"] != "federation-command#2" || c["name"] != fmt.Sprintf("HelloWorld-%d", stamp) ||
		c["target"] != "user-1/hello-world-target" || c["condoc_repo"] != "user-1/hello-world-condoc" || c["pointer_dir"] != "" {
		t.Errorf("hello's controls %v", c)
	}
	if c := fake.runs[3]["controls"].(map[string]string); c["fc"] != "federation-command#2" || c["profile"] != profile || len(c) != 2 {
		t.Errorf("condoc shutdown's controls %v", c)
	}
	for _, id := range []string{"demo-prep-%d", "demo-condoc-prep-%d", "demo-condoc-hello-%d", "demo-condoc-shutdown-%d", "demo-shutdown-%d", "demo-condoc-%d"} {
		if c := fake.comps[fmt.Sprintf(id, stamp)]; c == nil || len(asList(c["renders"])) != 1 {
			t.Errorf("composition %s: %+v", fmt.Sprintf(id, stamp), c)
		}
	}
	// A title, a chapter card and a render per part, and a closing card.
	video := fake.comps[fmt.Sprintf("demo-condoc-%d", stamp)]["video"].([]any)
	if len(video) != 12 || !strings.Contains(video[0].(map[string]any)["text"].(string), "A condocv2 demo on fake-node") {
		t.Fatalf("condoc composition %+v", video)
	}
	for i := 1; i <= 5; i++ {
		card, clip := video[2*i-1].(map[string]any), video[2*i].(map[string]any)
		if card["kind"] != "title" || !strings.HasPrefix(card["text"].(string), fmt.Sprintf("%d of 5 -- ", i)) {
			t.Errorf("chapter card %d: %+v", i, card)
		}
		if clip["file_id"] != "x_composer.webm" {
			t.Errorf("clip %d isn't a render: %+v", i, clip)
		}
	}

	if img, err = engine("delete").runSource(ctx, src, made); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(fake.sequences) != 0 || len(fake.comps) != 0 {
		t.Errorf("after delete: sequences %v, compositions %v", fake.sequences, fake.comps)
	}
	if img.Summary["kept"] != 5 {
		t.Errorf("delete kept %v, want the five runs", img.Summary)
	}

	// A failed hello stops the rest, leaving the windows open.
	fake = newFakeLR()
	fake.acURL = "http://ac.lan:8083"
	fake.failSeq = "dtt-demo-condoc-hello"
	srv2 := httptest.NewServer(fake)
	defer srv2.Close()
	e := engine("assert")
	e.lr = &localrepClient{base: srv2.URL, http: srv2.Client()}
	if img, err = e.runSource(ctx, src, nil); err == nil || img == nil {
		t.Fatalf("a failed hello: %v", err)
	}
	if got := phases(img); got != "prepare=done condoc-prepare=done hello=failed condoc-shut-down=not-run shut-down=not-run compose=not-run" {
		t.Errorf("failed phases: %s", got)
	}
	if len(fake.runs) != 3 {
		t.Errorf("%d runs, want the two preps' and the failed hello's", len(fake.runs))
	}
}

// TestDemoCondocHelloLeavesRobotTextAlone: the terminal shows the commands
// typed into it, and the robot reads the whole screen, so none of the
// condocv2 hello's terminal commands may hold the text the robot waits for
// on condocv2's page.
func TestDemoCondocHelloLeavesRobotTextAlone(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("examples", "resources", "demo-condoc-hello.control.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		if !strings.Contains(line, "op: fc-send") {
			continue
		}
		for _, robot := range []string{"Propose a condoc", "Accept — begin the condoc", "Select a condoc, or propose one"} {
			if strings.Contains(line, robot) {
				t.Errorf("a terminal command holds %q: %s", robot, line)
			}
		}
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// TestCallItself: a document calling itself is stopped while it's checked.
func TestCallItself(t *testing.T) {
	dir := t.TempDir()
	src := "dtt.calls:\n  select: {document: self.dtt.yaml}\n"
	if err := os.WriteFile(filepath.Join(dir, "self.dtt.yaml"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	e := testEngine("read", false)
	e.docDir = dir
	if _, err := e.runSource(context.Background(), src, nil); err == nil || !strings.Contains(err.Error(), "does a document call itself") {
		t.Fatalf("got %v", err)
	}
}

// TestLocalrepPollyConfigsExample: one clip per Polly configuration, each
// made with its config, on the node the run's LR is (its name from
// localrep.coordinator), and a last clip of the same text and config that
// The Conversationalist reuses rather than calling Polly
// (dTT-IntegrationImpls/Step3Prompt.md); then (Revision A) an audio-only
// composition of the four, rendered.
func TestLocalrepPollyConfigsExample(t *testing.T) {
	fake := newFakeLR()
	fake.acURL = "http://ac.lan:8080"
	srv := httptest.NewServer(fake)
	defer srv.Close()
	src := readExample(t, "localrep-polly-configs.dtt.yaml")
	ctx := context.Background()
	engine := func(mode string) *engine {
		e := testEngine(mode, false)
		e.lr = &localrepClient{base: srv.URL, http: srv.Client()}
		return e
	}
	clip := func(img *Image, which string) map[string]any {
		for _, o := range img.Objects {
			if o.ID == lrObjectID(lrClips, "configs-2026-10-24-1-"+which, "") {
				return o.State
			}
		}
		t.Fatalf("no clip %s in %+v", which, img.Objects)
		return nil
	}

	img, err := engine("read").runSource(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=would-change exists=would-change exists=would-change exists=would-change exists=would-change rendered=would-change" || fake.pollyReqs != 0 || len(fake.comps) != 0 {
		t.Fatalf("read: %s, %d clips asked for, compositions %v", got, fake.pollyReqs, fake.comps)
	}

	if img, err = engine("assert").runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if got := outcomes(img); got != "exists=changed exists=changed exists=changed exists=changed exists=changed rendered=changed" {
		t.Errorf("assert: %s", got)
	}
	made := roundTrip(t, img)
	if fake.pollyReqs != 4 || len(fake.polly) != 3 {
		t.Errorf("%d clips asked for, %d spoken; want 4 and 3 (the last reused)", fake.pollyReqs, len(fake.polly))
	}
	// Each is played on the node as it's made, in order (Revision B).
	if got := strings.Join(fake.played, " "); got != "configs-2026-10-24-1-default configs-2026-10-24-1-neural configs-2026-10-24-1-british configs-2026-10-24-1-default-again" {
		t.Errorf("played %q", got)
	}
	for i, c := range fake.clips {
		if want := []string{"default", "neural", "british"}[i]; c["config"] != want {
			t.Errorf("clip %d config %v, want %s", i, c["config"], want)
		}
	}
	if !strings.HasPrefix(fake.polly[0], "This is fake-node, speaking in the [[config]] configuration") || !strings.Contains(fake.polly[0], "cost about [[cost]]") {
		t.Errorf("the default clip's text: %q", fake.polly[0])
	}
	def, again := clip(img, "default"), clip(img, "default-again")
	if again["config"] != "default" || again["uses"] != 2 || again["exists"] != true || again["file_id"] == def["file_id"] {
		t.Errorf("default-again: %+v (default %+v)", again, def)
	}
	neural := clip(img, "neural")
	if neural["config"] != "neural" || neural["region"] != "us-east-1" || neural["cost"] == nil || neural["spoken_text"] == nil {
		t.Errorf("neural: %+v", neural)
	}
	if !strings.Contains(fake.polly[1], "asked for in [[region]]") {
		t.Errorf("the neural clip doesn't say its region: %q", fake.polly[1])
	}

	// The four clips, in order, make one audio-only composition, rendered.
	comp := fake.comps["configs-2026-10-24-1"]
	if comp == nil {
		t.Fatalf("compositions %+v", fake.comps)
	}
	var ids []string
	for _, a := range comp["audio"].([]any) {
		if a := a.(map[string]any); a["kind"] == "media" {
			ids = append(ids, a["file_id"].(string))
		}
	}
	want := []any{def["file_id"], neural["file_id"], clip(img, "british")["file_id"], again["file_id"]}
	if comp["output"] != "audio" || len(comp["video"].([]any)) != 0 || fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("composition %+v, media %v, want %v", comp, ids, want)
	}
	if renders, _ := comp["renders"].([]any); len(renders) != 1 {
		t.Errorf("renders %+v", comp["renders"])
	}

	// Asserting again changes nothing.
	if img, err = engine("assert").runSource(ctx, src, nil); err != nil || outcomes(img) != "composition=unchanged rendered=unchanged" || fake.pollyReqs != 4 {
		t.Errorf("a second assert: %v %q, %d clips asked for", err, outcomes(img), fake.pollyReqs)
	}

	// Deleting with the assert's image removes all four from the files tab,
	// and the composition.
	if img, err = engine("delete").runSource(ctx, src, made); err != nil || outcomes(img) != "exists=changed exists=changed exists=changed exists=changed exists=changed" {
		t.Fatalf("delete: %v %s", err, outcomes(img))
	}
	if len(fake.comps) != 0 {
		t.Errorf("compositions after delete: %v", fake.comps)
	}
	for _, fl := range fake.files {
		if strings.HasPrefix(fl["name"].(string), "polly-configs-") {
			t.Errorf("%s is still there", fl["name"])
		}
	}

	// play=false makes another take's clips without playing them.
	e := engine("assert")
	e.vars = map[string]string{"take": "2", "play": "false"}
	if img, err = e.runSource(ctx, src, nil); err != nil || fake.pollyReqs != 8 || len(fake.played) != 4 {
		t.Errorf("take 2, play=false: %v, %d clips asked for, played %v", err, fake.pollyReqs, fake.played)
	}
}

// TestLocalrepSequenceFrom checks a sequence the node holds in another
// version: held as ever, but replaced with from.source "*" (Revision B --
// take 2 of the condoc-lifecycle demo ran take 1's steps).
func TestLocalrepSequenceFrom(t *testing.T) {
	fake := newFakeLR()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	fake.sequences["dtt-robot-hello"] = "an-older-version"
	doc := func(from string) string {
		return "localrep.control.sequences:\n  select:\n    id: dtt-robot-hello\n  assert:\n    source: |\n      format: lr-control-v1\n      sequences:\n        - id: dtt-robot-hello\n" + from
	}
	run := func(src string) (*Image, error) {
		e := testEngine("assert", false)
		e.lr = &localrepClient{base: srv.URL, http: srv.Client()}
		return e.runSource(context.Background(), src, nil)
	}
	if img, err := run(doc("")); err != nil || outcomes(img) != "definition=held" || fake.sequences["dtt-robot-hello"] != "an-older-version" {
		t.Errorf("without from: %v %s %v", err, outcomes(img), fake.sequences)
	}
	if img, err := run(doc("    from.source: \"*\"\n")); err != nil || outcomes(img) != "definition=changed" || fake.sequences["dtt-robot-hello"] == "an-older-version" {
		t.Errorf("from.source *: %v %s %v", err, outcomes(img), fake.sequences)
	}
	if img, err := run(doc("    from.source: \"*\"\n")); err != nil || outcomes(img) != "definition=unchanged" {
		t.Errorf("again: %v %s", err, outcomes(img))
	}
	e := testEngine("assert", false)
	parsed, err := parseDocument("localrep.control.runs:\n  select:\n    sequence: s\n    key: k\n  assert:\n    wait: 60\n    from.wait: \"*\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.check(parsed); err == nil || !strings.Contains(err.Error(), "takes no from.wait") {
		t.Errorf("from on another field: %v", err)
	}
}

// TestLocalrepRunWaitsOutUnreachable checks that waiting for a run rides
// out agent-coordinator answering 502 for its node for a while, and gives up
// once that goes on past lrLostGrace (Revision C -- take 3 of the
// condoc-lifecycle demo failed on the first 502 while its node's
// local-representative reconnected).
func TestLocalrepRunWaitsOutUnreachable(t *testing.T) {
	defer func(grace, every time.Duration) { lrLostGrace, lrRetryEvery = grace, every }(lrLostGrace, lrRetryEvery)
	lrLostGrace, lrRetryEvery = 200*time.Millisecond, time.Millisecond

	fake := newFakeLR()
	fake.sequences["dtt-robot-hello"] = "def"
	var mu sync.Mutex
	lost := 0 // how many more polls of the run answer 502
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cut := r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/control/runs/") && lost != 0
		if cut && lost > 0 {
			lost--
		}
		mu.Unlock()
		if cut {
			http.Error(w, "local-representative on fake-node is not reachable", http.StatusBadGateway)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	run := func(key string, cuts int) (*Image, error) {
		mu.Lock()
		lost = cuts
		mu.Unlock()
		e := testEngine("assert", false)
		e.lr = &localrepClient{base: srv.URL, http: srv.Client()}
		return e.runSource(context.Background(), "localrep.control.runs:\n  select:\n    sequence: dtt-robot-hello\n    key: "+key+"\n  assert:\n    wait: 60\n", nil)
	}

	if img, err := run("k1", 3); err != nil || outcomes(img) != "exists=changed" {
		t.Errorf("three 502s: %v %s", err, outcomes(img))
	}
	if _, err := run("k2", -1); err == nil || !strings.Contains(err.Error(), "is not reachable") || !strings.Contains(err.Error(), "unreachable for over") {
		t.Errorf("502s for good: %v", err)
	}
}

// TestLocalrepCoordinatorAndLabels covers localrep.coordinator (found,
// overridden, not connected, read only) and a run's by_label (Revision Y).
func TestLocalrepCoordinatorAndLabels(t *testing.T) {
	fake := newFakeLR()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	engine := func(mode string) *engine {
		e := testEngine(mode, false)
		e.lr = &localrepClient{base: srv.URL, http: srv.Client()}
		e.docDir = "examples"
		return e
	}
	src := `localrep.coordinator:
  alias: ac

localrep.control.sequences:
  select:
    id: dtt-robot-hello
  assert:
    source_file: resources/robot-hello.control.yaml

localrep.control.runs:
  select:
    sequence: dtt-robot-hello
    key: labelled
  alias: run
  assert:
    controls:
      message: ${{ac.url}}/host/x/condocv2/
      labels: first second

localrep.polly.clips:
  select:
    name: labelled
  assert:
    text: ${{run.by_label.second.file_id}}
`
	// Not connected to agent-coordinator: no URL to give, so it fails.
	if _, err := engine("read").runSource(ctx, src, nil); err == nil || !strings.Contains(err.Error(), "can't tell agent-coordinator's URL") {
		t.Fatalf("not connected: %v", err)
	}

	fake.acURL = "http://ac.lan:8080"
	if _, err := engine("assert").runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if len(fake.runs) != 1 || fake.runs[0]["controls"].(map[string]string)["message"] != "http://ac.lan:8080/host/x/condocv2/" {
		t.Fatalf("runs %+v", fake.runs)
	}
	if len(fake.polly) != 1 || fake.polly[0] != "r1_1_second.webm" {
		t.Errorf("by_label.second: polly %q", fake.polly)
	}

	// select.url, when given, is the url.
	img, err := engine("read").runSource(ctx, "localrep.coordinator:\n  select:\n    url: https://other.lan:9443/\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st := img.Objects[0].State; st["url"] != "https://other.lan:9443" || st["found_url"] != "http://ac.lan:8080" || st["overridden"] != true {
		t.Errorf("override: %+v", st)
	}
	if _, err := engine("read").runSource(ctx, "localrep.coordinator:\n  select:\n    url: ftp://x\n", nil); err == nil {
		t.Error("accepted an ftp url")
	}
	// It's only read.
	if _, err := engine("assert").runSource(ctx, "localrep.coordinator:\n  assert:\n    url: x\n", nil); err == nil || !strings.Contains(err.Error(), "only read") {
		t.Errorf("an assert on the coordinator: %v", err)
	}
}

// TestCondocLifecycleDemo checks condocv2's demo document
// (condocv2/docs/demos) parses and checks, as the declare tool reads it,
// and that a single-provider tool refuses it.
func TestCondocLifecycleDemo(t *testing.T) {
	data, err := os.ReadFile(filepath.FromSlash("../condocv2/docs/demos/condoc-lifecycle.dtt.yaml"))
	if err != nil {
		t.Skipf("the demo isn't beside dtt here: %v", err)
	}
	check := func(only string, vars map[string]string) error {
		doc, err := parseDocument(string(data))
		if err != nil {
			t.Fatal(err)
		}
		e := testEngine("assert", false)
		e.only, e.vars = only, vars
		return e.check(doc)
	}
	if err := check("", nil); err == nil || !strings.Contains(err.Error(), "variable node has no default") {
		t.Errorf("without node: %v", err)
	}
	if err := check("", map[string]string{"node": "other-node", "take": "2"}); err != nil {
		t.Errorf("declare: %v", err)
	}
	if err := check("localrep", map[string]string{"node": "other-node"}); err == nil || !strings.Contains(err.Error(), "needs the github tool") {
		t.Errorf("the localrep tool: %v", err)
	}
}

// TestCondocLifecycleExample checks the examples/compositions/ copy of that demo: it
// needs both node and coordinator_url, checks once they're given, and
// imports the same control sequence the demo does.
func TestCondocLifecycleExample(t *testing.T) {
	src := readExample(t, "declare-condoc-lifecycle.dtt.yaml")
	check := func(vars map[string]string) error {
		doc, err := parseDocument(src)
		if err != nil {
			t.Fatal(err)
		}
		e := testEngine("assert", false)
		e.vars = vars
		return e.check(doc)
	}
	if err := check(map[string]string{"node": "other-node"}); err == nil || !strings.Contains(err.Error(), "variable coordinator_url has no default") {
		t.Errorf("without coordinator_url: %v", err)
	}
	if err := check(map[string]string{"coordinator_url": "http://ac.lan:8080"}); err == nil || !strings.Contains(err.Error(), "variable node has no default") {
		t.Errorf("without node: %v", err)
	}
	if err := check(map[string]string{"node": "other-node", "coordinator_url": "http://ac.lan:8080"}); err != nil {
		t.Errorf("both given: %v", err)
	}
	ours, err := examplesFS.ReadFile(compositionsRoot + "/resources/condoc-lifecycle.control.yaml")
	if err != nil {
		t.Fatal(err)
	}
	demo, err := os.ReadFile(filepath.FromSlash("../condocv2/docs/demos/condoc-lifecycle.control.yaml"))
	if err != nil {
		t.Skipf("the demo isn't beside dtt here: %v", err)
	}
	if string(ours) != string(demo) {
		t.Error("examples/compositions/resources/condoc-lifecycle.control.yaml differs from condocv2/docs/demos/condoc-lifecycle.control.yaml")
	}
}

// TestCondocLifecycleTargets checks that no action's or the sequence's name
// or description holds text the robot looks for on screen: the robot reads
// the whole screen, so the control tab showing the sequence could be read
// as condocv2. Texts are compared as the robot does (ianar's normalizeText:
// lower case, letters and digits only); an exact match needs a whole line.
func TestCondocLifecycleTargets(t *testing.T) {
	src, err := examplesFS.ReadFile(compositionsRoot + "/resources/condoc-lifecycle.control.yaml")
	if err != nil {
		t.Fatal(err)
	}
	norm := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, s)
	}
	op := regexp.MustCompile(`op: robot\.[a-z-]+, text: "([^"]*)", exact: "(true|false)"`)
	type target struct {
		text  string
		exact bool
	}
	var targets []target
	for _, m := range op.FindAllStringSubmatch(string(src), -1) {
		if !strings.Contains(m[1], "{{") {
			targets = append(targets, target{norm(m[1]), m[2] == "true"})
		}
	}
	if len(targets) < 10 {
		t.Fatalf("found only %d robot texts -- has the op format changed?", len(targets))
	}
	for i, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "description:") && !strings.HasPrefix(line, "    name:") {
			continue
		}
		_, value, _ := strings.Cut(trimmed, ":")
		got := norm(value)
		for _, tg := range targets {
			if (tg.exact && got == tg.text) || (!tg.exact && strings.Contains(got, tg.text)) {
				t.Errorf("line %d holds the robot's %q: %s", i+1, tg.text, trimmed)
			}
		}
	}
}

func TestDocumentVariables(t *testing.T) {
	dir := t.TempDir()
	src := "variables:\n  name:\n    help: whose directory\n  greeting: hello\n\nfilesystem.items:\n  select:\n    abspath: " + dir + "/${{var.name}}\n  assert:\n    items:\n      greeting.txt:\n        content: ${{var.greeting}}, ${{var.name}}\n"
	ctx := context.Background()
	e := testEngine("assert", false)
	if _, err := e.runSource(ctx, src, nil); err == nil || !strings.Contains(err.Error(), "variable name has no default") {
		t.Fatalf("a required variable missing: %v", err)
	}
	e = testEngine("assert", false)
	e.vars, _ = parseVarValues("name=ada\n# a comment\n")
	if _, err := e.runSource(ctx, src, nil); err != nil {
		t.Fatal(err)
	}
	if b, err := readFileOrStdin(dir+"/ada/greeting.txt", nil); err != nil || b != "hello, ada" {
		t.Errorf("ada/greeting.txt: %q %v", b, err)
	}
	if vars, err := parseVarValues(`{"name": "bob", "n": 2}`); err != nil || vars["name"] != "bob" || vars["n"] != "2" {
		t.Errorf("JSON variables: %v %v", vars, err)
	}
	for _, bad := range []string{
		"variables:\n  1x: a\ntimestamp:\n  select: now\n",
		"variables:\n  a: ${{timestamp.unix}}\ntimestamp:\n  select: now\n",
		"variables:\n  a: b\nvariables:\n  c: d\n",
		"timestamp:\n  select: now\n  alias: var\n",
	} {
		doc, err := parseDocument(bad)
		if err == nil {
			err = testEngine("read", false).check(doc)
		}
		if err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}
