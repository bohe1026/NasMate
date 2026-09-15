package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestArtifactReadRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"symlink-inside", "symlink-outside", "directory", "fifo", "oversized", "invalid-json"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			name := "health-report-20260915-120000.json"
			path := filepath.Join(root, name)
			var err error
			switch kind {
			case "symlink-inside", "symlink-outside":
				targetRoot := root
				if kind == "symlink-outside" {
					targetRoot = t.TempDir()
				}
				target := filepath.Join(targetRoot, "state.json")
				if err = os.WriteFile(target, []byte(`{"private":"never expose"}`), 0600); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(target, path)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "oversized":
				err = os.WriteFile(path, []byte(strings.Repeat(" ", (1<<20)+1)), 0600)
			case "invalid-json":
				err = os.WriteFile(path, []byte("not a report"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			server := NewServer(Config{DevMode: true, DataDir: root})
			res := request(t, server, http.MethodGet, "/api/artifacts/"+name, "")
			if res.Code == http.StatusOK || strings.Contains(res.Body.String(), "never expose") || strings.Contains(res.Body.String(), root) {
				t.Fatalf("unsafe artifact exposed: status=%d bytes=%d", res.Code, res.Body.Len())
			}
		})
	}
}

func TestArtifactReadReturnsJSONWithSafeHeaders(t *testing.T) {
	root := t.TempDir()
	name := "health-report-20260915-120000.json"
	data := `{"readOnly":true,"warnings":["<script>alert(1)</script>"]}`
	if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{DevMode: true, DataDir: root})
	res := request(t, server, http.MethodGet, "/api/artifacts/"+name, "")
	if res.Code != http.StatusOK || res.Body.String() != data || res.Header().Get("X-Content-Type-Options") != "nosniff" || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected artifact response: %d %v %s", res.Code, res.Header(), res.Body.String())
	}
}

type fixedReportStorage struct{ usage StorageUsage }

func (storage fixedReportStorage) Usage(context.Context) (StorageUsage, error) {
	return storage.usage, nil
}

func (fixedReportStorage) Search(context.Context, FileSearchOptions) ([]FileMetadata, error) {
	return nil, nil
}

func TestHealthReportComparesPreviousSnapshotAndCountsFailedDownloads(t *testing.T) {
	dataDir := t.TempDir()
	server := NewServer(Config{DevMode: true, DataDir: dataDir})
	previous := HealthReport{GeneratedAt: time.Now().UTC().Add(-time.Hour), Storage: &StorageUsage{
		TopItems: []UsageItem{{Path: "/photos", SizeBytes: 100}, {Path: "/videos", SizeBytes: 200}},
	}, ReadOnly: true}
	if err := server.persistHealthReport(previous); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "health-report-20991231-235958.json"), []byte("invalid report"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dataDir, "state.json"), filepath.Join(dataDir, "health-report-20991231-235959.json")); err != nil {
		t.Fatal(err)
	}
	server.storage = fixedReportStorage{usage: StorageUsage{TopItems: []UsageItem{
		{Path: "/photos", SizeBytes: 300}, {Path: "/videos", SizeBytes: 250}, {Path: "/new", SizeBytes: 80},
	}}}
	server.store.downloads["failed"] = &DownloadPlan{ID: "failed", Status: statusFailed, User: User{ID: "dev-user"}}
	server.store.downloads["other-user"] = &DownloadPlan{ID: "other-user", Status: statusFailed, User: User{ID: "other-user"}}
	server.store.downloads["completed"] = &DownloadPlan{ID: "completed", Status: statusCompleted, User: User{ID: "dev-user"}}
	server.store.tasks["failed"] = &Task{ID: "failed", Status: statusFailed, User: User{ID: "dev-user"}}
	server.store.tasks["other-user"] = &Task{ID: "other-user", Status: statusFailed, User: User{ID: "other-user"}}
	report := server.buildHealthReport(httptest.NewRequest(http.MethodGet, "/api/reports/health", nil))
	if report.FailedTasks != 1 || report.FailedDownloads != 1 || len(report.FastestGrowing) != 3 || report.FastestGrowing[0].Path != "/photos" || report.FastestGrowing[0].DeltaBytes != 200 {
		t.Fatalf("report missed download failures or ranked growth incorrectly: %+v", report)
	}
	if report.ComparedAt.IsZero() || !report.ComparedAt.Equal(previous.GeneratedAt) {
		t.Fatalf("report used the wrong baseline: %+v", report)
	}
}

func TestArtifactListDoesNotExposePathsOrUnsafeEntries(t *testing.T) {
	root := t.TempDir()
	name := "health-report-20260915-120000.json"
	if err := os.WriteFile(filepath.Join(root, name), []byte(`{"readOnly":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "health-report-invalid.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, name), filepath.Join(root, "health-report-20260915-120001.json")); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{DevMode: true, DataDir: root})
	res := request(t, server, http.MethodGet, "/api/artifacts", "")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), root) || strings.Contains(res.Body.String(), "health-report-invalid") || strings.Contains(res.Body.String(), "120001") {
		t.Fatalf("unsafe artifact listing: %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), name) {
		t.Fatalf("valid artifact missing: %s", res.Body.String())
	}
}

func TestFirstHealthReportHasNoInventedGrowth(t *testing.T) {
	server := NewServer(Config{DevMode: true, DataDir: t.TempDir()})
	server.storage = fixedReportStorage{usage: StorageUsage{TopItems: []UsageItem{{Path: "/photos", SizeBytes: 100}}}}
	report := server.buildHealthReport(httptest.NewRequest(http.MethodGet, "/api/reports/health", nil))
	if !report.ComparedAt.IsZero() || len(report.FastestGrowing) != 0 {
		t.Fatalf("first report invented growth without a prior snapshot: %+v", report)
	}
}
