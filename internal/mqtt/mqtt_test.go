package mqtt

import (
	"net"
	"testing"
)

func TestPublishOverLocalTCPListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	received := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 512)
		n, _ := conn.Read(buf)
		_, _ = conn.Write([]byte{0x20, 0x02, 0x00, 0x00})
		n2, _ := conn.Read(buf[n:])
		received <- buf[:n+n2]
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	c := New("127.0.0.1", port, "prusa/printer-hawk")
	if !c.Enabled() {
		t.Fatal("expected Enabled() to be true")
	}
	if err := c.Publish("alert", []byte("ON"), true); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	pkt := <-received
	if len(pkt) == 0 || c.LastError() != "" {
		t.Fatalf("unexpected publish result: len=%d err=%q", len(pkt), c.LastError())
	}
}

func TestDisabledNoOp(t *testing.T) {
	c := New("", 1883, "")
	if c.Enabled() {
		t.Fatal("expected disabled client")
	}
	if err := c.Publish("alert", []byte("OFF"), true); err != nil {
		t.Fatalf("expected nil error for disabled client, got %v", err)
	}
}
