package gateway

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

const (
	maxDNSLabelLength = 63
	maxDNSNameLength  = 253
)

type hostRoute struct {
	sandboxID  string
	targetPort int
}

func parseHostRoute(rawHost string, domains []string) (*hostRoute, error) {
	if len(domains) == 0 {
		return nil, nil
	}

	host := normalizeRequestHost(rawHost)
	if host == "" {
		return nil, nil
	}

	for _, domain := range domains {
		if host == domain {
			return nil, nil
		}

		suffix := "." + domain
		if !strings.HasSuffix(host, suffix) {
			continue
		}

		label := strings.TrimSuffix(host, suffix)
		if label == "" || strings.Contains(label, ".") || !strings.Contains(label, "-") {
			continue
		}
		if len(label) > maxDNSLabelLength {
			return nil, errors.New("invalid sandbox data-plane host: label too long")
		}

		portText, sandboxID, _ := strings.Cut(label, "-")
		if sandboxID == "" {
			return nil, errors.New("invalid sandbox data-plane host: sandbox id is empty")
		}
		if !isValidDNSLabel(sandboxID) {
			return nil, errors.New("invalid sandbox data-plane host: sandbox id is invalid")
		}

		port, err := strconv.Atoi(portText)
		if err != nil {
			return nil, errors.New("invalid sandbox data-plane host: port is not numeric")
		}
		if port <= 0 || port > 65535 {
			return nil, errors.New("invalid sandbox data-plane host: port out of range")
		}

		return &hostRoute{
			sandboxID:  sandboxID,
			targetPort: port,
		}, nil
	}

	return nil, nil
}

func normalizeProxyDomains(domains []string) ([]string, error) {
	seen := make(map[string]struct{}, len(domains))
	normalized := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = normalizeProxyDomain(domain)
		if domain == "" {
			continue
		}
		if !isValidProxyDomain(domain) {
			return nil, fmt.Errorf("gateway.sandbox_proxy_domains contains invalid domain %q", domain)
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		normalized = append(normalized, domain)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return len(normalized[i]) > len(normalized[j])
	})
	return normalized, nil
}

func normalizeProxyDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

func normalizeRequestHost(rawHost string) string {
	host := strings.TrimSpace(rawHost)
	if host == "" {
		return ""
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func isValidProxyDomain(domain string) bool {
	if domain == "" || len(domain) > maxDNSNameLength {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if !isValidDNSLabel(label) {
			return false
		}
	}
	return true
}

func isValidDNSLabel(label string) bool {
	if label == "" || len(label) > maxDNSLabelLength {
		return false
	}
	if !isLowerASCIILetterOrDigit(label[0]) || !isLowerASCIILetterOrDigit(label[len(label)-1]) {
		return false
	}
	for i := 1; i < len(label)-1; i++ {
		c := label[i]
		if !isLowerASCIILetterOrDigit(c) && c != '-' {
			return false
		}
	}
	return true
}

func isLowerASCIILetterOrDigit(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}
