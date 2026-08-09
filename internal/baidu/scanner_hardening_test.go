package baidu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestScanStopsOnRepeatedFullPage(t *testing.T) {
	items := make([]shareListItem, 0, shareListPageSize)
	for i := 0; i < shareListPageSize; i++ {
		items = append(items, shareListItem{
			FsID:           int64(1000 + i),
			ServerFilename: fmt.Sprintf("f-%03d.bin", i),
			Path:           fmt.Sprintf("/f-%03d.bin", i),
			Size:           1,
		})
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(shareListResponse{Errno: 0, List: items})
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	err = client.Scan(context.Background(), "task", link, ShareContext{BDSToken: "t"}, newMemorySink())
	if err == nil || !strings.Contains(err.Error(), "pagination made no progress") {
		t.Fatalf("expected pagination no-progress error, got %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d want=2", requests)
	}
}

func TestScanHandlesTenThousandFilesAcrossOneHundredPages(t *testing.T) {
	const total = 10000
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		pageNumber, _ := strconv.Atoi(r.URL.Query().Get("page"))
		start := (pageNumber - 1) * shareListPageSize
		if start >= total {
			_ = json.NewEncoder(w).Encode(shareListResponse{Errno: 0, List: []shareListItem{}})
			return
		}
		end := start + shareListPageSize
		if end > total {
			end = total
		}
		items := make([]shareListItem, 0, end-start)
		for i := start; i < end; i++ {
			items = append(items, shareListItem{
				FsID:           int64(100000 + i),
				ServerFilename: fmt.Sprintf("file-%05d.bin", i),
				Path:           fmt.Sprintf("/file-%05d.bin", i),
				Size:           int64(i % 17),
			})
		}
		_ = json.NewEncoder(w).Encode(shareListResponse{Errno: 0, List: items})
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	sink := newMemorySink()
	if err := client.Scan(context.Background(), "task", link, ShareContext{BDSToken: "t"}, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.files) != total {
		t.Fatalf("files=%d want=%d", len(sink.files), total)
	}
	if requests != 101 {
		t.Fatalf("requests=%d want=101", requests)
	}
	if got := sink.files[strconv.Itoa(100000+9999)].LogicalPath; got != "/file-09999.bin" {
		t.Fatalf("last logical path=%q", got)
	}
}

func TestScanPreservesLeadingAndTrailingSpacesInFilename(t *testing.T) {
	name := "  report .txt  "
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(shareListResponse{Errno: 0, List: []shareListItem{{
			FsID:           44,
			ServerFilename: name,
			Path:           "/" + name,
			Size:           1,
		}}})
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	sink := newMemorySink()
	if err := client.Scan(context.Background(), "task", link, ShareContext{BDSToken: "t"}, sink); err != nil {
		t.Fatal(err)
	}
	file := sink.files["44"]
	if file.Name != name || file.LogicalPath != "/"+name {
		t.Fatalf("name/path were normalized unexpectedly: name=%q path=%q", file.Name, file.LogicalPath)
	}
}

func TestScanRejectsUnsafeChildNamesAndPathMismatch(t *testing.T) {
	tests := []shareListItem{
		{FsID: 1, ServerFilename: "..", Path: "/..", Size: 1},
		{FsID: 2, ServerFilename: ".", Path: "/.", Size: 1},
		{FsID: 3, ServerFilename: "a/b", Path: "/a/b", Size: 1},
		{FsID: 4, ServerFilename: `a\b`, Path: `/a\b`, Size: 1},
		{FsID: 5, ServerFilename: "safe.bin", Path: "/other.bin", Size: 1},
	}
	for _, item := range tests {
		t.Run(fmt.Sprintf("fsid-%d", item.FsID), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(shareListResponse{Errno: 0, List: []shareListItem{item}})
			}))
			defer server.Close()
			client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
			link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
			if err := client.Scan(context.Background(), "task", link, ShareContext{BDSToken: "t"}, newMemorySink()); err == nil {
				t.Fatal("expected unsafe share entry rejection")
			}
		})
	}
}

func TestScanRejectsDuplicateLogicalPathWithinPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(shareListResponse{Errno: 0, List: []shareListItem{
			{FsID: 1, ServerFilename: "dup.bin", Path: "/dup.bin", Size: 1},
			{FsID: 2, ServerFilename: "dup.bin", Path: "/dup.bin", Size: 1},
		}})
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	if err := client.Scan(context.Background(), "task", link, ShareContext{BDSToken: "t"}, newMemorySink()); err == nil || !strings.Contains(err.Error(), "duplicate logical path") {
		t.Fatalf("expected duplicate logical path rejection, got %v", err)
	}
}
