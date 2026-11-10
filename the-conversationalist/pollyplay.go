package main

// Playing clips on this host (condocs/dTT-IntegrationImpls/Step3Prompt.md,
// Revision B): POST /api/polly with "play": true also plays the clip, out
// loud, on the host The Conversationalist runs on -- so a dtt making clips
// one after another (dtt/examples/compositions/localrep-polly-configs.dtt.yaml) is heard
// as it goes.
//
// TC has no speaker of its own: a browser tab does the playing. The clip is
// sent, as "polly-play", to one of TC's tabs open in a browser on this host
// -- TC's own page, or LR's/AC's convo tab when that browser is on this
// host -- and the tab queues it behind any clip it's still playing, so they
// play one after another, in the order they were made (App.tsx's
// useHostPlayback). Only one tab plays it, so two open tabs don't play it
// twice. A tab is a player once it reports its presence ("tab-presence",
// with whether it's visible); capture-mode tabs never do. The pick is a
// visible tab if there is one, else any, the most recently shown or
// connected first.
//
// A tab is on this host when its browser's address -- the first
// X-Forwarded-For entry, which LR's and AC's proxies add, else the
// connection's own -- is a loopback address or one of this host's. A tab
// open on another machine (AC's overlay, say) never plays: the point is to
// hear it where it was made.
//
// With no such tab open, the clip is made and saved all the same; the
// answer says it wasn't played and why.

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// PollyPlayMsg is the "polly-play" WebSocket payload: a clip made through
// the HTTP API with play, for this tab to play once those before it finish.
type PollyPlayMsg struct {
	Audio       string `json:"audio"`
	ContentType string `json:"content_type"`
	Clip        uint64 `json:"clip"`
	Config      string `json:"config,omitempty"`
	Voice       string `json:"voice,omitempty"`
	Engine      string `json:"engine,omitempty"`
	Cost        string `json:"cost,omitempty"`
	Reused      bool   `json:"reused,omitempty"`
	SavedAs     string `json:"saved_as,omitempty"`
}

// tabPresence is what a tab has said about itself, guarded by Server.mu.
type tabPresence struct {
	player  bool      // it has reported its presence: it plays "polly-play"
	visible bool      // it was visible when it last said
	shownAt time.Time // when it connected, or last became visible
}

// browserAddr is the address of the browser behind r: X-Forwarded-For's
// first entry when a proxy (LR's, AC's) added one, else r.RemoteAddr's host.
func browserAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// hostAddrs lists this host's interface addresses; tests replace it.
var hostAddrs = defaultHostAddrs

func defaultHostAddrs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ips []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			ips = append(ips, n.IP)
		}
	}
	return ips
}

// onThisHost reports whether addr (an IP) is this host's.
func onThisHost(addr string) bool {
	ip := net.ParseIP(strings.Trim(addr, "[]"))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, h := range hostAddrs() {
		if h.Equal(ip) {
			return true
		}
	}
	return false
}

// setTabPresence records what tab c says about itself.
func (s *Server) setTabPresence(c *wsClient, visible bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if visible && (!c.presence.visible || !c.presence.player) {
		c.presence.shownAt = time.Now()
	}
	c.presence.player, c.presence.visible = true, visible
}

// hostPlayer picks the tab to play a clip on this host: a player tab whose
// browser is on this host, visible ones first, then the most recently
// shown. nil when there's none.
func (s *Server) hostPlayer() *wsClient {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *wsClient
	for c := range s.clients {
		if !c.local || !c.presence.player {
			continue
		}
		if best == nil ||
			(c.presence.visible && !best.presence.visible) ||
			(c.presence.visible == best.presence.visible && c.presence.shownAt.After(best.presence.shownAt)) {
			best = c
		}
	}
	return best
}

var errNoHostTab = errors.New("no Conversationalist tab is open in a browser on this host -- open TC's page (or LR's convo tab) there to hear clips as they're made")

// playOnHost sends clip c, saved as savedAs, to a tab on this host to play.
// It says which kind of tab took it.
func (s *Server) playOnHost(c *pollyClip, reused bool, savedAs string) (string, error) {
	tab := s.hostPlayer()
	if tab == nil {
		return "", errNoHostTab
	}
	var res PollyAudioMsg
	res.setClip(c, reused)
	s.sendToClient(tab, "polly-play", PollyPlayMsg{
		Audio: res.Audio, ContentType: res.ContentType, Clip: c.ID, Config: c.Config,
		Voice: c.Voice, Engine: c.Engine, Cost: c.Cost, Reused: reused, SavedAs: savedAs,
	})
	s.mu.RLock()
	visible := tab.presence.visible
	s.mu.RUnlock()
	if visible {
		return "a visible tab on this host", nil
	}
	return "a hidden tab on this host", nil
}
