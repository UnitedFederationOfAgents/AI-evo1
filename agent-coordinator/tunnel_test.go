package main

import (
	"context"
	"net"
	"testing"
	"time"

	"representable"
)

// freeLoopbackAddr returns a loopback "host:port" that's momentarily bound
// then released, so dialing it immediately afterward reliably fails fast
// (connection refused) rather than timing out -- used below as a dial target
// that must never actually succeed, so a successful DialContext call can only
// mean it claimed the pooled tunnel instead.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// TestHostProxyTransportPrefersTunnel is an end-to-end (real TCP loopback)
// check of the NAT fix added in Step1SubstepBPrompt.md Revision I:
// newHostProxyTransport's DialContext, given a host id that has a pooled
// tunnel connection parked (opened by an LR via representable.DialTunnel),
// claims it instead of dialing the (here, deliberately unreachable) resolved
// address.
func TestHostProxyTransportPrefersTunnel(t *testing.T) {
	reprAddr := freeLoopbackAddr(t)
	reprSrv, err := representable.NewServer(reprAddr, representable.ModeOps)
	if err != nil {
		t.Fatalf("representable.NewServer: %v", err)
	}

	tunnelConn, err := representable.DialTunnel(reprAddr, "host-a", time.Second)
	if err != nil {
		t.Fatalf("DialTunnel: %v", err)
	}
	defer tunnelConn.Close()

	s := newServer()
	s.reprServer = reprSrv

	deadTarget := freeLoopbackAddr(t) // nothing listens here -- see freeLoopbackAddr
	base := context.WithValue(context.Background(), hostIDContextKey{}, "host-a")

	deadline := time.Now().Add(2 * time.Second)
	var claimed net.Conn
	for time.Now().Before(deadline) {
		// Bound each attempt well under hostDialTimeout so a slow-to-refuse
		// loopback dial (unlikely, but not guaranteed by any environment)
		// can't eat the whole 2s budget in one try.
		attemptCtx, cancel := context.WithTimeout(base, 100*time.Millisecond)
		c, err := s.hostProxyTransport.DialContext(attemptCtx, "tcp", deadTarget)
		cancel()
		if err == nil {
			claimed = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if claimed == nil {
		t.Fatal("DialContext never claimed the parked tunnel; it should have preferred it over dialing the unreachable target")
	}
	defer claimed.Close()

	// Once claimed, the server no longer has anything parked for host-a.
	if _, ok := reprSrv.ClaimTunnel("host-a"); ok {
		t.Error("tunnel should have been removed from the pool once DialContext claimed it")
	}
}

// TestHostProxyTransportFallsBackWithoutTunnel verifies DialContext still
// dials directly -- unchanged from before Revision I -- for a host id with
// nothing parked (no representable server at all, or a host that just hasn't
// opened a tunnel yet), so existing non-NAT'd/local setups and tests keep
// working.
func TestHostProxyTransportFallsBackWithoutTunnel(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer backend.Close()
	go func() {
		for {
			c, err := backend.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	s := newServer() // s.reprServer is nil -- no tunnel pool to consult at all
	ctx := context.WithValue(context.Background(), hostIDContextKey{}, "host-a")

	conn, err := s.hostProxyTransport.DialContext(ctx, "tcp", backend.Addr().String())
	if err != nil {
		t.Fatalf("DialContext should have fallen back to a direct dial, got: %v", err)
	}
	conn.Close()
}
