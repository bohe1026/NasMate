package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

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
