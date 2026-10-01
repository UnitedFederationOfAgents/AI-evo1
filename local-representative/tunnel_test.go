package main

import (
	"io"
	"net"
	"testing"
	"time"
)

// TestSingleConnListenerAcceptsOnceThenUnblocksOnClose covers the
// synchronization singleConnListener exists for (see serveTunnel, Revision I
// of condocs/initialDistributedSessionsImpls/Step1SubstepBPrompt.md): its
// first Accept hands back the one connection it wraps, a second call blocks,
// and closing the accepted connection (as net/http does once the peer
// disconnects) unblocks that pending call with io.EOF so http.Serve returns
// and a caller can redial a replacement tunnel.
func TestSingleConnListenerAcceptsOnceThenUnblocksOnClose(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	l := newSingleConnListener(server)

	first, err := l.Accept()
	if err != nil {
		t.Fatalf("first Accept: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("second Accept returned early (%v) before the first conn was closed", err)
	case <-time.After(50 * time.Millisecond):
		// expected: still blocked
	}

	if err := first.Close(); err != nil {
		t.Fatalf("closing the first accepted conn: %v", err)
	}

	select {
	case err := <-done:
		if err != io.EOF {
			t.Errorf("second Accept returned %v, want io.EOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Accept never returned after the accepted conn was closed")
	}

	// Close is idempotent -- a caller (or another peer-close race) calling it
	// again must not panic or block.
	if err := l.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
