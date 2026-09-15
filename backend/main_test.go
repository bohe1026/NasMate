package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func waitForTaskStatus(t *testing.T, server *Server, id, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		server.store.mu.RLock()
		task := server.store.tasks[id]
		status := ""
		if task != nil {
			status = task.Status
		}
		server.store.mu.RUnlock()
		if status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach %s", id, want)
}

func waitForTaskWorker(t *testing.T, server *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		server.taskMu.Lock()
		_, running := server.tasksCtx[id]
		server.taskMu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s worker did not stop", id)
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
	if task.ID == "" || task.Status != statusPlanning {
		t.Fatalf("unexpected task: %+v", task)
	}
	waitForTaskStatus(t, server, task.ID, statusCompleted)
	waitForTaskWorker(t, server, task.ID)
	if events := server.store.events[task.ID]; len(events) != 8 || events[len(events)-1].Type != "session.completed" {
		t.Fatalf("expected a completed session after eight durable events, got %+v", events)
	}
}

func TestCancelFinishedTaskIsRejected(t *testing.T) {
	server := testServer()
	res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 NAS 空间"}`)
	var task Task
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, server, task.ID, statusCompleted)
	res = request(t, server, http.MethodPost, "/api/tasks/"+task.ID+"/cancel", "")
	if res.Code != http.StatusConflict {
		t.Fatalf("expected finished task cancellation to be rejected, got %d: %s", res.Code, res.Body.String())
	}
}

func TestResumeFinishedTaskCreatesLinkedSession(t *testing.T) {
	server := testServer()
	res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 NAS 空间"}`)
	var original Task
	if err := json.NewDecoder(res.Body).Decode(&original); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, server, original.ID, statusCompleted)
	res = request(t, server, http.MethodPost, "/api/tasks/"+original.ID+"/resume", "")
	if res.Code != http.StatusCreated {
		t.Fatalf("resume failed: %d: %s", res.Code, res.Body.String())
	}
	var resumed Task
	if err := json.NewDecoder(res.Body).Decode(&resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.ParentTaskID != original.ID || resumed.Prompt != original.Prompt || resumed.ID == original.ID {
		t.Fatalf("invalid linked task: %+v", resumed)
	}
	waitForTaskWorker(t, server, resumed.ID)
	server.store.mu.RLock()
	defer server.store.mu.RUnlock()
	if server.store.tasks[original.ID].Status != statusCompleted {
		t.Fatal("resume changed original task")
	}
	foundFork := false
	for _, event := range server.store.events[resumed.ID] {
		if event.Type == "session.forked" {
			foundFork = true
			data, ok := event.Data.(map[string]string)
			if !ok || data["parentTaskId"] != original.ID || data["parentStatus"] == "" {
				t.Fatalf("fork event lost parent context: %+v", event.Data)
			}
		}
	}
	if !foundFork {
		t.Fatalf("missing fork event: %+v", server.store.events[resumed.ID])
	}
}

func TestResumeTaskCannotCrossUsers(t *testing.T) {
	server := testServer()
	now := time.Now().UTC()
	server.store.tasks["private"] = &Task{ID: "private", Prompt: "检查 NAS", Status: statusFailed, CreatedAt: now, UpdatedAt: now, User: User{ID: "other-user"}}
	res := request(t, server, http.MethodPost, "/api/tasks/private/resume", "")
	if res.Code != http.StatusNotFound {
		t.Fatalf("cross-user resume exposed task: %d: %s", res.Code, res.Body.String())
	}
}

