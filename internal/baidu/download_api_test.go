package baidu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenDownloadFresh(t *testing.T) {
	payload := "hello world"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/2.0/pcs/file" || r.URL.Query().Get("method") != "download" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("path") != "/BaiduDriveMover/task/b/file.bin" {
			t.Errorf("unexpected path %q", r.URL.Query().Get("path"))
		}
		if got := r.URL.Query().Get("app_id"); got != pcsAppID {
			t.Errorf("PCS app_id=%q want=%q", got, pcsAppID)
		}
		fmt.Fprint(w, payload)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	stream, err := client.OpenDownload(context.Background(), "/BaiduDriveMover/task/b/file.bin", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	data, err := io.ReadAll(stream.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != payload || stream.Start != 0 || stream.Total != int64(len(payload)) || stream.Partial {
		t.Fatalf("unexpected stream=%+v body=%q", stream, data)
	}
}

func TestOpenDownloadResumeValidatesContentRange(t *testing.T) {
	payload := "hello world"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=6-" {
			t.Errorf("Range=%q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", "bytes 6-10/11")
		w.WriteHeader(http.StatusPartialContent)
		fmt.Fprint(w, payload[6:])
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	stream, err := client.OpenDownload(context.Background(), "/BaiduDriveMover/task/b/file.bin", 6)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	data, _ := io.ReadAll(stream.Body)
	if string(data) != "world" || !stream.Partial || stream.Start != 6 || stream.Total != 11 || stream.Remaining != 5 {
		t.Fatalf("unexpected stream=%+v body=%q", stream, data)
	}
}

func TestOpenDownloadRejectsIgnoredRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "full body")
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	_, err := client.OpenDownload(context.Background(), "/BaiduDriveMover/task/b/file.bin", 5)
	if !errors.Is(err, ErrRangeNotHonored) {
		t.Fatalf("expected range-not-honored, got %v", err)
	}
}

func TestOpenDownloadClassifiesPCSError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error_code":31042,"error_msg":"not logged in"}`)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	_, err := client.OpenDownload(context.Background(), "/BaiduDriveMover/task/b/file.bin", 0)
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestParseContentRange(t *testing.T) {
	start, end, total, err := parseContentRange("bytes 5-9/10")
	if err != nil || start != 5 || end != 9 || total != 10 {
		t.Fatalf("unexpected parse %d %d %d err=%v", start, end, total, err)
	}
	for _, bad := range []string{"", "items 0-1/2", "bytes 2-1/3", "bytes 0-1/*", "bytes x-y/z"} {
		if _, _, _, err := parseContentRange(bad); err == nil {
			t.Fatalf("expected invalid range %q", bad)
		}
	}
}

func TestOpenDownloadNeverAcceptsOutsideStagingRoot(t *testing.T) {
	client, _ := NewClient("BDUSS=fake; STOKEN=fake")
	_, err := client.OpenDownload(context.Background(), "/user/private/file.bin", 0)
	if err == nil || !strings.Contains(err.Error(), "escapes tool root") {
		t.Fatalf("unexpected error: %v", err)
	}
}
