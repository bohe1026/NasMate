package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type SourceProbe struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Accessible  bool   `json:"accessible"`
	ContentType string `json:"contentType,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	FinalURL    string `json:"finalUrl,omitempty"`
	StatusCode  int    `json:"statusCode,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (s *Server) handleProbeSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	var req sourceValidationRequest
	if err := decodeJSON(w, r, &req, s.config.MaxBodyBytes); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}
	if len(req.Sources) == 0 || len(req.Sources) > 20 {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "来源数量必须在 1 到 20 之间")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	client := s.publicHTTPClient(8 * time.Second)
	results := make([]SourceProbe, 0, len(req.Sources))
	for _, source := range req.Sources {
		probe := SourceProbe{Title: source.Title, SizeBytes: -1}
		parsed, err := parseSourceURL(source.URL)
		if err != nil {
			probe.Error = "VALIDATION_FAILED"
			results = append(results, probe)
			continue
		}
		probe.URL = parsed.String()
		reqHTTP, err := http.NewRequestWithContext(ctx, http.MethodHead, parsed.String(), nil)
		if err != nil {
			probe.Error = "VALIDATION_FAILED"
			results = append(results, probe)
			continue
		}
		resp, err := client.Do(reqHTTP)
		if err != nil {
			probe.Error = sourceErrorCode(err)
			results = append(results, probe)
			continue
		}
		resp.Body.Close()
		probe.StatusCode = resp.StatusCode
		probe.Accessible = resp.StatusCode >= 200 && resp.StatusCode < 300
		if !probe.Accessible {
			probe.Error = "NETWORK_ERROR"
		}
		probe.ContentType = resp.Header.Get("Content-Type")
		probe.SizeBytes = resp.ContentLength
		probe.FinalURL = resp.Request.URL.String()
		results = append(results, probe)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "items": results, "readOnly": true, "nextActions": []string{"复核来源和许可证后创建下载计划"}})
}

var errNonPublicAddress = errors.New("non-public destination")

func parseSourceURL(raw string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || len(raw) > 4096 || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || hasSensitiveURLQuery(parsed) || parsed.Fragment != "" {
		return nil, errForbidden
	}
	return parsed, nil
}

func sourceErrorCode(err error) string {
	if errors.Is(err, errNonPublicAddress) {
		return "POLICY_BLOCKED"
	}
	if errors.Is(err, context.Canceled) {
		return "USER_CANCELLED"
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "TOOL_TIMEOUT"
	}
	return "NETWORK_ERROR"
}

// Resolve once, validate every answer, then connect to a numeric address. The
// HTTP transport must not resolve the hostname again (DNS rebinding).
type publicDialer struct {
	lookup func(context.Context, string, string) ([]net.IP, error)
	dial   func(context.Context, string, string) (net.Conn, error)
}

func (d publicDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errNonPublicAddress
	}
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		ips, err = d.lookup(ctx, "ip", host)
	}
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errNonPublicAddress
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return nil, errNonPublicAddress
		}
	}
	return d.dial(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

func newPublicTransport() *http.Transport {
	dialer := publicDialer{lookup: net.DefaultResolver.LookupIP, dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
	return &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second, MaxResponseHeaderBytes: 32 << 10, MaxIdleConns: 6, MaxConnsPerHost: 3, IdleConnTimeout: 30 * time.Second}
}

func (s *Server) publicHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: s.sourceTransport, Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

func isPrivateIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if addr.Is6() && !netip.MustParsePrefix("2000::/3").Contains(addr) {
		return true
	}
	for _, prefix := range strings.Fields("0.0.0.0/8 100.64.0.0/10 192.0.0.0/24 192.0.2.0/24 198.18.0.0/15 198.51.100.0/24 203.0.113.0/24 240.0.0.0/4 2001::/32 2001:db8::/32 2002::/16") {
		if netip.MustParsePrefix(prefix).Contains(addr) {
			return true
		}
	}
	return false
}
