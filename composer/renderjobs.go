package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- Render jobs ----
//
// A render job renders one saved composition without anyone pressing
// Render: it's how a caller with no browser of its own -- the dtt composer
// tool (dtt/builtin/composer) -- turns a composition into a file in
// local-representative's files area. POST /api/render-jobs starts one and
// GET /api/render-jobs/<id>?wait=<seconds> waits for it.
//
// A job is rendered one of two ways:
//
//   - "ffmpeg": composer renders it itself, with ffmpeg (ffmpeg.go). Nothing
//     needs to be open.
//   - "browser": an open compose tab renders it, exactly as its Render
//     button would. The job waits until a tab claims it (the tab does so
//     on its own once the page has been clicked, since browsers won't play
//     sound before that, or when its "render now" button is pressed), and
//     the tab must stay visible while it records.
//
// "auto", the default, uses ffmpeg when it's installed and a tab otherwise.
// Either way the result lands through saveRender, as a button render does.
// Jobs are kept in memory only, for an hour after they finish.

const (
	rendererAuto    = "auto"
	rendererFFmpeg  = "ffmpeg"
	rendererBrowser = "browser"

	jobQueued    = "queued"    // ffmpeg: about to start
	jobWaiting   = "waiting"   // browser: no tab has claimed it yet
	jobRendering = "rendering" // being rendered (by ffmpeg, or a tab that claimed it)
	jobDone      = "done"
	jobFailed    = "failed"

	browserClaimWindow = 10 * time.Minute // how long a browser job waits to be claimed
	jobKeep            = time.Hour        // how long a finished job stays readable
	maxJobWait         = 10 * time.Minute // the longest ?wait= honoured
)

// RenderJob is one job, as the API reports it.
type RenderJob struct {
	ID          string           `json:"id"`
	Composition string           `json:"composition"`
	Name        string           `json:"name"`     // the composition's name, for the tab's notice
	Renderer    string           `json:"renderer"` // "ffmpeg" or "browser" -- what auto chose
	State       string           `json:"state"`
	Error       string           `json:"error,omitempty"`
	Result      *RenderResultMsg `json:"result,omitempty"`
	CreatedAt   int64            `json:"created_at"`
	FinishedAt  int64            `json:"finished_at,omitempty"`

	claimedBy *wsClient
	done      chan struct{} // closed once the job is done or failed
}

func (j *RenderJob) finished() bool { return j.State == jobDone || j.State == jobFailed }

// RenderJobsMsg is the "render-jobs" WebSocket payload: the browser jobs no
// tab has claimed yet.
type RenderJobsMsg struct {
	Waiting []RenderJob `json:"waiting"`
}

// RenderJobMsg is the "render-job" WebSocket payload, sent only to the tab
// whose claim won: the job and the composition to render.
type RenderJobMsg struct {
	Job         RenderJob    `json:"job"`
	Composition *Composition `json:"composition"`
}

type jobQueue struct {
	mu   sync.Mutex
	jobs map[string]*RenderJob
	// changed is called (outside mu) whenever the waiting list may have
	// changed, to tell the tabs.
	changed func()
}

func newJobQueue() *jobQueue {
	return &jobQueue{jobs: map[string]*RenderJob{}, changed: func() {}}
}

func newJobID() string {
	var b [6]byte
	rand.Read(b[:])
	return "job-" + hex.EncodeToString(b[:])
}

// add records a new job for c, in state, after dropping long-finished ones.
func (q *jobQueue) add(c *Composition, renderer, state string) *RenderJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	for id, j := range q.jobs {
		if j.finished() && now.Sub(time.Unix(j.FinishedAt, 0)) > jobKeep {
			delete(q.jobs, id)
		}
	}
	j := &RenderJob{ID: newJobID(), Composition: c.ID, Name: c.Name, Renderer: renderer, State: state,
		CreatedAt: now.Unix(), done: make(chan struct{})}
	q.jobs[j.ID] = j
	return j
}

