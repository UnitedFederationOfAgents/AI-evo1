package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const helloDoc = `# Hello.
#
# Two nodes.

diagrammographer.diagrams:
  select:
    name: hello
  assert:
    title: Hello
    nodes:
      a:
        at: [40, 70]
      b:
        label: B node
        shape: round
        color: "#2d4a6b"
        at: [300, 70]
        size: [160, 60]
    edges:
      - from: a
        to: b
        label: says hello
    steps:
      - caption: A says hello.
        highlight: [a, a->b]
      - highlight: [b]
`

func TestParseDiagram(t *testing.T) {
	d, err := parseDiagram(helloDoc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "hello" || d.Title != "Hello" || d.Comment != "Hello.\n\nTwo nodes." {
		t.Fatalf("header: %+v", d)
	}
	// size left out: the nodes' extent plus the margin.
	if d.Width != 300+160+sizeMargin || d.Height != 70+60+sizeMargin {
		t.Fatalf("size %dx%d", d.Width, d.Height)
	}
	wantA := Node{ID: "a", Label: "a", Shape: "rect", X: 40, Y: 70, W: defaultNodeW, H: defaultNodeH}
	if !reflect.DeepEqual(d.Nodes[0], wantA) {
		t.Fatalf("node a: %+v", d.Nodes[0])
	}
	if d.Nodes[1].Label != "B node" || d.Nodes[1].Shape != "round" || d.Nodes[1].Color != "#2d4a6b" || d.Nodes[1].W != 160 {
		t.Fatalf("node b: %+v", d.Nodes[1])
	}
	if len(d.Edges) != 1 || d.Edges[0] != (Edge{From: "a", To: "b", Label: "says hello"}) {
		t.Fatalf("edges: %+v", d.Edges)
	}
	if len(d.Steps) != 2 || !reflect.DeepEqual(d.Steps[0].Highlight, []string{"a", "a->b"}) || d.Steps[1].Caption != "" {
		t.Fatalf("steps: %+v", d.Steps)
	}
}

func TestDTTRoundTrip(t *testing.T) {
	d, err := parseDiagram(helloDoc)
	if err != nil {
		t.Fatal(err)
	}
	out := d.DTT()
	again, err := parseDiagram(out)
	if err != nil {
		t.Fatalf("reading back:\n%s\n%v", out, err)
	}
	if !reflect.DeepEqual(d, again) {
		t.Fatalf("round trip changed the diagram:\n%s\n%+v\n%+v", out, d, again)
	}
	if !strings.HasPrefix(out, "# Hello.\n#\n# Two nodes.\n\ndiagrammographer.diagrams:\n") {
		t.Fatalf("comment block not kept:\n%s", out)
	}
}

func TestParseDiagramErrors(t *testing.T) {
	cases := map[string]string{
		"other statement": "github.users.x:\n  read: '*'\n",
		"no name":         "diagrammographer.diagrams:\n  assert:\n    title: x\n",
		"bad name":        "diagrammographer.diagrams:\n  select:\n    name: Bad Name\n  assert:\n    title: x\n",
		"unknown key":     "diagrammographer.diagrams:\n  select:\n    name: x\n  assert:\n    colour: red\n",
		"no at":           "diagrammographer.diagrams:\n  select:\n    name: x\n  assert:\n    nodes:\n      a:\n        label: A\n",
		"bad shape":       "diagrammographer.diagrams:\n  select:\n    name: x\n  assert:\n    nodes:\n      a:\n        shape: hexagon\n        at: [0, 0]\n",
		"dangling edge":   "diagrammographer.diagrams:\n  select:\n    name: x\n  assert:\n    nodes:\n      a:\n        at: [0, 0]\n    edges:\n      - from: a\n        to: b\n",
		"bad highlight":   "diagrammographer.diagrams:\n  select:\n    name: x\n  assert:\n    nodes:\n      a:\n        at: [0, 0]\n    steps:\n      - highlight: [a->a]\n",
		"reserved id":     "diagrammographer.diagrams:\n  select:\n    name: x\n  assert:\n    nodes:\n      dg_x:\n        at: [0, 0]\n",
	}
	for what, doc := range cases {
		if _, err := parseDiagram(doc); err == nil {
			t.Errorf("%s: no error", what)
		}
	}
}

func TestExamples(t *testing.T) {
	exs, err := loadExamples()
	if err != nil {
		t.Fatal(err)
	}
	if len(exs) < 4 {
		t.Fatalf("%d examples", len(exs))
	}
	dynamic, static := 0, 0
	for _, ex := range exs {
		if len(ex.diagram.Steps) > 0 {
			dynamic++
		} else {
			static++
		}
		// Each example's document, written afresh, reads back the same.
		again, err := parseDiagram(ex.diagram.DTT())
		if err != nil || !reflect.DeepEqual(ex.diagram, again) {
			t.Errorf("%s: round trip: %v", ex.diagram.Name, err)
		}
	}
	if dynamic < 2 || static < 2 {
		t.Errorf("%d dynamic and %d static examples, want at least 2 of each", dynamic, static)
	}
}

func TestD2(t *testing.T) {
	d, err := parseDiagram(helloDoc)
	if err != nil {
		t.Fatal(err)
	}
	out := d.D2()
	for _, want := range []string{
		"a: \"a\" {\n  shape: rectangle\n}",
		"b: \"B node\" {\n  shape: rectangle\n  style.border-radius: 8\n  style.fill: \"#2d4a6b\"\n}",
		"a -> b: \"says hello\"",
		"scenarios: {\n  1: {\n    a.style.stroke: \"#e0a060\"",
		"(a -> b)[0].style.stroke: \"#e0a060\"",
		"dg_caption: \"A says hello.\" {",
		"  2: {\n    b.style.stroke:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("D2 lacks %q:\n%s", want, out)
		}
	}

	d.Steps = nil
	if strings.Contains(d.D2(), "scenarios") {
		t.Error("static diagram has scenarios")
	}
}

