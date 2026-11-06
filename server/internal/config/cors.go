package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func corsOrigins(value string, allowAll bool) ([]string, error) {
	list := splitList(value)
	for i, raw := range list {
		if raw == "*" && allowAll && len(list) == 1 {
			return list, nil
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
			u.User != nil || (u.EscapedPath() != "" && u.EscapedPath() != "/") || strings.ContainsAny(raw, "*?#%") {
			return nil, fmt.Errorf("expected an exact HTTP(S) origin, got %q", raw)
		}
		host := strings.ToLower(u.Host)
		if port := u.Port(); port != "" {
			if _, err := strconv.ParseUint(port, 10, 16); err != nil {
				return nil, fmt.Errorf("invalid port in %q", raw)
			}
			if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
				host = strings.TrimSuffix(host, ":"+port)
			}
		} else if strings.HasSuffix(host, ":") {
			return nil, fmt.Errorf("empty port in %q", raw)
		}
		list[i] = u.Scheme + "://" + host
	}
	return list, nil
}
