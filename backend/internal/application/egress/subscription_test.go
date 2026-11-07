package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/pkg/tunnelproxy"
)

type subscriptionSyncRepositoryStub struct {
	OperationsRepository
	mu    sync.Mutex
	nodes map[uint64][]domain.Node
}

func (r *subscriptionSyncRepositoryStub) UpsertEgressNodesFromSource(_ context.Context, sourceID uint64, nodes []domain.Node) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[sourceID] = append([]domain.Node(nil), nodes...)
	return len(nodes), nil
}

func (r *subscriptionSyncRepositoryStub) UpdateEgressSourceSync(context.Context, uint64, time.Time, time.Time, int, string) error {
	return nil
}

func (r *subscriptionSyncRepositoryStub) nodesFor(sourceID uint64) []domain.Node {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.Node(nil), r.nodes[sourceID]...)
}

func TestParseProxySubscriptionAcceptsPlainAndBase64Lists(t *testing.T) {
	plain, skipped, err := parseProxySubscription(strings.Join([]string{
		"# proxy list",
		"http://user:pass@one.example:8080",
		"127.0.0.1:3128:legacy:password",
		"socks5h://two.example:1080",
		"http://user:pass@one.example:8080",
		"not a proxy",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 3 || skipped != 2 {
		t.Fatalf("plain entries=%d skipped=%d", len(plain), skipped)
	}
	if plain[1].ProxyURL != "http://legacy:password@127.0.0.1:3128" {
		t.Fatalf("legacy HTTP proxy = %q", plain[1].ProxyURL)
	}
	for _, entry := range plain {
		if entry.ProxyURL == "" || len(entry.Key) != 64 {
			t.Fatalf("unsafe parsed entry: %#v", entry)
		}
	}

	encodedInput := base64.RawStdEncoding.EncodeToString([]byte("https://three.example:8443\nsocks4a://four.example:1080\n"))
	encoded, encodedSkipped, err := parseProxySubscription(encodedInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 2 || encodedSkipped != 0 {
		t.Fatalf("base64 entries=%d skipped=%d", len(encoded), encodedSkipped)
	}
}

func TestParseProxySubscriptionPreservesLegacyAccountPlaceholder(t *testing.T) {
	content := strings.Join([]string{
		"127.0.0.1:8080:Default.{account}:pass:word",
		"http://Default.{account}:pass%3Aword@127.0.0.1:8080",
		"127.0.0.1:8080:user:{account}",
	}, "\n")
	for name, input := range map[string]string{
		"plain":  content,
		"base64": base64.StdEncoding.EncodeToString([]byte(content)),
	} {
		t.Run(name, func(t *testing.T) {
			entries, skipped, err := parseProxySubscription(input)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || skipped != 2 {
				t.Fatalf("entries=%d skipped=%d, want one entry, one duplicate and one invalid placeholder", len(entries), skipped)
			}
			if entries[0].ProxyURL != "http://Default.{account}:pass%3Aword@127.0.0.1:8080" {
				t.Fatalf("legacy account proxy = %q", entries[0].ProxyURL)
			}
		})
	}
}

func TestParseProxySubscriptionRejectsNoUsableEntries(t *testing.T) {
	if _, _, err := parseProxySubscription("# only comments\nfile:///tmp/proxies\n"); err == nil {
		t.Fatal("invalid proxy subscription was accepted")
	}
}

func TestParseProxySubscriptionImportsSupportedTunnelSchemes(t *testing.T) {
	vmess := "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(`{"v":"2","ps":"node","add":"proxy.example","port":"443","id":"123e4567-e89b-12d3-a456-426614174000","aid":"0","scy":"auto","net":"tcp"}`))
	entries, skipped, err := parseProxySubscription(strings.Join([]string{
		"http://proxy.example:8080",
		"trojan://password@proxy.example:443#remark",
		"vless://123e4567-e89b-12d3-a456-426614174000@proxy.example:443?encryption=none#remark",
		"ss://YWVzLTEyOC1nY206c2VjcmV0@proxy.example:8388#remark",
		vmess,
		"hysteria2://proxy.example:443",
		"tuic://user:pass@proxy.example:443",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 || skipped != 2 || entries[0].ProxyURL != "http://proxy.example:8080" {
		t.Fatalf("entries=%#v skipped=%d", entries, skipped)
	}
	for _, entry := range entries[1:] {
		if strings.Contains(entry.ProxyURL, "#") {
			t.Fatalf("subscription remark was retained: %q", entry.ProxyURL)
		}
	}
}

func TestParseProxySubscriptionAcceptsClashYAML(t *testing.T) {
	content := `
proxies:
  - name: http
    type: http
    server: http.example
    port: 8080
    username: user
    password: pass
  - name: socks
    type: socks5
    server: socks.example
    port: 1080
  - name: trojan
    type: trojan
    server: trojan.example
    port: 443
    password: secret
    network: ws
    sni: edge.example
    ws-opts:
      path: /ws
      headers:
        Host: edge.example
  - name: reality
    type: vless
    server: reality.example
    port: 443
    uuid: 123e4567-e89b-12d3-a456-426614174000
    network: tcp
    tls: true
    servername: edge.example
    flow: xtls-rprx-vision
    client-fingerprint: chrome
    alpn: [h2, http/1.1]
    reality-opts:
      public-key: SOW7P-17ibm_-kz-QUQwGGyitSbsa5wOmRGAigGvDH8
      short-id: 0123456789abcdef
  - name: shadowsocks
    type: ss
    server: ss.example
    port: 8388
    cipher: aes-128-gcm
    password: secret
  - name: vmess
    type: vmess
    server: vmess.example
    port: 443
    uuid: 123e4567-e89b-12d3-a456-426614174000
    alterId: 0
    cipher: auto
    network: ws
    tls: true
    servername: edge.example
    alpn: [h2, http/1.1]
    ws-opts:
      path: /vmess
      headers:
        Host: edge.example
  - name: ignored-hysteria
    type: hysteria2
    server: hy.example
    port: 443
    password: secret
  - name: ignored-tuic
    type: tuic
    server: tuic.example
    port: 443
    password: secret
proxy-groups:
  - name: auto
    type: select
    proxies: [http, socks]
`
	entries, skipped, err := parseProxySubscription(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 || skipped != 2 {
		t.Fatalf("Clash entries=%d skipped=%d values=%#v", len(entries), skipped, entries)
	}
	var realityConfig tunnelproxy.Config
	var vmessConfig tunnelproxy.Config
	for _, entry := range entries {
		if !strings.HasPrefix(entry.ProxyURL, "vless://") && !strings.HasPrefix(entry.ProxyURL, "vmess://") {
			continue
		}
		config, parseErr := tunnelproxy.Parse(entry.ProxyURL)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		switch config.Scheme {
		case "vless":
			realityConfig = config
		case "vmess":
			vmessConfig = config
		}
	}
	if realityConfig.Security != "reality" || realityConfig.Flow != "xtls-rprx-vision" || realityConfig.RealityPublicKey == "" || realityConfig.RealityShortID != "0123456789abcdef" || strings.Join(realityConfig.ALPN, ",") != "h2,http/1.1" {
		t.Fatalf("Clash Reality config = %#v", realityConfig)
	}
	if strings.Join(vmessConfig.ALPN, ",") != "h2,http/1.1" {
		t.Fatalf("Clash VMess config = %#v", vmessConfig)
	}
}

func TestParseProxySubscriptionSkipsMalformedClashEntriesIndividually(t *testing.T) {
	content := `
proxies:
  - type: http
    server: valid.example
    port: "8080"
  - type: hysteria2
    server: ignored.example
    port: invalid
`
	entries, skipped, err := parseProxySubscription(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || skipped != 1 || entries[0].ProxyURL != "http://valid.example:8080" {
		t.Fatalf("entries=%#v skipped=%d", entries, skipped)
	}
}

func TestPinSubscriptionRequestRewritesToResolvedIP(t *testing.T) {
	previous := subscriptionIPLookup
	subscriptionIPLookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.2.3.4")}, nil
	}
	t.Cleanup(func() { subscriptionIPLookup = previous })

	request, err := http.NewRequest(http.MethodGet, "https://pinned.example/list", nil)
	if err != nil {
		t.Fatal(err)
	}
	pinned, serverName, err := pinSubscriptionRequest(request, true)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.URL.Hostname() != "1.2.3.4" || pinned.URL.Port() != "443" {
		t.Fatalf("pinned url = %s", pinned.URL.Host)
	}
	if pinned.Host != "pinned.example" {
		t.Fatalf("host header = %q", pinned.Host)
	}
	if serverName != "pinned.example" {
		t.Fatalf("server name = %q", serverName)
	}
	if request.URL.Host != "pinned.example" || request.Host != "pinned.example" {
		t.Fatalf("caller request mutated: url host=%q host=%q", request.URL.Host, request.Host)
	}
}

func TestPinSubscriptionRequestRewritesHostOnlyForForwardProxiedHTTP(t *testing.T) {
	previous := subscriptionIPLookup
	subscriptionIPLookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.2.3.4")}, nil
	}
	t.Cleanup(func() { subscriptionIPLookup = previous })

	tests := []struct {
		target       string
		forwardProxy bool
		wantHost     string
	}{
		{target: "http://pinned.example/list", forwardProxy: true, wantHost: "1.2.3.4"},
		{target: "http://pinned.example:8080/list", forwardProxy: true, wantHost: "1.2.3.4:8080"},
		{target: "https://pinned.example/list", forwardProxy: true, wantHost: "pinned.example"},
		{target: "http://pinned.example/list", forwardProxy: false, wantHost: "pinned.example"},
		{target: "http://pinned.example:8080/list", forwardProxy: false, wantHost: "pinned.example:8080"},
	}
	for _, test := range tests {
		request, err := http.NewRequest(http.MethodGet, test.target, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Redirect hops arrive without Request.Host.
		request.Host = ""
		pinned, _, err := pinSubscriptionRequest(request, test.forwardProxy)
		if err != nil {
			t.Fatal(err)
		}
		if pinned.Host != test.wantHost || pinned.URL.Hostname() != "1.2.3.4" {
			t.Fatalf("%s forwardProxy=%v: host=%q url=%s, want host %q", test.target, test.forwardProxy, pinned.Host, pinned.URL.Host, test.wantHost)
		}
	}
}

// socksTargetListener completes a SOCKS5 CONNECT on every accepted connection
// and then hands it to the HTTP server, which plays the target.
type socksTargetListener struct {
	net.Listener
	mu      sync.Mutex
	targets []string
}

func (l *socksTargetListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		target, err := acceptSOCKS5Connect(connection)
		if err != nil {
			_ = connection.Close()
			continue
		}
		l.mu.Lock()
		l.targets = append(l.targets, target)
		l.mu.Unlock()
		return connection, nil
	}
}

func (l *socksTargetListener) Targets() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.targets...)
}

func acceptSOCKS5Connect(connection net.Conn) (string, error) {
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	defer connection.SetDeadline(time.Time{})
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(connection, greeting); err != nil {
		return "", err
	}
	if _, err := io.ReadFull(connection, make([]byte, greeting[1])); err != nil {
		return "", err
	}
	if _, err := connection.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return "", err
	}
	if header[3] != 1 {
		return "", errors.New("SOCKS target is not an IPv4 address")
	}
	address := make([]byte, 6)
	if _, err := io.ReadFull(connection, address); err != nil {
		return "", err
	}
	if _, err := connection.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return "", err
	}
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte(address[:4])), binary.BigEndian.Uint16(address[4:])).String(), nil
}

func TestSOCKSProxiedSubscriptionKeepsHostAcrossRelativeRedirect(t *testing.T) {
	previous := subscriptionIPLookup
	subscriptionIPLookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.2.3.4")}, nil
	}
	t.Cleanup(func() { subscriptionIPLookup = previous })

	var mu sync.Mutex
	var hosts []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		hosts = append(hosts, request.Host)
		mu.Unlock()
		if request.URL.Path == "/subscription" {
			http.Redirect(writer, request, "/final", http.StatusFound)
			return
		}
		_, _ = writer.Write([]byte("http://proxy.example:8080\n"))
	}))
	listener := &socksTargetListener{Listener: server.Listener}
	server.Listener = listener
	server.Start()
	defer server.Close()

	body, err := fetchProxySubscription(context.Background(), "http://pinned.example/subscription", "socks5://"+listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "proxy.example") {
		t.Fatalf("body = %q", body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hosts) != 2 || hosts[0] != "pinned.example" || hosts[1] != "pinned.example" {
		t.Fatalf("Host headers = %q", hosts)
	}
	targets := listener.Targets()
	if len(targets) == 0 {
		t.Fatal("SOCKS proxy saw no CONNECT")
	}
	for _, target := range targets {
		if target != "1.2.3.4:80" {
			t.Fatalf("SOCKS targets = %q", targets)
		}
	}
}