func TestDiagramsAPI(t *testing.T) {
	s := newServer()
	h := s.setupRoutes(true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/diagrams", nil))
	var list []DiagramSummary
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != len(s.examples) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/diagrams/"+list[0].Name, nil))
	var view DiagramView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &view) != nil || view.Diagram.Name != list[0].Name || view.DTT == "" || view.D2 == "" {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/diagrams/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", rec.Code)
	}
}

func TestConvertAPI(t *testing.T) {
	h := newServer().setupRoutes(true)
	post := func(body interface{}) (*httptest.ResponseRecorder, DiagramView) {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/convert", bytes.NewReader(b)))
		var v DiagramView
		json.Unmarshal(rec.Body.Bytes(), &v)
		return rec, v
	}

	rec, v := post(ConvertRequest{DTT: helloDoc})
	if rec.Code != http.StatusOK || v.Diagram.Name != "hello" || v.DTT != helloDoc {
		t.Fatalf("from dtt: %d %s", rec.Code, rec.Body)
	}

	// A canvas edit: move a node, get its document back.
	v.Diagram.Nodes[0].X = 55
	rec, v2 := post(ConvertRequest{Diagram: v.Diagram})
	if rec.Code != http.StatusOK || !strings.Contains(v2.DTT, "at: [55, 70]") {
		t.Fatalf("from diagram: %d %s", rec.Code, rec.Body)
	}

	rec, _ = post(ConvertRequest{DTT: "diagrammographer.diagrams:\n  select:\n    name: x\n"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "assert is required") {
		t.Fatalf("bad dtt: %d %s", rec.Code, rec.Body)
	}
}

func TestEncodeGIF(t *testing.T) {
	frame := func(c color.Color) string {
		img := image.NewRGBA(image.Rect(0, 0, 8, 6))
		for x := 0; x < 8; x++ {
			for y := 0; y < 6; y++ {
				img.Set(x, y, c)
			}
		}
		var buf bytes.Buffer
		png.Encode(&buf, img)
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	}
	out, err := encodeGIF(GIFRequest{Frames: []string{frame(color.Black), frame(color.White)}, DelayMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	g, err := gif.DecodeAll(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != 2 || g.Delay[0] != 100 || g.Image[0].Bounds().Dx() != 8 {
		t.Fatalf("frames %d, delay %v", len(g.Image), g.Delay)
	}

	if _, err := encodeGIF(GIFRequest{Frames: []string{"not a data url"}}); err == nil {
		t.Error("bad frame accepted")
	}
}
