package egress

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chenyme/grok2api/backend/internal/pkg/netguard"
	"github.com/chenyme/grok2api/backend/internal/pkg/tunnelproxy"
)

const (
	maxSubscriptionBytes     = 2 << 20
	maxSubscriptionEntries   = 10000
	maxSubscriptionHops      = 3
	subscriptionFetchTimeout = 20 * time.Second
	proxyHandshakeTimeout    = 10 * time.Second
)

// subscriptionIPLookup overrides destination resolution in tests.
var subscriptionIPLookup func(context.Context, string) ([]netip.Addr, error)

type subscriptionEntry struct {
	ProxyURL string
	Key      string
}

func normalizeSubscriptionURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxProxyURLBytes {
		return "", errors.New("订阅地址为空或过长")
	}
	if strings.IndexFunc(value, func(character rune) bool { return character < 0x20 || character == 0x7f }) >= 0 {
		return "", errors.New("订阅地址包含控制字符")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("订阅地址格式无效")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("订阅地址必须使用 HTTP 或 HTTPS")
	}
	if parsed.Fragment != "" {
		return "", errors.New("订阅地址不能包含片段")
	}
	return parsed.String(), nil
}

func fetchProxySubscription(ctx context.Context, value string, viaProxy string) ([]byte, error) {
	normalized, err := normalizeSubscriptionURL(value)
	if err != nil {
		return nil, err
	}
	transport, err := subscriptionTransport(viaProxy)
	if err != nil {
		return nil, err
	}
	proxied := strings.TrimSpace(viaProxy) != ""
	var roundTripper http.RoundTripper = transport
	if proxied {
		// Pin every request, including redirects, to an address resolved here.
		// The proxy then receives that IP instead of resolving the name again.
		roundTripper = &pinnedSubscriptionTransport{base: transport, forwardProxy: transport.Proxy != nil}
	}
	client := &http.Client{
		Transport: roundTripper,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= maxSubscriptionHops {
				return errors.New("订阅重定向次数过多")
			}
			redirectURL, err := normalizeSubscriptionURL(request.URL.String())
			if err != nil {
				return errors.New("订阅重定向地址无效")
			}
			if proxied {
				if err := validatePublicSubscriptionTarget(request.Context(), redirectURL); err != nil {
					return errors.New("订阅重定向地址不能指向内网")
				}
			}
			return nil
		},
	}
	defer client.CloseIdleConnections()
	requestCtx, cancel := context.WithTimeout(ctx, subscriptionFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, normalized, nil)
	if err != nil {
		return nil, err
	}
	if proxied {
		if err := validatePublicSubscriptionTarget(requestCtx, normalized); err != nil {
			return nil, err
		}
	}
	request.Header.Set("Accept", "text/plain, text/*;q=0.9, */*;q=0.1")
	request.Header.Set("User-Agent", "Clash.Meta")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("订阅服务返回 HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxSubscriptionBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSubscriptionBytes {
		return nil, errors.New("订阅内容超过大小限制")
	}
	return body, nil
}

// subscriptionTransport builds the HTTP transport used to pull remote proxy
// lists. Without a via-proxy the dialer rejects private destinations (SSRF).
// With a via-proxy the admin-configured proxy endpoint may be private, while
// fetchProxySubscription separately requires every subscription target and
// redirect to resolve exclusively to public addresses.
func subscriptionTransport(viaProxy string) (*http.Transport, error) {
	direct := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           publicDialContext(net.DefaultResolver),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   1,
		IdleConnTimeout:       15 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	viaProxy = strings.TrimSpace(viaProxy)
	if viaProxy == "" {
		return transport, nil
	}
	parsed, err := url.Parse(viaProxy)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("订阅拉取代理地址无效")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(parsed)
		// Dial the proxy endpoint itself; private admin proxies are allowed.
		transport.DialContext = direct.DialContext
	case "socks4", "socks4a", "socks5", "socks5h":
		dialer, err := tunnelproxy.NewSOCKSDialer(parsed, direct, proxyHandshakeTimeout)
		if err != nil {
			return nil, fmt.Errorf("创建订阅拉取 SOCKS 代理: %w", err)
		}
		transport.DialContext = dialer.DialContext
	case "trojan", "vless", "ss", "vmess":
		dialer, err := tunnelproxy.NewDialer(viaProxy)
		if err != nil {
			return nil, fmt.Errorf("创建订阅拉取隧道代理: %w", err)
		}
		transport.DialContext = dialer.DialContext
	default:
		return nil, errors.New("订阅拉取代理协议不受支持")
	}
	return transport, nil
}

