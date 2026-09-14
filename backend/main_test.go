package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testServer() *Server {
	_ = os.MkdirAll("/tmp/ugreen-workspace", 0700)
	return NewServer(Config{DevMode: true, SharedRoots: []string{"/tmp/ugreen-workspace"}, MaxBodyBytes: 1 << 20, AllowedOrigin: "http://127.0.0.1:5173"})
}

func request(t *testing.T, server http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	return res
}

func requestAsUser(t *testing.T, server http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Ugreen-User-ID", "test-user")
	req.Header.Set("Ugreen-User-Name", "测试用户")
	req.Header.Set("Ugreen-User-Type", "admin")
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	return res
}

func TestCreateTaskAndEvents(t *testing.T) {
	server := testServer()
	res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 NAS 空间"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", res.Code, res.Body.String())
	}
	var task Task
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.ID == "" || task.Status != statusCompleted {
		t.Fatalf("unexpected task: %+v", task)
	}
	if len(server.store.events[task.ID]) != 7 {
		t.Fatalf("expected seven durable events, got %d", len(server.store.events[task.ID]))
	}
}

func TestCancelFinishedTaskIsRejected(t *testing.T) {
	server := testServer()
	res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 NAS 空间"}`)
	var task Task
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	res = request(t, server, http.MethodPost, "/api/tasks/"+task.ID+"/cancel", "")
	if res.Code != http.StatusConflict {
		t.Fatalf("expected finished task cancellation to be rejected, got %d: %s", res.Code, res.Body.String())
	}
}

func TestExpensiveEndpointRateLimit(t *testing.T) {
	server := testServer()
	var last *httptest.ResponseRecorder
	for i := 0; i < 21; i++ {
		last = request(t, server, http.MethodGet, "/api/files/search", "")
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("expected rate limit after 20 searches, got %d: %s", last.Code, last.Body.String())
	}
}