func TestTaskEventsAreBoundedAndPaginated(t *testing.T) {
	server := testServer()
	for i := 0; i < 205; i++ {
		server.store.appendEvent("events", "task.progress", map[string]int{"step": i})
	}
	server.store.mu.Lock()
	server.store.tasks["events"] = &Task{ID: "events", Prompt: "查看轨迹", Status: statusCompleted, User: User{ID: "dev-user"}}
	server.store.mu.Unlock()
	res := request(t, server, http.MethodGet, "/api/tasks/events/events?limit=200", "")
	if res.Code != http.StatusOK {
		t.Fatalf("events page failed: %d: %s", res.Code, res.Body.String())
	}
	var page struct {
		Items      []Event `json:"items"`
		Truncated  bool    `json:"truncated"`
		NextBefore int64   `json:"nextBefore"`
	}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 200 || !page.Truncated || page.NextBefore == 0 || page.Items[0].ID != 6 {
		t.Fatalf("unexpected first page: %+v", page)
	}
	res = request(t, server, http.MethodGet, fmt.Sprintf("/api/tasks/events/events?limit=10&before=%d", page.NextBefore), "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"step":4`) {
		t.Fatalf("previous page failed: %d: %s", res.Code, res.Body.String())
	}
	res = request(t, server, http.MethodGet, "/api/tasks/events/events?limit=201", "")
	if res.Code != http.StatusBadRequest {
		t.Fatalf("oversized page was accepted: %d", res.Code)
	}
}

func TestSessionEventsHaveRetentionLimit(t *testing.T) {
	server := testServer()
	for i := 0; i < maxSessionEvents+25; i++ {
		server.store.appendEvent("retained", "task.progress", map[string]int{"step": i})
	}
	server.store.mu.RLock()
	events := append([]Event(nil), server.store.events["retained"]...)
	server.store.mu.RUnlock()
	if len(events) != maxSessionEvents || events[0].Data.(map[string]int)["step"] != 25 {
		t.Fatalf("event retention limit failed: len=%d first=%v", len(events), events[0].Data)
	}
}

func TestTaskEventsRejectCursorBeyondSession(t *testing.T) {
	server := testServer()
	server.store.mu.Lock()
	server.store.tasks["events-cursor"] = &Task{ID: "events-cursor", Prompt: "查看轨迹", Status: statusCompleted, User: User{ID: "dev-user"}}
	server.store.mu.Unlock()
	server.store.appendEvent("events-cursor", "task.progress", map[string]string{"ok": "yes"})
	res := request(t, server, http.MethodGet, "/api/tasks/events-cursor/events?before=1&limit=10", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"items":[]`) || strings.Contains(res.Body.String(), `"truncated":true`) {
		t.Fatalf("invalid cursor was treated as a page: %d: %s", res.Code, res.Body.String())
	}
}

func TestTaskEventsLastFullPageHasNoOlderCursor(t *testing.T) {
	server := testServer()
	server.store.appendEvent("another-task", "task.progress", map[string]string{"private": "other"})
	server.store.tasks["single"] = &Task{ID: "single", Prompt: "查看轨迹", Status: statusCompleted, User: User{ID: "dev-user"}}
	server.store.appendEvent("single", "task.progress", map[string]string{"step": "only"})
	res := request(t, server, http.MethodGet, "/api/tasks/single/events?limit=1", "")
	var page struct {
		Items      []Event `json:"items"`
		Truncated  bool    `json:"truncated"`
		NextBefore int64   `json:"nextBefore"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusOK || len(page.Items) != 1 || page.Truncated || page.NextBefore != 0 {
		t.Fatalf("global event ID incorrectly created an older cursor: %d: %+v", res.Code, page)
	}
}

func TestTaskListIsPaginatedAndScoped(t *testing.T) {
	server := testServer()
	now := time.Now().UTC()
	server.store.mu.Lock()
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("task-%03d", i)
		server.store.tasks[id] = &Task{ID: id, Prompt: id, Status: statusCompleted, UpdatedAt: now.Add(time.Duration(i) * time.Second), User: User{ID: "dev-user"}}
	}
	server.store.tasks["other"] = &Task{ID: "other", Prompt: "other", Status: statusCompleted, UpdatedAt: now.Add(time.Hour), User: User{ID: "other-user"}}
	server.store.mu.Unlock()
	res := request(t, server, http.MethodGet, "/api/tasks?limit=100", "")
	if res.Code != http.StatusOK {
		t.Fatalf("task page failed: %d: %s", res.Code, res.Body.String())
	}
	var page struct {
		Items      []Task `json:"items"`
		Truncated  bool   `json:"truncated"`
		NextOffset int    `json:"nextOffset"`
	}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 100 || !page.Truncated || page.NextOffset != 100 {
		t.Fatalf("unexpected first task page: %+v", page)
	}
	for _, item := range page.Items {
		if item.User.ID != "dev-user" {
			t.Fatalf("cross-user task leaked: %+v", item)
		}
	}
	res = request(t, server, http.MethodGet, "/api/tasks?limit=100&offset=100", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"truncated":false`) || !strings.Contains(res.Body.String(), `"nextOffset":101`) {
		t.Fatalf("last task page was marked incorrectly: %d: %s", res.Code, res.Body.String())
	}
}

func TestStopBackgroundWorkCancelsAndPersistsRunningTasks(t *testing.T) {
	server := testServer()
	ctx, cancel := context.WithCancel(context.Background())
	server.taskMu.Lock()
	server.tasksCtx["shutdown-task"] = cancel
	server.taskMu.Unlock()
	now := time.Now().UTC()
	server.store.mu.Lock()
	server.store.tasks["shutdown-task"] = &Task{ID: "shutdown-task", Prompt: "检查空间", Status: statusRunning, CreatedAt: now, UpdatedAt: now, User: User{ID: "dev-user"}}
	server.store.mu.Unlock()
	server.StopBackgroundWork()
	if ctx.Err() == nil {
		t.Fatal("running task context was not cancelled")
	}
	server.store.mu.RLock()
	task := *server.store.tasks["shutdown-task"]
	server.store.mu.RUnlock()
	if task.Status != statusCancelled || !strings.Contains(task.Summary, "服务关闭") {
		t.Fatalf("task was not safely cancelled: %+v", task)
	}
}

func TestStopBackgroundWorkRejectsNewTasks(t *testing.T) {
	server := testServer()
	server.StopBackgroundWork()
	res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 NAS"}`)
	if res.Code != http.StatusServiceUnavailable || !strings.Contains(res.Body.String(), "NAS_OFFLINE") {
		t.Fatalf("shutdown admission gate failed: %d %s", res.Code, res.Body.String())
	}
}

