package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Session ownership, mirroring clauditable/sessionlog.go: only a session's
// owner (session.yaml's owner field) writes session.jsonl. On any other host
// federation-command's own records (logRecord, cmdDoneMsg, ...) go to that
// host's secondary log, session.<host>.jsonl, which session-manager splices
// into the transcript and the owner later fuses in (see
// session-manager/sessionlog.go). A session with no session.yaml has no
// known owner and is written to as someone else's.

const sessionLogName = "session.jsonl"

// readSessionOwner returns session.yaml's owner field, and whether
// session.yaml exists.
func readSessionOwner(sessionDir string) (owner string, hasYAML bool) {
	if _, err := os.Stat(filepath.Join(sessionDir, "session.yaml")); err != nil {
		return "", false
	}
	return readSessionYAMLField(sessionDir, "owner"), true
}

// isSessionOwner reports whether hostID owns the session in sessionDir.
func isSessionOwner(sessionDir, hostID string) bool {
	owner, hasYAML := readSessionOwner(sessionDir)
	return hasYAML && (owner == "" || owner == hostID)
}

// hostFileTag mirrors clauditable/sessionlog.go's function of the same name.
func hostFileTag(hostID string) string {
	var b strings.Builder
	for _, r := range hostID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	tag := strings.Trim(b.String(), ".-")
	if tag == "" {
		tag = "unknown"
	}
	if strings.HasSuffix(tag, "-s") {
		tag += "_"
	}
	return tag
}

// sessionLogPathFor is the log this host appends to in sessionDir.
func sessionLogPathFor(sessionDir, hostID string) string {
	if isSessionOwner(sessionDir, hostID) {
		return filepath.Join(sessionDir, sessionLogName)
	}
	return filepath.Join(sessionDir, "session."+hostFileTag(hostID)+".jsonl")
}

// sessionLogWriter is federation-command's session log: each Write (one
// json.Encoder record) is appended, under the log's lock, to whichever log
// this host should write in dir at that moment. Nothing is held open, so a
// session.jsonl replaced by a sync, or a secondary log pruned by
// session-manager, is never written through a stale handle.
type sessionLogWriter struct {
	dir string
}

