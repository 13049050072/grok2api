package tunnelproxy

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	_ "github.com/bdandy/go-socks4"
	xproxy "golang.org/x/net/proxy"
)

// SOCKSHandshakeTimeout bounds the SOCKS proxy handshake.
const SOCKSHandshakeTimeout = 10 * time.Second

func IsSOCKSScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "socks4", "socks4a", "socks5", "socks5h":
		return true
	default:
		return false
	}
}

// SOCKSDialer dials through a SOCKS4/4A/5/5H proxy. Every proxy connection
// carries a deadline until the SOCKS handshake completes, so a proxy that
// accepts TCP but never answers cannot pin a goroutine or socket. SOCKS4
// dialers ignore contexts: cancellation returns immediately and the bounded
// handshake finishes in the background, closing its connection.
type SOCKSDialer struct {
	dialer  xproxy.Dialer
	timeout time.Duration
}

// NewSOCKSDialer uses forward to reach the proxy endpoint. A nil forward uses a
// plain TCP dialer and a non-positive timeout uses SOCKSHandshakeTimeout.
func NewSOCKSDialer(proxyURL *url.URL, forward *net.Dialer, timeout time.Duration) (*SOCKSDialer, error) {
	if proxyURL == nil || !IsSOCKSScheme(proxyURL.Scheme) {
		return nil, errors.New("SOCKS 代理地址无效")
	}
	if timeout <= 0 {
		timeout = SOCKSHandshakeTimeout
	}
	if forward == nil {
		forward = newServerNetDialer()
	}
	dialer, err := xproxy.FromURL(proxyURL, &handshakeDeadlineDialer{dialer: forward, timeout: timeout})
	if err != nil {
		return nil, err
	}
	return &SOCKSDialer{dialer: dialer, timeout: timeout}, nil
}

func (d *SOCKSDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

func (d *SOCKSDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d == nil || d.dialer == nil {
		return nil, errors.New("SOCKS 代理拨号器未初始化")
	}
	if contextual, ok := d.dialer.(xproxy.ContextDialer); ok {
		// x/net replaces the connection deadline with the context deadline
		// during negotiation, so the context must carry the handshake bound.
		handshakeCtx, cancel := context.WithTimeout(ctx, d.timeout)
		defer cancel()
		return clearHandshakeDeadline(contextual.DialContext(handshakeCtx, network, address))
	}
	type result struct {
		connection net.Conn
		err        error
	}
	completed := make(chan result, 1)
	go func() {
		connection, err := clearHandshakeDeadline(d.dialer.Dial(network, address))
		completed <- result{connection: connection, err: err}
	}()
	select {
	case value := <-completed:
		return value.connection, value.err
	case <-ctx.Done():
		go func() {
			if value := <-completed; value.connection != nil {
				_ = value.connection.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

func clearHandshakeDeadline(connection net.Conn, err error) (net.Conn, error) {
	if err != nil {
		if connection != nil {
			_ = connection.Close()
		}
		return nil, err
	}
	if connection == nil {
		return nil, errors.New("SOCKS 代理未返回连接")
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}

type handshakeDeadlineDialer struct {
	dialer  *net.Dialer
	timeout time.Duration
}

func (d *handshakeDeadlineDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

func (d *handshakeDeadlineDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	connection, err := d.dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	if err := connection.SetDeadline(time.Now().Add(d.timeout)); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}