type cancelAwareStorage struct {
	started     chan struct{}
	release     chan struct{}
	seenContext chan error
}

func (storage cancelAwareStorage) Usage(ctx context.Context) (StorageUsage, error) {
	close(storage.started)
	<-storage.release
	storage.seenContext <- ctx.Err()
	return StorageUsage{}, ctx.Err()
}

func (cancelAwareStorage) Search(context.Context, FileSearchOptions) ([]FileMetadata, error) {
	return nil, nil
}

func TestCancelRunningTaskStopsReadOnlyToolAndKeepsCancelledState(t *testing.T) {
	server := testServer()
	provider := cancelAwareStorage{started: make(chan struct{}), release: make(chan struct{}), seenContext: make(chan error, 1)}
	server.storage = provider
	created := make(chan *httptest.ResponseRecorder, 1)
	go func() { created <- request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 NAS 空间"}`) }()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("read-only tool did not start")
	}
	server.store.mu.RLock()
	var id string
	for taskID := range server.store.tasks {
		id = taskID
	}
	server.store.mu.RUnlock()
	if id == "" {
		t.Fatal("task not persisted before tool execution")
	}
	cancelled := request(t, server, http.MethodPost, "/api/tasks/"+id+"/cancel", "")
	if cancelled.Code != http.StatusOK {
		t.Fatalf("expected cancellation, got %d: %s", cancelled.Code, cancelled.Body.String())
	}
	close(provider.release)
	select {
	case err := <-provider.seenContext:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("tool context was not cancelled: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not observe cancellation")
	}
	select {
	case result := <-created:
		if result.Code != http.StatusCreated {
			t.Fatalf("task creation failed: %s", result.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("task creation did not finish after cancellation")
	}
	waitForTaskStatus(t, server, id, statusCancelled)
	waitForTaskWorker(t, server, id)
	server.store.mu.RLock()
	defer server.store.mu.RUnlock()
	for _, event := range server.store.events[id] {
		if event.Type == "tool.result" {
			t.Fatalf("cancelled task recorded a tool result after cancellation: %+v", event)
		}
	}
}

func TestCreateTaskRespondsBeforeSlowReadOnlyTool(t *testing.T) {
	server := testServer()
	provider := cancelAwareStorage{started: make(chan struct{}), release: make(chan struct{}), seenContext: make(chan error, 1)}
	server.storage = provider
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(provider.release) }) }
	defer release()
	created := make(chan *httptest.ResponseRecorder, 1)
	go func() { created <- request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 NAS 空间"}`) }()
	select {
	case result := <-created:
		if result.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", result.Code, result.Body.String())
		}
		var task Task
		if err := json.Unmarshal(result.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		if task.Status != statusPlanning {
			t.Fatalf("expected planning task when created, got %+v", task)
		}
		cancelled := request(t, server, http.MethodPost, "/api/tasks/"+task.ID+"/cancel", "")
		if cancelled.Code != http.StatusOK {
			t.Fatalf("expected cancellation to remain available, got %d", cancelled.Code)
		}
		release()
		waitForTaskWorker(t, server, task.ID)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("task creation blocked on the read-only tool")
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
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"not_generated"`) || !strings.Contains(res.Body.String(), `"bodyIndexEnabled":false`) {
		t.Fatalf("expected metadata-only index status, got %d: %s", res.Code, res.Body.String())
	}
}

func TestIndexStatusDistinguishesUnavailableIndex(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dataDir, "metadata-index.json"), 0700); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}, DataDir: dataDir})
	res := request(t, server, http.MethodGet, "/api/index/status", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"unavailable"`) {
		t.Fatalf("unavailable index was misreported: %d %s", res.Code, res.Body.String())
	}
}

