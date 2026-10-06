package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file is IANAR's "Save to file" (condocs/initialRobotImpls/
// Step2Prompt.md, Revision D): any capture, clip, screen inspection or
// sequence run can be uploaded into local-representative's files area -- its
// host-cache, which agent-coordinator's files tab also lists for this host --
// the same way the-conversationalist's "Save to File" does (see its
// transcribe.go's saveTranscript/uploadToFiles).
//
// Every result IANAR sends its frontend is kept here, and the result message
// carries its artifact id, so saving it later is just "save-artifact" {id}:
// the frontend never sends multi-megabyte recordings back. The file is only
// built when it's saved. A single image or video is uploaded as-is; anything
// with more parts (a frame-sampled clip, an inspection, a sequence run) is a
// .zip holding a plain-text report.txt alongside its images and recording.

const (
	maxArtifacts  = 16              // results kept for saving; older ones are dropped
	uploadTimeout = 2 * time.Minute // per upload to local-representative
)

// artifact is a result that can be saved: the filename to upload it as, and
// how to produce its contents.
type artifact struct {
	name  string
	build func() ([]byte, error)
}

// artifactStore keeps the last maxArtifacts results by id.
type artifactStore struct {
	mu    sync.Mutex
	next  int
	order []string
	items map[string]artifact
}

func newArtifactStore() *artifactStore {
	return &artifactStore{items: make(map[string]artifact)}
}

// keep stores a and returns its id.
func (st *artifactStore) keep(a artifact) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.next++
	id := strconv.Itoa(st.next)
	st.items[id] = a
	st.order = append(st.order, id)
	for len(st.order) > maxArtifacts {
		delete(st.items, st.order[0])
		st.order = st.order[1:]
	}
	return id
}

func (st *artifactStore) get(id string) (artifact, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	a, ok := st.items[id]
	return a, ok
}

// SaveResultMsg is the "save-result" WebSocket payload reporting whether an
// artifact made it into local-representative's files area.
type SaveResultMsg struct {
	ArtifactID string `json:"artifact_id"`
	Success    bool   `json:"success"`
	Name       string `json:"name,omitempty"`    // name as uploaded
	FileID     string `json:"file_id,omitempty"` // local-representative's id for it
	Error      string `json:"error,omitempty"`
}

// handleSaveArtifact uploads artifact id into local-representative's files
// area and reports the outcome as a "save-result".
func (s *Server) handleSaveArtifact(c *wsClient, id string) {
	uploaded, err := s.saveArtifact(id)
	if err != nil {
		s.sendToClient(c, "save-result", SaveResultMsg{ArtifactID: id, Error: err.Error()})
		return
	}
	s.sendToClient(c, "save-result", SaveResultMsg{ArtifactID: id, Success: true, Name: uploaded.Name, FileID: uploaded.ID})
}

// saveArtifact builds artifact id and uploads it into local-representative's
// files area.
func (s *Server) saveArtifact(id string) (*uploadedFile, error) {
	a, ok := s.artifacts.get(id)
	if !ok {
		return nil, fmt.Errorf("this result is no longer kept for saving -- only the last %d are", maxArtifacts)
	}
	base, err := s.lrBaseURL()
	if err != nil {
		return nil, err
	}
	data, err := a.build()
	if err != nil {
		return nil, fmt.Errorf("preparing %s: %v", a.name, err)
	}
	return uploadToFiles(base, a.name, data)
}

