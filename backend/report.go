package main

import (
	"net/http"
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

func (s *Server) handleHealthReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
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
	writeJSON(w, http.StatusOK, report)
}
