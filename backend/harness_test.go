package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type waitingModel struct {
	started   chan struct{}
	release   chan struct{}
	cancelled chan error
	seen      chan string
}

func (model waitingModel) Plan(ctx context.Context, prompt string, _ []ToolSpec) (AgentPlan, error) {
	if model.seen != nil {
		model.seen <- prompt
	}
	close(model.started)
	select {
	case <-model.release:
		return AgentPlan{Status: "success", Steps: []PlanStep{{Tool: "storage_usage", Reason: "只读检查容量"}}}, nil
	case <-ctx.Done():
		model.cancelled <- ctx.Err()
		return AgentPlan{}, ctx.Err()
	}
}

func TestModelPlanningOnlyReceivesLocalToolIntent(t *testing.T) {
	server := testServer()
	model := waitingModel{started: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan error, 1), seen: make(chan string, 1)}
	server.harness.model = model
	defer close(model.release)
	res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 /私密/家庭照片/hidden.png 的 NAS 空间"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("task creation failed: %d: %s", res.Code, res.Body.String())
	}
	var task Task
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-model.seen:
		if strings.Contains(input, "私密") || strings.Contains(input, "hidden.png") || !strings.Contains(input, "storage_usage") {
			t.Fatalf("model saw raw user data rather than a tool intent: %q", input)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("model request did not start")
	}
	request(t, server, http.MethodPost, "/api/tasks/"+task.ID+"/cancel", "")
	waitForTaskWorker(t, server, task.ID)
	server.store.mu.RLock()
	defer server.store.mu.RUnlock()
	found := false
	for _, event := range server.store.events[task.ID] {
		if event.Type == "model.input" {
			payload, _ := json.Marshal(event.Data)
			if strings.Contains(string(payload), "私密") || strings.Contains(string(payload), "hidden.png") {
				t.Fatalf("model input event leaked the raw request: %s", payload)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("model-visible input was not recorded")
	}
}

func TestSlowModelPlanningReturnsTaskBeforeModelFinishes(t *testing.T) {
	server := testServer()
	model := waitingModel{started: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan error, 1)}
	server.harness.model = model
	defer close(model.release)

	start := time.Now()
	res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 NAS 空间"}`)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("task creation blocked on model planning for %v", elapsed)
	}
	if res.Code != http.StatusCreated {
		t.Fatalf("task creation failed: %d: %s", res.Code, res.Body.String())
	}
	var task Task
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.Status != statusPlanning {
		t.Fatalf("expected planning status, got %+v", task)
	}
	select {
	case <-model.started:
	case <-time.After(3 * time.Second):
		t.Fatal("model planning did not start")
	}
	cancelled := request(t, server, http.MethodPost, "/api/tasks/"+task.ID+"/cancel", "")
	if cancelled.Code != http.StatusOK {
		t.Fatalf("planning cancellation failed: %d: %s", cancelled.Code, cancelled.Body.String())
	}
	select {
	case err := <-model.cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("model context was not cancelled: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("model request did not observe cancellation")
	}
	waitForTaskWorker(t, server, task.ID)
	server.store.mu.RLock()
	defer server.store.mu.RUnlock()
	if server.store.tasks[task.ID].Status != statusCancelled {
		t.Fatalf("cancelled plan changed task status: %+v", server.store.tasks[task.ID])
	}
	for _, event := range server.store.events[task.ID] {
		if event.Type == "tool.call" || event.Type == "agent.plan" {
			t.Fatalf("cancelled planning continued into execution: %+v", event)
		}
	}
}

func TestInterruptedPlanningIsNotReplayedAfterRestart(t *testing.T) {
	root := t.TempDir()
	before := NewStoreWithState(root+"/state.json", root+"/events.jsonl")
	now := time.Now().UTC()
	before.tasks["planning-task"] = &Task{ID: "planning-task", Prompt: "检查 NAS 空间", Status: statusPlanning, CreatedAt: now, UpdatedAt: now, User: User{ID: "user"}}
	before.persist()

	after := NewStoreWithState(root+"/state.json", root+"/events.jsonl")
	if after.tasks["planning-task"].Status != statusFailed || !strings.Contains(after.tasks["planning-task"].Summary, "重新发起") {
		t.Fatalf("interrupted planning resumed unexpectedly: %+v", after.tasks["planning-task"])
	}
	if len(after.events["planning-task"]) != 1 || after.events["planning-task"][0].Type != "session.failed" {
		t.Fatalf("interrupted planning was not audited: %+v", after.events["planning-task"])
	}
}

