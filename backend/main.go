package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	statusRunning   = "运行中"
	statusPending   = "待确认"
	statusCompleted = "已完成"
	statusCancelled = "已取消"
	statusFailed    = "失败"
)

var (
	errUnauthorized        = errors.New("unauthorized")
	errNotFound            = errors.New("resource not found")
	errInvalidInput        = errors.New("invalid input")
	errForbidden           = errors.New("operation is outside the authorized scope")
	errProviderUnavailable = errors.New("provider unavailable")
)

type Config struct {
	Addr           string
	DevMode        bool
	SharedRoots    []string
	DataDir        string
	EventLogPath   string
	StatePath      string
	MaxBodyBytes   int64
	AllowedOrigin  string
	HealthInterval time.Duration
}

func loadConfig() Config {
	root := os.Getenv("UGAPP_SHARED_DIR")
	devMode := os.Getenv("UGOS_DEV_MODE") == "1"
	if root == "" && devMode {
		root = "/tmp/ugreen-workspace"
		if err := os.MkdirAll(root, 0700); err != nil {
			log.Printf("create development workspace: %v", err)
		}
	}
	var sharedRoots []string
	if root != "" {
		sharedRoots = expandAuthorizedRoots(root)
	}
	dataDir := os.Getenv("UGAPP_DATA_DIR")
	if dataDir == "" && devMode {
		dataDir = filepath.Join(root, ".nasmate-data")
	}
	eventLogPath := ""
	if dataDir != "" {
		eventLogPath = filepath.Join(dataDir, "events.jsonl")
	}
	statePath := ""
	if dataDir != "" {
		statePath = filepath.Join(dataDir, "state.json")
	}
	addr := ":21010"
	allowedOrigin := "http://127.0.0.1:5173"
	if devMode {
		addr = envOr("UGREEN_AI_ADDR", addr)
		allowedOrigin = envOr("UGREEN_AI_ALLOWED_ORIGIN", allowedOrigin)
	}
	return Config{
		Addr:           addr,
		DevMode:        devMode,
		SharedRoots:    sharedRoots,
		DataDir:        dataDir,
		EventLogPath:   eventLogPath,
		StatePath:      statePath,
		MaxBodyBytes:   1 << 20,
		AllowedOrigin:  allowedOrigin,
		HealthInterval: parseDurationEnv("UGAPP_HEALTH_INTERVAL"),
	}
}

func parseDurationEnv(key string) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < time.Minute {
		return 0
	}
	return duration
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type Event struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"createdAt"`
	Data      any       `json:"data,omitempty"`
}

type Task struct {
	ID        string    `json:"id"`
	Prompt    string    `json:"prompt"`
	Status    string    `json:"status"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	User      User      `json:"user"`
}

type FileMetadata struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
	Extension  string    `json:"extension"`
}

type VolumeUsage struct {
	Name       string `json:"name"`
	TotalBytes int64  `json:"totalBytes"`
	UsedBytes  int64  `json:"usedBytes"`
	FreeBytes  int64  `json:"freeBytes"`
}

type UsageItem struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	Kind      string `json:"kind"`
}

type StorageUsage struct {
	Volumes     []VolumeUsage `json:"volumes"`
	TopItems    []UsageItem   `json:"topItems"`
	Suggestions []string      `json:"suggestions"`
}