func TestIndexStatusCountsOnlyAuthorizedItems(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	items := []FileMetadata{{Name: "inside.txt", Path: filepath.Join(root, "inside.txt")}, {Name: "outside.txt", Path: filepath.Join(outside, "outside.txt")}}
	data, _ := json.Marshal(map[string]any{"generatedAt": time.Now().UTC(), "items": items})
	if err := os.WriteFile(filepath.Join(dataDir, "metadata-index.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}, DataDir: dataDir})
	res := request(t, server, http.MethodGet, "/api/index/status", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"itemCount":1`) {
		t.Fatalf("unauthorized index entries counted: %d %s", res.Code, res.Body.String())
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
	res = request(t, server, http.MethodGet, "/api/index/status", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"available"`) || !strings.Contains(res.Body.String(), `"itemCount":1`) {
		t.Fatalf("expected generated index status, got %d: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(string(data), "metadata-only") || strings.Contains(string(data), "content") {
		t.Fatalf("index contains non-metadata content: %s", data)
	}
	res = request(t, server, http.MethodGet, "/api/index/search?keyword=notes", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "notes.txt") {
		t.Fatalf("expected indexed search result, got %d: %s", res.Code, res.Body.String())
	}
}

type cancelledIndexStorage struct{}

func (cancelledIndexStorage) Usage(context.Context) (StorageUsage, error) { return StorageUsage{}, nil }
func (cancelledIndexStorage) Search(ctx context.Context, _ FileSearchOptions) ([]FileMetadata, error) {
	return nil, ctx.Err()
}

func TestIndexRebuildReportsCancellation(t *testing.T) {
	root := t.TempDir()
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}, DataDir: filepath.Join(root, "data")})
	server.storage = cancelledIndexStorage{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/index/rebuild", nil).WithContext(ctx)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusRequestTimeout || !strings.Contains(res.Body.String(), "USER_CANCELLED") {
		t.Fatalf("cancellation was not reported: %d %s", res.Code, res.Body.String())
	}
}

func TestIndexRebuildCanPersistMoreThanDefaultSearchPage(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 1001; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%04d.txt", i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dataDir := filepath.Join(root, "data")
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}, DataDir: dataDir})
	res := request(t, server, http.MethodPost, "/api/index/rebuild", "")
	if res.Code != http.StatusCreated {
		t.Fatalf("index rebuild failed: %d %s", res.Code, res.Body.String())
	}
	status := request(t, server, http.MethodGet, "/api/index/status", "")
	if !strings.Contains(status.Body.String(), `"itemCount":1001`) {
		t.Fatalf("index was silently limited to default page: %s", status.Body.String())
	}
}