func TestPinnedSubscriptionTransportFollowsRelativeHTTPSRedirect(t *testing.T) {
	previous := subscriptionIPLookup
	subscriptionIPLookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.2.3.4")}, nil
	}
	t.Cleanup(func() { subscriptionIPLookup = previous })

	var mu sync.Mutex
	var seen []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		seen = append(seen, request.Host+"|"+request.TLS.ServerName)
		mu.Unlock()
		if request.URL.Path == "/subscription" {
			http.Redirect(writer, request, "/final", http.StatusFound)
			return
		}
		_, _ = writer.Write([]byte("ok"))
	}))
	var opened, closed atomic.Int32
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			opened.Add(1)
		case http.StateClosed, http.StateHijacked:
			closed.Add(1)
		}
	}
	listener := &socksTargetListener{Listener: server.Listener}
	server.Listener = listener
	server.StartTLS()
	defer server.Close()

	base, err := subscriptionTransport("socks5://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	base.TLSClientConfig = &tls.Config{RootCAs: roots}
	transport := &pinnedSubscriptionTransport{base: base}
	client := &http.Client{Transport: transport}
	response, err := client.Get("https://example.com/subscription")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(body) != "ok" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if response.Request.URL.String() != "https://example.com/final" {
		t.Fatalf("final request url = %s", response.Request.URL)
	}
	mu.Lock()
	if len(seen) != 2 || seen[0] != "example.com|example.com" || seen[1] != "example.com|example.com" {
		mu.Unlock()
		t.Fatalf("Host|SNI per hop = %q", seen)
	}
	mu.Unlock()
	for _, target := range listener.Targets() {
		if target != "1.2.3.4:443" {
			t.Fatalf("SOCKS targets = %q", listener.Targets())
		}
	}
	transport.mu.Lock()
	transports := len(transport.tlsByServerName)
	transport.mu.Unlock()
	if transports != 1 {
		t.Fatalf("TLS transports = %d, want one shared by both hops", transports)
	}
	client.CloseIdleConnections()
	deadline := time.Now().Add(2 * time.Second)
	for closed.Load() < opened.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("idle connections left open: opened=%d closed=%d", opened.Load(), closed.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestForwardProxiedSubscriptionPinsRelativeRedirect(t *testing.T) {
	previous := subscriptionIPLookup
	subscriptionIPLookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.2.3.4")}, nil
	}
	t.Cleanup(func() { subscriptionIPLookup = previous })

	var mu sync.Mutex
	var requestURIs []string
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requestURIs = append(requestURIs, request.RequestURI)
		mu.Unlock()
		if request.URL.Path == "/subscription" {
			http.Redirect(writer, request, "/final", http.StatusFound)
			return
		}
		_, _ = writer.Write([]byte("http://proxy.example:8080\n"))
	}))
	defer proxy.Close()

	body, err := fetchProxySubscription(context.Background(), "http://pinned.example/subscription", proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "proxy.example") {
		t.Fatalf("body = %q", body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requestURIs) != 2 || requestURIs[0] != "http://1.2.3.4/subscription" || requestURIs[1] != "http://1.2.3.4/final" {
		t.Fatalf("proxy request URIs = %q", requestURIs)
	}
}

