package egress

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestBrowserClientSOCKSDialHonorsContext(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	released := make(chan struct{}, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = io.Copy(io.Discard, connection)
		released <- struct{}{}
	}()
	browser, err := newBrowserClient("socks5://"+listener.Addr().String(), DefaultUserAgent)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	dialed := make(chan error, 1)
	go func() {
		connection, err := browser.inner.GetDialer().DialContext(ctx, "tcp", "203.0.113.10:443")
		if connection != nil {
			_ = connection.Close()
		}
		dialed <- err
	}()
	select {
	case err := <-dialed:
		if err == nil {
			t.Fatal("silent SOCKS proxy completed a handshake")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("browser SOCKS dial ignored its context")
	}
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("browser SOCKS dial kept the proxy connection open")
	}
}
