package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
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
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	results := make([]SourceProbe, 0, len(req.Sources))
	for _, source := range req.Sources {
		probe := SourceProbe{Title: source.Title, URL: source.URL}
		parsed, err := url.ParseRequestURI(source.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || hasSensitiveURLQuery(parsed) {
			results = append(results, probe)
			continue
		}
		if err := rejectPrivateHost(parsed.Hostname()); err != nil {
			results = append(results, probe)
			continue
		}
		reqHTTP, err := http.NewRequestWithContext(r.Context(), http.MethodHead, parsed.String(), nil)
		if err != nil {
			results = append(results, probe)
			continue
		}
		resp, err := client.Do(reqHTTP)
		if err != nil {
			results = append(results, probe)
			continue
		}
		resp.Body.Close()
		probe.Accessible = resp.StatusCode >= 200 && resp.StatusCode < 400
		probe.ContentType = resp.Header.Get("Content-Type")
		probe.SizeBytes = resp.ContentLength
		probe.FinalURL = resp.Request.URL.String()
		results = append(results, probe)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "items": results, "readOnly": true, "nextActions": []string{"复核来源和许可证后创建下载计划"}})
}

func rejectPrivateHost(host string) error {
	if net.ParseIP(host) != nil {
		if isPrivateIP(net.ParseIP(host)) {
			return fmt.Errorf("private address")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("private address")
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || strings.HasPrefix(ip.String(), "0.")
}
