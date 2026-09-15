package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type sourceRoundTripper func(*http.Request) (*http.Response, error)

func (fn sourceRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestPublicDialerPinsValidatedDNSAnswer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ips     []net.IP
		blocked bool
	}{
		{"public", []net.IP{net.ParseIP("93.184.216.34")}, false},
		{"mixed", []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("127.0.0.1")}, true},
		{"empty", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookups, dials := 0, 0
			d := publicDialer{
				lookup: func(context.Context, string, string) ([]net.IP, error) { lookups++; return tc.ips, nil },
				dial: func(_ context.Context, _, address string) (net.Conn, error) {
					dials++
					if address != "93.184.216.34:443" {
						t.Errorf("dial re-resolves hostname: %s", address)
					}
					return nil, nil
				},
			}
			_, err := d.DialContext(context.Background(), "tcp", "example.com:443")
			if tc.blocked != errors.Is(err, errNonPublicAddress) || lookups != 1 || (tc.blocked && dials != 0) || (!tc.blocked && dials != 1) {
				t.Fatalf("unexpected dial: %v, lookups %d, dials %d", err, lookups, dials)
			}
		})
	}
}

func TestProbeReportsHTTPStatusWithoutFollowingRedirects(t *testing.T) {
	for _, status := range []int{200, 302, 404, 405, 503} {
		server := testServer()
		calls := 0
		server.sourceTransport = sourceRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != http.MethodHead {
				t.Errorf("probe sent %s", r.Method)
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Location": {"http://127.0.0.1/admin"}, "Content-Type": {"image/jpeg"}}, Body: io.NopCloser(strings.NewReader("")), ContentLength: 42, Request: r}, nil
		})
		res := request(t, server, http.MethodPost, "/api/network/sources/probe", `{"sources":[{"url":"https://example.com/image.jpg"}]}`)
		var body struct {
			Items []SourceProbe `json:"items"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || len(body.Items) != 1 {
			t.Fatalf("unexpected calls %d or result %s", calls, res.Body.String())
		}
		probe := body.Items[0]
		if probe.StatusCode != status || probe.Accessible != (status == 200) || probe.SizeBytes != 42 {
			t.Fatalf("incorrect probe: %+v", probe)
		}
	}
}

func TestProbeErrorsAreBoundedAndDoNotExposeNetworkDetails(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{context.Canceled, "USER_CANCELLED"}, {context.DeadlineExceeded, "TOOL_TIMEOUT"}, {errors.New("dial internal-host: credential-details"), "NETWORK_ERROR"},
	} {
		server := testServer()
		server.sourceTransport = sourceRoundTripper(func(r *http.Request) (*http.Response, error) {
			if _, ok := r.Context().Deadline(); !ok {
				t.Error("probe lacks timeout")
			}
			return nil, tc.err
		})
		res := request(t, server, http.MethodPost, "/api/network/sources/probe", `{"sources":[{"url":"https://example.com/file"}]}`)
		if !strings.Contains(res.Body.String(), tc.code) || strings.Contains(res.Body.String(), "credential-details") {
			t.Errorf("unexpected response %s", res.Body.String())
		}
	}
	transport := newPublicTransport()
	defer transport.CloseIdleConnections()
	if transport.Proxy != nil || transport.MaxResponseHeaderBytes == 0 {
		t.Error("public transport must not trust environment proxies or unlimited headers")
	}
}

func TestOutboundAddressesMustBePublic(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "169.254.169.254", "0.0.0.0", "::", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "224.0.0.1", "ff02::1", "255.255.255.255", "100.64.0.1", "198.18.0.1", "192.0.2.1", "2001:db8::1", "64:ff9b::7f00:1"} {
		t.Run(address, func(t *testing.T) {
			if !isPrivateIP(net.ParseIP(address)) {
				t.Error("non-public destination allowed")
			}
		})
	}
	for _, address := range []string{"8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"} {
		if isPrivateIP(net.ParseIP(address)) {
			t.Errorf("public destination %s denied", address)
		}
	}
}

func TestProbeBlockedSourcesReturnSafeErrors(t *testing.T) {
	for _, tc := range []struct{ url, code string }{
		{"http://127.0.0.1/file.txt", "POLICY_BLOCKED"},
		{"file:///etc/passwd", "VALIDATION_FAILED"},
		{"https://example.com/file.txt?token=test-sensitive", "VALIDATION_FAILED"},
	} {
		t.Run(tc.code+tc.url, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"sources": []DownloadSource{{URL: tc.url}}})
			res := request(t, testServer(), http.MethodPost, "/api/network/sources/probe", string(body))
			if res.Code != http.StatusOK {
				t.Fatalf("unexpected status %d", res.Code)
			}
			var result struct {
				Items []struct {
					Accessible bool   `json:"accessible"`
					Error      string `json:"error"`
				} `json:"items"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Items) != 1 || result.Items[0].Accessible || result.Items[0].Error != tc.code {
				t.Fatalf("missing safe error: %s", res.Body.String())
			}
			if strings.Contains(res.Body.String(), "test-sensitive") {
				t.Error("credential was echoed")
			}
		})
	}
}

func TestDownloadCannotRequestLoopback(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("private"))
	}))
	defer origin.Close()
	root := t.TempDir()
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}})
	server.store.downloads["private-test"] = &DownloadPlan{ID: "private-test", Status: statusRunning, TargetDirectory: root, Sources: []DownloadSource{{URL: origin.URL + "/file.txt", SizeBytes: 7}}}
	server.executeDownload(context.Background(), "private-test")
	if hits.Load() != 0 {
		t.Error("download contacted a loopback service")
	}
	if server.store.downloads["private-test"].Status != statusFailed {
		t.Error("blocked download was not failed")
	}
}