type ContainerInfo struct {
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Status      string            `json:"status"`
	CPUPercent  float64           `json:"cpuPercent"`
	MemoryBytes int64             `json:"memoryBytes"`
	Ports       []string          `json:"ports"`
	Mounts      []string          `json:"mounts"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type ContainerDiagnostic struct {
	Container ContainerInfo `json:"container"`
	Logs      []string      `json:"logs,omitempty"`
	Findings  []string      `json:"findings"`
}

type BackupStatus struct {
	Name            string    `json:"name"`
	LastRunAt       time.Time `json:"lastRunAt"`
	LastRunStatus   string    `json:"lastRunStatus"`
	Target          string    `json:"target"`
	Readable        bool      `json:"readable"`
	SampledFiles    int       `json:"sampledFiles"`
	VerifiedFiles   int       `json:"verifiedFiles"`
	RecoveryReady   bool      `json:"recoveryReady"`
	Recommendations []string  `json:"recommendations"`
}

type RecoveryPlan struct {
	Status           string   `json:"status"`
	Summary          string   `json:"summary"`
	BackupName       string   `json:"backupName"`
	Target           string   `json:"target"`
	SampledFiles     int      `json:"sampledFiles"`
	Steps            []string `json:"steps"`
	WillOverwrite    bool     `json:"willOverwrite"`
	RequiresApproval bool     `json:"requiresApproval"`
	ReadOnly         bool     `json:"readOnly"`
}

type DownloadSource struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	License   string `json:"license"`
	SizeBytes int64  `json:"sizeBytes"`
}

type DownloadPlan struct {
	ID              string           `json:"id"`
	TargetDirectory string           `json:"targetDirectory"`
	Sources         []DownloadSource `json:"sources"`
	EstimatedBytes  int64            `json:"estimatedBytes"`
	Status          string           `json:"status"`
	CreatedAt       time.Time        `json:"createdAt"`
	ApprovedAt      *time.Time       `json:"approvedAt,omitempty"`
	DownloadedBytes int64            `json:"downloadedBytes"`
	CurrentSource   string           `json:"currentSource,omitempty"`
	ErrorCode       string           `json:"errorCode,omitempty"`
	User            User             `json:"user"`
}

type taskCreateRequest struct {
	Prompt string `json:"prompt"`
}

type downloadPrepareRequest struct {
	TargetDirectory string           `json:"targetDirectory"`
	Sources         []DownloadSource `json:"sources"`
}

type sourceValidationRequest struct {
	Sources []DownloadSource `json:"sources"`
}

type FileSearchOptions struct {
	Keyword        string
	Extensions     []string
	ModifiedAfter  *time.Time
	ModifiedBefore *time.Time
	RootPath       string
	MinSizeBytes   *int64
	MaxSizeBytes   *int64
	MaxResults     int
}

type Store struct {
	mu        sync.RWMutex
	tasks     map[string]*Task
	events    map[string][]Event
	downloads map[string]*DownloadPlan
	sequence  int64
	sink      *EventSink
	statePath string
	persistMu sync.Mutex
}

type storeSnapshot struct {
	Tasks     map[string]*Task         `json:"tasks"`
	Events    map[string][]Event       `json:"events"`
	Downloads map[string]*DownloadPlan `json:"downloads"`
	Sequence  int64                    `json:"sequence"`
}

type EventSink struct {
	mu   sync.Mutex
	path string
}

func NewEventSink(path string) *EventSink {
	return &EventSink{path: path}
}

func (sink *EventSink) Append(sessionID string, event Event) {
	if sink == nil || sink.path == "" {
		return
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(sink.path), 0700); err != nil {
		log.Printf("create event log directory: %v", err)
		return
	}
	file, err := os.OpenFile(sink.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("open event log: %v", err)
		return
	}
	defer file.Close()
	entry := struct {
		SessionID string `json:"sessionId"`
		Event     Event  `json:"event"`
	}{SessionID: sessionID, Event: event}
	if err := json.NewEncoder(file).Encode(entry); err != nil {
		log.Printf("write event log: %v", err)
	}
}

func NewStore(eventLogPath string) *Store {
	return NewStoreWithState("", eventLogPath)
}

func NewStoreWithState(statePath, eventLogPath string) *Store {
	store := &Store{
		tasks:     make(map[string]*Task),
		events:    make(map[string][]Event),
		downloads: make(map[string]*DownloadPlan),
		sink:      NewEventSink(eventLogPath),
		statePath: statePath,
	}
	store.load()
	return store
}

func (s *Store) appendEvent(sessionID, eventType string, data any) Event {
	s.mu.Lock()
	s.sequence++
	event := Event{ID: s.sequence, Type: eventType, CreatedAt: time.Now().UTC(), Data: data}
	s.events[sessionID] = append(s.events[sessionID], event)
	s.mu.Unlock()
	s.sink.Append(sessionID, event)
	s.persist()
	return event
}

func (s *Store) load() {
	if s.statePath == "" {
		return
	}
	data, err := os.ReadFile(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		log.Printf("load state: %v", err)
		return
	}
	var snapshot storeSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		log.Printf("decode state: %v", err)
		return
	}
	if snapshot.Tasks != nil {
		s.tasks = snapshot.Tasks
	}
	if snapshot.Events != nil {
		s.events = snapshot.Events
	}
	if snapshot.Downloads != nil {
		s.downloads = snapshot.Downloads
	}
	s.sequence = snapshot.Sequence
}

func (s *Store) persist() {
	if s.statePath == "" {
		return
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.RLock()
	snapshot := storeSnapshot{
		Tasks:     s.tasks,
		Events:    s.events,
		Downloads: s.downloads,
		Sequence:  s.sequence,
	}
	data, err := json.Marshal(snapshot)
	s.mu.RUnlock()
	if err != nil {
		log.Printf("encode state: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0700); err != nil {
		log.Printf("create state directory: %v", err)
		return
	}
	temporary := s.statePath + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		log.Printf("write state: %v", err)
		return
	}
	if err := os.Rename(temporary, s.statePath); err != nil {
		log.Printf("replace state: %v", err)
	}
}

type StorageProvider interface {
	Usage(context.Context) (StorageUsage, error)
	Search(context.Context, FileSearchOptions) ([]FileMetadata, error)
}

type DockerProvider interface {
	List(context.Context) ([]ContainerDiagnostic, error)
	Inspect(context.Context, string, int) (ContainerDiagnostic, error)
}

type BackupProvider interface {
	Status(context.Context) (BackupStatus, error)
}

type MockStorage struct{}

func (MockStorage) Usage(context.Context) (StorageUsage, error) {
	return StorageUsage{
		Volumes: []VolumeUsage{{Name: "volume1", TotalBytes: 7516192768000, UsedBytes: 5261334937600, FreeBytes: 2254857830400}},
		TopItems: []UsageItem{
			{Path: "/volume1/家庭影像", SizeBytes: 1811265331200, Kind: "directory"},
			{Path: "/volume1/影视素材", SizeBytes: 1267689062400, Kind: "directory"},
			{Path: "/volume1/docker", SizeBytes: 734003200000, Kind: "directory"},
		},
		Suggestions: []string{"家庭影像是最近 30 天增长最快的目录。", "docker 目录存在 18 个旧镜像层，可人工复核。"},
	}, nil
}

func (MockStorage) Search(_ context.Context, options FileSearchOptions) ([]FileMetadata, error) {
	items := []FileMetadata{
		{Name: "装修方案-最终版.pdf", Path: "/volume1/文档/装修方案-最终版.pdf", SizeBytes: 8388608, ModifiedAt: time.Now().AddDate(-1, 0, 4).UTC(), Extension: ".pdf"},
		{Name: "装修预算.xlsx", Path: "/volume1/文档/装修预算.xlsx", SizeBytes: 2097152, ModifiedAt: time.Now().AddDate(-1, 0, 1).UTC(), Extension: ".xlsx"},
		{Name: "客厅布局.png", Path: "/volume1/家庭项目/客厅布局.png", SizeBytes: 3145728, ModifiedAt: time.Now().AddDate(-1, 0, 8).UTC(), Extension: ".png"},
	}
	filtered := make([]FileMetadata, 0, len(items))
	for _, item := range items {
		if options.Keyword != "" && !strings.Contains(strings.ToLower(item.Name), strings.ToLower(options.Keyword)) {
			continue
		}
		if len(options.Extensions) > 0 && !containsString(options.Extensions, strings.ToLower(item.Extension)) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}

type MockDocker struct{}

type UnavailableDocker struct{}

func (UnavailableDocker) List(context.Context) ([]ContainerDiagnostic, error) {
	return nil, errProviderUnavailable
}

func (UnavailableDocker) Inspect(context.Context, string, int) (ContainerDiagnostic, error) {
	return ContainerDiagnostic{}, errProviderUnavailable
}

func (MockDocker) List(context.Context) ([]ContainerDiagnostic, error) {
	return []ContainerDiagnostic{
		{Container: ContainerInfo{Name: "media-server", Image: "jellyfin/jellyfin:10.9", Status: "running", CPUPercent: 12.4, MemoryBytes: 1258291200, Ports: []string{"8096:8096/tcp"}, Mounts: []string{"/volume1/media:/media"}, Labels: map[string]string{"managed-by": "compose"}}, Findings: []string{"容器运行正常", "最近 24 小时未发现重启。"}},
		{Container: ContainerInfo{Name: "paperless", Image: "paperlessngx/paperless-ngx:2.8", Status: "unhealthy", CPUPercent: 1.8, MemoryBytes: 734003200, Ports: []string{"8000:8000/tcp"}, Mounts: []string{"/volume1/documents:/usr/src/paperless/media"}}, Logs: []string{"health check returned 503", "database connection retry scheduled"}, Findings: []string{"健康检查返回 503", "建议先检查数据库容器和网络连接。"}},
	}, nil
}

func (m MockDocker) Inspect(ctx context.Context, name string, lines int) (ContainerDiagnostic, error) {
	items, err := m.List(ctx)
	if err != nil {
		return ContainerDiagnostic{}, err
	}
	for _, item := range items {
		if item.Container.Name == name {
			if lines > 0 && len(item.Logs) > lines {
				item.Logs = item.Logs[len(item.Logs)-lines:]
			}
			return item, nil
		}
	}
	return ContainerDiagnostic{}, fmt.Errorf("container %q: %w", name, errNotFound)
}

type MockBackup struct{}

type UnavailableBackup struct{}

func (UnavailableBackup) Status(context.Context) (BackupStatus, error) {
	return BackupStatus{}, errProviderUnavailable
}

func (MockBackup) Status(context.Context) (BackupStatus, error) {
	return BackupStatus{
		Name: "家庭数据夜间备份", LastRunAt: time.Now().Add(-7 * time.Hour).UTC(), LastRunStatus: "success", Target: "/volume2/backup", Readable: true, SampledFiles: 24, VerifiedFiles: 24, RecoveryReady: true,
		Recommendations: []string{"最近恢复点可读取。建议每月执行一次完整恢复演练。"},
	}, nil
}

type Server struct {
	config          Config
	store           *Store
	storage         StorageProvider
	docker          DockerProvider
	backup          BackupProvider
	harness         *Harness
	taskMu          sync.Mutex
	tasksCtx        map[string]context.CancelFunc
	downloadMu      sync.Mutex
	downloadsCtx    map[string]context.CancelFunc
	rateMu          sync.Mutex
	rateBuckets     map[string]rateBucket
	sourceTransport http.RoundTripper
}

type rateBucket struct {
	started time.Time
	count   int
}

func NewServer(config Config) *Server {
	docker := DockerProvider(UnavailableDocker{})
	backup := BackupProvider(UnavailableBackup{})
	if config.DevMode {
		docker = MockDocker{}
		backup = MockBackup{}
	}
	return &Server{config: config, store: NewStoreWithState(config.StatePath, config.EventLogPath), storage: NewFilesystemStorage(config.SharedRoots), docker: docker, backup: backup, harness: NewHarness(), tasksCtx: make(map[string]context.CancelFunc), downloadsCtx: make(map[string]context.CancelFunc), rateBuckets: make(map[string]rateBucket), sourceTransport: newPublicTransport()}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.setHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Path == "/api/health" {
		s.handleHealth(w, r)
		return
	}
	if _, err := s.userFromRequest(r); err != nil {
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "需要 UGOS 用户认证")
		return
	}
	user, _ := s.userFromRequest(r)
	limit := 60
	if (r.URL.Path == "/api/files/search") || (r.URL.Path == "/api/tasks" && r.Method == http.MethodPost) {
		limit = 20
	}
	if r.URL.Path == "/api/downloads/prepare" || r.URL.Path == "/api/network/sources/probe" {
		limit = 10
	}
	if !s.allowRequest(user.ID, r.URL.Path, limit) {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "请求过于频繁，请稍后重试")
		return
	}

	path := strings.Trim(r.URL.Path, "/")
	switch {
	case path == "api/tasks":
		s.handleTasks(w, r)
	case path == "api/agent/plan":
		s.harness.HandlePlan(w, r)
	case path == "api/models/status":
		s.handleModelStatus(w, r)
	case path == "api/index/status":
		s.handleIndexStatus(w, r)
	case path == "api/index/rebuild":
		s.handleIndexRebuild(w, r)
	case path == "api/index/search":
		s.handleIndexSearch(w, r)
	case path == "api/organize/dry-run":
		s.handleOrganizeDryRun(w, r)
	case path == "api/reports/health":
		s.handleHealthReport(w, r)
	case path == "api/reports/health/generate":
		s.handleGenerateHealthReport(w, r)
	case path == "api/artifacts":
		s.handleArtifacts(w, r)
	case strings.HasPrefix(path, "api/artifacts/"):
		s.handleArtifact(w, r, strings.TrimPrefix(path, "api/artifacts/"))
	case strings.HasPrefix(path, "api/tasks/"):
		s.handleTask(w, r, strings.TrimPrefix(path, "api/tasks/"))
	case path == "api/storage/usage":
		s.handleStorageUsage(w, r)
	case path == "api/files/search":
		s.handleFileSearch(w, r)
	case path == "api/docker/containers":
		s.handleDocker(w, r)
	case path == "api/backups/status":
		s.handleBackup(w, r)
	case path == "api/backups/recovery-plan":
		s.handleRecoveryPlan(w, r)
	case path == "api/downloads/prepare":
		s.handlePrepareDownload(w, r)
	case path == "api/network/sources/validate":
		s.handleValidateSources(w, r)
	case path == "api/network/sources/probe":
		s.handleProbeSources(w, r)
	case path == "api/downloads":
		s.handleDownloads(w, r)
	case strings.HasPrefix(path, "api/downloads/"):
		s.handleDownload(w, r, strings.TrimPrefix(path, "api/downloads/"))
	default:
		writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "接口不存在")
	}
}

func (s *Server) handleModelStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	writeJSON(w, http.StatusOK, s.harness.ModelStatus())
}

func (s *Server) handleIndexStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":                    "available",
		"mode":                      "metadata-only",
		"roots":                     s.config.SharedRoots,
		"bodyIndexEnabled":          false,
		"ocrEnabled":                false,
		"mediaTranscriptionEnabled": false,
		"requiresExplicitConsent":   true,
	})
}

func (s *Server) handleIndexRebuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	if s.config.DataDir == "" {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "应用数据目录不可用")
		return
	}
	items, err := s.storage.Search(r.Context(), FileSearchOptions{MaxResults: 100000})
	if err != nil && !errors.Is(err, errSearchLimit) {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "暂时无法读取文件元数据")
		return
	}
	path := filepath.Join(s.config.DataDir, "metadata-index.json")
	if err := os.MkdirAll(s.config.DataDir, 0700); err != nil {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "无法保存索引")
		return
	}
	data, err := json.Marshal(map[string]any{"mode": "metadata-only", "generatedAt": time.Now().UTC(), "items": items})
	if err != nil || os.WriteFile(path, data, 0600) != nil {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "无法保存索引")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "success", "summary": "元数据索引已重建", "artifact": path, "itemCount": len(items), "readOnly": true})
}

func (s *Server) handleIndexSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	if s.config.DataDir == "" {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "应用数据目录不可用")
		return
	}
	items, err := s.searchMetadataIndex(r.URL.Query().Get("keyword"))
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "索引尚未生成")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "索引不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "readMode": "metadata-only", "source": "local-index"})
}

func (s *Server) searchMetadataIndex(keyword string) ([]FileMetadata, error) {
	if s.config.DataDir == "" {
		return nil, errProviderUnavailable
	}
	data, err := os.ReadFile(filepath.Join(s.config.DataDir, "metadata-index.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read metadata index: %w", err)
	}
	var index struct {
		Items []FileMetadata `json:"items"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("decode metadata index: %w", err)
	}
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	items := make([]FileMetadata, 0)
	for _, item := range index.Items {
		if !s.authorizePath(item.Path) {
			continue
		}
		if keyword == "" || strings.Contains(strings.ToLower(item.Name), keyword) {
			items = append(items, item)
			if len(items) == 100 {
				break
			}
		}
	}
	return items, nil
}

