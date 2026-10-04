//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// This file focuses a window by asking gnome-shell over D-Bus, for GNOME
// Wayland sessions, where a native Wayland terminal is invisible to
// robotgo's X11 window lookup and Wayland itself offers clients no way to
// focus another app's window.
//
// Two interfaces are tried:
//   - org.gnome.Shell.Eval runs a snippet inside gnome-shell. Recent GNOME
//     only honours it in unsafe mode (looking glass: global.context.unsafe_mode
//     = true), answering success=false otherwise.
//   - the "Window Calls" extension (org.gnome.Shell.Extensions.Windows), when
//     installed, exposes List/GetTitle/Activate without needing unsafe mode.
//
// init() overrides window.go's focusViaGnomeShell and focusViaWindowCalls
// vars, so non-Linux builds keep the "unavailable" stubs and tests can
// substitute their own.

// windowMatch is the outcome of picking a window out of a listing.
type windowMatch struct {
	Found bool     `json:"found"`
	Title string   `json:"title,omitempty"`
	Pid   int      `json:"pid,omitempty"`
	Seen  []string `json:"seen,omitempty"` // titles seen, for the "not found" error
}

func (m windowMatch) describe() string {
	return fmt.Sprintf("%q (pid %d)", m.Title, m.Pid)
}

func (m windowMatch) notFound(t windowTarget) error {
	return fmt.Errorf("no window titled %q or owned by %s's process tree (pids %v); open windows: %s",
		t.title, fcProcessName, t.pids, strings.Join(m.Seen, ", "))
}

// focusEvalJS picks and activates the window in gnome-shell: by exact title
// first, then by the best-ranked owning pid. Returns a windowMatch as JSON.
const focusEvalJS = `(() => {
  const title = %s, pids = %s;
  const wins = global.get_window_actors().map(a => a.meta_window).filter(w => !w.is_skip_taskbar());
  let best = wins.find(w => (w.get_title() || '').trim() === title) || null;
  if (!best) {
    let rank = Infinity;
    for (const w of wins) {
      const r = pids.indexOf(w.get_pid());
      if (r >= 0 && r < rank) { best = w; rank = r; }
    }
  }
  if (!best) return JSON.stringify({found: false, seen: wins.map(w => w.get_title() || '')});
  const now = global.get_current_time();
  const ws = best.get_workspace();
  if (ws) ws.activate_with_focus(best, now); else best.activate(now);
  return JSON.stringify({found: true, title: best.get_title() || '', pid: best.get_pid()});
})()`

func init() {
	focusViaGnomeShell = func(t windowTarget) (string, error) {
		conn, err := dbus.ConnectSessionBus()
		if err != nil {
			return "", fmt.Errorf("%w: connecting to the session bus: %v", errFocusPathUnavailable, err)
		}
		defer conn.Close()

		title, _ := json.Marshal(t.title)
		pids, _ := json.Marshal(t.pids)
		if t.pids == nil {
			pids = []byte("[]")
		}
		var ok bool
		var result string
		err = conn.Object("org.gnome.Shell", dbus.ObjectPath("/org/gnome/Shell")).
			Call("org.gnome.Shell.Eval", 0, fmt.Sprintf(focusEvalJS, title, pids)).Store(&ok, &result)
		if err != nil {
			return "", fmt.Errorf("%w: org.gnome.Shell.Eval: %v", errFocusPathUnavailable, err)
		}
		if !ok {
			if result == "" {
				return "", fmt.Errorf("%w: org.gnome.Shell.Eval refused (gnome-shell isn't in unsafe mode)", errFocusPathUnavailable)
			}
			return "", fmt.Errorf("org.gnome.Shell.Eval: %s", result)
		}
		var m windowMatch
		if err := json.Unmarshal([]byte(result), &m); err != nil {
			return "", fmt.Errorf("org.gnome.Shell.Eval: unexpected result %q", result)
		}
		if !m.Found {
			return "", m.notFound(t)
		}
		return m.describe(), nil
	}

	focusViaWindowCalls = func(t windowTarget) (string, error) {
		conn, err := dbus.ConnectSessionBus()
		if err != nil {
			return "", fmt.Errorf("%w: connecting to the session bus: %v", errFocusPathUnavailable, err)
		}
		defer conn.Close()

		const iface = "org.gnome.Shell.Extensions.Windows"
		obj := conn.Object("org.gnome.Shell", dbus.ObjectPath("/org/gnome/Shell/Extensions/Windows"))
		var listing string
		if err := obj.Call(iface+".List", 0).Store(&listing); err != nil {
			return "", fmt.Errorf("%w: %s.List (is the Window Calls extension installed?): %v", errFocusPathUnavailable, iface, err)
		}
		var wins []struct {
			ID  uint32 `json:"id"`
			Pid int    `json:"pid"`
		}
		if err := json.Unmarshal([]byte(listing), &wins); err != nil {
			return "", fmt.Errorf("%s.List: unexpected result: %v", iface, err)
		}

		titles := make([]string, len(wins))
		for i, w := range wins {
			obj.Call(iface+".GetTitle", 0, w.ID).Store(&titles[i])
		}
		pick := -1
		for i := range wins {
			if strings.TrimSpace(titles[i]) == t.title {
				pick = i
				break
			}
		}
		if pick < 0 {
			rank := len(t.pids)
			for i, w := range wins {
				for r, p := range t.pids {
					if p == w.Pid && r < rank {
						pick, rank = i, r
					}
				}
			}
		}
		if pick < 0 {
			return "", windowMatch{Seen: titles}.notFound(t)
		}
		if err := obj.Call(iface+".Activate", 0, wins[pick].ID).Err; err != nil {
			return "", fmt.Errorf("%s.Activate: %w", iface, err)
		}
		return windowMatch{Title: titles[pick], Pid: wins[pick].Pid}.describe(), nil
	}
}