func TestValidateNetworkSources(t *testing.T) {
	server := testServer()
	res := request(t, server, http.MethodPost, "/api/network/sources/validate", `{"sources":[{"url":"https://example.com/assets/demo.jpg","license":"CC BY","sizeBytes":100}]}`)
	if res.Code != http.StatusOK {
		t.Fatalf("expected source validation success, got %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		RequiresApproval bool  `json:"requiresApproval"`
		EstimatedBytes   int64 `json:"estimatedBytes"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.RequiresApproval || body.EstimatedBytes != 100 {
		t.Fatalf("unexpected validation result: %+v", body)
	}
	res = request(t, server, http.MethodPost, "/api/network/sources/validate", `{"sources":[{"url":"https://example.com/a?token=secret"}]}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected credential-bearing URL rejection, got %d", res.Code)
	}
}

func TestModelStatusDoesNotExposeCredentials(t *testing.T) {
	server := testServer()
	res := request(t, server, http.MethodGet, "/api/models/status", "")
	if res.Code != http.StatusOK {
		t.Fatalf("expected model status success, got %d", res.Code)
	}
	body := res.Body.String()
	if strings.Contains(body, "APIKey") || strings.Contains(body, "secret") {
		t.Fatalf("model status exposed sensitive fields: %s", body)
	}
}

func TestIndexStatusIsMetadataOnly(t *testing.T) {
	server := testServer()
	res := request(t, server, http.MethodGet, "/api/index/status", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"bodyIndexEnabled":false`) {
		t.Fatalf("expected metadata-only index status, got %d: %s", res.Code, res.Body.String())
	}
}

func TestIndexRebuildPersistsMetadataOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "data")
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}, DataDir: dataDir, MaxBodyBytes: 1 << 20})
	res := request(t, server, http.MethodPost, "/api/index/rebuild", "")
	if res.Code != http.StatusCreated {
		t.Fatalf("expected index rebuild, got %d: %s", res.Code, res.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "metadata-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "metadata-only") || strings.Contains(string(data), "content") {
		t.Fatalf("index contains non-metadata content: %s", data)
	}
	res = request(t, server, http.MethodGet, "/api/index/search?keyword=notes", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "notes.txt") {
		t.Fatalf("expected indexed search result, got %d: %s", res.Code, res.Body.String())
	}
}

func TestValidPlanRejectsUnsafeShape(t *testing.T) {
	tools := NewHarness().Tools
	if validPlan(AgentPlan{Status: "success", Steps: []PlanStep{{Tool: "search_files"}}}, tools) {
		t.Fatal("accepted empty plan reason")
	}
	steps := make([]PlanStep, 4)
	for i := range steps {
		steps[i] = PlanStep{Tool: "search_files", Reason: "read metadata"}
	}
	if validPlan(AgentPlan{Status: "success", Steps: steps}, tools) {
		t.Fatal("accepted more than three steps")
	}
}

func TestGenerateHealthReportPersistsArtifact(t *testing.T) {
	root := t.TempDir()
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}, DataDir: filepath.Join(root, "data"), MaxBodyBytes: 1 << 20})
	res := request(t, server, http.MethodPost, "/api/reports/health/generate", "")
	if res.Code != http.StatusCreated {
		t.Fatalf("expected report creation, got %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Artifact string `json:"artifact"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Artifact == "" {
		t.Fatal("expected persisted report artifact path")
	}
	if _, err := os.Stat(body.Artifact); err != nil {
		t.Fatalf("report artifact not persisted: %v", err)
	}
	res = request(t, server, http.MethodGet, "/api/artifacts/"+filepath.Base(body.Artifact), "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "readOnly") {
		t.Fatalf("expected artifact read, got %d: %s", res.Code, res.Body.String())
	}
	res = request(t, server, http.MethodGet, "/api/artifacts/../state.json", "")
	if res.Code != http.StatusNotFound {
		t.Fatalf("expected traversal artifact rejection, got %d", res.Code)
	}
	res = request(t, server, http.MethodGet, "/api/artifacts", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "health-report-") {
		t.Fatalf("expected artifact listing, got %d: %s", res.Code, res.Body.String())
	}
}

func TestHealthSchedulerDisabledByDefault(t *testing.T) {
	server := NewServer(Config{DevMode: true})
	stop := server.StartHealthScheduler()
	stop()
	if server.config.HealthInterval != 0 {
		t.Fatalf("expected scheduler disabled by default, got %s", server.config.HealthInterval)
	}
}

func TestDownloadExistingTargetIsRejected(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "photos")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(target, "demo.jpg")
	if err := os.WriteFile(existing, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}, MaxBodyBytes: 1 << 20})
	res := request(t, server, http.MethodPost, "/api/downloads/prepare", `{"targetDirectory":"`+target+`","sources":[{"title":"demo","url":"https://example.com/demo.jpg","license":"CC BY","sizeBytes":3}]}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("prepare failed: %d %s", res.Code, res.Body.String())
	}
	var plan DownloadPlan
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	res = request(t, server, http.MethodPost, "/api/downloads/"+plan.ID+"?action=approve", "")
	if res.Code != http.StatusOK {
		t.Fatalf("approve failed: %d %s", res.Code, res.Body.String())
	}
	time.Sleep(100 * time.Millisecond)
	server.store.mu.RLock()
	status := server.store.downloads[plan.ID].Status
	server.store.mu.RUnlock()
	if status != statusFailed {
		t.Fatalf("expected existing target to fail safely, got %s", status)
	}
}

func TestRecoveryPlanIsReadOnlyAndRequiresApproval(t *testing.T) {
	server := testServer()
	res := request(t, server, http.MethodGet, "/api/backups/recovery-plan", "")
	if res.Code != http.StatusOK {
		t.Fatalf("expected recovery plan, got %d: %s", res.Code, res.Body.String())
	}
	var plan RecoveryPlan
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if !plan.ReadOnly || !plan.RequiresApproval || plan.WillOverwrite || len(plan.Steps) == 0 {
		t.Fatalf("unsafe recovery plan: %+v", plan)
	}
}

func TestHealthReportsPruneOldArtifactsOnly(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		name := filepath.Join(dataDir, fmt.Sprintf("health-report-2026010%d-000000.json", i+1))
		if err := os.WriteFile(name, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(dataDir, "state.json")
	if err := os.WriteFile(keep, []byte("state"), 0600); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{DataDir: dataDir})
	if err := server.pruneHealthReports(2); err != nil {
		t.Fatal(err)
	}
	items, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range items {
		if strings.HasPrefix(item.Name(), "health-report-") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected two reports retained, got %d", count)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("non-report file was removed: %v", err)
	}
}

func TestExecuteTaskRunsMultipleReadOnlySteps(t *testing.T) {
	server := testServer()
	now := time.Now().UTC()
	task := &Task{ID: "multi-step", Prompt: "检查 NAS", Status: statusRunning, CreatedAt: now, UpdatedAt: now, User: User{ID: "dev-user"}}
	server.store.tasks[task.ID] = task
	server.executeTask(task.ID, task.Prompt, AgentPlan{Status: "success", Steps: []PlanStep{
		{Tool: "storage_usage", Reason: "容量"},
		{Tool: "inspect_containers", Reason: "容器"},
	}})
	events := server.store.events[task.ID]
	toolCalls := 0
	for _, event := range events {
		if event.Type == "tool.call" {
			toolCalls++
		}
	}
	if toolCalls != 2 {
		t.Fatalf("expected two tool calls, got %d", toolCalls)
	}
	if server.store.tasks[task.ID].Status != statusCompleted {
		t.Fatalf("expected completed task, got %s", server.store.tasks[task.ID].Status)
	}
}

func TestExtractSearchKeyword(t *testing.T) {
	tests := map[string]string{
		"搜索装修方案文件": "装修方案",
		"查找报告":     "报告",
		"找出家庭照片":   "家庭照片",
	}
	for input, want := range tests {
		if got := extractSearchKeyword(input); got != want {
			t.Errorf("extractSearchKeyword(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDownloadRequiresAuthorizedPathAndApproval(t *testing.T) {
	server := testServer()
	body := `{"targetDirectory":"/tmp/not-authorized","sources":[{"title":"demo","url":"https://example.com/demo.jpg","license":"CC BY","sizeBytes":100}]}`
	res := request(t, server, http.MethodPost, "/api/downloads/prepare", body)
	if res.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", res.Code)
	}

	body = `{"targetDirectory":"/tmp/ugreen-workspace/photos","sources":[{"title":"demo","url":"https://example.com/demo.jpg","license":"CC BY","sizeBytes":100}]}`
	res = request(t, server, http.MethodPost, "/api/downloads/prepare", body)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", res.Code, res.Body.String())
	}
	var plan DownloadPlan
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if plan.Status != statusPending {
		t.Fatalf("expected pending approval, got %s", plan.Status)
	}
	res = request(t, server, http.MethodPost, "/api/downloads/"+plan.ID+"?action=approve", "")
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body.String())
	}
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if plan.Status != statusRunning || plan.ApprovedAt == nil {
		t.Fatalf("approval did not update plan: %+v", plan)
	}
	res = request(t, server, http.MethodPost, "/api/downloads/"+plan.ID+"?action=approve", "")
	if res.Code != http.StatusConflict {
		t.Fatalf("expected replayed approval to be rejected, got %d: %s", res.Code, res.Body.String())
	}

	credentialsBody := `{"targetDirectory":"/tmp/ugreen-workspace/photos","sources":[{"title":"demo","url":"https://user:password@example.com/demo.jpg","license":"CC BY","sizeBytes":100}]}`
	res = request(t, server, http.MethodPost, "/api/downloads/prepare", credentialsBody)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected URL credentials to be rejected, got %d: %s", res.Code, res.Body.String())
	}
	queryCredentialsBody := `{"targetDirectory":"/tmp/ugreen-workspace/photos","sources":[{"title":"demo","url":"https://example.com/demo.jpg?access_token=secret","license":"CC BY","sizeBytes":100}]}`
	res = request(t, server, http.MethodPost, "/api/downloads/prepare", queryCredentialsBody)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected URL query credentials to be rejected, got %d: %s", res.Code, res.Body.String())
	}
}

func TestPathTraversalIsRejected(t *testing.T) {
	server := testServer()
	if server.authorizePath("/tmp/ugreen-workspace/../secrets") {
		t.Fatal("path traversal escaped authorized root")
	}
}

func TestSymlinkEscapeIsRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := resolveAuthorizedPath(filepath.Join(link, "secret.txt"), []string{root}); err == nil {
		t.Fatal("symlink path escaped authorized root")
	}
}

func TestFilesystemStorageSearchAndUsage(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	resolvedProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatalf("resolve project path: %v", err)
	}
	report := filepath.Join(project, "report.txt")
	if err := os.WriteFile(report, []byte("report contents"), 0600); err != nil {
		t.Fatalf("write report: %v", err)
	}
	resolvedReport, err := filepath.EvalSymlinks(report)
	if err != nil {
		t.Fatalf("resolve report path: %v", err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatalf("create outside symlink: %v", err)
	}

	storage := NewFilesystemStorage([]string{root})
	minSize := int64(len("report contents"))
	items, err := storage.Search(context.Background(), FileSearchOptions{
		Keyword:      "REPORT",
		Extensions:   []string{"txt"},
		RootPath:     project,
		MinSizeBytes: &minSize,
		MaxResults:   10,
	})
	if err != nil {
		t.Fatalf("search files: %v", err)
	}
	if len(items) != 1 || items[0].Path != resolvedReport || items[0].Extension != ".txt" {
		t.Fatalf("unexpected search result: %+v", items)
	}

	items, err = storage.Search(context.Background(), FileSearchOptions{Keyword: "secret", MaxResults: 10})
	if err != nil {
		t.Fatalf("search symlink files: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("symlink target was searched: %+v", items)
	}

	usage, err := storage.Usage(context.Background())
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if len(usage.Volumes) != 1 || usage.Volumes[0].TotalBytes <= 0 || usage.Volumes[0].FreeBytes <= 0 {
		t.Fatalf("unexpected volume usage: %+v", usage.Volumes)
	}
	if len(usage.TopItems) == 0 || usage.TopItems[0].Path != resolvedProject {
		t.Fatalf("unexpected top usage items: %+v", usage.TopItems)
	}
}

func TestUGOSSharedDirectorySymlinksAreAuthorized(t *testing.T) {
	shared := t.TempDir()
	authorized := t.TempDir()
	file := filepath.Join(authorized, "notes.txt")
	if err := os.WriteFile(file, []byte("notes"), 0600); err != nil {
		t.Fatalf("write authorized file: %v", err)
	}
	link := filepath.Join(shared, "documents")
	if err := os.Symlink(authorized, link); err != nil {
		t.Fatalf("create UGOS shared link: %v", err)
	}

	roots := expandAuthorizedRoots(shared)
	if len(roots) != 2 {
		t.Fatalf("expected virtual and resolved roots, got %v", roots)
	}
	resolvedFile, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatalf("resolve authorized file: %v", err)
	}
	if _, err := resolveAuthorizedPath(filepath.Join(link, "notes.txt"), roots); err != nil {
		t.Fatalf("UGOS authorized symlink was rejected: %v", err)
	}
	items, err := NewFilesystemStorage(roots).Search(context.Background(), FileSearchOptions{Keyword: "notes", MaxResults: 10})
	if err != nil {
		t.Fatalf("search authorized symlink: %v", err)
	}
	if len(items) != 1 || items[0].Path != resolvedFile {
		t.Fatalf("unexpected authorized symlink search result: %+v", items)
	}
}

func TestFileSearchHTTPFiltersAndRejectsUnauthorizedRoot(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notes.md")
	if err := os.WriteFile(file, []byte("notes"), 0600); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	resolvedFile, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatalf("resolve notes path: %v", err)
	}
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}, MaxBodyBytes: 1 << 20})
	path := "/api/files/search?rootPath=" + url.QueryEscape(root) + "&extension=md&minSize=5&modifiedAfter=" + url.QueryEscape(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	res := request(t, server, http.MethodGet, path, "")
	if res.Code != http.StatusOK {
		t.Fatalf("expected search success, got %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Items []FileMetadata `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].Path != resolvedFile {
		t.Fatalf("unexpected HTTP search response: %+v", body.Items)
	}

	res = request(t, server, http.MethodGet, "/api/files/search?rootPath="+url.QueryEscape(filepath.Dir(root)), "")
	if res.Code != http.StatusForbidden {
		t.Fatalf("expected unauthorized root to be rejected, got %d: %s", res.Code, res.Body.String())
	}
}

