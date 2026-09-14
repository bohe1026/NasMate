package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
)

type OrganizeRequest struct {
	RootPath string `json:"rootPath"`
	Mode     string `json:"mode"`
}
type OrganizeItem struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Reason      string `json:"reason"`
}
type OrganizePlan struct {
	Status     string         `json:"status"`
	Summary    string         `json:"summary"`
	Items      []OrganizeItem `json:"items"`
	Conflicts  []string       `json:"conflicts"`
	Skipped    []string       `json:"skipped"`
	SpaceDelta int64          `json:"spaceDelta"`
	DryRun     bool           `json:"dryRun"`
}

func (s *Server) handleOrganizeDryRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	var req OrganizeRequest
	if err := decodeJSON(w, r, &req, s.config.MaxBodyBytes); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}
	root, err := s.resolveAuthorizedPath(req.RootPath)
	if err != nil {
		writeError(w, http.StatusForbidden, "PATH_NOT_ALLOWED", "整理路径不在授权范围内")
		return
	}
	if req.Mode == "" {
		req.Mode = "date"
	}
	if req.Mode != "date" && req.Mode != "extension" {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "mode 仅支持 date 或 extension")
		return
	}
	items := make([]OrganizeItem, 0)
	skipped := make([]string, 0)
	conflicts := make([]string, 0)
	files, scanErr := s.storage.Search(context.Background(), FileSearchOptions{RootPath: root, MaxResults: 1000})
	if scanErr != nil && len(files) == 0 {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "暂时无法读取文件元数据")
		return
	}
	seen := map[string]bool{}
	for _, file := range files {
		var folder string
		if req.Mode == "extension" {
			folder = strings.TrimPrefix(file.Extension, ".")
			if folder == "" {
				folder = "无扩展名"
			}
		} else {
			folder = file.ModifiedAt.Format("2006-01")
		}
		dest := filepath.Join(root, folder, file.Name)
		if dest == file.Path {
			skipped = append(skipped, file.Path)
			continue
		}
		if seen[dest] {
			conflicts = append(conflicts, dest)
			continue
		}
		seen[dest] = true
		items = append(items, OrganizeItem{Source: file.Path, Destination: dest, Reason: "Dry Run 仅生成建议，不执行移动"})
	}
	returnPlan := OrganizePlan{Status: "success", Summary: "已生成批量整理预览，未修改任何文件", Items: items, Conflicts: conflicts, Skipped: skipped, DryRun: true}
	writeJSON(w, http.StatusOK, returnPlan)
}