func (s *Server) handleValidateSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	var req sourceValidationRequest
	if err := decodeJSON(w, r, &req, s.config.MaxBodyBytes); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}
	if len(req.Sources) == 0 || len(req.Sources) > 50 {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "来源数量必须在 1 到 50 之间")
		return
	}
	var total int64
	for i := range req.Sources {
		source := &req.Sources[i]
		parsed, err := url.ParseRequestURI(source.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || hasSensitiveURLQuery(parsed) {
			writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "下载来源必须是有效且不含凭据的 HTTP(S) URL")
			return
		}
		if strings.TrimSpace(source.Title) == "" {
			source.Title = filepath.Base(parsed.Path)
		}
		if source.SizeBytes < 0 || source.SizeBytes > 10<<30 {
			writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "单个下载文件大小超出限制")
			return
		}
		total += source.SizeBytes
	}
	if total > 100<<30 {
		writeError(w, http.StatusBadRequest, "STORAGE_INSUFFICIENT", "来源总大小超过 100GB 限制")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "sources": req.Sources, "estimatedBytes": total, "requiresApproval": true, "nextActions": []string{"选择授权目录后创建下载计划"}})
}

func (s *Server) allowRequest(userID, path string, limit int) bool {
	now := time.Now()
	key := userID + "|" + path
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	bucket := s.rateBuckets[key]
	if bucket.started.IsZero() || now.Sub(bucket.started) >= time.Minute {
		s.rateBuckets[key] = rateBucket{started: now, count: 1}
		return true
	}
	if bucket.count >= limit {
		return false
	}
	bucket.count++
	s.rateBuckets[key] = bucket
	return true
}

