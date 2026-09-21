// Package restartsignal defines the small stdout protocol a sub-application
// uses to ask ufa-loader for a restart: an identifying banner line, one line
// of structured JSON, and a footer line, printed as the sub-application's
// final act before it exits. See condocs/InitialDistributedDevelopment.md
// Step 2 and docs/DevMode.md's "Loader" entry for the background.
//
// A sub-application never restarts itself — Announce only discloses that a
// restart is wanted and why, then the caller is expected to exit. Actually
// relaunching the binary (which ufa-loader expects to have been replaced
// with a newer build by then) is ufa-loader's job, not this package's, so
// that the restart mechanics live in one shared place as more
// sub-applications adopt this protocol.
package restartsignal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Banner and Footer delimit the structured section on stdout. They're
// deliberately distinctive so an ordinary log line is never mistaken for
// one, and so the section stays recognizable even if the JSON between them
// is ever pretty-printed across multiple lines.
const (
	Banner = "=== UFA-LOADER-RESTART ==="
	Footer = "=== END-UFA-LOADER-RESTART ==="
)

// InitEnvVar is the environment variable ufa-loader sets to a non-empty
// value on every sub-application it launches. A launched sub-application
// checks it at startup (see IsLoaderManaged) to tell whether it is
// loader-managed — i.e. whether announcing a restart and exiting can be
// expected to actually relaunch it — without needing to know anything else
// about how it was invoked.
const InitEnvVar = "UFA_LOADER_INIT"

// StateEnvVar is the environment variable ufa-loader sets, alongside
// InitEnvVar, when relaunching a sub-application whose prior exit announced
// restart State (see AnnounceState): the newly launched instance finds its
// own predecessor's disclosed state here (see PreviousState) rather than
// needing any other channel back to it. Unset on the very first launch, or
// whenever the prior announcement carried no State.
const StateEnvVar = "UFA_LOADER_STATE"

// IsLoaderManaged reports whether this process was launched by ufa-loader,
// per InitEnvVar.
func IsLoaderManaged() bool {
	return os.Getenv(InitEnvVar) != ""
}

// PreviousState returns the State a prior instance of this same
// sub-application attached to the restart announcement that led to this
// launch (see AnnounceState and StateEnvVar), as raw JSON ready for the
// caller to unmarshal into its own app-specific type — restartsignal itself
// is deliberately ignorant of what's inside. ok is false on a fresh launch
// or whenever the prior announcement carried no State.
func PreviousState() (raw json.RawMessage, ok bool) {
	v, present := os.LookupEnv(StateEnvVar)
	if !present || v == "" {
		return nil, false
	}
	return json.RawMessage(v), true
}

// Announcement is the structured payload between Banner and Footer.
type Announcement struct {
	App    string `json:"app"`              // sub-application name, e.g. "local-representative"
	Reason string `json:"reason,omitempty"` // human-readable trigger, e.g. "sighup"
	PID    int    `json:"pid"`
	Time   string `json:"time"` // RFC3339, UTC

	// State is optional app-defined data — e.g. local-representative's
	// in-memory toggles (see
	// condocs/initialDistributedDevelopmentImpls/Step3Prompt.md Revision D)
	// — describing live state established since startup that the app would
	// like the instance replacing it to pick back up. Opaque to
	// restartsignal and ufa-loader alike: they only carry it from this
	// announcement into StateEnvVar on the next launch (see PreviousState).
	State json.RawMessage `json:"state,omitempty"`
}

// Announce writes the full Banner/JSON/Footer sequence to w. Call it as the
// very last act before the process exits: from that point on, a loader
// watching this output treats the child's exit as a restart request rather
// than a stop. Equivalent to AnnounceState(w, app, reason, nil).
func Announce(w io.Writer, app, reason string) error {
	return AnnounceState(w, app, reason, nil)
}

// AnnounceState behaves like Announce but also attaches state — marshaled
// as-is into the announcement's State field — for the instance that
// replaces this one to read back via PreviousState. Pass nil for state to
// disclose none (equivalent to calling Announce).
func AnnounceState(w io.Writer, app, reason string, state interface{}) error {
	a := Announcement{
		App:    app,
		Reason: reason,
		PID:    os.Getpid(),
		Time:   time.Now().UTC().Format(time.RFC3339),
	}
	if state != nil {
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		a.State = raw
	}
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n%s\n%s\n", Banner, body, Footer)
	return err
}

// Scanner watches a stream of output lines — typically a managed child's
// stdout, fed line by line as it's produced — for an Announce sequence. It
// is not safe for concurrent use.
type Scanner struct {
	inBody bool
	body   strings.Builder
}

// NewScanner returns a Scanner ready to watch a fresh output stream.
func NewScanner() *Scanner {
	return &Scanner{}
}

// Feed processes one line of output (a trailing "\r" from a CRLF stream is
// tolerated either way). It returns the parsed Announcement and true the
// moment Footer completes a sequence that started with Banner; otherwise
// (nil, false). A body that fails to parse as JSON is discarded rather than
// returned, and the scanner goes back to watching for another Banner — one
// malformed sequence can't wedge restart detection permanently.
func (s *Scanner) Feed(line string) (*Announcement, bool) {
	line = strings.TrimRight(line, "\r")
	if !s.inBody {
		if line == Banner {
			s.inBody = true
			s.body.Reset()
		}
		return nil, false
	}
	if line == Footer {
		s.inBody = false
		var a Announcement
		if err := json.Unmarshal([]byte(s.body.String()), &a); err != nil {
			return nil, false
		}
		return &a, true
	}
	s.body.WriteString(line)
	return nil, false
}

// ScanReader drains r line by line, echoing every line to echo (if non-nil)
// as it's read — this is how ufa-loader passes a managed child's stdout
// straight through to the operator while still watching it — and returns the
// last Announcement seen once r reaches EOF (which happens when the child's
// stdout closes, typically because the process exited).
func ScanReader(r io.Reader, echo io.Writer) *Announcement {
	scanner := NewScanner()
	var last *Announcement
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if echo != nil {
			fmt.Fprintln(echo, line)
		}
		if a, ok := scanner.Feed(line); ok {
			last = a
		}
	}
	return last
}