func TestProtectedRoutesRequireUGOSUser(t *testing.T) {
	server := NewServer(Config{SharedRoots: []string{"/tmp/ugreen-workspace"}, MaxBodyBytes: 1 << 20})
	res := request(t, server, http.MethodGet, "/api/storage/usage", "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.Code)
	}
}

func TestProductionDoesNotExposeMockProviders(t *testing.T) {
	server := NewServer(Config{DevMode: false, SharedRoots: []string{t.TempDir()}, MaxBodyBytes: 1 << 20})
	res := requestAsUser(t, server, http.MethodGet, "/api/docker/containers", "")
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected unavailable Docker provider, got %d: %s", res.Code, res.Body.String())
	}
	res = requestAsUser(t, server, http.MethodGet, "/api/backups/status", "")
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected unavailable backup provider, got %d: %s", res.Code, res.Body.String())
	}
}

func TestStorePersistsTasksAndEvents(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "data", "state.json")
	eventLogPath := filepath.Join(root, "data", "events.jsonl")
	config := Config{
		DevMode:       true,
		SharedRoots:   []string{root},
		StatePath:     statePath,
		EventLogPath:  eventLogPath,
		MaxBodyBytes:  1 << 20,
		AllowedOrigin: "http://127.0.0.1:5173",
	}
	server := NewServer(config)
	res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"持久化测试"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create task failed: %d: %s", res.Code, res.Body.String())
	}
	var created Task
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	restarted := NewServer(config)
	res = request(t, restarted, http.MethodGet, "/api/tasks", "")
	if res.Code != http.StatusOK {
		t.Fatalf("list tasks after restart failed: %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Items []Task `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != created.ID {
		t.Fatalf("task was not restored: %+v", body.Items)
	}
	if len(restarted.store.events[created.ID]) != 7 {
		t.Fatalf("events were not restored: %+v", restarted.store.events[created.ID])
	}
}