func (s *Server) setHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if origin := r.Header.Get("Origin"); origin != "" && origin == s.config.AllowedOrigin {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Ugreen-Ttk")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	}
}

func (s *Server) userFromRequest(r *http.Request) (User, error) {
	id := strings.TrimSpace(r.Header.Get("Ugreen-User-ID"))
	name := strings.TrimSpace(r.Header.Get("Ugreen-User-Name"))
	typeName := strings.TrimSpace(r.Header.Get("Ugreen-User-Type"))
	if id == "" && s.config.DevMode {
		return User{ID: "dev-user", Name: "本地开发用户", Type: "admin"}, nil
	}
	if id == "" {
		return User{}, errUnauthorized
	}
	if name == "" {
		name = id
	}
	if typeName == "" {
		typeName = "users"
	}
	return User{ID: id, Name: name, Type: typeName}, nil
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "nasmate-backend", "time": time.Now().UTC()})
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	user, _ := s.userFromRequest(r)
	switch r.Method {
	case http.MethodGet:
		s.store.mu.RLock()
		items := make([]Task, 0, len(s.store.tasks))
		for _, task := range s.store.tasks {
			if task.User.ID == user.ID {
				items = append(items, *task)
			}
		}
		s.store.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var req taskCreateRequest
		if err := decodeJSON(w, r, &req, s.config.MaxBodyBytes); err != nil {
			writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
			return
		}
		if length := len([]rune(strings.TrimSpace(req.Prompt))); length < 2 || length > 2000 {
			writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "prompt 长度必须在 2 到 2000 个字符之间")
			return
		}
		now := time.Now().UTC()
		plan := s.harness.Plan(r.Context(), strings.TrimSpace(req.Prompt))
		task := &Task{ID: newID("task"), Prompt: strings.TrimSpace(req.Prompt), Status: statusRunning, Summary: plan.Summary, CreatedAt: now, UpdatedAt: now, User: user}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		s.taskMu.Lock()
		s.tasksCtx[task.ID] = cancel
		s.taskMu.Unlock()
		s.store.mu.Lock()
		s.store.tasks[task.ID] = task
		s.store.mu.Unlock()
		s.store.persist()
		s.store.appendEvent(task.ID, "session.created", map[string]string{"userId": user.ID})
		s.store.appendEvent(task.ID, "user.message", map[string]string{"prompt": task.Prompt})
		s.store.appendEvent(task.ID, "agent.plan", map[string]any{"mode": "policy-constrained", "scope": s.config.SharedRoots, "plan": plan})
		s.store.appendEvent(task.ID, "task.progress", map[string]string{"message": "已完成权限检查"})
		// Execute read-only plans before responding so persisted state is durable
		// when a caller immediately restarts the application.
		s.executeTask(ctx, cancel, task.ID, task.Prompt, plan)
		writeJSON(w, http.StatusCreated, task)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
	}
}

