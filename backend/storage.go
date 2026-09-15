package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultSearchLimit = 1000
	maxScannedEntries  = 200000
)

var (
	errSearchLimit      = errors.New("storage scan limit reached")
	errNoAuthorizedRoot = errors.New("no authorized storage root configured")
)

type FilesystemStorage struct {
	roots []string
}

// UGOS exposes each user-approved folder as a top-level symlink below
// UGAPP_SHARED_DIR. Keep the shared directory itself as a virtual root while
// adding only those resolved targets to the authorized roots.
func expandAuthorizedRoots(root string) []string {
	roots := []string{root}
	entries, err := os.ReadDir(root)
	if err != nil {
		return roots
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		target, err := filepath.EvalSymlinks(filepath.Join(root, entry.Name()))
		if err != nil || !containsPath(roots, target) {
			if err == nil {
				roots = append(roots, target)
			}
		}
	}
	return roots
}

func containsPath(paths []string, candidate string) bool {
	for _, path := range paths {
		if filepath.Clean(path) == filepath.Clean(candidate) {
			return true
		}
	}
	return false
}

func NewFilesystemStorage(roots []string) *FilesystemStorage {
	copyRoots := append([]string(nil), roots...)
	return &FilesystemStorage{roots: copyRoots}
}

func (storage *FilesystemStorage) Usage(ctx context.Context) (StorageUsage, error) {
	if len(storage.roots) == 0 {
		return StorageUsage{}, errNoAuthorizedRoot
	}

	result := StorageUsage{Volumes: make([]VolumeUsage, 0, len(storage.roots))}
	items := make([]UsageItem, 0)
	scanned := 0
	for _, root := range storage.roots {
		resolved, err := resolveAuthorizedPath(root, storage.roots)
		if err != nil {
			return StorageUsage{}, fmt.Errorf("resolve storage root: %w", err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return StorageUsage{}, fmt.Errorf("stat storage root: %w", err)
		}
		total, free, err := filesystemUsage(resolved)
		if err != nil {
			return StorageUsage{}, fmt.Errorf("read filesystem usage: %w", err)
		}
		result.Volumes = append(result.Volumes, VolumeUsage{
			Name:       filepath.Base(resolved),
			TotalBytes: total,
			UsedBytes:  total - free,
			FreeBytes:  free,
		})

		if info.IsDir() {
			entries, err := os.ReadDir(resolved)
			if err != nil {
				return StorageUsage{}, fmt.Errorf("read storage root: %w", err)
			}
			for _, entry := range entries {
				if entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				if err := ctx.Err(); err != nil {
					return StorageUsage{}, err
				}
				path := filepath.Join(resolved, entry.Name())
				size, err := pathSize(ctx, path, &scanned)
				if err != nil {
					return StorageUsage{}, err
				}
				kind := "file"
				if entry.IsDir() {
					kind = "directory"
				}
				items = append(items, UsageItem{Path: path, SizeBytes: size, Kind: kind})
			}
		} else {
			items = append(items, UsageItem{Path: resolved, SizeBytes: info.Size(), Kind: "file"})
		}
	}

	sort.Slice(items, func(i, j int) bool { return items[i].SizeBytes > items[j].SizeBytes })
	if len(items) > 20 {
		items = items[:20]
	}
	result.TopItems = items
	result.Suggestions = storageSuggestions(result.Volumes, items)
	return result, nil
}

func (storage *FilesystemStorage) Search(ctx context.Context, options FileSearchOptions) ([]FileMetadata, error) {
	roots := storage.roots
	if options.RootPath != "" {
		resolved, err := resolveAuthorizedPath(options.RootPath, storage.roots)
		if err != nil {
			return nil, errForbidden
		}
		roots = []string{resolved}
	}
	if len(roots) == 0 {
		return nil, errNoAuthorizedRoot
	}
	if options.MaxResults <= 0 {
		options.MaxResults = defaultSearchLimit
	}
	if options.MaxResults > maxScannedEntries {
		options.MaxResults = maxScannedEntries
	}
	for i := range options.Extensions {
		options.Extensions[i] = normalizeExtension(options.Extensions[i])
	}

	items := make([]FileMetadata, 0)
	scanned := 0
	for _, root := range roots {
		resolved, err := resolveAuthorizedPath(root, storage.roots)
		if err != nil {
			return nil, fmt.Errorf("resolve search root: %w", err)
		}
		walkErr := filepath.WalkDir(resolved, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			scanned++
			if scanned > maxScannedEntries {
				return errSearchLimit
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !matchesFile(info, entry.Name(), options) {
				return nil
			}
			if len(items) >= options.MaxResults {
				return errSearchLimit
			}
			items = append(items, FileMetadata{
				Name:       entry.Name(),
				Path:       path,
				SizeBytes:  info.Size(),
				ModifiedAt: info.ModTime().UTC(),
				Extension:  normalizeExtension(filepath.Ext(entry.Name())),
			})
			return nil
		})
		if walkErr != nil {
			if errors.Is(walkErr, errSearchLimit) {
				return items, errSearchLimit
			}
			return nil, walkErr
		}
	}
	return items, nil
}

func matchesFile(info os.FileInfo, name string, options FileSearchOptions) bool {
	if options.Keyword != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(options.Keyword)) {
		return false
	}
	extension := normalizeExtension(filepath.Ext(name))
	if len(options.Extensions) > 0 && !containsString(options.Extensions, extension) {
		return false
	}
	if options.ModifiedAfter != nil && info.ModTime().Before(*options.ModifiedAfter) {
		return false
	}
	if options.ModifiedBefore != nil && info.ModTime().After(*options.ModifiedBefore) {
		return false
	}
	if options.MinSizeBytes != nil && info.Size() < *options.MinSizeBytes {
		return false
	}
	if options.MaxSizeBytes != nil && info.Size() > *options.MaxSizeBytes {
		return false
	}
	return true
}

