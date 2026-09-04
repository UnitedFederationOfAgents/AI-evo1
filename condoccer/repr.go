package main

import (
	"encoding/json"
	"log"
	"net"
	"strings"
	"time"

	"representable"
)

// Auto-connect (--auto-connect) tuning: on startup condoccer dials
// local-representative in the background, retrying on an interval until the
// window elapses. Mirrors federation-command's and local-representative's
// --auto-connect so condoccer joins the same autolaunch chain.
const (
	autoConnectInterval    = 10 * time.Second
	autoConnectWindow      = 10 * time.Minute
	autoConnectDialTimeout = 3 * time.Second
)

// CondoccerStateMsg is the "condoccer-state" data payload condoccer pushes to
// local-representative (which forwards a copy up to agent-coordinator). It lets
// the rest of the stack show a condoc summary for this box and know which port
// to reverse-proxy the condoccer UI from.
type CondoccerStateMsg struct {
	HTTPPort string       `json:"http_port"`
	Root     string       `json:"root"`
	Condocs  []CondocInfo `json:"condocs"`
}

// ReprStatusMsg is the "repr-status" WebSocket payload pushed to the frontend
// so its manual connect/disconnect widget reflects condoccer's actual link to
// local-representative, whichever of --auto-connect or the widget started it.
type ReprStatusMsg struct {
	Status string `json:"status"` // "disconnected" | "connecting" | "connected"
	Host   string `json:"host,omitempty"`
	Port   string `json:"port,omitempty"`
}

// startConnectLoop launches a fresh connectLoop dialling host:port, first
// stopping any loop already running (an earlier --auto-connect or widget
// "connect"). Used both for --auto-connect at startup and for the frontend's
// manual "Connect" button.
func (s *Server) startConnectLoop(host, port string) {
	s.stopConnectLoop()
	stopCh := make(chan struct{})
	s.reprMu.Lock()
	s.reprStop = stopCh
	s.reprHost = host
	s.reprPort = port
	s.reprMu.Unlock()
	go s.connectLoop(host, port, stopCh)
}

// stopConnectLoop signals any running connectLoop to give up rather than
// retry, and closes an active connection if there is one. It's the
// "disconnect" half of the manual widget; startConnectLoop also calls it
// first so a fresh "connect" replaces rather than layers on a prior attempt.
func (s *Server) stopConnectLoop() {
	s.reprMu.Lock()
	stopCh := s.reprStop
	client := s.reprClient
	s.reprStop = nil
	s.reprClient = nil
	s.reprMu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
	if client != nil {
		client.Close()
	}
	if stopCh != nil || client != nil {
		s.setReprStatus("disconnected")
	}
}

// connectLoop maintains condoccer's representable connection to
// local-representative. It retries every autoConnectInterval for up to
// autoConnectWindow to establish the link; once connected it pushes the current
// condoc summary and blocks until the connection drops, then starts a fresh
// window. It gives up instead of retrying as soon as stopCh is closed — that's
// how a widget "disconnect" (or a replacing "connect") ends a previous loop.
// Runs in its own goroutine.
func (s *Server) connectLoop(host, port string, stopCh chan struct{}) {
	addr := net.JoinHostPort(host, port)
	for {
		select {
		case <-stopCh:
			return
		default:
		}

		s.setReprStatus("connecting")
		deadline := time.Now().Add(autoConnectWindow)
		var client *representable.Client
		for client == nil {
			c, err := representable.Connect(addr, s.name, autoConnectDialTimeout)
			if err == nil {
				client = c
				break
			}
			select {
			case <-stopCh:
				s.setReprStatus("disconnected")
				return
			default:
			}
			if time.Now().After(deadline) {
				log.Printf("connect: gave up after %s — local-representative at %s did not respond",
					autoConnectWindow, addr)
				s.setReprStatus("disconnected")
				return
			}
			time.Sleep(autoConnectInterval)
		}

		log.Printf("connected to local-representative at %s as %q", addr, s.name)
		s.reprMu.Lock()
		s.reprClient = client
		s.reprMu.Unlock()
		s.setReprStatus("connected")

		client.SetCommandHandler(s.handleReprCommand)
		s.pushCondoccerState()

		<-client.DisconnectCh()

		s.reprMu.Lock()
		if s.reprClient == client {
			s.reprClient = nil
		}
		s.reprMu.Unlock()

		select {
		case <-stopCh:
			s.setReprStatus("disconnected")
			return
		default:
		}
		log.Printf("disconnected from local-representative at %s — retrying", addr)
	}
}

// setReprStatus records the current connection status and pushes it to every
// WebSocket client so the manual connect/disconnect widget stays live.
func (s *Server) setReprStatus(status string) {
	s.reprMu.Lock()
	s.reprStatus = status
	host, port := s.reprHost, s.reprPort
	s.reprMu.Unlock()
	s.broadcastReprStatus(status, host, port)
}

// broadcastReprStatus sends a "repr-status" message to every connected
// WebSocket client.
func (s *Server) broadcastReprStatus(status, host, port string) {
	msg := s.marshalMsg("repr-status", ReprStatusMsg{Status: status, Host: host, Port: port})
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// sendReprStatus sends the current connection status to a single (usually
// newly-connected) WebSocket client.
func (s *Server) sendReprStatus(c *wsClient) {
	s.reprMu.Lock()
	status, host, port := s.reprStatus, s.reprHost, s.reprPort
	s.reprMu.Unlock()
	s.sendToClient(c, "repr-status", ReprStatusMsg{Status: status, Host: host, Port: port})
}

// pushCondoccerState sends the current condoc summary to local-representative.
// No-op when not connected.
func (s *Server) pushCondoccerState() {
	s.reprMu.Lock()
	client := s.reprClient
	s.reprMu.Unlock()
	if client == nil {
		return
	}
	infos, _ := findCondocs(s.root)
	client.SendData("condoccer-state", CondoccerStateMsg{
		HTTPPort: s.httpPort,
		Root:     s.root,
		Condocs:  infos,
	})
}

// handleReprCommand handles commands local-representative relays down the
// representable channel (originating from agent-coordinator). condoccer only
// acts on the "__condoccer:" namespace, mirroring FC's "__ridealong:" and LR's
// "__system:" conventions; the forwarded WebSocket UI carries everything else.
//
//	__condoccer:action <json ActionRequest>
//	__condoccer:refresh
func (s *Server) handleReprCommand(raw string) {
	if !strings.HasPrefix(raw, "__condoccer:") {
		return
	}
	rest := strings.TrimSpace(strings.TrimPrefix(raw, "__condoccer:"))
	verb, arg, _ := strings.Cut(rest, " ")
	switch verb {
	case "action":
		var a ActionRequest
		if err := json.Unmarshal([]byte(strings.TrimSpace(arg)), &a); err != nil {
			log.Printf("repr: bad __condoccer:action payload: %v", err)
			return
		}
		if err := s.performAction(a); err != nil {
			log.Printf("repr: __condoccer:action %s failed: %v", a.Action, err)
			return
		}
		s.broadcastCondocUpdate(a.Path)
		s.pushCondoccerState()
	case "refresh":
		s.pushCondoccerState()
	default:
		log.Printf("repr: ignoring unrecognised command %q", raw)
	}
}
