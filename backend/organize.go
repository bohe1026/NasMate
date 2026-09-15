package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	Truncated  bool           `json:"truncated"`
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
	if req.Mode != "date" && req.Mode != "extension" && req.Mode != "project" && req.Mode != "duplicate" {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "mode 仅支持 date、extension、project 或 duplicate")
		return
	}
	items := make([]OrganizeItem, 0)
	skipped := make([]string, 0)
	conflicts := make([]string, 0)
	files, scanErr := s.storage.Search(r.Context(), FileSearchOptions{RootPath: root, MaxResults: 1001})
	if errors.Is(scanErr, context.Canceled) || r.Context().Err() != nil {
		writeError(w, http.StatusRequestTimeout, "USER_CANCELLED", "整理预览已取消")
		return
	}
	if scanErr != nil && len(files) == 0 {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "暂时无法读取文件元数据")
		return
	}
	seen := map[string]bool{}
	duplicateSeen := map[string]string{}
	truncated := len(files) > 1000 || scanErr != nil
	if len(files) > 1000 {
		files = files[:1000]
	}
	for _, file := range files {
		var folder string
		if req.Mode == "duplicate" {
			key := strconv.FormatInt(file.SizeBytes, 10) + ":" + file.Extension
			if previous, ok := duplicateSeen[key]; ok {
				skipped = append(skipped, file.Path+"（疑似重复，参考："+previous+"）")
				continue
			}
			duplicateSeen[key] = file.Path
			folder = "重复文件待确认"
		} else if req.Mode == "extension" {
			folder = strings.TrimPrefix(file.Extension, ".")
			if folder == "" {
				folder = "无扩展名"
			}
		} else if req.Mode == "project" {
			folder = filepath.Base(filepath.Dir(file.Path))
			if folder == "." || folder == "" {
				folder = "未分类项目"
			}
		} else {
			folder = file.ModifiedAt.Format("2006-01")
		}
		dest := filepath.Join(root, folder, file.Name)
		if dest == file.Path {
			skipped = append(skipped, file.Path)
			continue
		}
		if _, err := s.resolveAuthorizedPath(dest); err != nil {
			skipped = append(skipped, file.Path)
			continue
		}
		_, statErr := os.Lstat(dest)
		if statErr == nil {
			conflicts = append(conflicts, dest)
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
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
	returnPlan := OrganizePlan{Status: "success", Summary: "已生成批量整理预览，未修改任何文件", Items: items, Conflicts: conflicts, Skipped: skipped, DryRun: true, Truncated: truncated}
	writeJSON(w, http.StatusOK, returnPlan)
}