func pathSize(ctx context.Context, path string, scanned *int) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return info.Size(), nil
	}

	var total int64
	err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		*scanned++
		if *scanned > maxScannedEntries {
			return errSearchLimit
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func filesystemUsage(path string) (int64, int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	blockSize := uint64(stat.Bsize)
	total := int64FromUint64(uint64(stat.Blocks) * blockSize)
	free := int64FromUint64(uint64(stat.Bavail) * blockSize)
	if free > total {
		free = total
	}
	return total, free, nil
}

func filesystemFreeSpace(path string) (int64, error) {
	existing := filepath.Clean(path)
	for {
		if _, err := os.Stat(existing); err == nil {
			_, free, usageErr := filesystemUsage(existing)
			return free, usageErr
		} else if !os.IsNotExist(err) {
			return 0, err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return 0, os.ErrNotExist
		}
		existing = parent
	}
}

func int64FromUint64(value uint64) int64 {
	const maxInt64 = uint64(^uint64(0) >> 1)
	if value > maxInt64 {
		return int64(maxInt64)
	}
	return int64(value)
}

func storageSuggestions(volumes []VolumeUsage, items []UsageItem) []string {
	suggestions := make([]string, 0, 2)
	for _, volume := range volumes {
		if volume.TotalBytes > 0 && volume.UsedBytes*100 >= volume.TotalBytes*80 {
			suggestions = append(suggestions, "存储使用率已达到 80%，建议复核占用最大的目录。")
			break
		}
	}
	if len(items) > 0 {
		suggestions = append(suggestions, fmt.Sprintf("当前占用最大的路径是 %s。", items[0].Path))
	}
	return suggestions
}

func resolveAuthorizedPath(candidate string, roots []string) (string, error) {
	if strings.TrimSpace(candidate) == "" {
		return "", errForbidden
	}
	cleanCandidate, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", errForbidden
	}
	resolvedCandidate, err := resolvePathForAuthorization(cleanCandidate)
	if err != nil {
		return "", errForbidden
	}
	for _, root := range roots {
		cleanRoot, err := filepath.Abs(filepath.Clean(root))
		if err != nil {
			continue
		}
		resolvedRoot, err := resolvePathForAuthorization(cleanRoot)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(resolvedRoot, resolvedCandidate)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return resolvedCandidate, nil
		}
	}
	return "", errForbidden
}

func resolvePathForAuthorization(path string) (string, error) {
	path = filepath.Clean(path)
	missing := make([]string, 0)
	existing := path
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", os.ErrNotExist
		}
		missing = append(missing, filepath.Base(existing))
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return filepath.Clean(resolved), nil
}

func parseFileSearchOptions(values url.Values) (FileSearchOptions, error) {
	options := FileSearchOptions{
		Keyword:    strings.TrimSpace(values.Get("keyword")),
		RootPath:   strings.TrimSpace(values.Get("rootPath")),
		MaxResults: defaultSearchLimit,
	}
	if len([]rune(options.Keyword)) > 200 || len(options.RootPath) > 4096 {
		return FileSearchOptions{}, fmt.Errorf("搜索条件过长")
	}

	extensionValues := append([]string{}, values["extension"]...)
	extensionValues = append(extensionValues, values["extensions"]...)
	for _, raw := range extensionValues {
		for _, value := range strings.Split(raw, ",") {
			extension := normalizeExtension(value)
			if extension != "" && !containsString(options.Extensions, extension) {
				options.Extensions = append(options.Extensions, extension)
			}
		}
	}
	if len(options.Extensions) > 20 {
		return FileSearchOptions{}, fmt.Errorf("文件类型筛选最多支持 20 项")
	}

	var err error
	if options.ModifiedAfter, err = parseOptionalTime(values.Get("modifiedAfter")); err != nil {
		return FileSearchOptions{}, err
	}
	if options.ModifiedBefore, err = parseOptionalTime(values.Get("modifiedBefore")); err != nil {
		return FileSearchOptions{}, err
	}
	if options.ModifiedAfter != nil && options.ModifiedBefore != nil && options.ModifiedAfter.After(*options.ModifiedBefore) {
		return FileSearchOptions{}, fmt.Errorf("修改时间范围无效")
	}
	if options.MinSizeBytes, err = parseOptionalSize(values.Get("minSize"), values.Get("minSizeBytes")); err != nil {
		return FileSearchOptions{}, err
	}
	if options.MaxSizeBytes, err = parseOptionalSize(values.Get("maxSize"), values.Get("maxSizeBytes")); err != nil {
		return FileSearchOptions{}, err
	}
	if options.MinSizeBytes != nil && options.MaxSizeBytes != nil && *options.MinSizeBytes > *options.MaxSizeBytes {
		return FileSearchOptions{}, fmt.Errorf("文件大小范围无效")
	}
	return options, nil
}

func parseOptionalTime(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("时间必须使用 RFC3339 格式")
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func parseOptionalSize(values ...string) (*int64, error) {
	value := ""
	for _, candidate := range values {
		if strings.TrimSpace(candidate) != "" {
			value = strings.TrimSpace(candidate)
			break
		}
	}
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return nil, fmt.Errorf("文件大小必须是非负整数")
	}
	return &parsed, nil
}

func normalizeExtension(extension string) string {
	extension = strings.ToLower(strings.TrimSpace(extension))
	if extension == "" {
		return ""
	}
	if !strings.HasPrefix(extension, ".") {
		extension = "." + extension
	}
	return extension
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
