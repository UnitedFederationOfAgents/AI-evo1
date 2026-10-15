package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// unzip returns a saved .zip's files by name.
func unzip(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("reading zip: %v", err)
	}
	files := make(map[string][]byte)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = b
	}
	return files
}

func TestArtifactStoreDropsOldest(t *testing.T) {
	st := newArtifactStore()
	first := st.keep(artifact{name: "first"})
	var last string
	for i := 0; i < maxArtifacts; i++ {
		last = st.keep(artifact{name: "n" + strconv.Itoa(i)})
	}
	if _, ok := st.get(first); ok {
		t.Errorf("the oldest artifact should have been dropped once more than %d were kept", maxArtifacts)
	}
	if a, ok := st.get(last); !ok || a.name != "n"+strconv.Itoa(maxArtifacts-1) {
		t.Errorf("latest artifact = %+v, %v", a, ok)
	}
}

func TestDecodeDataURL(t *testing.T) {
	mimeType, data, err := decodeDataURL("data:image/png;base64,Zm9v")
	if err != nil || mimeType != "image/png" || string(data) != "foo" {
		t.Errorf("got %q %q %v", mimeType, data, err)
	}
	if _, _, err := decodeDataURL("not a data url"); err == nil {
		t.Errorf("expected an error for a non-data: URL")
	}
}

func TestCaptureArtifactIsTheImageAsIs(t *testing.T) {
	a := captureArtifact("native", "data:image/png;base64,Zm9v")
	if !strings.HasPrefix(a.name, "ianar-capture-native-") || !strings.HasSuffix(a.name, ".png") {
		t.Errorf("name = %q", a.name)
	}
	if data, err := a.build(); err != nil || string(data) != "foo" {
		t.Errorf("build = %q, %v", data, err)
	}
}

func TestClipArtifact(t *testing.T) {
	video := clipArtifact(ClipResultMsg{Success: true, VideoURL: "data:video/webm;base64,Zm9v"})
	if !strings.HasSuffix(video.name, ".webm") {
		t.Errorf("a compositor clip should be saved as its video, got %q", video.name)
	}

	frames := clipArtifact(ClipResultMsg{
		Success:    true,
		Via:        "sampled frames (test)",
		DurationMs: 200,
		Frames: []ClipFrame{
			{ImageURL: "data:image/jpeg;base64,YQ==", AtMs: 0},
			{ImageURL: "data:image/jpeg;base64,Yg==", AtMs: 100},
		},
	})
	if !strings.HasSuffix(frames.name, ".zip") {
		t.Fatalf("a frame-sampled clip should be saved as a .zip, got %q", frames.name)
	}
	data, err := frames.build()
	if err != nil {
		t.Fatal(err)
	}
	files := unzip(t, data)
	if string(files["frames/frame-001-000000ms.jpg"]) != "a" || string(files["frames/frame-002-000100ms.jpg"]) != "b" {
		t.Errorf("unexpected frames: %v", keys(files))
	}
	if !strings.Contains(string(files["report.txt"]), "sampled frames (test)") {
		t.Errorf("report.txt = %q", files["report.txt"])
	}
}

func TestInspectArtifact(t *testing.T) {
	var shot bytes.Buffer
	if err := jpeg.Encode(&shot, litImage(64, 32), nil); err != nil {
		t.Fatal(err)
	}
	res := InspectResultMsg{
		Success:    true,
		ImageURL:   "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(shot.Bytes()),
		Width:      128,
		Height:     64,
		CaptureVia: "test",
		Lines:      []OCRLine{{Text: "federation-command", X: 10, Y: 4, W: 60, H: 8}, {Text: "other", X: 10, Y: 30, W: 20, H: 8}},
		Find:       "federation-command",
		Matches:    []OCRLine{{Text: "federation-command", X: 10, Y: 4, W: 60, H: 8}},
	}
	data, err := inspectArtifact(res).build()
	if err != nil {
		t.Fatal(err)
	}
	files := unzip(t, data)
	for _, name := range []string{"report.txt", "capture.jpg", "capture-boxed.png", "reading.json"} {
		if len(files[name]) == 0 {
			t.Errorf("missing %s (have %v)", name, keys(files))
		}
	}
	if !strings.Contains(string(files["report.txt"]), `exact   (10, 4) 60x8 "federation-command"`) {
		t.Errorf("report.txt = %s", files["report.txt"])
	}
	if strings.Contains(string(files["reading.json"]), "base64") {
		t.Errorf("reading.json shouldn't repeat the image")
	}
}

