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

// autoConnectLR maintains condoccer's representable connection to
// local-representative. It retries every autoConnectInterval for up to
// autoConnectWindow to establish the link; once connected it pushes the current
// condoc summary and blocks until the connection drops, then starts a fresh
// window. Runs in its own goroutine.
func (s *Server) autoConnectLR(host, port string) {
	addr := net.JoinHostPort(host, port)
	for {
		deadline := time.Now().Add(autoConnectWindow)
		var client *representable.Client
		for client == nil {
			c, err := representable.Connect(addr, s.name, autoConnectDialTimeout)
			if err == nil {
				client = c
				break
			}
			if time.Now().After(deadline) {
				log.Printf("auto-connect: gave up after %s — local-representative at %s did not respond",
					autoConnectWindow, addr)
				return
			}
			time.Sleep(autoConnectInterval)
		}

		log.Printf("connected to local-representative at %s as %q", addr, s.name)
		s.reprMu.Lock()
		s.reprClient = client
		s.reprMu.Unlock()

		client.SetCommandHandler(s.handleReprCommand)
		s.pushCondoccerState()

		<-client.DisconnectCh()

		s.reprMu.Lock()
		if s.reprClient == client {
			s.reprClient = nil
		}
		s.reprMu.Unlock()
		log.Printf("disconnected from local-representative at %s — retrying", addr)
	}
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
