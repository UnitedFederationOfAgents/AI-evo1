package main

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http/httptest"
	"testing"
)

func TestBrowserOnThisHost(t *testing.T) {
	hostAddrs = func() []net.IP { return []net.IP{net.ParseIP("10.0.0.1")} }
	t.Cleanup(func() { hostAddrs = defaultHostAddrs })

	for _, c := range []struct {
		remote, xff string
		local       bool
	}{
		{"127.0.0.1:5000", "", true},
		{"[::1]:5000", "", true},
		{"10.0.0.1:5000", "", true},
		{"10.0.0.2:5000", "", false},
		// Through LR's proxy, the connection is LR's: the browser is the
		// first X-Forwarded-For entry.
		{"127.0.0.1:5000", "10.0.0.2", false},
		{"127.0.0.1:5000", "10.0.0.1, 127.0.0.1", true},
		{"127.0.0.1:5000", "::1", true},
	} {
		r := httptest.NewRequest("GET", "/ws", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := onThisHost(browserAddr(r)); got != c.local {
			t.Errorf("remote %s, X-Forwarded-For %q: on this host = %v, want %v", c.remote, c.xff, got, c.local)
		}
	}
}

// TestPlayOnHost covers Revision B: a clip made with play goes to one tab,
// a player on this host -- a visible one before a hidden one -- and none
// is an error.
func TestPlayOnHost(t *testing.T) {
	s := newServer()
	fakePolly(t, s)
	spoken, err := s.speak("play me", "", "api")
	if err != nil {
		t.Fatal(err)
	}
	tab := func(local, player, visible bool) *wsClient {
		c := &wsClient{send: make(chan []byte, 4), local: local}
		s.clients[c] = true
		if player {
			s.setTabPresence(c, visible)
		}
		return c
	}

	if _, err := s.playOnHost(spoken.clip, false, "polly-x.mp3"); err != errNoHostTab {
		t.Errorf("no tabs: err = %v", err)
	}
	away := tab(false, true, true)     // on another machine
	capture := tab(true, false, false) // never said it plays
	hidden := tab(true, true, false)   // on this host, hidden
	if on, err := s.playOnHost(spoken.clip, false, "polly-x.mp3"); err != nil || on != "a hidden tab on this host" {
		t.Errorf("only a hidden tab here: %q, %v", on, err)
	}
	if len(hidden.send) != 1 || len(away.send) != 0 || len(capture.send) != 0 {
		t.Fatalf("sent to hidden %d, away %d, capture %d; want 1, 0, 0", len(hidden.send), len(away.send), len(capture.send))
	}
	var m struct {
		Type    string       `json:"type"`
		Payload PollyPlayMsg `json:"payload"`
	}
	json.Unmarshal(<-hidden.send, &m)
	audio, _ := base64.StdEncoding.DecodeString(m.Payload.Audio)
	if m.Type != "polly-play" || string(audio) != "mp3:Joanna:standard:play me" || m.Payload.SavedAs != "polly-x.mp3" || m.Payload.Clip != spoken.clip.ID {
		t.Errorf("polly-play = %+v (audio %q)", m, audio)
	}

	visible := tab(true, true, true)
	if on, err := s.playOnHost(spoken.clip, true, "polly-y.mp3"); err != nil || on != "a visible tab on this host" {
		t.Errorf("with a visible tab: %q, %v", on, err)
	}
	if len(visible.send) != 1 || len(hidden.send) != 0 {
		t.Errorf("sent to visible %d, hidden %d; want 1, 0", len(visible.send), len(hidden.send))
	}
}