func TestHTTPSForwardProxyHandshakeKeepsProxyServerName(t *testing.T) {
	previous := subscriptionIPLookup
	subscriptionIPLookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.2.3.4")}, nil
	}
	t.Cleanup(func() { subscriptionIPLookup = previous })

	target := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(request.Host + "|" + request.TLS.ServerName))
	}))
	defer target.Close()
	var mu sync.Mutex
	var proxyServerNames, connectTargets []string
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		proxyServerNames = append(proxyServerNames, request.TLS.ServerName)
		connectTargets = append(connectTargets, request.Host)
		mu.Unlock()
		if request.Method != http.MethodConnect {
			http.Error(writer, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		upstream, err := net.Dial("tcp", target.Listener.Addr().String())
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer connection.Close()
		if _, err := io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		go func() {
			_, _ = io.Copy(upstream, connection)
			_ = upstream.Close()
		}()
		_, _ = io.Copy(connection, upstream)
	}))
	defer proxy.Close()

	base, err := subscriptionTransport(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(proxy.Certificate())
	roots.AddCert(target.Certificate())
	base.TLSClientConfig = &tls.Config{RootCAs: roots}
	client := &http.Client{Transport: &pinnedSubscriptionTransport{base: base, forwardProxy: true}}
	defer client.CloseIdleConnections()
	response, err := client.Get("https://example.com/subscription")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(body) != "example.com|example.com" {
		t.Fatalf("target Host|SNI = %q, err=%v", body, err)
	}
	mu.Lock()
	defer mu.Unlock()
	// The proxy is addressed by IP, so its handshake carries no SNI; the
	// subscription host belongs only to the tunnelled handshake.
	if len(proxyServerNames) != 1 || proxyServerNames[0] != "" || connectTargets[0] != "1.2.3.4:443" {
		t.Fatalf("proxy SNI = %q, CONNECT targets = %q", proxyServerNames, connectTargets)
	}
}