// snapshot copies job id for reporting.
func (q *jobQueue) snapshot(id string) (RenderJob, <-chan struct{}, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[id]
	if !ok {
		return RenderJob{}, nil, false
	}
	return *j, j.done, true
}

// setState moves an unfinished job to state.
func (q *jobQueue) setState(id, state string) {
	q.mu.Lock()
	if j, ok := q.jobs[id]; ok && !j.finished() {
		j.State = state
	}
	q.mu.Unlock()
}

// finish records a job's result: done when the render reached the files
// area, failed otherwise. A job already finished is left alone.
func (q *jobQueue) finish(id string, result RenderResultMsg) {
	q.mu.Lock()
	j, ok := q.jobs[id]
	if !ok || j.finished() {
		q.mu.Unlock()
		return
	}
	wasWaiting := j.State == jobWaiting
	r := result
	j.Result = &r
	j.State, j.Error = jobDone, ""
	if result.FileID == "" {
		j.State, j.Error = jobFailed, result.Error
		if j.Error == "" {
			j.Error = "the render didn't reach local-representative's files"
		}
	}
	j.FinishedAt = time.Now().Unix()
	j.claimedBy = nil
	close(j.done)
	q.mu.Unlock()
	if wasWaiting {
		q.changed()
	}
}

// fail finishes job id with an error.
func (q *jobQueue) fail(id, composition, msg string) {
	q.finish(id, RenderResultMsg{Composition: composition, Error: msg})
}

// waiting lists the unclaimed browser jobs, oldest first.
func (q *jobQueue) waiting() []RenderJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []RenderJob{}
	for _, j := range q.jobs {
		if j.State == jobWaiting {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt < out[b].CreatedAt || (out[a].CreatedAt == out[b].CreatedAt && out[a].ID < out[b].ID) })
	return out
}

// claim gives waiting browser job id to tab c; false if it's gone or
// another tab got it first.
func (q *jobQueue) claim(id string, c *wsClient) (RenderJob, bool) {
	q.mu.Lock()
	j, ok := q.jobs[id]
	if !ok || j.State != jobWaiting {
		q.mu.Unlock()
		return RenderJob{}, false
	}
	j.State, j.claimedBy = jobRendering, c
	snap := *j
	q.mu.Unlock()
	q.changed()
	return snap, true
}

// release puts the jobs tab c had claimed back to waiting, when it goes away
// before finishing them. One past its claim window (whose timer has already
// run) fails instead, so it can't wait forever.
func (q *jobQueue) release(c *wsClient) {
	q.mu.Lock()
	released := false
	var expired []*RenderJob
	for _, j := range q.jobs {
		if j.claimedBy == c && j.State == jobRendering {
			j.State, j.claimedBy = jobWaiting, nil
			released = true
			if time.Since(time.Unix(j.CreatedAt, 0)) >= browserClaimWindow {
				expired = append(expired, j)
			}
		}
	}
	q.mu.Unlock()
	for _, j := range expired {
		q.fail(j.ID, j.Composition, "the compose tab rendering it went away, and no other tab took it up in time -- render it again")
	}
	if released {
		q.changed()
	}
}

// heldBy reports whether tab c holds job id.
func (q *jobQueue) heldBy(id string, c *wsClient) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[id]
	return ok && j.claimedBy == c && j.State == jobRendering
}

// ---- Starting jobs ----

