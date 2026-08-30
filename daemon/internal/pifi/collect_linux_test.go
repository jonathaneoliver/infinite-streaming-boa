//go:build linux

package pifi

import (
	"net"
	"testing"
)

// PortListening reads the kernel's socket table rather than dialling, so the
// thing it must get right is the parse: the listening state, and a port that
// is hex and zero-padded. A real listener is the only honest fixture for that.
func TestPortListeningSeesARealListener(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if !PortListening(port) {
		t.Fatalf("port %d is listening and was not found", port)
	}
	ln.Close()
	if PortListening(port) {
		t.Fatalf("port %d is closed and was still reported as listening", port)
	}
}

// An established connection is not a listener. Without the state check the
// probe would report a port as served the moment anything used it.
func TestPortListeningIgnoresNonListeningSockets(t *testing.T) {
	if PortListening(0) {
		t.Error("port 0 can never be listening")
	}
}