func TestProxiedSubscriptionPinsResolvedAddress(t *testing.T) {
	previous := subscriptionIPLookup
	subscriptionIPLookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.2.3.4")}, nil
	}
	t.Cleanup(func() { subscriptionIPLookup = previous })

	var requestURI string
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestURI = request.RequestURI
		_, _ = writer.Write([]byte("http://proxy.example:8080\n"))
	}))
	defer proxy.Close()

	body, err := fetchProxySubscription(context.Background(), "http://pinned.example/subscription", proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requestURI, "1.2.3.4") || strings.Contains(requestURI, "pinned.example") {
		t.Fatalf("proxy request uri = %q", requestURI)
	}
	if !strings.Contains(string(body), "proxy.example") {
		t.Fatalf("body = %q", body)
	}
}

func TestFetchProxySubscriptionUsesClashUserAgent(t *testing.T) {
	var userAgent string
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		userAgent = request.Header.Get("User-Agent")
		_, _ = writer.Write([]byte("http://proxy.example:8080\n"))
	}))
	defer proxy.Close()

	body, err := fetchProxySubscription(context.Background(), "http://1.1.1.1/subscription", proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	if userAgent != "Clash.Meta" || !strings.Contains(string(body), "proxy.example") {
		t.Fatalf("User-Agent=%q body=%q", userAgent, body)
	}
}

