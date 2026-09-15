package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedDownload(t *testing.T, maxBytes int64) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	now := time.Now()
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}})
	server.store.downloads["test-download"] = &DownloadPlan{ID: "test-download", Status: statusRunning, TargetDirectory: root, EstimatedBytes: maxBytes, ApprovedAt: &now, Sources: []DownloadSource{{Title: "test", URL: "https://example.com/file.txt", SizeBytes: maxBytes, License: "CC0"}}}
	return server, root
}

func sourceResponse(req *http.Request, data string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader(data)), ContentLength: int64(len(data)), Request: req}
}

func TestDownloadDoesNotOverwriteRacingDestination(t *testing.T) {
	s, root := seedDownload(t, 3)
	path := filepath.Join(root, "file.txt")
	s.sourceTransport = sourceRoundTripper(func(r *http.Request) (*http.Response, error) {
		if err := os.WriteFile(path, []byte("user's file"), 0600); err != nil {
			t.Fatal(err)
		}
		return sourceResponse(r, "new"), nil
	})
	s.executeDownload(context.Background(), "test-download")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "user's file" {
		t.Fatalf("existing file overwritten: %q, %v", data, err)
	}
	if s.store.downloads["test-download"].Status != statusFailed {
		t.Fatal("collision not reported")
	}
}

func TestDownloadHonorsApprovedByteBudget(t *testing.T) {
	s, root := seedDownload(t, 3)
	s.sourceTransport = sourceRoundTripper(func(r *http.Request) (*http.Response, error) {
		resp := sourceResponse(r, "larger than approved")
		resp.ContentLength = -1
		return resp, nil
	})
	s.executeDownload(context.Background(), "test-download")
	if s.store.downloads["test-download"].Status != statusFailed {
		t.Fatal("download exceeded approved size")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("partial or oversized files left behind: %v", entries)
	}
}

func TestDownloadCancellationCannotBecomeCompleted(t *testing.T) {
	s, root := seedDownload(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	s.sourceTransport = sourceRoundTripper(func(r *http.Request) (*http.Response, error) {
		s.store.downloads["test-download"].Status = statusCancelled
		cancel()
		return sourceResponse(r, "new"), nil
	})
	s.executeDownload(ctx, "test-download")
	if s.store.downloads["test-download"].Status != statusCancelled {
		t.Fatal("cancelled task changed to completed")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("cancelled download wrote files: %v", entries)
	}
}

type brokenSource struct{ read bool }

func (b *brokenSource) Read(p []byte) (int, error) {
	if !b.read {
		b.read = true
		return copy(p, "partial"), nil
	}
	return 0, errors.New("network credentials-secret")
}
func (*brokenSource) Close() error { return nil }

func TestDownloadFailureCleansPartialAndRedactsErrors(t *testing.T) {
	s, root := seedDownload(t, 100)
	s.sourceTransport = sourceRoundTripper(func(r *http.Request) (*http.Response, error) {
		resp := sourceResponse(r, "")
		resp.Body = &brokenSource{}
		resp.ContentLength = -1
		return resp, nil
	})
	s.executeDownload(context.Background(), "test-download")
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Errorf("partial file exposed: %v", entries)
	}
	events, _ := json.Marshal(s.store.events)
	if strings.Contains(string(events), "credentials-secret") {
		t.Error("raw network error logged")
	}
}

func TestDownloadDirectorySwapCannotEscapeRoot(t *testing.T) {
	s, root := seedDownload(t, 3)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	s.store.downloads["test-download"].TargetDirectory = target
	s.sourceTransport = sourceRoundTripper(func(r *http.Request) (*http.Response, error) {
		if err := os.Rename(target, filepath.Join(root, "original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, target); err != nil {
			t.Fatal(err)
		}
		return sourceResponse(r, "new"), nil
	})
	s.executeDownload(context.Background(), "test-download")
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Error("download escaped authorized directory after symlink swap")
	}
}

func TestDownloadPlanRequiresLicenseBudgetAndAllowedType(t *testing.T) {
	for _, source := range []DownloadSource{
		{URL: "https://example.com/file.txt", SizeBytes: 10},
		{URL: "https://example.com/file.txt", License: "CC0", SizeBytes: 0},
		{URL: "https://example.com/program.exe", License: "CC0", SizeBytes: 10},
	} {
		root := t.TempDir()
		s := NewServer(Config{DevMode: true, SharedRoots: []string{root}})
		body, _ := json.Marshal(downloadPrepareRequest{TargetDirectory: root, Sources: []DownloadSource{source}})
		res := request(t, s, http.MethodPost, "/api/downloads/prepare", string(body))
		if res.Code != http.StatusBadRequest {
			t.Errorf("unsafe download plan accepted: %s", res.Body.String())
		}
		if len(s.store.downloads) != 0 {
			t.Error("invalid plan persisted")
		}
	}
}

func TestDownloadApprovalLimitsConcurrentWorkers(t *testing.T) {
	s, root := seedDownload(t, 3)
	for _, id := range []string{"busy-1", "busy-2", "busy-3"} {
		s.downloadsCtx[id] = func() {}
	}
	body, _ := json.Marshal(downloadPrepareRequest{TargetDirectory: root, Sources: []DownloadSource{{URL: "https://example.com/file.txt", License: "CC0", SizeBytes: 3}}})
	res := request(t, s, http.MethodPost, "/api/downloads/prepare", string(body))
	var plan DownloadPlan
	if err := json.Unmarshal(res.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	res = request(t, s, http.MethodPost, "/api/downloads/"+plan.ID+"?action=approve", "")
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrency not limited: %d", res.Code)
	}
	if s.store.downloads[plan.ID].Status != statusPending {
		t.Fatal("failed approval changed pending plan")
	}
}