// startRenderJob starts rendering saved composition id with renderer
// (auto, ffmpeg or browser).
func (s *Server) startRenderJob(id, renderer string) (RenderJob, error) {
	c, err := s.store.get(id)
	if err != nil {
		return RenderJob{}, err
	}
	switch renderer {
	case "", rendererAuto:
		renderer = rendererBrowser
		if _, err := s.ffmpegPath(); err == nil {
			renderer = rendererFFmpeg
		}
	case rendererFFmpeg:
		if _, err := s.ffmpegPath(); err != nil {
			return RenderJob{}, err
		}
	case rendererBrowser:
	default:
		return RenderJob{}, fmt.Errorf("renderer %q should be auto, ffmpeg or browser", renderer)
	}
	if _, err := s.lrBaseFn(); err != nil {
		return RenderJob{}, fmt.Errorf("%v -- the render is saved in its files area", err)
	}

	if renderer == rendererFFmpeg {
		j := s.jobs.add(c, renderer, jobQueued)
		log.Printf("render job %s started for %s (renderer ffmpeg)", j.ID, c.ID)
		go s.runFFmpegJob(j.ID, c)
		snap, _, _ := s.jobs.snapshot(j.ID)
		return snap, nil
	}
	j := s.jobs.add(c, renderer, jobWaiting)
	log.Printf("render job %s started for %s (renderer browser, waiting for a compose tab)", j.ID, c.ID)
	time.AfterFunc(browserClaimWindow, func() {
		if snap, _, ok := s.jobs.snapshot(j.ID); ok && snap.State == jobWaiting {
			s.jobs.fail(j.ID, c.ID, fmt.Sprintf("no compose tab rendered it within %s -- open the compose tab (and click in it once), or install ffmpeg so composer can render on its own", browserClaimWindow))
		}
	})
	s.broadcastRenderJobs()
	snap, _, _ := s.jobs.snapshot(j.ID)
	return snap, nil
}

// runFFmpegJob renders job id's composition with ffmpeg and saves it.
func (s *Server) runFFmpegJob(id string, c *Composition) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("render job %s: panic: %v", id, r)
			s.jobs.fail(id, c.ID, fmt.Sprintf("internal error: %v", r))
		}
	}()
	s.jobs.setState(id, jobRendering)
	ctx, cancel := context.WithTimeout(context.Background(), ffmpegTimeout)
	defer cancel()
	body, err := s.renderFFmpeg(ctx, c)
	if err != nil {
		log.Printf("render job %s (%s): %v", id, c.ID, err)
		s.jobs.fail(id, c.ID, err.Error())
		s.broadcast("render-result", RenderResultMsg{Composition: c.ID, Error: err.Error()})
		return
	}
	result, _ := s.saveRender(c, body, rendererFFmpeg, id)
	s.jobs.finish(id, result)
}

// broadcastRenderJobs tells every tab which browser jobs are waiting.
func (s *Server) broadcastRenderJobs() {
	s.broadcast("render-jobs", RenderJobsMsg{Waiting: s.jobs.waiting()})
}

// claimRenderJob answers a tab's "claim-render-job": the job and its
// composition if the tab won it.
func (s *Server) claimRenderJob(c *wsClient, id string) {
	job, ok := s.jobs.claim(id, c)
	if !ok {
		return // another tab has it, or it's gone; the next render-jobs says so
	}
	comp, err := s.store.get(job.Composition)
	if err != nil {
		s.jobs.fail(id, job.Composition, err.Error())
		return
	}
	s.sendToClient(c, "render-job", RenderJobMsg{Job: job, Composition: comp})
}

// ---- HTTP ----

// handleRenderJobs implements POST /api/render-jobs ({composition,
// renderer}) and GET /api/render-jobs/<id>[?wait=<seconds>], which waits up
// to that long for the job to finish before answering.
func (s *Server) handleRenderJobs(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/render-jobs"), "/")
	switch {
	case id == "" && r.Method == http.MethodPost:
		var req struct {
			Composition string `json:"composition"`
			Renderer    string `json:"renderer"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad request: "+err.Error())
			return
		}
		job, err := s.startRenderJob(strings.TrimSpace(req.Composition), strings.TrimSpace(req.Renderer))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	case id != "" && !strings.Contains(id, "/") && r.Method == http.MethodGet:
		job, done, ok := s.jobs.snapshot(id)
		if !ok {
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("no render job %q (jobs are kept in memory, for an hour after they finish)", id))
			return
		}
		if wait, _ := strconv.ParseFloat(r.URL.Query().Get("wait"), 64); wait > 0 && !job.finished() {
			d := time.Duration(wait * float64(time.Second))
			if d > maxJobWait {
				d = maxJobWait
			}
			select {
			case <-done:
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
			job, _, _ = s.jobs.snapshot(id)
		}
		writeJSON(w, http.StatusOK, job)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
