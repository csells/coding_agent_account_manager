package refresh

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func validateTokenEndpoint(raw string, allowHosts []string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("token endpoint is empty")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid token endpoint: %w", err)
	}

	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return fmt.Errorf("token endpoint missing host")
	}

	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	loopbackHTTP := scheme == "http" && isLoopbackHost(host)
	if scheme != "https" && !loopbackHTTP {
		return fmt.Errorf("refusing token endpoint scheme %q (host=%q)", scheme, host)
	}

	// Always allow loopback endpoints (used in tests).
	if isLoopbackHost(host) {
		return nil
	}

	for _, allowed := range allowHosts {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == "" {
			continue
		}
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return nil
		}
	}

	return fmt.Errorf("refusing token endpoint host %q (not allowlisted)", host)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
