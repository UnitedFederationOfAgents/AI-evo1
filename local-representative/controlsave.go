package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This file is the control runner's "save to files" (condocs/
// initialRobotImpls/Step3Prompt.md, Revision J): a finished run -- the
// current one, the only one LR keeps -- can be uploaded into the files tab,
// the way the robot's sequence-v2 runs are (ianar/artifacts.go's
// sequenceArtifact). It's a .zip holding a plain-text report.txt of the run
// (its controls, every step, its values and output), run.json (the run as
// the control tab saw it), and a copy of every recording, screenshot and
// fetched file the run put in the files tab, so the run travels as one file
// even after those expire.
//
// Saving is a library request (op "save-run", id the run's), so it reaches
// LR from its own control tab and through agent-coordinator's relay alike,
// and the reply says what it was saved as. The run then lists the saved
// file (ControlRunMsg.Saved) so every view of it can link to it.

// ControlSavedRun is the files-tab entry a run was saved as.
type ControlSavedRun struct {
	FileID string `json:"file_id"` // GET /api/files/<file_id>
	Name   string `json:"name"`
}

// saveRun uploads run id ("" for whichever is kept) into the files tab and
// notes it on the run.
func (e *controlEngine) saveRun(id string) (FileInfo, error) {
	run := e.runCopy()
	switch {
	case run == nil:
		return FileInfo{}, errors.New("there is no control run to save")
	case id != "" && id != run.ID:
		return FileInfo{}, fmt.Errorf("run %s is no longer kept -- only the most recent run can be saved", id)
	case run.Status == "running":
		return FileInfo{}, errors.New("the run is still going -- save it once it ends")
	}
	data, err := e.s.controlRunZip(*run)
	if err != nil {
		return FileInfo{}, fmt.Errorf("preparing the run's file: %v", err)
	}
	name := fmt.Sprintf("lr-control-%s-%s.zip", run.Sequence, time.Now().Format("2006-01-02T15-04-05"))
	info, err := e.s.saveFileFrom(name, bytes.NewReader(data))
	if err != nil {
		return FileInfo{}, err
	}
	e.s.broadcastFiles()

	e.mu.Lock()
	if e.run != nil && e.run.ID == run.ID {
		e.run.Saved = append(e.run.Saved, ControlSavedRun{FileID: info.ID, Name: info.Name})
	}
	e.mu.Unlock()
	e.broadcast()
	return info, nil
}

// controlRunZip builds the .zip saveRun uploads: report.txt, run.json, and
// the run's files under files/.
func (s *Server) controlRunZip(run ControlRunMsg) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "local-representative control run: %s (%s)\n", run.Name, run.Sequence)
	fmt.Fprintf(&b, "node: %s\nrun: %s\nstarted: %s\n", s.lrName, run.ID, time.UnixMilli(run.StartedAt).Format(time.RFC3339))
	switch run.Status {
	case "success":
		fmt.Fprintf(&b, "result: succeeded in %s\n", formatSeconds(run.DurationMs))
	default:
		fmt.Fprintf(&b, "result: %s after %s: %s\n", strings.ToUpper(run.Status), formatSeconds(run.DurationMs), run.Error)
	}
	if len(run.Controls) > 0 {
		b.WriteString("\ncontrols:\n")
		names := make([]string, 0, len(run.Controls))
		for n := range run.Controls {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "  %s: %s\n", n, run.Controls[n])
		}
	}

	b.WriteString("\nsteps:\n")
	for i, st := range run.Steps {
		fmt.Fprintf(&b, "\n%d. [%s] %s", i+1, st.Status, st.Label)
		if st.Status != "skipped" && st.Status != "pending" {
			fmt.Fprintf(&b, " (%s)", formatSeconds(st.DurationMs))
		}
		b.WriteString("\n")
		if st.Detail != "" {
			fmt.Fprintf(&b, "   %s\n", st.Detail)
		}
		for _, d := range st.Do {
			fmt.Fprintf(&b, "     - %s\n", d)
		}
		if st.Message != "" {
			fmt.Fprintf(&b, "   %s\n", st.Message)
		}
	}

	writeValues := func(head string, vals []ControlValue) {
		if len(vals) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", head)
		for _, v := range vals {
			fmt.Fprintf(&b, "  %s: %s\n", v.Label, v.Value)
		}
	}
	writeValues("output", run.Output)
	writeValues("values", run.Values)
	if run.RecordOverride != "" {
		fmt.Fprintf(&b, "\nrecording overridden in the runner: %s\n", run.RecordOverride)
	}
	if len(run.Records) > 0 {
		b.WriteString("\nrecordings:\n")
		for _, rs := range run.Records {
			fmt.Fprintf(&b, "  %s, %s: %s", rs.Node, rs.steps(), rs.Status)
			if rs.Label != "" {
				fmt.Fprintf(&b, " (%s)", rs.Label)
			}
			if rs.Message != "" {
				fmt.Fprintf(&b, " -- %s", rs.Message)
			}
			b.WriteString("\n")
		}
	}

	var files []zipEntry
	if len(run.Recordings) > 0 {
		b.WriteString("\nfiles:\n")
	}
	used := map[string]bool{}
	for _, rec := range run.Recordings {
		what := "recording of " + rec.Who + "'s screen"
		if rec.From > 0 {
			what += ", " + ControlRecordBlock{From: rec.From, To: rec.To}.steps()
		}
		switch {
		case rec.Image:
			what = "screenshot of " + rec.Who + "'s screen"
		case rec.File:
			what = rec.Who + ": " + rec.Path
		}
		dir, ok := s.locateFile(rec.FileID)
		if !ok {
			fmt.Fprintf(&b, "  %s -- %s (no longer in the files tab, not included)\n", rec.Name, what)
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, rec.FileID))
		if err != nil {
			fmt.Fprintf(&b, "  %s -- %s (couldn't be read: %v)\n", rec.Name, what, err)
			continue
		}
		name := uniqueZipName("files/"+sanitizeFilename(rec.Name), used)
		files = append(files, zipEntry{name, data})
		fmt.Fprintf(&b, "  %s -- %s\n", name, what)
	}

	run.Saved = nil // what this save adds; it's not part of the run itself
	js, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return nil, err
	}
	entries := append([]zipEntry{{"report.txt", []byte(b.String())}, {"run.json", js}}, files...)
	return buildRunZip(entries)
}

// zipEntry is one file in a saved run's .zip.
type zipEntry struct {
	name string
	data []byte
}

// uniqueZipName makes name unique among used, numbering repeats.
func uniqueZipName(name string, used map[string]bool) string {
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	used[name] = true
	return name
}

// buildRunZip packs entries into a .zip: text compressed, everything else
// (images, video, archives -- already compressed) stored.
func buildRunZip(entries []zipEntry) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range entries {
		method := zip.Store
		switch strings.ToLower(path.Ext(f.name)) {
		case ".txt", ".json", ".log", ".yaml", ".yml":
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