func (w sessionLogWriter) Write(p []byte) (int, error) {
	if err := appendSessionLog(sessionLogPathFor(w.dir, fcHostID), p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// sessionEncoder returns the encoder federation-command logs records to for
// the session in dir.
func sessionEncoder(dir string) *json.Encoder {
	return json.NewEncoder(sessionLogWriter{dir: dir})
}

// appendSessionLog mirrors clauditable/sessionlog.go's function of the same
// name: one write under an exclusive flock, reopening if the file was
// replaced while waiting for the lock.
func appendSessionLog(path string, data []byte) error {
	for attempt := 0; attempt < 5; attempt++ {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return err
		}
		held, err1 := f.Stat()
		current, err2 := os.Stat(path)
		if err1 == nil && err2 == nil && os.SameFile(held, current) {
			_, werr := f.Write(data)
			cerr := f.Close()
			if werr != nil {
				return werr
			}
			return cerr
		}
		f.Close()
	}
	return fmt.Errorf("%s kept being replaced while appending", path)
}

// ---- set-session: join an existing session, never create one silently ----

// sessionExistsLocally reports whether id is a session this host has: its
// directory with a session.yaml. A bare directory doesn't count -- that's
// what an earlier set-session or a restart left behind for a session it
// never actually had.
func sessionExistsLocally(recordsPath, id string) bool {
	_, err := os.Stat(filepath.Join(recordsPath, id, "session.yaml"))
	return err == nil
}

// setSessionPullGlobs are the files set-session pulls in from other hosts
// before deciding a session doesn't exist -- the same ones session-manager's
// view keeps fresh (local-representative decides which host each may come
// from).
var setSessionPullGlobs = []struct {
	glob    string
	maxSize int64
}{
	{"session.yaml", 0},
	{"session.jsonl", 0},
	{"*-processed.txt", 0},
	{"*-resource-*", 256 << 10},
}

// setSessionPullTimeout bounds each of setSessionPullGlobs' requests; it has
// to cover local-representative's own hops to agent-coordinator and a peer.
const setSessionPullTimeout = 8 * time.Second

// pullSessionFromPeers asks local-representative to pull session id in from
// the other LR-active hosts. Best-effort: with no local-representative
// connection it does nothing, and the caller just finds the session isn't
// here. reachedAll is false when local-representative couldn't reach some
// host -- the session may exist there.
func (m appModel) pullSessionFromPeers(id string) (reachedAll bool) {
	if m.reprClient == nil {
		return false
	}
	port := m.reprClient.PeerHTTPPort()
	host, _, err := net.SplitHostPort(m.lrAddr)
	if port == "" || err != nil {
		return false
	}
	reachedAll = true
	client := &http.Client{Timeout: setSessionPullTimeout}
	for _, g := range setSessionPullGlobs {
		u := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/api/sessions/" + id + "/pull"}
		q := u.Query()
		q.Set("glob", g.glob)
		if g.maxSize > 0 {
			q.Set("max_size", strconv.FormatInt(g.maxSize, 10))
		}
		u.RawQuery = q.Encode()
		resp, err := client.Post(u.String(), "application/octet-stream", nil)
		if err != nil {
			reachedAll = false
			continue
		}
		var result struct {
			Errors int `json:"errors"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&result) != nil || result.Errors > 0 {
			reachedAll = false
		}
		resp.Body.Close()
	}
	return reachedAll
}

// sessionIDTime returns the creation time a session ID generated by
// new-session/clear-session starts with ("2006-01-02_15-04-05_..."), if any.
func sessionIDTime(id string) (time.Time, bool) {
	const layout = "2006-01-02_15-04-05"
	if len(id) < len(layout) {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(layout, id[:len(layout)], time.Local)
	return t, err == nil
}

// createSessionWithID creates session id on this host, owned by it -- only
// ever after the user confirmed it (see buildCreateSessionConfirmScript) or
// for an ID this host just generated itself (clear-session).
func createSessionWithID(recordsPath, id, name string) error {
	dir := filepath.Join(recordsPath, id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	yamlPath := filepath.Join(dir, "session.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return nil
	}
	content := fmt.Sprintf("id: %s\nname: %s\nowner: %s\ncreated: %s\n", id, name, fcHostID, time.Now().Format(time.RFC3339))
	f, err := os.OpenFile(yamlPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	_, err = f.WriteString(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// buildCreateSessionConfirmScript returns a bash script asking the user to
// confirm creating session id on this host; it exits 0 only on "yes".
// reachedAll is pullSessionFromPeers' verdict: when some host couldn't be
// reached (or there's no local-representative to ask), the session may well
// exist there, and creating it here forks it.
func buildCreateSessionConfirmScript(id string, reachedAll bool, now time.Time) string {
	escape := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
	}
	var notes []string
	if !reachedAll {
		notes = append(notes, "Not every other host could be checked -- it may exist on one that's offline.")
	}
	if t, ok := sessionIDTime(id); ok && now.Sub(t) > time.Minute {
		notes = append(notes, fmt.Sprintf("Its ID says it was created %s, so it most likely already exists somewhere.", t.Format("2006-01-02 15:04")))
	}
	if len(notes) > 0 {
		notes = append(notes, "Creating it here makes a separate session with the same ID.")
	}
	var noteLines strings.Builder
	for _, n := range notes {
		fmt.Fprintf(&noteLines, "printf \"  ${Y}%%s${R}\\n\" %s\n", escape(n))
	}
	return fmt.Sprintf(`
Y='\033[1;33m'; W='\033[1;37m'; R='\033[0m'
printf "\n"
printf "  ${W}Session not found:${R} %%s\n" %s
printf "  It isn't on this host, and no connected host has it.\n"
%sprintf "\n"
read -p "  Create it here, owned by this host? Type 'yes' to confirm: " _ufa_confirm
if [ "$_ufa_confirm" = "yes" ]; then
    exit 0
fi
printf "\n  Not created.\n"
exit 1
`, escape(id), noteLines.String())
}
