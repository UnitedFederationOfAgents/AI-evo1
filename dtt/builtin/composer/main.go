// composer -- a built-in T3 (see AI-evo1/dtt/README.md "composer"), runtime
// go: dtt builds this package before running it.
//
// It saves a composition in composer over PUT /api/compositions/<id> and,
// unless render is false, starts a render job (POST /api/render-jobs) and
// waits for it (GET /api/render-jobs/<id>?wait=...). composer uploads the
// finished WebM into local-representative's files area itself, so the
// result here is where it landed. See composer/api.go and
// composer/renderjobs.go for the other side.
//
// Inputs arrive as JSON on stdin; results go to $DTT_OUTPUT as JSON; stderr
// is the run's log. Standard library only, so dtt builds it without a
// go.mod.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

type inputs struct {
	Composition *string `json:"composition"`
	ID          *string `json:"id"`
	Render      *bool   `json:"render"`
	Renderer    *string `json:"renderer"`
	Wait        *int64  `json:"wait"`
	ComposerURL *string `json:"composer_url"`
}

func str(p *string, def string) string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return def
	}
	return strings.TrimSpace(*p)
}

// helloWorld is the composition used when none is given: composer's own
// hello-world example, renamed so it doesn't take that one's id.
const helloWorld = `{
  "name": "Hello from dtt",
  "width": 1280,
  "height": 720,
  "video": [
    {"kind": "title", "duration": 2, "text": "Hello, world!", "color": "#ffffff", "background": "#1e3a5f"},
    {"kind": "title", "duration": 2, "text": "composed by composer, for dtt", "color": "#1a1a1a", "background": "#e0c060"}
  ],
  "audio": [
    {"kind": "tone", "duration": 0.4, "frequency": 440},
    {"kind": "silence", "duration": 1.6},
    {"kind": "tone", "duration": 0.4, "frequency": 660}
  ]
}`

// maxWait is the longest wait honoured, inside tool.yaml's timeout: 15m.
const maxWait = 14 * 60

var idRe =regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// slug makes an id from a name, as composer does for a new composition.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.TrimRight(b.String(), "-")
}

