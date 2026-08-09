package baidu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListStagingPathForCleanupReturnsExactEntries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/2.0/pcs/file" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("method") != "list" || r.URL.Query().Get("path") != "/BaiduDriveMover/task-safe" {
			t.Fatalf("unexpected cleanup list query: %v", r.URL.Query())
		}
		fmt.Fprint(w, `{"error_code":0,"list":[{"fs_id":1,"server_filename":"leftover","path":"/BaiduDriveMover/task-safe/leftover","size":0,"isdir":1}]}`)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	items, err := client.ListStagingPathForCleanup(context.Background(), "/BaiduDriveMover/task-safe")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "leftover" || !items[0].IsDir {
		t.Fatalf("unexpected cleanup listing: %+v", items)
	}
}

func TestListStagingPathForCleanupPreservesExplicitNotFound(t *testing.T) {
	for _, code := range []int{31066, 31202} {
		t.Run(fmt.Sprintf("code-%d", code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprintf(w, `{"error_code":%d,"error_msg":"missing"}`, code)
			}))
			defer server.Close()
			client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
			_, err := client.ListStagingPathForCleanup(context.Background(), "/BaiduDriveMover/task-safe")
			if !errors.Is(err, ErrStagingNotFound) {
				t.Fatalf("expected ErrStagingNotFound, got %v", err)
			}
		})
	}
}

func TestDeleteStagingPathUsesOnlyValidatedPCSDelete(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/rest/2.0/pcs/file" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("method"); got != "delete" {
			t.Fatalf("method=%q", got)
		}
		if got := r.URL.Query().Get("path"); got != "/BaiduDriveMover/task-safe/b-123" {
			t.Fatalf("path=%q", got)
		}
		fmt.Fprint(w, `{"error_code":0}`)
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteStagingPath(context.Background(), "/BaiduDriveMover/task-safe/b-123"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d want=1", calls)
	}
}

func TestDeleteStagingPathRejectsGlobalAndEscapedPathsBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	for _, bad := range []string{
		"/BaiduDriveMover",
		"/other/task",
		"/BaiduDriveMover/task/../escape",
		`\BaiduDriveMover\task\b`,
	} {
		if err := client.DeleteStagingPath(context.Background(), bad); err == nil {
			t.Fatalf("expected delete path %q to be rejected", bad)
		}
	}
	if calls != 0 {
		t.Fatalf("unsafe delete paths triggered %d network calls", calls)
	}
}

func TestDeleteStagingPathTreatsOnlyExplicitMissingCodesAsNotFound(t *testing.T) {
	for _, code := range []int{31066, 31202} {
		t.Run(fmt.Sprintf("code-%d", code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprintf(w, `{"error_code":%d,"error_msg":"missing"}`, code)
			}))
			defer server.Close()
			client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
			err := client.DeleteStagingPath(context.Background(), "/BaiduDriveMover/task/b")
			if !errors.Is(err, ErrStagingNotFound) {
				t.Fatalf("expected ErrStagingNotFound, got %v", err)
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error_code":31070,"error_msg":"delete failed"}`)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	if err := client.DeleteStagingPath(context.Background(), "/BaiduDriveMover/task/b"); err == nil || errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("non-missing delete error was misclassified: %v", err)
	}
}
