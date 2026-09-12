package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Harness keeps planning, policy and tool execution separate. Tool inputs are
// schema-shaped values; a model never receives direct command execution.
type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"readOnly"`
}

type PlanStep struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
}

type AgentPlan struct {
	Status      string     `json:"status"`
	Summary     string     `json:"summary"`
	NextActions []string   `json:"next_actions"`
	Artifacts   []string   `json:"artifacts"`
	Steps       []PlanStep `json:"steps"`
}

type Harness struct {
	Tools []ToolSpec
}

func NewHarness() *Harness {
	return &Harness{Tools: []ToolSpec{
		{Name: "search_files", Description: "搜索授权目录中的文件元数据", ReadOnly: true},
		{Name: "storage_usage", Description: "读取授权目录和文件系统容量", ReadOnly: true},
		{Name: "inspect_containers", Description: "读取 Docker 容器状态和受限日志", ReadOnly: true},
		{Name: "backup_status", Description: "读取备份状态和恢复有效性", ReadOnly: true},
		{Name: "prepare_download", Description: "生成下载计划并等待用户确认", ReadOnly: false},
	}}
}

func (h *Harness) Plan(_ context.Context, prompt string) AgentPlan {
	prompt = strings.TrimSpace(prompt)
	steps := make([]PlanStep, 0, 2)
	switch {
	case strings.Contains(prompt, "下载"):
		steps = append(steps, PlanStep{Tool: "prepare_download", Reason: "下载属于外部网络和本地写入操作，必须先生成审批计划"})
	case strings.Contains(prompt, "Docker") || strings.Contains(prompt, "容器"):
		steps = append(steps, PlanStep{Tool: "inspect_containers", Reason: "先执行只读容器诊断"})
	case strings.Contains(prompt, "备份") || strings.Contains(prompt, "恢复"):
		steps = append(steps, PlanStep{Tool: "backup_status", Reason: "先读取备份状态，再生成恢复建议"})
	case strings.Contains(prompt, "容量") || strings.Contains(prompt, "空间") || strings.Contains(prompt, "增长"):
		steps = append(steps, PlanStep{Tool: "storage_usage", Reason: "先读取容量和目录占用"})
	default:
		steps = append(steps, PlanStep{Tool: "search_files", Reason: "默认从授权范围内的元数据搜索开始"})
	}
	return AgentPlan{Status: "success", Summary: "已生成受策略约束的工具计划", NextActions: []string{"查看工具计划", "需要写入时等待真实用户审批"}, Artifacts: []string{}, Steps: steps}
}

func (h *Harness) HandlePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	var req taskCreateRequest
	if err := decodeJSON(w, r, &req, 1<<20); err != nil || strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "prompt 不能为空")
		return
	}
	plan := h.Plan(r.Context(), req.Prompt)
	writeJSON(w, http.StatusOK, plan)
}

func (h *Harness) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Tools []ToolSpec `json:"tools"`
	}{Tools: h.Tools})
}

func (h *Harness) String() string { return fmt.Sprintf("harness tools=%d", len(h.Tools)) }