type pinnedSubscriptionTransport struct {
	base *http.Transport
	// forwardProxy marks HTTP(S) proxies, which receive plain-HTTP requests in
	// absolute form instead of through a tunnel.
	forwardProxy bool

	mu sync.Mutex
	// tlsByServerName must be closed with the base transport; one entry per
	// TLS server name lets redirect hops share connections.
	tlsByServerName map[string]*http.Transport
}

func (p *pinnedSubscriptionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if p == nil || p.base == nil || request == nil {
		return nil, errors.New("订阅地址格式无效")
	}
	pinned, serverName, err := pinSubscriptionRequest(request, p.forwardProxy)
	if err != nil {
		return nil, err
	}
	transport := p.base
	if pinned.URL.Scheme == "https" {
		transport = p.tlsTransport(serverName)
	}
	response, err := transport.RoundTrip(pinned)
	if response != nil {
		response.Request = request
	}
	return response, err
}

func (p *pinnedSubscriptionTransport) tlsTransport(serverName string) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if transport, ok := p.tlsByServerName[serverName]; ok {
		return transport
	}
	transport := p.base.Clone()
	config := &tls.Config{}
	if p.base.TLSClientConfig != nil {
		config = p.base.TLSClientConfig.Clone()
	}
	config.ServerName = serverName
	config.NextProtos = []string{"h2", "http/1.1"}
	transport.TLSClientConfig = config
	if p.forwardProxy {
		transport.DialTLSContext = p.dialProxyTLS(config)
	}
	if p.tlsByServerName == nil {
		p.tlsByServerName = make(map[string]*http.Transport)
	}
	p.tlsByServerName[serverName] = transport
	return transport
}

// dialProxyTLS verifies an HTTPS proxy under its own name. net/http applies
// TLSClientConfig.ServerName to the proxy handshake as well, which would check
// the proxy certificate against the subscription host.
func (p *pinnedSubscriptionTransport) dialProxyTLS(config *tls.Config) func(context.Context, string, string) (net.Conn, error) {
	dial := p.base.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	timeout := p.base.TLSHandshakeTimeout
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		connection, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		proxyConfig := config.Clone()
		proxyConfig.ServerName = host
		// CONNECT is written as HTTP/1.1 on this connection.
		proxyConfig.NextProtos = []string{"http/1.1"}
		tlsConnection := tls.Client(connection, proxyConfig)
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			_ = connection.Close()
			return nil, err
		}
		return tlsConnection, nil
	}
}

func (p *pinnedSubscriptionTransport) CloseIdleConnections() {
	p.base.CloseIdleConnections()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, transport := range p.tlsByServerName {
		transport.CloseIdleConnections()
	}
}

// pinSubscriptionRequest returns a copy of request aimed at a public IP, plus
// the TLS server name. The caller's request stays untouched because the client
// resolves redirects against it.
func pinSubscriptionRequest(request *http.Request, forwardProxy bool) (*http.Request, string, error) {
	if request == nil || request.URL == nil || request.URL.Hostname() == "" {
		return nil, "", errors.New("订阅地址格式无效")
	}
	host := request.URL.Hostname()
	port := request.URL.Port()
	if port == "" {
		if request.URL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	addresses, err := resolvePublicAddresses(request.Context(), net.DefaultResolver, host)
	if err != nil {
		return nil, "", err
	}
	pinned := request.Clone(request.Context())
	pinned.URL.Host = net.JoinHostPort(addresses[0].String(), port)
	if pinned.Host == "" {
		pinned.Host = request.URL.Host
	}
	// A forward proxy builds the absolute request-target from Request.Host, so
	// the pinned IP has to live there or the proxy resolves the name again.
	// Tunnels and SOCKS already dial the pinned IP and keep the original Host.
	if forwardProxy && request.URL.Scheme != "https" {
		pinned.Host = pinned.URL.Host
		if port == "80" {
			pinned.Host = addresses[0].String()
			if addresses[0].Is6() {
				pinned.Host = "[" + pinned.Host + "]"
			}
		}
	}
	return pinned, host, nil
}

// validatePublicSubscriptionTarget closes the SSRF gap introduced by remote
// proxy resolution. Requiring every locally resolved address to be public also
// rejects split-horizon names that expose an internal address on this host.
func validatePublicSubscriptionTarget(ctx context.Context, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return errors.New("订阅地址格式无效")
	}
	host := strings.Trim(strings.TrimSpace(parsed.Hostname()), "[]")
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		if !isPublicAddress(address) {
			return errors.New("订阅地址不能指向内网")
		}
		return nil
	}
	addresses, err := lookupHostIPs(ctx, net.DefaultResolver, host)
	if err != nil {
		return fmt.Errorf("解析订阅地址: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("订阅地址没有可用的公网 IP")
	}
	for _, address := range addresses {
		if !isPublicAddress(address) {
			return errors.New("订阅地址不能指向内网")
		}
	}
	return nil
}

// publicDialContext resolves every destination immediately before dialing. It
// rejects loopback, private, link-local, multicast, and carrier-grade NAT
// addresses, including redirect destinations, to avoid subscription SSRF.
func publicDialContext(resolver *net.Resolver) func(context.Context, string, string) (net.Conn, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := resolvePublicAddresses(ctx, resolver, host)
		if err != nil {
			return nil, err
		}
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
		var lastErr error
		for _, ip := range addresses {
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("订阅地址没有可用的公网 IP")
	}
}

func resolvePublicAddresses(ctx context.Context, resolver *net.Resolver, host string) ([]netip.Addr, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if parsed, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddress(parsed) {
			return nil, errors.New("订阅地址不能指向内网")
		}
		return []netip.Addr{parsed.Unmap()}, nil
	}
	addresses, err := lookupHostIPs(ctx, resolver, host)
	if err != nil {
		return nil, err
	}
	public := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		if isPublicAddress(address) {
			public = append(public, address.Unmap())
		}
	}
	if len(public) == 0 {
		return nil, errors.New("订阅地址不能指向内网")
	}
	return public, nil
}

func lookupHostIPs(ctx context.Context, resolver *net.Resolver, host string) ([]netip.Addr, error) {
	if subscriptionIPLookup != nil {
		return subscriptionIPLookup(ctx, host)
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return resolver.LookupNetIP(ctx, "ip", host)
}

func isPublicAddress(value netip.Addr) bool {
	return netguard.IsPublicAddress(value)
}

func parseProxySubscription(value string) ([]subscriptionEntry, int, error) {
	entries, skipped := parseProxyLines(value)
	if len(entries) > 0 {
		return entries, skipped, nil
	}
	compact := strings.Map(func(character rune) rune {
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' {
			return -1
		}
		return character
	}, strings.TrimPrefix(value, "\ufeff"))
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err := encoding.DecodeString(compact)
		if err != nil || len(decoded) == 0 || len(decoded) > maxSubscriptionBytes {
			continue
		}
		entries, decodedSkipped := parseProxyLines(string(decoded))
		if len(entries) > 0 {
			// The original Base64 text is not an invalid proxy entry. Once it
			// decodes to a valid list, report only invalid decoded entries.
			return entries, decodedSkipped, nil
		}
	}
	if entries, clashSkipped, matched := parseClashSubscription(value); matched && len(entries) > 0 {
		return entries, clashSkipped, nil
	}
	return nil, skipped, errors.New("订阅中没有可用的代理节点")
}

func parseProxyLines(value string) ([]subscriptionEntry, int) {
	value = strings.TrimPrefix(value, "\ufeff")
	seen := make(map[string]struct{})
	entries := make([]subscriptionEntry, 0)
	skipped := 0
	for line := range strings.SplitSeq(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		normalized, err := NormalizeProxyURL(line)
		if err != nil {
			skipped++
			continue
		}
		digest := sha256.Sum256([]byte(normalized))
		key := hex.EncodeToString(digest[:])
		if _, exists := seen[key]; exists {
			skipped++
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, subscriptionEntry{ProxyURL: normalized, Key: key})
		if len(entries) > maxSubscriptionEntries {
			return nil, skipped
		}
	}
	return entries, skipped
}

func sourceNodeName(sourceName string, index int) string {
	suffix := fmt.Sprintf(" %03d", index+1)
	value := strings.TrimSpace(sourceName)
	for len(value)+len(suffix) > 160 && value != "" {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return strings.TrimSpace(value) + suffix
}
