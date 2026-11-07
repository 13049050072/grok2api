package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedProxiesForwardedHTTPS(t *testing.T) {
	proxies := NewTrustedProxies([]string{"10.0.0.0/8", " 192.0.2.10 ", "::ffff:198.51.100.7", "2001:db8::/32", "invalid", "300.0.0.0/8"})
	tests := []struct {
		name       string
		remoteAddr string
		proto      string
		want       bool
	}{
		{name: "trusted cidr", remoteAddr: "10.1.2.3:443", proto: "https", want: true},
		{name: "case insensitive", remoteAddr: "10.1.2.3:443", proto: "HTTPS", want: true},
		{name: "first value wins", remoteAddr: "10.1.2.3:443", proto: " https , http", want: true},
		{name: "first value http", remoteAddr: "10.1.2.3:443", proto: "http, https"},
		{name: "plain http", remoteAddr: "10.1.2.3:443", proto: "http"},
		{name: "missing header", remoteAddr: "10.1.2.3:443"},
		{name: "trusted bare ip", remoteAddr: "192.0.2.10:443", proto: "https", want: true},
		{name: "bare ip is single host", remoteAddr: "192.0.2.11:443", proto: "https"},
		{name: "mapped bare ip matches ipv4 peer", remoteAddr: "198.51.100.7:443", proto: "https", want: true},
		{name: "trusted ipv6 cidr", remoteAddr: "[2001:db8::1]:443", proto: "https", want: true},
		{name: "untrusted peer", remoteAddr: "203.0.113.9:443", proto: "https"},
		{name: "untrusted ipv6 peer", remoteAddr: "[2001:db9::1]:443", proto: "https"},
		{name: "malformed remote address", remoteAddr: "10.1.2.3", proto: "https"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = test.remoteAddr
			if test.proto != "" {
				request.Header.Set("X-Forwarded-Proto", test.proto)
			}
			if got := proxies.ForwardedHTTPS(request); got != test.want {
				t.Fatalf("ForwardedHTTPS(%s, %q) = %v, want %v", test.remoteAddr, test.proto, got, test.want)
			}
		})
	}
}

func TestEmptyTrustedProxiesNeverTrustForwardedProto(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "127.0.0.1:443"
	request.Header.Set("X-Forwarded-Proto", "https")
	if NewTrustedProxies(nil).ForwardedHTTPS(request) || (TrustedProxies{}).ForwardedHTTPS(request) {
		t.Fatal("forwarded proto trusted without configured proxies")
	}
	if NewTrustedProxies([]string{"127.0.0.1"}).ForwardedHTTPS(nil) {
		t.Fatal("nil request reported HTTPS")
	}
}