func TestClashRealityPreservesSupportedClientFingerprints(t *testing.T) {
	content := `
proxies:
  - type: vless
    server: chrome.example
    port: 443
    uuid: 123e4567-e89b-12d3-a456-426614174000
    flow: xtls-rprx-vision
    client-fingerprint: chrome
    reality-opts: &reality
      public-key: SOW7P-17ibm_-kz-QUQwGGyitSbsa5wOmRGAigGvDH8
      short-id: 0123456789abcdef
  - type: vless
    server: edge.example
    port: 443
    uuid: 123e4567-e89b-12d3-a456-426614174000
    flow: xtls-rprx-vision
    client-fingerprint: edge
    reality-opts: *reality
  - type: vless
    server: safari.example
    port: 443
    uuid: 123e4567-e89b-12d3-a456-426614174000
    flow: xtls-rprx-vision
    client-fingerprint: safari
    reality-opts: *reality
`
	entries, skipped, err := parseProxySubscription(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || skipped != 0 {
		t.Fatalf("entries=%d skipped=%d", len(entries), skipped)
	}
	fingerprints := make(map[string]bool)
	for _, entry := range entries {
		config, parseErr := tunnelproxy.Parse(entry.ProxyURL)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		fingerprints[config.ClientFingerprint] = true
	}
	for _, fingerprint := range []string{"chrome", "edge", "safari"} {
		if !fingerprints[fingerprint] {
			t.Fatalf("missing client fingerprint %q", fingerprint)
		}
	}
}

func TestIsPublicAddressRejectsNonPublicRanges(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.1", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.10.1",
		"192.0.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "240.0.0.1",
		"::1", "fc00::1", "2001:db8::1", "::ffff:127.0.0.1",
		"64:ff9b::127.0.0.1", "64:ff9b:1::1", "100::1", "2002:7f00:1::", "3fff::1", "5f00::1",
	} {
		if isPublicAddress(netip.MustParseAddr(raw)) {
			t.Fatalf("non-public address accepted: %s", raw)
		}
	}
	if !isPublicAddress(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public address rejected")
	}
}