func (s *Server) executeTask(ctx context.Context, cancel context.CancelFunc, id, prompt string, plan AgentPlan) {
	defer func() {
		s.taskMu.Lock()
		delete(s.tasksCtx, id)
		s.taskMu.Unlock()
		cancel()
	}()
	if len(plan.Steps) == 0 {
		s.finishTask(id, statusFailed, "没有可执行的工具步骤")
		return
	}
	readOnlySteps := 0
	for _, step := range plan.Steps {
		if step.Tool != "prepare_download" {
			readOnlySteps++
		}
	}
	if readOnlySteps == 0 {
		s.finishTask(id, statusPending, "已生成下载计划，等待用户确认")
		return
	}
	for index, step := range plan.Steps {
		if ctx.Err() != nil {
			return
		}
		// Keep the execution budget bounded even if a provider returns an
		// unexpectedly large plan. Only registered tools can reach this switch.
		if index >= 3 {
			s.store.appendEvent(id, "task.progress", map[string]any{"status": "warning", "summary": "已达到单任务工具调用上限", "next_actions": []string{"拆分任务后重试"}})
			break
		}
		if step.Tool == "prepare_download" {
			// Planning a write is intentionally separated from execution. The
			// download approval endpoint is the only path that can start it.
			continue
		}
		s.store.appendEvent(id, "tool.call", map[string]any{"name": step.Tool, "input": map[string]string{"prompt": prompt}})
		result, err := s.executeReadOnlyTool(ctx, step.Tool, prompt)
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		if err != nil {
			s.store.appendEvent(id, "tool.result", map[string]any{"status": "error", "summary": "工具不可用", "next_actions": []string{"检查授权目录或系统能力后重试"}})
			s.finishTask(id, statusFailed, "工具执行失败：请检查授权范围或系统能力")
			return
		}
		s.store.appendEvent(id, "tool.result", map[string]any{"status": "success", "summary": "工具执行完成", "next_actions": []string{"查看结果"}, "artifacts": []string{}, "result": result})
	}
	s.finishTask(id, statusCompleted, "只读工具执行完成，可查看执行轨迹")
}

