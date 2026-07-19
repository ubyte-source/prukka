package control_test

import (
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/control"
)

// handshake runs a TLS handshake over an in-memory pipe; without the
// deadlines a rejected handshake deadlocks the synchronous pipe.
func handshake(t *testing.T, server, client *tls.Config) error {
	t.Helper()

	serverConn, clientConn := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)

	if err := serverConn.SetDeadline(deadline); err != nil {
		t.Fatalf("set server deadline: %v", err)
	}

	if err := clientConn.SetDeadline(deadline); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}

	done := make(chan error, 1)

	go func() {
		s := tls.Server(serverConn, server)
		done <- s.HandshakeContext(t.Context())
	}()

	c := tls.Client(clientConn, client)
	clientErr := c.HandshakeContext(t.Context())

	serverErr := <-done

	if closeErr := serverConn.Close(); closeErr != nil {
		t.Logf("closing server pipe: %v", closeErr)
	}

	if closeErr := clientConn.Close(); closeErr != nil {
		t.Logf("closing client pipe: %v", closeErr)
	}

	if clientErr != nil {
		return clientErr
	}

	return serverErr
}

func TestIPCTLSPinnedHandshake(t *testing.T) {
	t.Parallel()

	state := t.TempDir()

	server, err := control.ServerIPCTLS(state)
	if err != nil {
		t.Fatalf("ServerIPCTLS returned error: %v", err)
	}

	client, clientErr := control.ClientIPCTLS(state)
	if clientErr != nil {
		t.Fatalf("ClientIPCTLS returned error: %v", clientErr)
	}

	if hsErr := handshake(t, server, client); hsErr != nil {
		t.Fatalf("pinned handshake failed: %v", hsErr)
	}

	// A second load must reuse the minted keypair, not replace it.
	again, againErr := control.ServerIPCTLS(state)
	if againErr != nil {
		t.Fatalf("second ServerIPCTLS returned error: %v", againErr)
	}

	if !again.Certificates[0].Leaf.Equal(server.Certificates[0].Leaf) {
		t.Fatal("second load minted a different certificate")
	}
}

func TestIPCTLSRejectsForeignCertificate(t *testing.T) {
	t.Parallel()

	server, err := control.ServerIPCTLS(t.TempDir())
	if err != nil {
		t.Fatalf("ServerIPCTLS returned error: %v", err)
	}

	// Mint a second, unrelated install and pin the client to it: the
	// handshake against the first daemon must fail.
	otherState := t.TempDir()
	if _, mintErr := control.ServerIPCTLS(otherState); mintErr != nil {
		t.Fatalf("minting foreign install: %v", mintErr)
	}

	client, clientErr := control.ClientIPCTLS(otherState)
	if clientErr != nil {
		t.Fatalf("ClientIPCTLS returned error: %v", clientErr)
	}

	if hsErr := handshake(t, server, client); hsErr == nil {
		t.Fatal("handshake with a foreign certificate succeeded, want pin failure")
	}
}

func TestClientIPCTLSNeedsTheDaemonCertificate(t *testing.T) {
	t.Parallel()

	if _, err := control.ClientIPCTLS(t.TempDir()); err == nil {
		t.Fatal("ClientIPCTLS succeeded without a pinned certificate")
	}
}