func TestValidatePublicSubscriptionTargetRejectsPrivateAddresses(t *testing.T) {
	for _, value := range []string{
		"http://127.0.0.1/subscription",
		"http://10.0.0.1/subscription",
		"http://169.254.169.254/latest/meta-data",
		"http://[::1]/subscription",
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data",
		"http://[2002:a9fe:a9fe::]/latest/meta-data",
	} {
		if err := validatePublicSubscriptionTarget(context.Background(), value); err == nil {
			t.Fatalf("private subscription target accepted: %s", value)
		}
	}
	for _, value := range []string{"https://1.1.1.1/subscription", "https://[2606:4700:4700::1111]/subscription"} {
		if err := validatePublicSubscriptionTarget(context.Background(), value); err != nil {
			t.Fatalf("public subscription target rejected: %s: %v", value, err)
		}
	}
}

func TestSubscriptionTransportSupportsConfiguredProxyProtocols(t *testing.T) {
	for _, proxyURL := range []string{
		"http://127.0.0.1:8080",
		"https://127.0.0.1:8443",
		"socks4://127.0.0.1:1080",
		"socks4a://127.0.0.1:1080",
		"socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080",
	} {
		transport, err := subscriptionTransport(proxyURL)
		if err != nil {
			t.Fatalf("proxy %s: %v", proxyURL, err)
		}
		transport.CloseIdleConnections()
	}
}

func TestSubscriptionFetchProxyRejectsCorruptSourceSecret(t *testing.T) {
	cipher, err := security.NewCipher("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{cipher: cipher}
	if _, err := service.subscriptionFetchProxy(domain.SubscriptionSource{EncryptedProxyURL: "not-ciphertext"}); err == nil {
		t.Fatal("corrupt per-source subscription proxy was accepted")
	}
}

func TestSyncSourceUsesItsOwnFetchProxy(t *testing.T) {
	cipher, err := security.NewCipher("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	type proxyFixture struct {
		hits      atomic.Int32
		server    *httptest.Server
		nodeProxy string
	}
	newProxy := func(nodeProxy string) *proxyFixture {
		fixture := &proxyFixture{nodeProxy: nodeProxy}
		fixture.server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			fixture.hits.Add(1)
			if request.URL.Host != "1.1.1.1" {
				response.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = response.Write([]byte(fixture.nodeProxy + "\n"))
		}))
		t.Cleanup(fixture.server.Close)
		return fixture
	}
	firstProxy := newProxy("socks5h://first-node.example:1080")
	secondProxy := newProxy("http://second-node.example:8080")

	encryptedSourceURL, err := cipher.Encrypt("http://1.1.1.1/subscription")
	if err != nil {
		t.Fatal(err)
	}
	repository := &subscriptionSyncRepositoryStub{nodes: make(map[uint64][]domain.Node)}
	service := &Service{cipher: cipher}
	for id, fixture := range map[uint64]*proxyFixture{1: firstProxy, 2: secondProxy} {
		encryptedProxyURL, encryptErr := cipher.Encrypt(fixture.server.URL)
		if encryptErr != nil {
			t.Fatal(encryptErr)
		}
		_, syncErr := service.syncSource(context.Background(), repository, domain.SubscriptionSource{
			ID: id, Name: "source", Scope: domain.ScopeBuild, Enabled: true,
			EncryptedURL: encryptedSourceURL, EncryptedProxyURL: encryptedProxyURL, RefreshIntervalSeconds: 900,
		})
		if syncErr != nil {
			t.Fatalf("sync source %d: %v", id, syncErr)
		}
	}

	if firstProxy.hits.Load() != 1 || secondProxy.hits.Load() != 1 {
		t.Fatalf("proxy hits: first=%d second=%d", firstProxy.hits.Load(), secondProxy.hits.Load())
	}
	for id, expected := range map[uint64]string{1: firstProxy.nodeProxy, 2: secondProxy.nodeProxy} {
		nodes := repository.nodesFor(id)
		if len(nodes) != 1 {
			t.Fatalf("source %d imported %d nodes", id, len(nodes))
		}
		actual, decryptErr := cipher.Decrypt(nodes[0].EncryptedProxyURL)
		if decryptErr != nil {
			t.Fatal(decryptErr)
		}
		if actual != expected {
			t.Fatalf("source %d imported proxy %q, want %q", id, actual, expected)
		}
	}
}