// lrBaseURL is local-representative's HTTP base URL, learned over IANAR's
// representable link to it.
func (s *Server) lrBaseURL() (string, error) {
	s.reprMu.Lock()
	client := s.reprClient
	host := s.reprHost
	s.reprMu.Unlock()
	if client == nil {
		return "", fmt.Errorf("not connected to local-representative")
	}
	port := client.PeerHTTPPort()
	if port == "" {
		return "", fmt.Errorf("local-representative has not disclosed its HTTP port")
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// uploadedFile mirrors the subset of local-representative's FileInfo JSON
// (local-representative/files.go) this caller reads back.
type uploadedFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// uploadToFiles POSTs one file into local-representative's files area
// exactly as its own upload widget does: multipart/form-data with a single
// "file" field (see local-representative/files.go's handleFileUpload, and
// the-conversationalist/transcribe.go's function of the same name).
// Overridable in tests.
var uploadToFiles = func(base, filename string, content []byte) (*uploadedFile, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(content); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, base+"/api/files", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := (&http.Client{Timeout: uploadTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("uploading %s: %w", filename, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("uploading %s: unexpected status %d", filename, resp.StatusCode)
	}

	var result struct {
		Files []uploadedFile `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("uploading %s: %w", filename, err)
	}
	if len(result.Files) == 0 {
		return nil, fmt.Errorf("uploading %s: local-representative returned no file", filename)
	}
	return &result.Files[0], nil
}

// ---- Building the files ----

// stamp names a saved file after when its result arrived.
func stamp() string { return clock().Format("2006-01-02T15-04-05") }

// decodeDataURL splits a base64 data: URL into its MIME type and bytes.
func decodeDataURL(u string) (mimeType string, data []byte, err error) {
	head, b64, ok := strings.Cut(u, ",")
	if !ok || !strings.HasPrefix(head, "data:") || !strings.HasSuffix(head, ";base64") {
		return "", nil, fmt.Errorf("not a base64 data: URL")
	}
	data, err = base64.StdEncoding.DecodeString(b64)
	return strings.TrimSuffix(strings.TrimPrefix(head, "data:"), ";base64"), data, err
}

// extForMIME is the file extension for a capture or recording's MIME type.
func extForMIME(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "video/mp4":
		return ".mp4"
	case "video/x-matroska":
		return ".mkv"
	case "video/webm":
		return ".webm"
	default:
		return ".bin"
	}
}

// singleFile is an artifact holding one data: URL as-is, named base plus
// the extension its MIME type calls for.
func singleFile(base, dataURL string) artifact {
	mimeType, _, _ := strings.Cut(strings.TrimPrefix(dataURL, "data:"), ";")
	return artifact{
		name: base + extForMIME(mimeType),
		build: func() ([]byte, error) {
			_, data, err := decodeDataURL(dataURL)
			return data, err
		},
	}
}

// zipFile is one file in a saved .zip.
type zipFile struct {
	name string
	data []byte
}

// buildZip packs files into a .zip: text compressed, images and video (which
// already are) stored.
func buildZip(files []zipFile) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		method := zip.Store
		if strings.HasSuffix(f.name, ".txt") || strings.HasSuffix(f.name, ".json") {
			method = zip.Deflate
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: method, Modified: time.Now()})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// clipFiles returns a clip's files, under dir, and a line describing them
// for a report: its video, or each sampled frame named for its index and
// time into the clip.
func clipFiles(dir string, clip ClipResultMsg) ([]zipFile, string, error) {
	if clip.VideoURL != "" {
		mimeType, data, err := decodeDataURL(clip.VideoURL)
		if err != nil {
			return nil, "", fmt.Errorf("recording: %w", err)
		}
		name := dir + "recording" + extForMIME(mimeType)
		return []zipFile{{name, data}}, name, nil
	}
	var files []zipFile
	for i, f := range clip.Frames {
		_, data, err := decodeDataURL(f.ImageURL)
		if err != nil {
			return nil, "", fmt.Errorf("frame %d: %w", i+1, err)
		}
		files = append(files, zipFile{fmt.Sprintf("%sframe-%03d-%06dms.jpg", dir, i+1, f.AtMs), data})
	}
	return files, fmt.Sprintf("%d frames in %s (frame-NNN-<ms into the clip>ms.jpg)", len(files), dir), nil
}

func seconds(ms int64) string { return fmt.Sprintf("%.1fs", float64(ms)/1000) }

// captureArtifact is a successful capture, saved as-is.
func captureArtifact(source, dataURL string) artifact {
	return singleFile(fmt.Sprintf("ianar-capture-%s-%s", source, stamp()), dataURL)
}

// clipArtifact is a successful Native Clip: a compositor recording as-is,
// or a .zip of its sampled frames.
func clipArtifact(clip ClipResultMsg) artifact {
	return namedClipArtifact("ianar-clip-"+stamp(), clip)
}

// namedClipArtifact is clipArtifact under the file name base (plus its
// extension).
func namedClipArtifact(base string, clip ClipResultMsg) artifact {
	if clip.VideoURL != "" {
		return singleFile(base, clip.VideoURL)
	}
	return artifact{
		name: base + ".zip",
		build: func() ([]byte, error) {
			files, desc, err := clipFiles("frames/", clip)
			if err != nil {
				return nil, err
			}
			report := fmt.Sprintf("IANAR native clip\nrecorded via: %s\nduration: %s\n%s\n", clip.Via, seconds(clip.DurationMs), desc)
			return buildZip(append([]zipFile{{"report.txt", []byte(report)}}, files...))
		},
	}
}

// inspectArtifact is a successful screen inspection: a .zip of the capture,
// the capture with the lines read off it boxed (as InspectView draws them),
// the reading as text, and the reading as JSON.
func inspectArtifact(res InspectResultMsg) artifact {
	return artifact{
		name: "ianar-inspect-" + stamp() + ".zip",
		build: func() ([]byte, error) {
			_, shot, err := decodeDataURL(res.ImageURL)
			if err != nil {
				return nil, fmt.Errorf("capture: %w", err)
			}
			files := []zipFile{{"report.txt", []byte(inspectReport(res))}, {"capture.jpg", shot}}
			if boxed, err := boxInspection(shot, res); err == nil {
				files = append(files, zipFile{"capture-boxed.png", boxed})
			} else {
				files[0].data = append(files[0].data, fmt.Sprintf("\n(couldn't draw capture-boxed.png: %v)\n", err)...)
			}
			reading := res
			reading.ImageURL = ""
			if js, err := json.MarshalIndent(reading, "", "  "); err == nil {
				files = append(files, zipFile{"reading.json", js})
			}
			return buildZip(files)
		},
	}
}

func inspectReport(res InspectResultMsg) string {
	var b strings.Builder
	fmt.Fprintf(&b, "IANAR screen inspection\ncapture: %dx%d via %s (capture.jpg may be downscaled)\nread in: %s\n",
		res.Width, res.Height, res.CaptureVia, seconds(res.DurationMs))
	if res.Find != "" {
		fmt.Fprintf(&b, "looking for: %q -- %d exact line(s) (green), %d partial (amber)\n", res.Find, len(res.Matches), len(res.Partial))
		for _, l := range res.Matches {
			fmt.Fprintf(&b, "  exact   (%d, %d) %dx%d %q\n", l.X, l.Y, l.W, l.H, l.Text)
		}
		for _, l := range res.Partial {
			fmt.Fprintf(&b, "  partial (%d, %d) %dx%d %q\n", l.X, l.Y, l.W, l.H, l.Text)
		}
	}
	fmt.Fprintf(&b, "\n%d lines read, at (x, y) in capture pixels:\n", len(res.Lines))
	for _, l := range res.Lines {
		fmt.Fprintf(&b, "  (%d, %d) %s\n", l.X, l.Y, l.Text)
	}
	return b.String()
}

// boxInspection draws res's lines onto its (possibly downscaled) capture:
// matches green, partial matches amber, the rest grey.
func boxInspection(shot []byte, res InspectResultMsg) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(shot))
	if err != nil {
		return nil, err
	}
	if res.Width <= 0 {
		return nil, fmt.Errorf("inspection has no capture size")
	}
	scale := float64(img.Bounds().Dx()) / float64(res.Width)
	rect := func(l OCRLine) image.Rectangle {
		return image.Rect(int(float64(l.X)*scale), int(float64(l.Y)*scale),
			int(float64(l.X+l.W)*scale), int(float64(l.Y+l.H)*scale))
	}
	flagged := make(map[[2]int]bool)
	boxes := make(map[color.RGBA][]image.Rectangle)
	for _, l := range res.Matches {
		flagged[[2]int{l.X, l.Y}] = true
		boxes[matchColor] = append(boxes[matchColor], rect(l))
	}
	for _, l := range res.Partial {
		flagged[[2]int{l.X, l.Y}] = true
		boxes[otherColor] = append(boxes[otherColor], rect(l))
	}
	lineColor := color.RGBA{0x90, 0x90, 0x90, 0xff}
	for _, l := range res.Lines {
		if !flagged[[2]int{l.X, l.Y}] {
			boxes[lineColor] = append(boxes[lineColor], rect(l))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, annotate(img, boxes)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sequenceArtifact is a finished sequence run, successful or not: a .zip of
// a report of every step, what each step saw, and the run's recording.
func sequenceArtifact(def SequenceDef, res SequenceResultMsg) artifact {
	return artifact{
		name: fmt.Sprintf("ianar-sequence-%s-%s.zip", def.ID, stamp()),
		build: func() ([]byte, error) {
			var files []zipFile
			var b strings.Builder
			fmt.Fprintf(&b, "IANAR sequence run: %s (%s)\n", def.Name, def.ID)
			if res.Success {
				fmt.Fprintf(&b, "result: succeeded in %s\n", seconds(res.DurationMs))
			} else {
				fmt.Fprintf(&b, "result: FAILED after %s: %s\n", seconds(res.DurationMs), res.Error)
			}
			if res.KeyboardVia != "" {
				fmt.Fprintf(&b, "keyboard via: %s\n", res.KeyboardVia)
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(&b, "warning: %s\n", w)
			}
			for _, o := range res.Outputs {
				if o.Label != "" {
					fmt.Fprintf(&b, "printed: %s: %s\n", o.Label, o.Value)
				} else {
					fmt.Fprintf(&b, "printed: %s\n", o.Value)
				}
			}
			switch rec := res.Recording; {
			case rec == nil:
				b.WriteString("recording: none\n")
			case !rec.Success:
				fmt.Fprintf(&b, "recording: failed: %s\n", rec.Error)
			default:
				recFiles, desc, err := clipFiles("recording/", *rec)
				if err != nil {
					fmt.Fprintf(&b, "recording: via %s, %s, but couldn't be saved: %v\n", rec.Via, seconds(rec.DurationMs), err)
					break
				}
				files = append(files, recFiles...)
				fmt.Fprintf(&b, "recording: via %s, %s -- %s\n", rec.Via, seconds(rec.DurationMs), desc)
			}

			b.WriteString("\nsteps:\n")
			for i, st := range res.Steps {
				label := fmt.Sprintf("step %d", i+1)
				var detail []string
				if i < len(def.Steps) {
					label, detail = def.Steps[i].Label, def.Steps[i].Detail
				}
				fmt.Fprintf(&b, "\n%d. [%s] %s", i+1, st.Status, label)
				if st.Status != "skipped" {
					fmt.Fprintf(&b, " (%s)", seconds(st.DurationMs))
				}
				b.WriteString("\n")
				for _, d := range detail {
					fmt.Fprintf(&b, "     - %s\n", d)
				}
				if st.Message != "" {
					fmt.Fprintf(&b, "   %s\n", st.Message)
				}
				if st.ImageURL != "" {
					mimeType, data, err := decodeDataURL(st.ImageURL)
					if err != nil {
						fmt.Fprintf(&b, "   (couldn't save what this step saw: %v)\n", err)
						continue
					}
					name := fmt.Sprintf("step-%d%s", i+1, extForMIME(mimeType))
					files = append(files, zipFile{name, data})
					fmt.Fprintf(&b, "   saw: %s\n", name)
				}
			}
			return buildZip(append([]zipFile{{"report.txt", []byte(b.String())}}, files...))
		},
	}
}
