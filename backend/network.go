package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

type NetworkSearchResult struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	Snippet  string `json:"snippet,omitempty"`
	Provider string `json:"provider"`
}

type networkSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type duckSearchTopic struct {
	Text     string            `json:"Text"`
	FirstURL string            `json:"FirstURL"`
	Topics   []duckSearchTopic `json:"Topics"`
}

type duckSearchResponse struct {
	Heading       string            `json:"Heading"`
	AbstractText  string            `json:"AbstractText"`
	AbstractURL   string            `json:"AbstractURL"`
	RelatedTopics []duckSearchTopic `json:"RelatedTopics"`
}

func (s *Server) handleSearchSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	var req networkSearchRequest
	if err := decodeJSON(w, r, &req, s.config.MaxBodyBytes); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}
	query := strings.TrimSpace(req.Query)
	if length := len([]rune(query)); length < 2 || length > 200 || containsCredential(query) {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "搜索词长度必须在 2 到 200 个字符之间且不能包含凭据")
		return
	}
	limit := req.Limit
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > 20 {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "limit 必须在 1 到 20 之间")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	items, err := s.searchPublicSources(ctx, query, limit)
	if err != nil {
		code := sourceErrorCode(err)
		status := http.StatusBadGateway
		if code == "TOOL_TIMEOUT" {
			status = http.StatusGatewayTimeout
		} else if code == "USER_CANCELLED" {
			status = http.StatusRequestTimeout
		}
		writeError(w, status, code, "公开网络搜索暂时不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "items": items, "readOnly": true, "untrusted": true, "nextActions": []string{"核实来源、许可证和文件地址后再创建下载计划"}})
}

func (s *Server) searchPublicSources(ctx context.Context, query string, limit int) ([]NetworkSearchResult, error) {
	endpoint := s.searchEndpoint
	if endpoint == "" {
		endpoint = "https://api.duckduckgo.com/"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, errForbidden
	}
	params := parsed.Query()
	params.Set("q", query)
	params.Set("format", "json")
	params.Set("no_html", "1")
	params.Set("no_redirect", "1")
	params.Set("skip_disambig", "1")
	parsed.RawQuery = params.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.publicHTTPClient(10 * time.Second).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New("search provider returned non-success status")
	}
	var payload duckSearchResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("invalid search provider response")
	}
	items := make([]NetworkSearchResult, 0, limit)
	seen := make(map[string]struct{}, limit)
	appendResult := func(title, target, snippet string) {
		if len(items) >= limit || strings.TrimSpace(target) == "" {
			return
		}
		validated, err := parseSourceURL(target)
		if err != nil {
			return
		}
		host := strings.ToLower(validated.Hostname())
		if ip := net.ParseIP(host); ip != nil && isPrivateIP(ip) || host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
			return
		}
		canonical := validated.String()
		if _, exists := seen[canonical]; exists {
			return
		}
		seen[canonical] = struct{}{}
		items = append(items, NetworkSearchResult{Title: strings.TrimSpace(title), URL: canonical, Snippet: strings.TrimSpace(snippet), Provider: "DuckDuckGo"})
	}
	if payload.AbstractURL != "" {
		appendResult(payload.Heading, payload.AbstractURL, payload.AbstractText)
	}
	var flatten func([]duckSearchTopic)
	flatten = func(topics []duckSearchTopic) {
		for _, topic := range topics {
			appendResult(topic.Text, topic.FirstURL, "")
			if len(items) >= limit {
				return
			}
			flatten(topic.Topics)
			if len(items) >= limit {
				return
			}
		}
	}
	flatten(payload.RelatedTopics)
	return items, nil
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
