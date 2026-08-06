package provider

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPClientOptions controls provider HTTP transport behavior.
type HTTPClientOptions struct {
	ProxyURL    string
	ForceHTTP11 bool
}

// NewHTTPClient returns a provider HTTP client. Empty proxyURL preserves the
// default environment proxy behavior from http.Transport.
func NewHTTPClient(timeout time.Duration, proxyURL string) (*http.Client, error) {
	return NewHTTPClientWithOptions(timeout, HTTPClientOptions{ProxyURL: proxyURL})
}

// NewHTTPClientWithOptions returns a provider HTTP client with transport options.
func NewHTTPClientWithOptions(timeout time.Duration, opts HTTPClientOptions) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opts.ForceHTTP11 {
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	proxyURL := strings.TrimSpace(opts.ProxyURL)
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("proxy URL must include scheme and host")
		}
		transport.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

// Stream transport timeouts. These bound only the phases before the response
// body begins streaming (connect + response headers) so a request cannot hang
// forever, while leaving the potentially long-lived SSE body to an idle/activity
// timeout applied by the caller (see NewIdleTimeoutReadCloser).
const (
	streamConnectTimeout        = 30 * time.Second
	streamResponseHeaderTimeout = 2 * time.Minute
)

// NewStreamHTTPClient returns an HTTP client suited to long-lived streaming
// requests. Unlike NewHTTPClientWithOptions, it does not impose a single
// wall-clock Client.Timeout that would cap the entire (potentially long) SSE
// body; instead it bounds only the connect and response-header phases.
func NewStreamHTTPClient(proxyURL string) (*http.Client, error) {
	return NewStreamHTTPClientWithOptions(HTTPClientOptions{ProxyURL: proxyURL})
}

// NewStreamHTTPClientWithOptions is NewStreamHTTPClient with explicit options.
func NewStreamHTTPClientWithOptions(opts HTTPClientOptions) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opts.ForceHTTP11 {
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	proxyURL := strings.TrimSpace(opts.ProxyURL)
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("proxy URL must include scheme and host")
		}
		transport.Proxy = http.ProxyURL(u)
	}
	transport.DialContext = (&net.Dialer{
		Timeout:   streamConnectTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.ResponseHeaderTimeout = streamResponseHeaderTimeout
	return &http.Client{Transport: transport}, nil
}

// ApplyHeaders applies configured custom headers after provider defaults.
func ApplyHeaders(req *http.Request, headers map[string]string) {
	for name, value := range headers {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		req.Header.Set(name, value)
	}
}
