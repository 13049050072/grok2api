package middleware

import (
	"net"
	"net/http"
	"strings"
)

// TrustedProxies 判定 TCP 直连对端是否属于 server.trustedProxies（IP 或 CIDR，
// 不带前缀的地址按单个主机匹配）。
type TrustedProxies struct {
	networks []*net.IPNet
}

// NewTrustedProxies 跳过无法解析的条目：配置校验与 Gin 已在启动时拒绝它们，
// 这里只会少信任、不会多信任。
func NewTrustedProxies(values []string) TrustedProxies {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if strings.Contains(value, "/") {
			if _, network, err := net.ParseCIDR(value); err == nil {
				networks = append(networks, network)
			}
			continue
		}
		ip := net.ParseIP(value)
		if ip == nil {
			continue
		}
		if ipv4 := ip.To4(); ipv4 != nil {
			ip = ipv4
		}
		networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(8*len(ip), 8*len(ip))})
	}
	return TrustedProxies{networks: networks}
}

// ForwardedHTTPS 仅在直连对端是可信代理时采信 X-Forwarded-Proto，并按首个值判断
// 浏览器到边缘代理的连接是否为 HTTPS。
func (p TrustedProxies) ForwardedHTTPS(request *http.Request) bool {
	if request == nil || !p.containsPeer(request.RemoteAddr) {
		return false
	}
	proto, _, _ := strings.Cut(request.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

func (p TrustedProxies) containsPeer(remoteAddr string) bool {
	if len(p.networks) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, network := range p.networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
