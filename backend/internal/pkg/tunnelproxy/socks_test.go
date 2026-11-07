package tunnelproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"runtime"
	"testing"
	"time"
)

// startSilentSOCKSProxy accepts TCP connections and never answers the SOCKS
// handshake. Each accepted connection reports on closed once the dialer
// released it.
func startSilentSOCKSProxy(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{}, 16)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(io.Discard, connection)
				closed <- struct{}{}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().String(), closed
}

func waitForProxyRelease(t *testing.T, closed <-chan struct{}, within time.Duration) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(within):
		t.Fatalf("proxy connection was not released within %s", within)
	}
}

func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, want <= %d", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSOCKSDialerBoundsSilentProxyHandshake(t *testing.T) {
	for _, scheme := range []string{"socks4", "socks4a", "socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			address, closed := startSilentSOCKSProxy(t)
			dialer, err := NewSOCKSDialer(&url.URL{Scheme: scheme, Host: address}, nil, 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			baseline := runtime.NumGoroutine()
			started := time.Now()
			connection, err := dialer.DialContext(context.Background(), "tcp", "203.0.113.10:443")
			if err == nil {
				_ = connection.Close()
				t.Fatal("silent SOCKS proxy completed a handshake")
			}
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("handshake took %s despite a 100ms bound", elapsed)
			}
			waitForProxyRelease(t, closed, 2*time.Second)
			waitForGoroutines(t, baseline)
		})
	}
}

func TestSOCKSDialerCancellationReleasesContextFreeHandshake(t *testing.T) {
	address, closed := startSilentSOCKSProxy(t)
	dialer, err := NewSOCKSDialer(&url.URL{Scheme: "socks4a", Host: address}, nil, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := dialer.DialContext(ctx, "tcp", "203.0.113.10:443"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("cancelled dial returned after %s", elapsed)
	}
	waitForProxyRelease(t, closed, 2*time.Second)
	waitForGoroutines(t, baseline)
}

func TestSOCKSDialerClearsHandshakeDeadlineAfterNegotiation(t *testing.T) {
	for _, scheme := range []string{"socks4a", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			const timeout = 100 * time.Millisecond
			served := make(chan error, 1)
			go func() {
				connection, err := listener.Accept()
				if err != nil {
					served <- err
					return
				}
				defer connection.Close()
				reader := bufio.NewReader(connection)
				if scheme == "socks5" {
					err = acceptSOCKS5Connect(reader, connection)
				} else {
					err = acceptSOCKS4Connect(reader, connection)
				}
				if err != nil {
					served <- err
					return
				}
				time.Sleep(3 * timeout)
				_, err = connection.Write([]byte("ok"))
				served <- err
			}()
			dialer, err := NewSOCKSDialer(&url.URL{Scheme: scheme, Host: listener.Addr().String()}, nil, timeout)
			if err != nil {
				t.Fatal(err)
			}
			connection, err := dialer.DialContext(context.Background(), "tcp", "203.0.113.10:443")
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			buffer := make([]byte, 2)
			if _, err := io.ReadFull(connection, buffer); err != nil || string(buffer) != "ok" {
				t.Fatalf("read after handshake = %q, %v", buffer, err)
			}
			if err := <-served; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func acceptSOCKS4Connect(reader *bufio.Reader, writer io.Writer) error {
	header := make([]byte, 8)
	if _, err := io.ReadFull(reader, header); err != nil {
		return err
	}
	if header[0] != 4 || header[1] != 1 {
		return errors.New("unexpected SOCKS4 request")
	}
	if _, err := reader.ReadString(0); err != nil {
		return err
	}
	if header[4] == 0 && header[5] == 0 && header[6] == 0 && header[7] != 0 {
		if _, err := reader.ReadString(0); err != nil {
			return err
		}
	}
	_, err := writer.Write([]byte{0, 0x5a, 0, 0, 0, 0, 0, 0})
	return err
}

func acceptSOCKS5Connect(reader *bufio.Reader, writer io.Writer) error {
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(reader, greeting); err != nil {
		return err
	}
	if _, err := io.ReadFull(reader, make([]byte, greeting[1])); err != nil {
		return err
	}
	if _, err := writer.Write([]byte{5, 0}); err != nil {
		return err
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(reader, request); err != nil {
		return err
	}
	var addressLength int
	switch request[3] {
	case 1:
		addressLength = 4
	case 4:
		addressLength = 16
	case 3:
		length, err := reader.ReadByte()
		if err != nil {
			return err
		}
		addressLength = int(length)
	default:
		return errors.New("unexpected SOCKS5 address type")
	}
	if _, err := io.ReadFull(reader, make([]byte, addressLength+2)); err != nil {
		return err
	}
	_, err := writer.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}
