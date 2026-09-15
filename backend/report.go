package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type HealthReport struct {
	GeneratedAt        time.Time             `json:"generatedAt"`
	ComparedAt         time.Time             `json:"comparedAt,omitempty"`
	Storage            *StorageUsage         `json:"storage,omitempty"`
	FastestGrowing     []UsageGrowth         `json:"fastestGrowing,omitempty"`
	Docker             []ContainerDiagnostic `json:"docker,omitempty"`
	Backup             *BackupStatus         `json:"backup,omitempty"`
	FailedTasks        int                   `json:"failedTasks"`
	FailedDownloads    int                   `json:"failedDownloads"`
	UserMetricsOmitted bool                  `json:"userMetricsOmitted,omitempty"`
	Warnings           []string              `json:"warnings"`
	ReadOnly           bool                  `json:"readOnly"`
}

type UsageGrowth struct {
	Path       string `json:"path"`
	DeltaBytes int64  `json:"deltaBytes"`
}

func (s *Server) previousHealthReport() (HealthReport, bool) {
	if s.config.DataDir == "" {
		return HealthReport{}, false
	}
	entries, err := os.ReadDir(s.config.DataDir)
	if err != nil {
		return HealthReport{}, false
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasPrefix(name, "health-report-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if _, err := time.Parse("20060102-150405", strings.TrimSuffix(strings.TrimPrefix(name, "health-report-"), ".json")); err != nil {
			continue
		}
		file, err := os.Open(filepath.Join(s.config.DataDir, name))
		if err != nil {
			continue
		}
		var report HealthReport
		err = json.NewDecoder(io.LimitReader(file, 256<<10)).Decode(&report)
		_ = file.Close()
		if err == nil && report.Storage != nil && !report.GeneratedAt.IsZero() {
			return report, true
		}
	}
	return HealthReport{}, false
}

func storageGrowth(current, previous []UsageItem) []UsageGrowth {
	baseline := make(map[string]int64, len(previous))
	for _, item := range previous {
		baseline[item.Path] = item.SizeBytes
	}
	items := make([]UsageGrowth, 0, len(current))
	for _, item := range current {
		if delta := item.SizeBytes - baseline[item.Path]; delta > 0 {
			items = append(items, UsageGrowth{Path: item.Path, DeltaBytes: delta})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DeltaBytes > items[j].DeltaBytes })
	if len(items) > 3 {
		items = items[:3]
	}
	return items
}

type Artifact struct {
	Name      string    `json:"name"`
	SizeBytes int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Server) StartHealthScheduler() func() {
	if s.config.HealthInterval <= 0 {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(s.config.HealthInterval)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-ticker.C:
				report := s.buildHealthReport(&http.Request{Method: http.MethodGet})
				_ = s.persistHealthReport(report)
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop); <-done }
}

func (s *Server) persistHealthReport(report HealthReport) error {
	if s.config.DataDir == "" {
		return nil
	}
	if err := os.MkdirAll(s.config.DataDir, 0700); err != nil {
		return err
	}
	report.FailedTasks = 0
	report.FailedDownloads = 0
	report.UserMetricsOmitted = true
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.config.DataDir, fmt.Sprintf("health-report-%s.json", report.GeneratedAt.Format("20060102-150405")))
	tmp, err := os.CreateTemp(s.config.DataDir, ".health-report-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return s.pruneHealthReports(30)
}

func (s *Server) pruneHealthReports(keep int) error {
	if keep < 1 || s.config.DataDir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.config.DataDir)
	if err != nil {
		return err
	}
	type reportFile struct {
		name string
		mod  time.Time
	}
	files := make([]reportFile, 0)
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !validHealthReportName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			files = append(files, reportFile{name: entry.Name(), mod: info.ModTime()})
		}
	}
	if len(files) <= keep {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, file := range files[:len(files)-keep] {
		if err := os.Remove(filepath.Join(s.config.DataDir, file.name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Server) handleArtifacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	items := make([]Artifact, 0)
	if s.config.DataDir != "" {
		entries, err := os.ReadDir(s.config.DataDir)
		if err != nil && !os.IsNotExist(err) {
			writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "无法读取结果文件")
			return
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !validHealthReportName(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil || info.Size() > maxHealthReportBytes {
				continue
			}
			if info.Size() > maxHealthReportBytes {
				continue
			}
			items = append(items, Artifact{Name: entry.Name(), SizeBytes: info.Size(), CreatedAt: info.ModTime().UTC()})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "readOnly": true})
}

const maxHealthReportBytes = 1 << 20

func validHealthReportName(name string) bool {
	if !strings.HasPrefix(name, "health-report-") || !strings.HasSuffix(name, ".json") {
		return false
	}
	_, err := time.Parse("20060102-150405", strings.TrimSuffix(strings.TrimPrefix(name, "health-report-"), ".json"))
	return err == nil
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	if s.config.DataDir == "" || !validHealthReportName(name) {
		writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "结果文件不存在")
		return
	}
	root, err := os.OpenRoot(s.config.DataDir)
	if err != nil {
		writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "结果文件不存在")
		return
	}
	defer root.Close()
	// Do not follow even in-root symlinks: they could expose private state or keys.
	// Non-blocking open prevents a substituted FIFO from hanging the request.
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "结果文件不存在")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxHealthReportBytes {
		writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "结果文件不可用")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxHealthReportBytes+1))
	if err != nil || len(data) > maxHealthReportBytes || !json.Valid(data) {
		writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "结果文件不可用")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleHealthReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	report := s.buildHealthReport(r)
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleGenerateHealthReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	report := s.buildHealthReport(r)
	artifact := ""
	if s.config.DataDir != "" {
		if err := s.persistHealthReport(report); err != nil {
			writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "无法保存健康报告")
			return
		}
		artifact = filepath.Join(s.config.DataDir, fmt.Sprintf("health-report-%s.json", report.GeneratedAt.Format("20060102-150405")))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "success", "summary": "健康报告已生成", "artifact": artifact, "report": report, "readOnly": true})
}

func (s *Server) buildHealthReport(r *http.Request) HealthReport {
	report := HealthReport{GeneratedAt: time.Now().UTC(), Warnings: []string{}, ReadOnly: true}
	if storage, err := s.storage.Usage(r.Context()); err == nil {
		report.Storage = &storage
		if previous, ok := s.previousHealthReport(); ok {
			report.ComparedAt = previous.GeneratedAt
			report.FastestGrowing = storageGrowth(storage.TopItems, previous.Storage.TopItems)
		}
	} else {
		report.Warnings = append(report.Warnings, "存储状态暂时不可用")
	}
	if docker, err := s.docker.List(r.Context()); err == nil {
		report.Docker = docker
	} else {
		report.Warnings = append(report.Warnings, "Docker 状态暂时不可用")
	}
	if backup, err := s.backup.Status(r.Context()); err == nil {
		report.Backup = &backup
	} else {
		report.Warnings = append(report.Warnings, "备份状态暂时不可用")
	}
	user, userErr := s.userFromRequest(r)
	if userErr == nil {
		s.store.mu.RLock()
		for _, task := range s.store.tasks {
			if task.User.ID == user.ID && task.Status == statusFailed {
				report.FailedTasks++
			}
		}
		for _, download := range s.store.downloads {
			if download.User.ID == user.ID && download.Status == statusFailed {
				report.FailedDownloads++
			}
		}
		s.store.mu.RUnlock()
	} else {
		report.Warnings = append(report.Warnings, "未提供用户身份，报告不包含私人任务和下载统计")
	}
	return report
}