func TestCredentialBearingTaskPromptIsRejectedBeforeLogging(t *testing.T) {
	for _, prompt := range []string{"检查 token=credential-123 的 Docker 容器", "检查 Authorization: Bearer credential-123", `查找 {"api_key":"credential-123"} 相关文件`} {
		t.Run(prompt, func(t *testing.T) {
			server := testServer()
			body, _ := json.Marshal(taskCreateRequest{Prompt: prompt})
			res := request(t, server, http.MethodPost, "/api/tasks", string(body))
			if res.Code != http.StatusBadRequest || strings.Contains(res.Body.String(), "credential-123") {
				t.Fatalf("credential was accepted or echoed: %d: %s", res.Code, res.Body.String())
			}
			if len(server.store.tasks) != 0 || len(server.store.events) != 0 {
				t.Fatal("credential-bearing prompt was persisted")
			}
		})
	}
}

func TestConcurrentTaskLimitIsReleasedAfterCancellation(t *testing.T) {
	server := testServer()
	models := make(chan context.Context, 4)
	server.harness.model = blockingModel{contexts: models}
	var ids []string
	for i := 0; i < 3; i++ {
		res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 NAS 空间"}`)
		if res.Code != http.StatusCreated {
			t.Fatalf("task %d was rejected: %d: %s", i, res.Code, res.Body.String())
		}
		var task Task
		if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	defer func() {
		for _, id := range ids {
			request(t, server, http.MethodPost, "/api/tasks/"+id+"/cancel", "")
		}
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-models:
		case <-time.After(3 * time.Second):
			t.Fatal("model did not enter planning")
		}
	}
	res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 NAS 空间"}`)
	if res.Code != http.StatusTooManyRequests || !strings.Contains(res.Body.String(), "RATE_LIMITED") {
		t.Fatalf("unbounded concurrent task was accepted: %d: %s", res.Code, res.Body.String())
	}
	if len(server.store.tasks) != 3 {
		t.Fatalf("rejected task was persisted: %d", len(server.store.tasks))
	}
	request(t, server, http.MethodPost, "/api/tasks/"+ids[0]+"/cancel", "")
	waitForTaskWorker(t, server, ids[0])
	res = request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 NAS 空间"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("task slot was not released after cancellation: %d: %s", res.Code, res.Body.String())
	}
	var next Task
	if err := json.NewDecoder(res.Body).Decode(&next); err != nil {
		t.Fatal(err)
	}
	ids = append(ids, next.ID)
}

type blockingModel struct{ contexts chan context.Context }

func (model blockingModel) Plan(ctx context.Context, _ string, _ []ToolSpec) (AgentPlan, error) {
	model.contexts <- ctx
	<-ctx.Done()
	return AgentPlan{}, ctx.Err()
}

func TestOpenAICompatiblePlanningRequestRecordsNoOriginalUserData(t *testing.T) {
	received := make(chan map[string]any, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"status\":\"success\",\"summary\":\"已规划\",\"steps\":[{\"tool\":\"storage_usage\",\"reason\":\"只读容量检查\"}]}"}}]}`))
	}))
	defer upstream.Close()
	server := testServer()
	server.harness.model = OpenAICompatibleProvider{BaseURL: upstream.URL, APIKey: "local-test-key", Model: "test-model"}
	res := request(t, server, http.MethodPost, "/api/tasks", `{"prompt":"检查 /私密/家庭照片/hidden.png 的 NAS 空间"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("task creation failed: %d", res.Code)
	}
	select {
	case body := <-received:
		encoded, _ := json.Marshal(body)
		if strings.Contains(string(encoded), "私密") || strings.Contains(string(encoded), "hidden.png") || !strings.Contains(string(encoded), "storage_usage") {
			t.Fatalf("outbound model request included private data or omitted task intent: %s", encoded)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("model request did not arrive")
	}
}