func (s *Server) executeReadOnlyTool(ctx context.Context, name, prompt string) (any, error) {
	switch name {
	case "storage_usage":
		return s.storage.Usage(ctx)
	case "inspect_containers":
		return s.docker.List(ctx)
	case "backup_status":
		return s.backup.Status(ctx)
	case "search_files":
		return s.storage.Search(ctx, FileSearchOptions{Keyword: extractSearchKeyword(prompt), MaxResults: 100})
	case "search_index":
		return s.searchMetadataIndex(extractSearchKeyword(prompt))
	default:
		return nil, fmt.Errorf("unknown tool %q: %w", name, errInvalidInput)
	}
}

func extractSearchKeyword(prompt string) string {
	value := strings.TrimSpace(prompt)
	for _, prefix := range []string{"搜索", "查找", "寻找", "找出", "找"} {
		value = strings.TrimSpace(strings.TrimPrefix(value, prefix))
	}
	for _, suffix := range []string{"文件", "资料", "文档"} {
		value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
	}
	return value
}

func (s *Server) finishTask(id, status, summary string) {
	s.store.mu.Lock()
	task, ok := s.store.tasks[id]
	if ok && task.Status == statusRunning {
		task.Status = status
		task.Summary = summary
		task.UpdatedAt = time.Now().UTC()
	} else {
		ok = false
	}
	s.store.mu.Unlock()
	if ok {
		s.store.persist()
		s.store.appendEvent(id, "task.progress", map[string]string{"status": status, "summary": summary})
	}
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request, suffix string) {
	parts := strings.Split(strings.Trim(suffix, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "任务不存在")
		return
	}
	id := parts[0]
	user, _ := s.userFromRequest(r)
	s.store.mu.RLock()
	task, ok := s.store.tasks[id]
	if ok && task.User.ID != user.ID {
		ok = false
	}
	var taskCopy Task
	if ok {
		taskCopy = *task
	}
	events := append([]Event(nil), s.store.events[id]...)
	s.store.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "任务不存在")
		return
	}
	if len(parts) == 2 && parts[1] == "events" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": events})
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
			return
		}
		s.store.mu.Lock()
		if task.Status == statusCompleted || task.Status == statusFailed || task.Status == statusCancelled {
			s.store.mu.Unlock()
			writeError(w, http.StatusConflict, "VALIDATION_FAILED", "任务已经结束，不能重复取消")
			return
		}
		task.Status = statusCancelled
		task.UpdatedAt = time.Now().UTC()
		taskCopy = *task
		s.store.mu.Unlock()
		s.taskMu.Lock()
		cancel := s.tasksCtx[id]
		s.taskMu.Unlock()
		if cancel != nil {
			cancel()
		}
		s.store.persist()
		s.store.appendEvent(id, "task.cancelled", map[string]string{"userId": user.ID})
		writeJSON(w, http.StatusOK, &taskCopy)
		return
	}
	if r.Method != http.MethodGet || len(parts) != 1 {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	writeJSON(w, http.StatusOK, &taskCopy)
}

