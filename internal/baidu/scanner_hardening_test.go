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
	"time"
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
	failedOnce := make(map[int]bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		pageNumber, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if !failedOnce[pageNumber] && (pageNumber%13 == 0 || pageNumber%17 == 0) {
			failedOnce[pageNumber] = true
			if pageNumber%17 == 0 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
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
	client, err := NewClient("BDUSS=fake; STOKEN=fake",
		WithBaseURL(server.URL),
		WithSleep(func(context.Context, time.Duration) error { return nil }),
	)
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
	if requests != 113 {
		t.Fatalf("requests=%d want=113", requests)
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

func TestScanPreservesRemoteOnlyFilenameFormsAsLogicalMetadata(t *testing.T) {
	names := []string{"CON", "report:final?.txt", "资料-😀.bin", "trailing.", strings.Repeat("长", 120)}
	items := make([]shareListItem, 0, len(names))
	for i, name := range names {
		items = append(items, shareListItem{
			FsID:           int64(100 + i),
			ServerFilename: name,
			Path:           "/" + name,
			Size:           int64(i),
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(shareListResponse{Errno: 0, List: items})
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	sink := newMemorySink()
	if err := client.Scan(context.Background(), "task", link, ShareContext{BDSToken: "t"}, sink); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		file := sink.files[strconv.Itoa(100+i)]
		if file.Name != name || file.LogicalPath != "/"+name {
			t.Fatalf("name %q was normalized unexpectedly: %+v", name, file)
		}
	}
}

func TestScanRejectsNonPositiveFileIDs(t *testing.T) {
	for _, fsID := range []int64{0, -1} {
		t.Run(strconv.FormatInt(fsID, 10), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(shareListResponse{Errno: 0, List: []shareListItem{{
					FsID:           fsID,
					ServerFilename: "invalid-id.bin",
					Path:           "/invalid-id.bin",
					Size:           1,
				}}})
			}))
			defer server.Close()
			client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
			link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
			err := client.Scan(context.Background(), "task", link, ShareContext{BDSToken: "t"}, newMemorySink())
			if err == nil || !strings.Contains(err.Error(), "valid fs_id") {
				t.Fatalf("expected invalid fs_id rejection, got %v", err)
			}
		})
	}
}

func TestScanAcceptsQuotedDecimalNumericFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"errno":0,"list":[{"fs_id":"9223372036854775807","server_filename":"safe.bin","path":"/safe.bin","size":"1","isdir":"0"}]}`)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	sink := newMemorySink()
	if err := client.Scan(context.Background(), "task", link, ShareContext{BDSToken: "t"}, sink); err != nil {
		t.Fatal(err)
	}
	if file, ok := sink.files["9223372036854775807"]; !ok || file.Name != "safe.bin" {
		t.Fatalf("decimal string fs_id was not preserved: %+v", sink.files)
	}
}

func TestShareListItemRejectsInvalidDirectoryAndSizeFields(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{name: "missing isdir", body: `{"fs_id":1,"size":1}`, field: "isdir"},
		{name: "null isdir", body: `{"fs_id":1,"isdir":null,"size":1}`, field: "isdir"},
		{name: "empty isdir", body: `{"fs_id":1,"isdir":"","size":1}`, field: "isdir"},
		{name: "negative isdir", body: `{"fs_id":1,"isdir":"-1","size":1}`, field: "isdir"},
		{name: "invalid isdir value", body: `{"fs_id":1,"isdir":"2","size":1}`, field: "isdir"},
		{name: "fractional isdir", body: `{"fs_id":1,"isdir":"1.0","size":1}`, field: "isdir"},
		{name: "exponent isdir", body: `{"fs_id":1,"isdir":"1e0","size":1}`, field: "isdir"},
		{name: "overflow isdir", body: `{"fs_id":1,"isdir":"9223372036854775808","size":1}`, field: "isdir"},
		{name: "missing size", body: `{"fs_id":1,"isdir":0}`, field: "size"},
		{name: "null size", body: `{"fs_id":1,"isdir":0,"size":null}`, field: "size"},
		{name: "negative numeric size", body: `{"fs_id":1,"isdir":0,"size":-1}`, field: "size"},
		{name: "negative string size", body: `{"fs_id":1,"isdir":0,"size":"-1"}`, field: "size"},
		{name: "fractional size", body: `{"fs_id":1,"isdir":0,"size":"1.5"}`, field: "size"},
		{name: "exponent size", body: `{"fs_id":1,"isdir":0,"size":"1e3"}`, field: "size"},
		{name: "overflow size", body: `{"fs_id":1,"isdir":0,"size":"9223372036854775808"}`, field: "size"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var item shareListItem
			err := json.Unmarshal([]byte(test.body), &item)
			if err == nil || !strings.Contains(err.Error(), "invalid "+test.field) {
				t.Fatalf("expected invalid %s rejection, got item=%+v err=%v", test.field, item, err)
			}
		})
	}
}

func TestScanRejectsSuccessfulResponseWithoutListArray(t *testing.T) {
	for _, body := range []string{`{"errno":0}`, `{"errno":0,"list":null}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
			link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
			err := client.Scan(context.Background(), "task", link, ShareContext{BDSToken: "t"}, newMemorySink())
			if err == nil || !strings.Contains(err.Error(), "missing a valid list array") {
				t.Fatalf("expected missing list rejection, got %v", err)
			}
		})
	}
}

func TestScanRejectsNonPositiveDecimalStringFileIDs(t *testing.T) {
	for _, fsID := range []string{"0", "-1"} {
		t.Run(fsID, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"errno":0,"list":[{"fs_id":%q,"server_filename":"invalid-id.bin","path":"/invalid-id.bin","size":1,"isdir":0}]}`, fsID)
			}))
			defer server.Close()
			client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
			link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
			err := client.Scan(context.Background(), "task", link, ShareContext{BDSToken: "t"}, newMemorySink())
			if err == nil || !strings.Contains(err.Error(), "valid fs_id") {
				t.Fatalf("expected non-positive decimal string fs_id rejection, got %v", err)
			}
		})
	}
}

func TestShareListItemRejectsMalformedStringFileIDs(t *testing.T) {
	for _, fsID := range []string{`null`, `""`, `"+1"`, `" 1"`, `"1.0"`, `"1e3"`, `"abc"`, `"9223372036854775808"`, `1.0`, `1e3`, `9223372036854775808`} {
		t.Run(fsID, func(t *testing.T) {
			var item shareListItem
			err := json.Unmarshal([]byte(`{"fs_id":`+fsID+`}`), &item)
			if err == nil || !strings.Contains(err.Error(), "invalid fs_id") {
				t.Fatalf("expected invalid string fs_id rejection, got item=%+v err=%v", item, err)
			}
		})
	}
	var missing shareListItem
	if err := json.Unmarshal([]byte(`{}`), &missing); err == nil || !strings.Contains(err.Error(), "invalid fs_id") {
		t.Fatalf("expected missing fs_id rejection, got item=%+v err=%v", missing, err)
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