func TestIndexSearchPaginatesAuthorizedMatches(t *testing.T) {
	root := t.TempDir()
	items := make([]FileMetadata, 0, 105)
	for i := 0; i < 105; i++ {
		name := fmt.Sprintf("note-%03d.txt", i)
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		items = append(items, FileMetadata{Name: name, Path: path, Extension: ".txt"})
	}
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"generatedAt": time.Now().UTC(), "items": items})
	if err := os.WriteFile(filepath.Join(dataDir, "metadata-index.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}, DataDir: dataDir, MaxBodyBytes: 1 << 20})
	res := request(t, server, http.MethodGet, "/api/index/search?keyword=note&limit=100&offset=100", "")
	if res.Code != http.StatusOK {
		t.Fatalf("expected paged search, got %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Items     []FileMetadata `json:"items"`
		Total     int            `json:"total"`
		Truncated bool           `json:"truncated"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 105 || len(body.Items) != 5 || body.Truncated {
		t.Fatalf("unexpected page: total=%d items=%d truncated=%v", body.Total, len(body.Items), body.Truncated)
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

func TestHarnessExposesMetadataIndexAsReadOnlyTool(t *testing.T) {
	harness := NewHarness()
	found := false
	for _, tool := range harness.Tools {
		if tool.Name == "search_index" {
			found = true
			if !tool.ReadOnly || !strings.Contains(tool.Description, "元数据") {
				t.Fatalf("index tool must be read-only metadata search: %+v", tool)
			}
		}
	}
	if !found {
		t.Fatal("harness did not register search_index")
	}
}

func TestIndexPromptUsesControlledIndexTool(t *testing.T) {
	plan := NewHarness().Plan(context.Background(), "查询本地索引里的合同")
	if len(plan.Steps) != 1 || plan.Steps[0].Tool != "search_index" {
		t.Fatalf("expected index query to use search_index, got %+v", plan)
	}
}

func TestRedactSecrets(t *testing.T) {
	got := redactSecrets("token=abc password=hunter2 status=ok")
	if strings.Contains(got, "abc") || strings.Contains(got, "hunter2") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("secrets were not redacted: %s", got)
	}
	jsonLog := redactSecrets(`{"token":"abc","nested":{"password":"hunter2"},"status":"ok"}`)
	if strings.Contains(jsonLog, "abc") || strings.Contains(jsonLog, "hunter2") || !strings.Contains(jsonLog, "[REDACTED]") {
		t.Fatalf("JSON secrets were not redacted: %s", jsonLog)
	}
}

func TestRejectPrivateHost(t *testing.T) {
	dialer := newPublicTransport()
	defer dialer.CloseIdleConnections()
	if _, err := dialer.DialContext(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("loopback host was accepted")
	}
	if _, err := dialer.DialContext(context.Background(), "tcp", "192.168.1.10:80"); err == nil {
		t.Fatal("private host was accepted")
	}
}

func TestGenerateHealthReportPersistsArtifact(t *testing.T) {
	root := t.TempDir()
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}, DataDir: filepath.Join(root, "data"), MaxBodyBytes: 1 << 20})
	server.store.tasks["private-task"] = &Task{ID: "private-task", Status: statusFailed, User: User{ID: "dev-user"}}
	server.store.downloads["private-download"] = &DownloadPlan{ID: "private-download", Status: statusFailed, User: User{ID: "dev-user"}}
	res := request(t, server, http.MethodPost, "/api/reports/health/generate", "")
	if res.Code != http.StatusCreated {
		t.Fatalf("expected report creation, got %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Artifact string       `json:"artifact"`
		Report   HealthReport `json:"report"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Artifact == "" {
		t.Fatal("expected persisted report artifact path")
	}
	if body.Report.FailedTasks != 1 || body.Report.FailedDownloads != 1 {
		t.Fatalf("interactive user report omitted private status: %+v", body.Report)
	}
	if _, err := os.Stat(body.Artifact); err != nil {
		t.Fatalf("report artifact not persisted: %v", err)
	}
	artifactBytes, err := os.ReadFile(body.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	var saved HealthReport
	if err := json.Unmarshal(artifactBytes, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.UserMetricsOmitted || saved.FailedTasks != 0 || saved.FailedDownloads != 0 {
		t.Fatalf("shared artifact exposed private task status: %+v", saved)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.executeTask(ctx, task.ID, task.Prompt, AgentPlan{Status: "success", Steps: []PlanStep{
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

func TestReadOnlyStorageEndpointsReportUserCancellation(t *testing.T) {
	server := NewServer(Config{DevMode: true, SharedRoots: []string{t.TempDir()}})
	server.storage = cancelledReadOnlyStorage{}
	for _, path := range []string{"/api/storage/usage", "/api/files/search"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		res := httptest.NewRecorder()
		server.ServeHTTP(res, req)
		if res.Code != http.StatusRequestTimeout || !strings.Contains(res.Body.String(), "USER_CANCELLED") {
			t.Fatalf("%s cancellation mismatch: %d %s", path, res.Code, res.Body.String())
		}
	}
}

type cancelledReadOnlyStorage struct{}

func (cancelledReadOnlyStorage) Usage(ctx context.Context) (StorageUsage, error) {
	return StorageUsage{}, ctx.Err()
}
func (cancelledReadOnlyStorage) Search(ctx context.Context, _ FileSearchOptions) ([]FileMetadata, error) {
	return nil, ctx.Err()
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
	waitForTaskStatus(t, server, created.ID, statusCompleted)
	waitForTaskWorker(t, server, created.ID)

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
	if len(restarted.store.events[created.ID]) != 8 || restarted.store.events[created.ID][7].Type != "session.completed" {
		t.Fatalf("events were not restored: %+v", restarted.store.events[created.ID])
	}
}

func TestDownloadRequestWithoutSourcesNeverCreatesFakeApproval(t *testing.T) {
	server := testServer()
	res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"下载一些公开照片"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("task creation failed: %d: %s", res.Code, res.Body.String())
	}
	var task Task
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, server, task.ID, statusFailed)
	waitForTaskWorker(t, server, task.ID)
	server.store.mu.RLock()
	defer server.store.mu.RUnlock()
	if !strings.Contains(server.store.tasks[task.ID].Summary, "下载计划") || len(server.store.downloads) != 0 {
		t.Fatalf("unverified download plan was presented: %+v", server.store.tasks[task.ID])
	}
	events := server.store.events[task.ID]
	if events[len(events)-1].Type != "session.failed" {
		t.Fatalf("failure was not recorded: %+v", events)
	}
	for _, event := range events {
		if event.Type == "approval.requested" || event.Type == "approval.granted" || event.Type == "tool.call" || event.Type == "tool.result" {
			t.Fatalf("unexecuted download plan generated tool or approval events: %+v", event)
		}
	}
}

func TestStoreMarksInterruptedRunsFailedAfterRestart(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	eventPath := filepath.Join(root, "events.jsonl")
	before := NewStoreWithState(statePath, eventPath)
	now := time.Now().UTC()
	before.tasks["running-task"] = &Task{ID: "running-task", Prompt: "检查 NAS 空间", Status: statusRunning, CreatedAt: now, UpdatedAt: now, User: User{ID: "user"}}
	before.downloads["running-download"] = &DownloadPlan{ID: "running-download", Status: statusRunning, User: User{ID: "user"}}
	before.persist()

	after := NewStoreWithState(statePath, eventPath)
	if after.tasks["running-task"].Status != statusFailed || !strings.Contains(after.tasks["running-task"].Summary, "重新发起") {
		t.Fatalf("interrupted task was not recoverable: %+v", after.tasks["running-task"])
	}
	if after.downloads["running-download"].Status != statusFailed || after.downloads["running-download"].ErrorCode == "" {
		t.Fatalf("interrupted download still appears active: %+v", after.downloads["running-download"])
	}
	if len(after.events["running-task"]) != 1 || after.events["running-task"][0].Type != "session.failed" {
		t.Fatalf("task interruption not audited: %+v", after.events["running-task"])
	}
	if len(after.events["running-download"]) != 1 || after.events["running-download"][0].Type != "session.failed" {
		t.Fatalf("download interruption not audited: %+v", after.events["running-download"])
	}
	reopened := NewStoreWithState(statePath, eventPath)
	if len(reopened.events["running-task"]) != 1 || reopened.tasks["running-task"].Status != statusFailed {
		t.Fatalf("restart reconciliation was not persisted: %+v", reopened.events["running-task"])
	}
}