type renderJob struct {
	ID       string `json:"id"`
	Renderer string `json:"renderer"`
	State    string `json:"state"`
	Error    string `json:"error"`
	Result   *struct {
		FileID string `json:"file_id"`
		Name   string `json:"name"`
		Size   int64  `json:"size"`
	} `json:"result"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "composer:", err)
		os.Exit(1)
	}
}

func run() error {
	var in inputs
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		return fmt.Errorf("reading inputs: %v", err)
	}
	base := strings.TrimRight(str(in.ComposerURL, "http://localhost:8089"), "/")
	renderer := str(in.Renderer, "auto")
	wait := int64(600)
	if in.Wait != nil && *in.Wait > 0 {
		wait = *in.Wait
	}
	// Past the tool's 15m timeout dtt kills it before it can report
	// anything, so stop waiting a little short of that.
	if wait > maxWait {
		fmt.Fprintf(os.Stderr, "wait %ds is longer than this tool may run; waiting %ds\n", wait, maxWait)
		wait = maxWait
	}
	render := in.Render == nil || *in.Render

	// The composition: given, or the hello-world.
	src := str(in.Composition, "")
	if src == "" {
		fmt.Fprintln(os.Stderr, "no composition given; using the hello-world")
		src = helloWorld
	}
	var comp map[string]any
	if err := json.Unmarshal([]byte(src), &comp); err != nil {
		return fmt.Errorf("composition isn't a JSON object: %v", err)
	}
	name, _ := comp["name"].(string)
	id := str(in.ID, "")
	if id == "" {
		if id = slug(name); id == "" {
			return errors.New("give the composition a name, or give id")
		}
	}
	if !idRe.MatchString(id) {
		return fmt.Errorf("id %q should be lower-case letters, digits and '-', starting with a letter or digit", id)
	}
	delete(comp, "id")
	delete(comp, "renders")

	out := map[string]any{"composition_id": id}
	defer writeOutputs(out) // whatever was learnt comes back, even on failure

	status, err := getJSON(base + "/api/status")
	if err != nil {
		return fmt.Errorf("composer isn't answering at %s (%v) -- launch compose from local-representative's system tab, or set composer_url", base, err)
	}
	if status["repr_status"] != "connected" {
		return fmt.Errorf("composer is %v, not connected, to local-representative, whose files area the render goes into -- connect it from the compose tab", status["repr_status"])
	}

	// Save it.
	body, _ := json.Marshal(comp)
	var saved struct {
		Composition json.RawMessage `json:"composition"`
		Created     bool            `json:"created"`
		Error       string          `json:"error"`
	}
	if err := call(http.MethodPut, base+"/api/compositions/"+id, body, &saved); err != nil {
		return fmt.Errorf("saving composition %s: %v", id, err)
	}
	out["created"] = saved.Created
	out["composition_json"] = string(saved.Composition)
	verb := "replaced"
	if saved.Created {
		verb = "created"
	}
	fmt.Fprintf(os.Stderr, "%s composition %s (%q)\n", verb, id, name)
	if !render {
		return nil
	}

	// Render it, and wait.
	req, _ := json.Marshal(map[string]string{"composition": id, "renderer": renderer})
	var job renderJob
	if err := call(http.MethodPost, base+"/api/render-jobs", req, &job); err != nil {
		return fmt.Errorf("starting the render: %v", err)
	}
	out["rendered_by"] = job.Renderer
	fmt.Fprintf(os.Stderr, "render job %s started (renderer %s)\n", job.ID, job.Renderer)
	if job.Renderer == "browser" {
		fmt.Fprintln(os.Stderr, "composer has no ffmpeg, so an open compose tab renders it: open the compose tab, click in it once (or press its \"render now\"), and keep it visible while it records")
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	lastState := job.State
	for job.State != "done" && job.State != "failed" {
		left := time.Until(deadline)
		if left <= 0 {
			return fmt.Errorf("the render didn't finish within %ds (job %s is %s) -- composer keeps working on it; raise wait to wait longer", wait, job.ID, job.State)
		}
		step := 30 * time.Second
		if left < step {
			step = left
		}
		url := fmt.Sprintf("%s/api/render-jobs/%s?wait=%d", base, job.ID, int(step.Seconds())+1)
		if err := call(http.MethodGet, url, nil, &job); err != nil {
			return fmt.Errorf("checking render job %s: %v", job.ID, err)
		}
		if job.State != lastState {
			fmt.Fprintf(os.Stderr, "render job %s: %s\n", job.ID, job.State)
			lastState = job.State
		}
	}
	if job.State == "failed" {
		return fmt.Errorf("the render failed: %s", job.Error)
	}
	if job.Result == nil {
		return errors.New("the render finished but composer didn't say where it went")
	}
	out["file_id"] = job.Result.FileID
	out["file_name"] = job.Result.Name
	out["size"] = job.Result.Size
	fmt.Fprintf(os.Stderr, "rendered %s (%d bytes) into local-representative's files\n", job.Result.Name, job.Result.Size)
	return nil
}

var client = &http.Client{Timeout: 2 * time.Minute}

// call sends body (JSON, or nothing) and decodes the JSON answer into v. An
// answer outside 2xx is an error carrying composer's message.
func call(method, url string, body []byte, v any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, v)
}

func getJSON(url string) (map[string]any, error) {
	var m map[string]any
	err := call(http.MethodGet, url, nil, &m)
	return m, err
}

func writeOutputs(values map[string]any) {
	data, _ := json.Marshal(map[string]any{"values": values})
	if err := os.WriteFile(os.Getenv("DTT_OUTPUT"), data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "composer: writing outputs:", err)
	}
}