func TestSequenceArtifactReportsAFailedRun(t *testing.T) {
	stubSequence(t)
	def := fcHelloWorld(t).def()
	res := SequenceResultMsg{
		SequenceID: def.ID,
		FailedStep: 1,
		Error:      "step 2 (" + def.Steps[1].Label + "): boom",
		DurationMs: 1500,
		Steps: []SequenceStepResult{
			{Status: "success", Message: "clicked it", DurationMs: 800, ImageURL: "data:image/jpeg;base64,c2hvdA=="},
			{Status: "error", Message: "boom", DurationMs: 700},
		},
		KeyboardVia: "test",
		Recording:   &ClipResultMsg{Success: true, Via: "test", VideoURL: "data:video/webm;base64,Zm9v", DurationMs: 3000},
	}
	for i := 2; i < len(def.Steps); i++ {
		res.Steps = append(res.Steps, SequenceStepResult{Status: "skipped"})
	}
	a := sequenceArtifact(def, res)
	if !strings.HasPrefix(a.name, "ianar-sequence-fc-hello-world-") || !strings.HasSuffix(a.name, ".zip") {
		t.Errorf("name = %q", a.name)
	}
	data, err := a.build()
	if err != nil {
		t.Fatal(err)
	}
	files := unzip(t, data)
	if string(files["step-1.jpg"]) != "shot" || string(files["recording/recording.webm"]) != "foo" {
		t.Errorf("unexpected files: %v", keys(files))
	}
	report := string(files["report.txt"])
	for _, want := range []string{"result: FAILED", "boom", "[skipped]", "saw: step-1.jpg", def.Steps[0].Detail[0], "recording/recording.webm"} {
		if !strings.Contains(report, want) {
			t.Errorf("report.txt lacks %q:\n%s", want, report)
		}
	}
}

func TestHandleRunSequenceKeepsTheRunForSaving(t *testing.T) {
	stubSequence(t)
	// Nothing on screen: the output check fails, but a failed run is kept
	// for saving too.
	stubScreenLines(t, func(int) []OCRLine { return nil })
	s := newServer()
	c := &wsClient{send: make(chan []byte, 64), done: make(chan struct{})}

	s.handleRunSequenceV2(c, "fc-hello-world", nil)
	decodeSent(t, c, "seq2-started", nil)
	// Steps 1-4 start and succeed, and step 5 starts and fails.
	for i := 0; i < 10; i++ {
		decodeSent(t, c, "seq2-progress", nil)
	}
	var res SequenceResultMsg
	decodeSent(t, c, "seq2-result", &res)
	if res.Success || res.FailedStep != 4 {
		t.Errorf("unexpected result: %+v", res)
	}
	if _, ok := s.artifacts.get(res.ArtifactID); !ok {
		t.Errorf("the run should be kept for saving, got artifact id %q", res.ArtifactID)
	}
}

func TestHandleSaveArtifactNeedsLocalRepresentative(t *testing.T) {
	s := newServer()
	c := &wsClient{send: make(chan []byte, 4), done: make(chan struct{})}
	id := s.artifacts.keep(captureArtifact("native", "data:image/png;base64,Zm9v"))

	s.handleSaveArtifact(c, id)
	var res SaveResultMsg
	decodeSent(t, c, "save-result", &res)
	if res.Success || res.ArtifactID != id || !strings.Contains(res.Error, "not connected") {
		t.Errorf("unexpected result: %+v", res)
	}

	s.handleSaveArtifact(c, "no-such-artifact")
	decodeSent(t, c, "save-result", &res)
	if res.Success || res.Error == "" {
		t.Errorf("an unknown artifact should fail, got %+v", res)
	}
}

func TestUploadToFilesSendsMultipartFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/files" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(f)
		if hdr.Filename != "clip.webm" || string(body) != "foo" {
			http.Error(w, "unexpected file", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"files": []uploadedFile{{ID: "abc_clip.webm", Name: "clip.webm"}}})
	}))
	defer srv.Close()

	got, err := uploadToFiles(srv.URL, "clip.webm", []byte("foo"))
	if err != nil || got.ID != "abc_clip.webm" || got.Name != "clip.webm" {
		t.Errorf("uploadToFiles = %+v, %v", got, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer bad.Close()
	if _, err := uploadToFiles(bad.URL, "clip.webm", []byte("foo")); err == nil {
		t.Errorf("expected an error for a non-200 response")
	}
}

func TestBoxInspectionScalesToTheImage(t *testing.T) {
	var shot bytes.Buffer
	jpeg.Encode(&shot, litImage(50, 50), nil)
	if _, err := boxInspection(shot.Bytes(), InspectResultMsg{Width: 0}); err == nil {
		t.Errorf("expected an error without a capture size")
	}
	out, err := boxInspection(shot.Bytes(), InspectResultMsg{Width: 100, Lines: []OCRLine{{X: 10, Y: 10, W: 40, H: 20}}})
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 50 {
		t.Errorf("boxed image should keep the capture image's size: %v %v", img, err)
	}
}

func keys(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
