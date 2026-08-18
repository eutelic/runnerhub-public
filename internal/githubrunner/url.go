package githubrunner

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

func parseAPIBase(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "https://api.github.com"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("github api base url must contain only scheme, host, and optional path")
	}
	if u.Scheme == "https" {
		return u, nil
	}
	if u.Scheme == "http" && isLoopbackHost(u.Hostname()) {
		return u, nil
	}
	return nil, errors.New("github api base url must use https; http is allowed only for loopback tests")
}

func apiEndpoint(base *url.URL, path string) (string, error) {
	if base == nil {
		return "", errors.New("github api base url is nil")
	}
	rel, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	if rel.IsAbs() || rel.Host != "" {
		return "", errors.New("github api path must be relative")
	}
	baseText := strings.TrimRight(base.String(), "/")
	return baseText + "/" + strings.TrimLeft(path, "/"), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
