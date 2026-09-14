package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type HealthReport struct {
	GeneratedAt time.Time             `json:"generatedAt"`
	Storage     *StorageUsage         `json:"storage,omitempty"`
	Docker      []ContainerDiagnostic `json:"docker,omitempty"`
	Backup      *BackupStatus         `json:"backup,omitempty"`
	FailedTasks int                   `json:"failedTasks"`
	Warnings    []string              `json:"warnings"`
	ReadOnly    bool                  `json:"readOnly"`
}

type Artifact struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	SizeBytes int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
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
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "health-report-") || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			items = append(items, Artifact{Name: entry.Name(), Path: filepath.Join(s.config.DataDir, entry.Name()), SizeBytes: info.Size(), CreatedAt: info.ModTime().UTC()})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "readOnly": true})
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
		if err := os.MkdirAll(s.config.DataDir, 0700); err != nil {
			writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "无法保存健康报告")
			return
		}
		artifact = filepath.Join(s.config.DataDir, fmt.Sprintf("health-report-%s.json", report.GeneratedAt.Format("20060102-150405")))
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil || os.WriteFile(artifact, data, 0600) != nil {
			writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "无法保存健康报告")
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "success", "summary": "健康报告已生成", "artifact": artifact, "report": report, "readOnly": true})
}

func (s *Server) buildHealthReport(r *http.Request) HealthReport {
	report := HealthReport{GeneratedAt: time.Now().UTC(), Warnings: []string{}, ReadOnly: true}
	if storage, err := s.storage.Usage(r.Context()); err == nil {
		report.Storage = &storage
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
	s.store.mu.RLock()
	for _, task := range s.store.tasks {
		if task.Status == statusFailed {
			report.FailedTasks++
		}
	}
	s.store.mu.RUnlock()
	return report
}
