package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
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

type ModelObservation struct {
	Status      string     `json:"status"`
	Summary     string     `json:"summary"`
	NextActions []string   `json:"next_actions"`
	Artifacts   []string   `json:"artifacts"`
	Plan        *AgentPlan `json:"plan,omitempty"`
}

type Harness struct {
	Tools []ToolSpec
	model ModelProvider
}

type ModelStatus struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Configured bool   `json:"configured"`
	Mode       string `json:"mode"`
}

type ModelProvider interface {
	Plan(context.Context, string, []ToolSpec) (AgentPlan, error)
}

var credentialAssignment = regexp.MustCompile(`(?i)(?:password|passwd|token|api[_-]?key|authorization|cookie|secret)\s*["']?\s*[:=]\s*["']?\S+`)
var credentialBearer = regexp.MustCompile(`(?i)\bbearer\s+\S+|\b(?:sk-[a-z0-9_-]{16,}|gh[opsu]_[a-z0-9]{16,})\b`)

func containsCredential(prompt string) bool {
	return credentialAssignment.MatchString(prompt) || credentialBearer.MatchString(prompt)
}

func NewHarness() *Harness {
	h := &Harness{Tools: []ToolSpec{
		{Name: "search_files", Description: "搜索授权目录中的文件元数据", ReadOnly: true},
		{Name: "search_index", Description: "查询授权目录中的本地元数据索引", ReadOnly: true},
		{Name: "storage_usage", Description: "读取授权目录和文件系统容量", ReadOnly: true},
		{Name: "inspect_containers", Description: "读取 Docker 容器状态和受限日志", ReadOnly: true},
		{Name: "backup_status", Description: "读取备份状态和恢复有效性", ReadOnly: true},
		{Name: "prepare_download", Description: "下载计划必须由独立接口校验来源和目标目录后创建，模型不能代替用户确认", ReadOnly: false},
	}}
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
	key := envOr("LLM_API_KEY", os.Getenv("DEEPSEEK_API_KEY"))
	switch provider {
	case "ollama":
		h.model = OpenAICompatibleProvider{Name: "Ollama", Mode: "local", BaseURL: envOr("LLM_BASE_URL", "http://127.0.0.1:11434"), Model: envOr("LLM_MODEL", "llama3.2")}
	case "vllm":
		h.model = OpenAICompatibleProvider{Name: "vLLM", Mode: "local", BaseURL: envOr("LLM_BASE_URL", "http://127.0.0.1:8000"), Model: envOr("LLM_MODEL", "local-model")}
	case "openai-compatible":
		if key != "" {
			h.model = OpenAICompatibleProvider{Name: "OpenAI-compatible", BaseURL: envOr("LLM_BASE_URL", "https://api.openai.com"), APIKey: key, Model: envOr("LLM_MODEL", "gpt-4o-mini")}
		}
	default:
		if key != "" {
			h.model = OpenAICompatibleProvider{Name: "DeepSeek", BaseURL: envOr("LLM_BASE_URL", "https://api.deepseek.com"), APIKey: key, Model: envOr("LLM_MODEL", "deepseek-chat")}
		}
	}
	return h
}

func (h *Harness) ModelStatus() ModelStatus {
	if provider, ok := h.model.(OpenAICompatibleProvider); ok {
		name := provider.Name
		if name == "" {
			name = "OpenAI-compatible"
			if strings.Contains(strings.ToLower(provider.BaseURL), "deepseek") {
				name = "DeepSeek"
			}
		}
		mode := provider.Mode
		if mode == "" {
			mode = "cloud"
		}
		return ModelStatus{Provider: name, Model: provider.Model, Configured: provider.APIKey != "" || mode == "local", Mode: mode}
	}
	return ModelStatus{Provider: "规则规划器", Model: "builtin-policy", Configured: true, Mode: "local"}
}

func (h *Harness) Plan(ctx context.Context, prompt string) AgentPlan {
	plan, _ := h.planWithTrace(ctx, prompt)
	return plan
}