func (s *Server) handleStorageUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	result, err := s.storage.Usage(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "暂时无法读取存储状态")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleFileSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	options, err := parseFileSearchOptions(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}
	if options.RootPath != "" {
		resolved, resolveErr := s.resolveAuthorizedPath(options.RootPath)
		if resolveErr != nil {
			writeError(w, http.StatusForbidden, "PATH_NOT_ALLOWED", "搜索路径不在授权范围内")
			return
		}
		options.RootPath = resolved
	}
	items, err := s.storage.Search(r.Context(), options)
	if err != nil && !errors.Is(err, errSearchLimit) {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "暂时无法读取文件元数据")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "truncated": errors.Is(err, errSearchLimit), "readMode": "metadata-only"})
}

func (s *Server) handleDocker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	name := r.URL.Query().Get("container")
	lines, _ := strconv.Atoi(r.URL.Query().Get("logLines"))
	if name != "" {
		item, err := s.docker.Inspect(r.Context(), name, min(lines, 100))
		if errors.Is(err, errNotFound) {
			writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "容器不存在")
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "暂时无法读取容器状态")
			return
		}
		item = redactDiagnostic(item)
		writeJSON(w, http.StatusOK, item)
		return
	}
	items, err := s.docker.List(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "暂时无法读取容器状态")
		return
	}
	for i := range items {
		items[i] = redactDiagnostic(items[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "readOnly": true})
}

func redactDiagnostic(item ContainerDiagnostic) ContainerDiagnostic {
	for i, line := range item.Logs {
		item.Logs[i] = redactSecrets(line)
	}
	return item
}

func redactSecrets(value string) string {
	keys := []string{"password", "passwd", "token", "api_key", "apikey", "authorization", "cookie", "secret"}
	fields := strings.Fields(value)
	for i, field := range fields {
		for _, key := range keys {
			if index := strings.Index(strings.ToLower(field), key+"="); index >= 0 {
				equal := strings.Index(field[index:], "=") + index
				fields[i] = field[:equal+1] + "[REDACTED]"
				break
			}
		}
	}
	return strings.Join(fields, " ")
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	result, err := s.backup.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "暂时无法读取备份状态")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRecoveryPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	status, err := s.backup.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "暂时无法读取备份状态")
		return
	}
	plan := RecoveryPlan{Status: "success", Summary: "已生成只读恢复演练计划；未执行恢复", BackupName: status.Name, Target: status.Target, SampledFiles: status.SampledFiles, WillOverwrite: false, RequiresApproval: true, ReadOnly: true, Steps: []string{
		"确认最近成功备份时间与恢复点范围",
		"在用户授权的新目录中准备恢复目标，禁止覆盖原文件",
		"抽样恢复文件并比较文件数量、大小与可读取性",
		"记录验证结果并由用户决定是否执行完整恢复",
	}}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handlePrepareDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	user, _ := s.userFromRequest(r)
	var req downloadPrepareRequest
	if err := decodeJSON(w, r, &req, s.config.MaxBodyBytes); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}
	targetDirectory, err := s.resolveAuthorizedPath(req.TargetDirectory)
	if err != nil {
		writeError(w, http.StatusForbidden, "PATH_NOT_ALLOWED", "下载目录不在授权范围内")
		return
	}
	if len(req.Sources) == 0 || len(req.Sources) > 50 {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "下载来源数量必须在 1 到 50 之间")
		return
	}
	var total int64
	for i := range req.Sources {
		source := &req.Sources[i]
		if err := validateDownloadSource(*source); err != nil {
			writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "下载来源需提供公开 HTTP(S) URL、受支持的文件类型、许可证或使用说明及 1B 到 10GB 的大小上限")
			return
		}
		total += source.SizeBytes
		if total > 100<<30 {
			writeError(w, http.StatusBadRequest, "STORAGE_INSUFFICIENT", "本次下载计划超过 100GB 限制")
			return
		}
	}
	if _, ok := s.storage.(*FilesystemStorage); ok && total > 0 {
		freeBytes, err := filesystemFreeSpace(targetDirectory)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "NAS_OFFLINE", "暂时无法读取目标存储空间")
			return
		}
		if total > freeBytes {
			writeError(w, http.StatusInsufficientStorage, "STORAGE_INSUFFICIENT", "目标存储空间不足")
			return
		}
	}
	plan := &DownloadPlan{ID: newID("download"), TargetDirectory: targetDirectory, Sources: req.Sources, EstimatedBytes: total, Status: statusPending, CreatedAt: time.Now().UTC(), User: user}
	s.store.mu.Lock()
	s.store.downloads[plan.ID] = plan
	s.store.mu.Unlock()
	s.store.persist()
	s.store.appendEvent(plan.ID, "approval.requested", map[string]any{"targetDirectory": plan.TargetDirectory, "estimatedBytes": plan.EstimatedBytes, "sourceCount": len(plan.Sources)})
	writeJSON(w, http.StatusCreated, plan)
}

