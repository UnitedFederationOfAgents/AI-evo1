package main

import (
	"log"
	"net"
	"time"

	"representable"
	ufaversion "ufa-version"
)

// Auto-connect (--auto-connect) tuning: on startup diagrammographer dials
// local-representative in the background, retrying on an interval until the
// window elapses. Mirrors composer's --auto-connect so it joins the same
// autolaunch chain.
const (
	autoConnectInterval    = 10 * time.Second
	autoConnectWindow      = 10 * time.Minute
	autoConnectDialTimeout = 3 * time.Second
)

// ReprStatusMsg is the "repr-status" WebSocket payload pushed to the frontend
// so its manual connect/disconnect widget reflects the actual link to
// local-representative, whichever of --auto-connect or the widget started it.
type ReprStatusMsg struct {
	Status string `json:"status"` // "disconnected" | "connecting" | "connected"
	Host   string `json:"host,omitempty"`
	Port   string `json:"port,omitempty"`
	// AutoConnect is the persistent auto-connect toggle: true whenever the
	// cycle is armed, whether or not it's currently connected/connecting.
	AutoConnect bool `json:"auto_connect,omitempty"`
}

// SelfInfoMsg discloses this instance's own dev-mode status and build
// version to its frontend (see docs/DevMode.md) -- sent once when a browser
// client connects.
type SelfInfoMsg struct {
	DevMode bool   `json:"dev_mode"`
	Version string `json:"version"`
}

// ModeMismatchMsg discloses that the connected local-representative's
// dev-mode status differs from this instance's own. Mismatched=false clears
// a previously-disclosed mismatch.
type ModeMismatchMsg struct {
	Mismatched bool   `json:"mismatched"`
	PeerMode   string `json:"peer_mode,omitempty"`
}

// setModeMismatch records the current mismatch verdict against
// local-representative and broadcasts it to every connected browser client.
func (s *Server) setModeMismatch(mismatched bool, peerMode string) {
	s.reprMu.Lock()
	s.modeMismatch = mismatched
	s.modeMismatchPeer = peerMode
	s.reprMu.Unlock()
	s.broadcast("mode-mismatch", ModeMismatchMsg{Mismatched: mismatched, PeerMode: peerMode})
}

// sendModeMismatch sends the current mismatch verdict to a single (usually
// newly-connected) WebSocket client.
func (s *Server) sendModeMismatch(c *wsClient) {
	s.reprMu.Lock()
	mismatched, peerMode := s.modeMismatch, s.modeMismatchPeer
	s.reprMu.Unlock()
	s.sendToClient(c, "mode-mismatch", ModeMismatchMsg{Mismatched: mismatched, PeerMode: peerMode})
}

// startConnectLoop launches a fresh connectLoop dialling host:port, first
// stopping any loop already running (an earlier --auto-connect or widget
// "connect").
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
// retry, and closes an active connection if there is one. Unlike
// disconnectRepr it leaves the auto-connect toggle untouched.
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
		s.setModeMismatch(false, "")
	}
}

// disconnectRepr is the widget's explicit "disconnect" action. Being
// operator-driven, it also turns auto-connect off.
func (s *Server) disconnectRepr() {
	s.reprMu.Lock()
	s.reprAutoConnect = false
	s.reprMu.Unlock()
	s.stopConnectLoop()
}

// setAutoConnect drives the persistent auto-connect toggle from the frontend
// widget, or from --auto-connect at startup. Enabling it arms the flag and
// starts a connectLoop unless one is already running; disabling it only
// stops that cycle from resuming. It never forces an active connection down;
// only disconnectRepr does that.
func (s *Server) setAutoConnect(enabled bool, host, port string) {
	s.reprMu.Lock()
	s.reprAutoConnect = enabled
	if host == "" {
		host = s.reprHost
	}
	if port == "" {
		port = s.reprPort
	}
	running := s.reprStop != nil
	status := s.reprStatus
	s.reprMu.Unlock()

	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "8082"
	}

	if enabled && !running {
		s.startConnectLoop(host, port)
		return
	}
	s.setReprStatus(status)
}

// connectLoop maintains the representable connection to
// local-representative: it retries every autoConnectInterval for up to
// autoConnectWindow, says hello as a plug-in once connected, and blocks
// until the connection drops. It gives up as soon as stopCh is closed. Runs
// in its own goroutine.
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
			c, err := representable.Connect(addr, s.name, representable.Mode(s.devMode), autoConnectDialTimeout)
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
				s.reprMu.Lock()
				if s.reprStop == stopCh {
					s.reprStop = nil
				}
				s.reprMu.Unlock()
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

		client.SetModeMismatchHandler(func(mismatched bool, peerMode string) {
			s.setModeMismatch(mismatched, peerMode)
		})
		s.sendPluginHello() // see plugin.go
		s.sendVersion()

		<-client.DisconnectCh()

		s.reprMu.Lock()
		if s.reprClient == client {
			s.reprClient = nil
		}
		autoConnect := s.reprAutoConnect
		s.reprMu.Unlock()
		s.setModeMismatch(false, "")

		select {
		case <-stopCh:
			s.setReprStatus("disconnected")
			return
		default:
		}
		if !autoConnect {
			log.Printf("disconnected from local-representative at %s", addr)
			s.reprMu.Lock()
			if s.reprStop == stopCh {
				s.reprStop = nil
			}
			s.reprMu.Unlock()
			s.setReprStatus("disconnected")
			return
		}
		log.Printf("disconnected from local-representative at %s — auto-connect resuming the retry cycle", addr)
	}
}

// setReprStatus records the current connection status and pushes it to every
// WebSocket client so the manual connect/disconnect widget stays live.
func (s *Server) setReprStatus(status string) {
	s.reprMu.Lock()
	s.reprStatus = status
	host, port, autoConnect := s.reprHost, s.reprPort, s.reprAutoConnect
	s.reprMu.Unlock()
	s.broadcast("repr-status", ReprStatusMsg{Status: status, Host: host, Port: port, AutoConnect: autoConnect})
}

// sendReprStatus sends the current connection status to a single (usually
// newly-connected) WebSocket client.
func (s *Server) sendReprStatus(c *wsClient) {
	s.reprMu.Lock()
	status, host, port, autoConnect := s.reprStatus, s.reprHost, s.reprPort, s.reprAutoConnect
	s.reprMu.Unlock()
	s.sendToClient(c, "repr-status", ReprStatusMsg{Status: status, Host: host, Port: port, AutoConnect: autoConnect})
}

// versionPayload is sent once over the representable data channel right
// after connecting, so local-representative's system tab can list this
// instance's build version alongside its own (see docs/DevMode.md
// "Versioning").
type versionPayload struct {
	Version string `json:"version"`
}

// sendVersion reports this binary's build version to local-representative.
func (s *Server) sendVersion() {
	s.reprMu.Lock()
	client := s.reprClient
	s.reprMu.Unlock()
	if client == nil {
		return
	}
	client.SendData("version", versionPayload{Version: ufaversion.Version})
}
