package bwiki

import (
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

var trustedAudioDomainRoots = []string{
	"biligame.com",
	"hdslb.com",
	"bilivideo.com",
	"bilivideo.cn",
	"biliapi.net",
}

func bwikiRedirectPolicy(previous func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("BWIKI 重定向次数过多")
		}
		if !isTrustedBWikiURL(request.URL) {
			return errors.New("BWIKI 页面重定向到了不受信任的地址，已停止访问")
		}
		if previous != nil {
			return previous(request, via)
		}
		return nil
	}
}

func isTrustedBWikiURL(value *url.URL) bool {
	return isSafeWebURL(value) && normalizedHostname(value) == "wiki.biligame.com"
}

func isTrustedAudioURL(value *url.URL) bool {
	if !isSafeWebURL(value) {
		return false
	}
	host := normalizedHostname(value)
	if !isDNSHostname(host) {
		return false
	}
	for _, root := range trustedAudioDomainRoots {
		if host == root || strings.HasSuffix(host, "."+root) {
			return true
		}
	}
	return false
}

func isDNSHostname(host string) bool {
	if host == "" || len(host) > 253 || strings.Contains(host, "%") {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || !isASCIILetterOrDigit(label[0]) || !isASCIILetterOrDigit(label[len(label)-1]) {
			return false
		}
		for index := 1; index < len(label)-1; index++ {
			if !isASCIILetterOrDigit(label[index]) && label[index] != '-' {
				return false
			}
		}
	}
	return true
}

func isASCIILetterOrDigit(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func isSafeWebURL(value *url.URL) bool {
	if value == nil || value.User != nil || value.Host == "" || value.Scheme != "http" && value.Scheme != "https" {
		return false
	}
	port := value.Port()
	return port == "" || value.Scheme == "http" && port == "80" || value.Scheme == "https" && port == "443"
}

func normalizedHostname(value *url.URL) string {
	if value == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(value.Hostname(), "."))
}
