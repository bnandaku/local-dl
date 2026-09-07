package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Only HTTPS Put.io origins are implicit. Extra exact origins are an explicit
// operator setting for a trusted mirror; they also permit its private address.
func archiveHTTPClient() (*http.Client, error) {
	origins := map[string]bool{}
	privateHosts := map[string]bool{}
	for _, raw := range strings.Split(os.Getenv("ARCHIVE_DOWNLOAD_ORIGINS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("invalid ARCHIVE_DOWNLOAD_ORIGINS setting")
		}
		origins[u.Scheme+"://"+u.Host] = true
		port := u.Port()
		if port == "" {
			port = "443"
			if u.Scheme == "http" {
				port = "80"
			}
		}
		privateHosts[net.JoinHostPort(u.Hostname(), port)] = true
	}
	allowed := func(u *url.URL) bool {
		if u == nil || u.User != nil {
			return false
		}
		if origins[u.Scheme+"://"+u.Host] {
			return true
		}
		host := strings.ToLower(u.Hostname())
		return u.Scheme == "https" && (u.Port() == "" || u.Port() == "443") && (host == "put.io" || strings.HasSuffix(host, ".put.io"))
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if privateHosts[address] {
			return dialer.DialContext(ctx, network, address)
		}
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, fmt.Errorf("invalid archive download address")
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, fmt.Errorf("archive download DNS unavailable")
		}
		for _, entry := range ips {
			ip := entry.IP
			if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || !ip.IsGlobalUnicast() {
				return nil, fmt.Errorf("archive download resolved to a private or reserved address")
			}
		}
		for _, entry := range ips {
			if conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(entry.IP.String(), port)); e == nil {
				return conn, nil
			}
		}
		return nil, fmt.Errorf("archive download address unavailable")
	}
	guarded := &archiveOriginTransport{base: transport, allowed: allowed}
	return &http.Client{Timeout: 6 * time.Hour, Transport: guarded, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !allowed(req.URL) {
			return fmt.Errorf("archive redirect outside approved origins")
		}
		return nil
	}}, nil
}

type archiveOriginTransport struct {
	base    *http.Transport
	allowed func(*url.URL) bool
}

func (t *archiveOriginTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !t.allowed(r.URL) {
		return nil, fmt.Errorf("archive download outside approved origins")
	}
	return t.base.RoundTrip(r)
}
func (t *archiveOriginTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }
