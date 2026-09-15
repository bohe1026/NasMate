package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errDownloadBudget = errors.New("download exceeds approved byte limit")

func validateDownloadSource(source DownloadSource) error {
	parsed, err := parseSourceURL(source.URL)
	if err != nil || strings.TrimSpace(source.License) == "" || len(source.License) > 2000 || len(source.Title) > 300 || source.SizeBytes <= 0 || source.SizeBytes > 10<<30 {
		return errInvalidInput
	}
	switch strings.ToLower(filepath.Ext(parsed.Path)) {
	case ".txt", ".md", ".csv", ".json", ".pdf", ".jpg", ".jpeg", ".png", ".gif", ".webp", ".mp3", ".mp4", ".wav", ".m4a", ".flac", ".zip":
		return nil
	default:
		return errInvalidInput
	}
}

// Anchor operations to an authorized root before traversing untrusted folders.
// os.Root prevents symlink swaps during subsequent filesystem operations.
func (s *Server) openDownloadDirectory(target string) (*os.Root, error) {
	target, err := s.resolveAuthorizedPath(target)
	if err != nil {
		return nil, errForbidden
	}
	for _, configured := range s.config.SharedRoots {
		rootPath, err := filepath.EvalSymlinks(configured)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(rootPath, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			continue
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			return nil, err
		}
		directory, err := root.OpenRoot(relative)
		root.Close()
		return directory, err
	}
	return nil, errForbidden
}

func (s *Server) executeDownload(ctx context.Context, id string) {
	defer func() { s.downloadMu.Lock(); delete(s.downloadsCtx, id); s.downloadMu.Unlock() }()
	s.store.mu.RLock()
	plan, ok := s.store.downloads[id]
	if !ok || plan.Status != statusRunning {
		s.store.mu.RUnlock()
		return
	}
	copyPlan := *plan
	s.store.mu.RUnlock()
	directory, err := s.openDownloadDirectory(copyPlan.TargetDirectory)
	if err != nil {
		s.failDownload(id, err)
		return
	}
	defer directory.Close()
	for _, source := range copyPlan.Sources {
		if ctx.Err() != nil {
			return
		}
		if err := s.downloadSource(ctx, id, directory, source); err != nil {
			if !errors.Is(err, context.Canceled) {
				s.failDownload(id, err)
			}
			return
		}
	}
	s.store.mu.Lock()
	plan, ok = s.store.downloads[id]
	completed := ok && plan.Status == statusRunning && ctx.Err() == nil
	if completed {
		plan.Status = statusCompleted
		plan.CurrentSource = ""
	}
	s.store.mu.Unlock()
	if completed {
		s.store.appendEvent(id, "task.progress", map[string]string{"status": statusCompleted, "summary": "下载完成"})
	}
}

func (s *Server) downloadSource(ctx context.Context, id string, directory *os.Root, source DownloadSource) error {
	parsed, err := parseSourceURL(source.URL)
	if err != nil {
		return errInvalidInput
	}
	if source.SizeBytes <= 0 || source.SizeBytes > 10<<30 {
		return errDownloadBudget
	}
	name := filepath.Base(parsed.Path)
	if name == "." || name == "/" || name == "" {
		return errInvalidInput
	}
	if _, err := directory.Lstat(name); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.updateDownload(id, 0, source.Title)
	var resp *http.Response
	for attempt := 1; attempt <= 3; attempt++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
		if reqErr != nil {
			return errInvalidInput
		}
		resp, err = s.publicHTTPClient(60 * time.Second).Do(req)
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, errNonPublicAddress) {
			break
		}
		if attempt < 3 {
			s.store.appendEvent(id, "task.retry", map[string]any{"attempt": attempt + 1})
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
			}
		}
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("source unavailable")
	}
	if resp.ContentLength > source.SizeBytes {
		return errDownloadBudget
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Never expose a partial file under the intended filename. Link atomically
	// publishes the complete file and fails if another writer created the target.
	partial := ".nasmate-" + newID("download") + ".part"
	file, err := directory.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	defer directory.Remove(partial)
	written, err := io.Copy(file, io.LimitReader(resp.Body, source.SizeBytes+1))
	if err != nil {
		return err
	}
	if written > source.SizeBytes {
		return errDownloadBudget
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.store.mu.Lock()
	plan, ok := s.store.downloads[id]
	if !ok || plan.Status != statusRunning || ctx.Err() != nil {
		s.store.mu.Unlock()
		return context.Canceled
	}
	err = directory.Link(partial, name)
	s.store.mu.Unlock()
	if err != nil {
		return err
	}
	s.updateDownload(id, written, "")
	return nil
}

func (s *Server) updateDownload(id string, bytes int64, source string) {
	s.store.mu.Lock()
	plan, ok := s.store.downloads[id]
	if !ok || plan.Status != statusRunning {
		s.store.mu.Unlock()
		return
	}
	plan.DownloadedBytes += bytes
	plan.CurrentSource = source
	total := plan.DownloadedBytes
	s.store.mu.Unlock()
	s.store.appendEvent(id, "task.progress", map[string]any{"downloadedBytes": total, "currentSource": source})
}

func (s *Server) failDownload(id string, err error) {
	code := sourceErrorCode(err)
	switch {
	case errors.Is(err, errDownloadBudget):
		code = "STORAGE_INSUFFICIENT"
	case errors.Is(err, os.ErrExist):
		code = "POLICY_BLOCKED"
	case errors.Is(err, errForbidden):
		code = "PATH_NOT_ALLOWED"
	}
	s.store.mu.Lock()
	plan, ok := s.store.downloads[id]
	if !ok || plan.Status != statusRunning {
		s.store.mu.Unlock()
		return
	}
	plan.Status = statusFailed
	plan.CurrentSource = ""
	plan.ErrorCode = code
	s.store.mu.Unlock()
	s.store.appendEvent(id, "task.progress", map[string]string{"status": statusFailed, "summary": "下载未完成；已完成文件保留，未完成临时文件清理", "errorCode": code})
}