func (h *Harness) planWithTrace(ctx context.Context, prompt string) (AgentPlan, *ModelObservation) {
	fallback := localPlan(prompt)
	if h.model != nil {
		plan, err := h.model.Plan(ctx, modelIntent(fallback), h.Tools)
		if err == nil && validPlan(plan, h.Tools) {
			return plan, &ModelObservation{Status: "success", Summary: "模型计划通过本地工具白名单校验", NextActions: []string{"按策略检查工具步骤"}, Artifacts: []string{}, Plan: &plan}
		}
		if ctx.Err() != nil {
			return fallback, &ModelObservation{Status: "warning", Summary: "模型请求已取消或超时，不执行其计划", NextActions: []string{"由用户重新发起任务"}, Artifacts: []string{}}
		}
		return fallback, &ModelObservation{Status: "warning", Summary: "模型请求失败或计划未通过工具白名单，已使用本地规则", NextActions: []string{"检查模型配置后重试", "查看本地规则计划"}, Artifacts: []string{}}
	}
	return fallback, nil
}

func modelIntent(plan AgentPlan) string {
	return "本地识别的任务类型：" + plan.Steps[0].Tool + "。仅规划授权的只读工具或待审批计划，不包含用户原文、文件路径或内容。"
}

func localPlan(prompt string) AgentPlan {
	prompt = strings.TrimSpace(prompt)
	steps := make([]PlanStep, 0, 2)
	switch {
	case strings.Contains(prompt, "下载"):
		steps = append(steps, PlanStep{Tool: "prepare_download", Reason: "下载需要结构化来源、授权目录和真实用户审批，对话本身不能创建计划"})
	case strings.Contains(prompt, "索引"):
		steps = append(steps, PlanStep{Tool: "search_index", Reason: "查询本地元数据索引，不读取文件正文"})
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

func validPlan(plan AgentPlan, tools []ToolSpec) bool {
	if plan.Status != "success" || len(plan.Steps) == 0 || len(plan.Steps) > 3 {
		return false
	}
	for _, step := range plan.Steps {
		if strings.TrimSpace(step.Tool) == "" || strings.TrimSpace(step.Reason) == "" {
			return false
		}
		found := false
		for _, tool := range tools {
			if step.Tool == tool.Name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type OpenAICompatibleProvider struct {
	Name    string
	Mode    string
	BaseURL string
	APIKey  string
	Model   string
}

func planningSystemPrompt(tools []ToolSpec) string {
	schema := `{"status":"success","summary":"...","next_actions":["..."],"artifacts":[],"steps":[{"tool":"search_files","reason":"..."}]}`
	return "Return only JSON matching this shape: " + schema + ". Allowed tools: " + toolNames(tools) + ". Tool execution and user approval are handled locally; do not assume they have occurred."
}

func (p OpenAICompatibleProvider) Plan(ctx context.Context, prompt string, tools []ToolSpec) (AgentPlan, error) {
	type request struct {
		Model          string              `json:"model"`
		Messages       []map[string]string `json:"messages"`
		Temperature    int                 `json:"temperature"`
		ResponseFormat map[string]string   `json:"response_format"`
	}
	body, _ := json.Marshal(request{Model: p.Model, Temperature: 0, ResponseFormat: map[string]string{"type": "json_object"}, Messages: []map[string]string{{"role": "system", "content": planningSystemPrompt(tools)}, {"role": "user", "content": prompt}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return AgentPlan{}, err
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return AgentPlan{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AgentPlan{}, fmt.Errorf("model status %d", resp.StatusCode)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 64<<10)).Decode(&envelope); err != nil || len(envelope.Choices) == 0 {
		return AgentPlan{}, fmt.Errorf("invalid model response")
	}
	var plan AgentPlan
	if err := json.Unmarshal([]byte(envelope.Choices[0].Message.Content), &plan); err != nil {
		return AgentPlan{}, err
	}
	return plan, nil
}
func toolNames(tools []ToolSpec) string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return strings.Join(names, ",")
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