func hasSensitiveURLQuery(parsed *url.URL) bool {
	if parsed == nil {
		return true
	}
	for key := range parsed.Query() {
		switch strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", "")) {
		case "accesstoken", "apikey", "auth", "authorization", "cookie", "key", "password", "passwd", "secret", "signature", "token":
			return true
		}
	}
	return false
}

func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
		return
	}
	user, _ := s.userFromRequest(r)
	s.store.mu.RLock()
	items := make([]DownloadPlan, 0, len(s.store.downloads))
	for _, plan := range s.store.downloads {
		if plan.User.ID == user.ID {
			items = append(items, *plan)
		}
	}
	s.store.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" {
		writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "下载计划不存在")
		return
	}
	user, _ := s.userFromRequest(r)
	s.store.mu.RLock()
	plan, ok := s.store.downloads[id]
	var planCopy DownloadPlan
	if ok && plan.User.ID == user.ID {
		planCopy = *plan
	} else {
		ok = false
	}
	s.store.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "RESOURCE_NOT_FOUND", "下载计划不存在")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, &planCopy)
		return
	}
	if r.Method != http.MethodPost || (r.URL.Query().Get("action") != "approve" && r.URL.Query().Get("action") != "deny" && r.URL.Query().Get("action") != "cancel") {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "请使用 action=approve、deny 或 cancel")
		return
	}
	action := r.URL.Query().Get("action")
	s.downloadMu.Lock()
	defer s.downloadMu.Unlock()
	if action == "cancel" {
		if planCopy.Status != statusRunning && planCopy.Status != statusPending {
			writeError(w, http.StatusConflict, "VALIDATION_FAILED", "当前下载任务不能取消")
			return
		}
		if cancel, exists := s.downloadsCtx[id]; exists {
			cancel()
		}
		s.store.mu.Lock()
		if current, exists := s.store.downloads[id]; exists && (current.Status == statusPending || current.Status == statusRunning) {
			current.Status = statusCancelled
			current.CurrentSource = ""
			planCopy = *current
		}
		s.store.mu.Unlock()
		s.store.persist()
		s.store.appendEvent(id, "task.cancelled", map[string]string{"userId": user.ID, "kind": "download"})
		writeJSON(w, http.StatusOK, &planCopy)
		return
	}
	s.store.mu.Lock()
	if plan.Status != statusPending {
		s.store.mu.Unlock()
		writeError(w, http.StatusConflict, "VALIDATION_FAILED", "下载计划已经处理，不能重复审批")
		return
	}
	if action == "approve" {
		if len(s.downloadsCtx) >= 3 {
			s.store.mu.Unlock()
			writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "最多同时运行 3 个下载任务，请稍后确认")
			return
		}
		now := time.Now().UTC()
		plan.Status = statusRunning
		plan.ApprovedAt = &now
	} else {
		plan.Status = statusCancelled
	}
	planCopy = *plan
	s.store.mu.Unlock()
	s.store.persist()
	eventType := "approval.granted"
	if action == "deny" {
		eventType = "approval.denied"
	}
	s.store.appendEvent(id, eventType, map[string]string{"userId": user.ID})
	if action == "approve" {
		ctx, cancel := context.WithCancel(context.Background())
		s.downloadsCtx[id] = cancel
		go s.executeDownload(ctx, id)
	}
	writeJSON(w, http.StatusOK, &planCopy)
}

func (s *Server) authorizePath(candidate string) bool {
	_, err := s.resolveAuthorizedPath(candidate)
	return err == nil
}

func (s *Server) resolveAuthorizedPath(candidate string) (string, error) {
	return resolveAuthorizedPath(candidate, s.config.SharedRoots)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func newID(prefix string) string {
	var bytes [6]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(bytes[:])
}

func min(left, right int) int {
	if left < 1 {
		return right
	}
	if left < right {
		return left
	}
	return right
}

func main() {
	config := loadConfig()
	app := NewServer(config)
	stopReports := app.StartHealthScheduler()
	defer stopReports()
	server := &http.Server{Addr: config.Addr, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		log.Printf("NasMate backend listening on %s", config.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
