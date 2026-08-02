package download

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var blockedNetworkPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func restrictedHTTPClient(source *http.Client) *http.Client {
	client := *source
	client.CheckRedirect = restrictedRedirectPolicy(source.CheckRedirect)

	base := source.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	if transport, ok := base.(*http.Transport); ok {
		clone := transport.Clone()
		// 直接连接经过校验的目标 IP，避免代理端二次解析或 DNS 重绑定绕过。
		clone.Proxy = nil
		clone.ProxyConnectHeader = nil
		clone.GetProxyConnectHeader = nil
		clone.DialContext = restrictedDialContext(&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		})
		clone.DialTLSContext = nil
		clone.DialTLS = nil
		client.Transport = clone
	} else {
		// 非标准 RoundTripper 无法替换拨号器，至少在每次请求前解析并校验目标。
		client.Transport = &restrictedRoundTripper{base: base}
	}
	return &client
}

func restrictedRedirectPolicy(previous func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("下载重定向次数过多")
		}
		if err := validateDownloadURL(request.URL); err != nil {
			return err
		}
		if previous != nil {
			return previous(request, via)
		}
		return nil
	}
}

type restrictedRoundTripper struct {
	base http.RoundTripper
}

func (transport *restrictedRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := validateDownloadURL(request.URL); err != nil {
		return nil, err
	}
	if err := validateHostAddresses(request.Context(), request.URL.Hostname()); err != nil {
		return nil, err
	}
	return transport.base.RoundTrip(request)
}

func restrictedDialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("解析下载服务器地址：%w", err)
		}
		if strings.Contains(host, "%") {
			return nil, errors.New("不允许访问带区域标识的网络地址")
		}
		addresses, err := resolveHost(ctx, host)
		if err != nil {
			return nil, err
		}
		var dialErrors []error
		for _, candidate := range addresses {
			if strings.HasSuffix(network, "4") && !candidate.Is4() || strings.HasSuffix(network, "6") && !candidate.Is6() {
				continue
			}
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			dialErrors = append(dialErrors, dialErr)
		}
		if len(dialErrors) == 0 {
			return nil, errors.New("下载服务器没有可用的公网地址")
		}
		return nil, fmt.Errorf("连接下载服务器失败：%w", errors.Join(dialErrors...))
	}
}

func validateDownloadURL(value *url.URL) error {
	if value == nil || value.Scheme != "http" && value.Scheme != "https" || value.Host == "" || value.User != nil {
		return errors.New("下载地址必须是无用户凭据的 HTTP 或 HTTPS URL")
	}
	if literal, err := netip.ParseAddr(strings.Trim(value.Hostname(), "[]")); err == nil {
		if isBlockedAddress(literal) {
			return fmt.Errorf("不允许访问本机或内网地址 %s", literal)
		}
	}
	return nil
}

func validateHostAddresses(ctx context.Context, host string) error {
	_, err := resolveHost(ctx, host)
	return err
}

func resolveHost(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return nil, errors.New("下载服务器主机名为空")
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		literal = literal.Unmap()
		if isBlockedAddress(literal) {
			return nil, fmt.Errorf("不允许访问本机或内网地址 %s", literal)
		}
		return []netip.Addr{literal}, nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("解析下载服务器 %q：%w", host, err)
	}
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if isBlockedAddress(address) {
			return nil, fmt.Errorf("下载服务器 %q 解析到不允许的本机或内网地址 %s", host, address)
		}
		result = append(result, address)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("下载服务器 %q 没有可用的公网地址", host)
	}
	return result, nil
}

func isBlockedAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return true
	}
	for _, prefix := range blockedNetworkPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
