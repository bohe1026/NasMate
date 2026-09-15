package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrganizeDetectsExistingDestinationAndDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"a", "txt"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a/report.txt", "txt/report.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}})
	body, _ := json.Marshal(OrganizeRequest{RootPath: root, Mode: "extension"})
	res := request(t, server, http.MethodPost, "/api/organize/dry-run", string(body))
	var plan OrganizePlan
	if err := json.Unmarshal(res.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Conflicts) != 1 || len(plan.Items) != 0 || len(plan.Skipped) != 1 || !plan.DryRun || plan.SpaceDelta != 0 {
		t.Fatalf("unsafe preview: %+v", plan)
	}
	for _, name := range []string{"a/report.txt", "txt/report.txt"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != name {
			t.Errorf("dry run changed %s", name)
		}
	}
}

type interruptedOrganizeStorage struct{ MockStorage }

func (interruptedOrganizeStorage) Search(ctx context.Context, _ FileSearchOptions) ([]FileMetadata, error) {
	return nil, ctx.Err()
}

func TestOrganizeHonorsRequestCancellation(t *testing.T) {
	root := t.TempDir()
	server := NewServer(Config{DevMode: true, SharedRoots: []string{root}})
	server.storage = interruptedOrganizeStorage{}
	body, _ := json.Marshal(OrganizeRequest{RootPath: root, Mode: "date"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/organize/dry-run", strings.NewReader(string(body))).WithContext(ctx)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if !strings.Contains(res.Body.String(), "USER_CANCELLED") {
		t.Fatalf("cancel ignored: %s", res.Body.String())
	}
}
