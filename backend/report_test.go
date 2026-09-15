package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func TestFirstHealthReportHasNoInventedGrowth(t *testing.T) {
	server := NewServer(Config{DevMode: true, DataDir: t.TempDir()})
	server.storage = fixedReportStorage{usage: StorageUsage{TopItems: []UsageItem{{Path: "/photos", SizeBytes: 100}}}}
	report := server.buildHealthReport(httptest.NewRequest(http.MethodGet, "/api/reports/health", nil))
	if !report.ComparedAt.IsZero() || len(report.FastestGrowing) != 0 {
		t.Fatalf("first report invented growth without a prior snapshot: %+v", report)
	}
}
