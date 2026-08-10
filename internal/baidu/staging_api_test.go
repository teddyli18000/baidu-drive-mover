package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestLoginCookiesReachBaiduPCSSubdomain(t *testing.T) {
	panURL, _ := url.Parse("https://pan.baidu.com/")
	pcsURL, _ := url.Parse("https://pcs.baidu.com/")
	jar, err := newCookieJar(panURL, "BDUSS=fake-bduss; STOKEN=fake-stoken")
	if err != nil {
		t.Fatal(err)
	}
	got := cookieString(jar.Cookies(pcsURL))
	if !strings.Contains(got, "BDUSS=fake-bduss") || !strings.Contains(got, "STOKEN=fake-stoken") {
		t.Fatalf("cross-subdomain cookies missing: %q", got)
	}
}

func TestEnsureStagingDirectoryCreatesOnlyInternalAncestors(t *testing.T) {
	var mu sync.Mutex
	var created []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/2.0/pcs/file" || r.URL.Query().Get("method") != "mkdir" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		created = append(created, r.URL.Query().Get("path"))
		mu.Unlock()
		fmt.Fprint(w, `{"fs_id":1}`)
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.EnsureStagingDirectory(context.Background(), "/BaiduDriveMover/task-safe/b-123"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/BaiduDriveMover", "/BaiduDriveMover/task-safe", "/BaiduDriveMover/task-safe/b-123"}
	if !reflect.DeepEqual(created, want) {
		t.Fatalf("created=%v want=%v", created, want)
	}
}

func TestEnsureStagingDirectoryTreatsAlreadyExistsAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error_code":31061,"error_msg":"exists"}`)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	if err := client.EnsureStagingDirectory(context.Background(), "/BaiduDriveMover"); err != nil {
		t.Fatalf("existing staging root should be accepted: %v", err)
	}
}

func TestStagingPathEscapeRejectedBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	for _, bad := range []string{"/other/path", "/BaiduDriveMover/../escape", `\BaiduDriveMover\task`} {
		if err := client.EnsureStagingDirectory(context.Background(), bad); err == nil {
			t.Fatalf("expected path %q to be rejected", bad)
		}
	}
	if calls != 0 {
		t.Fatalf("unsafe path triggered %d network calls", calls)
	}
}

func TestListStagingDirectoryParsesBoundedObjects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"list": []map[string]any{
				{"fs_id": 7, "path": "/BaiduDriveMover/task/b/a.bin", "server_filename": "a.bin", "size": 12, "md5": "abc", "isdir": 0},
				{"fs_id": 8, "path": "/BaiduDriveMover/task/b/sub", "server_filename": "sub", "size": 0, "isdir": 1},
			},
		})
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	files, err := client.ListStagingDirectory(context.Background(), "/BaiduDriveMover/task/b")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name != "a.bin" || files[0].Size != 12 || files[0].MD5 != "abc" || !files[1].IsDir {
		t.Fatalf("unexpected staging list: %+v", files)
	}
}

func TestPCSQuotaErrorsAreClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error_code":31218,"error_msg":"quota"}`)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	err := client.EnsureStagingDirectory(context.Background(), "/BaiduDriveMover")
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected quota error, got %v", err)
	}
}

func TestTransferFilesUsesIndividualIDsAndIsolatedTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/share/transfer" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.Form.Get("fsidlist") != "[11,22,33]" {
			t.Errorf("fsidlist=%q", r.Form.Get("fsidlist"))
		}
		if r.Form.Get("path") != "/BaiduDriveMover/task/b-1" {
			t.Errorf("path=%q", r.Form.Get("path"))
		}
		if r.URL.Query().Get("shareid") != "123" || r.URL.Query().Get("from") != "456" || r.URL.Query().Get("bdstoken") != "token" {
			t.Errorf("unexpected transfer query: %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"errno":0,"info":[{"errno":0,"fsid":9001,"path":"/BaiduDriveMover/task/b-1/a"}]}`)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	err := client.TransferFiles(context.Background(), link, ShareContext{BDSToken: "token", ShareID: "123", ShareUK: "456"}, []int64{11, 22, 33}, "/BaiduDriveMover/task/b-1")
	if err != nil {
		t.Fatal(err)
	}
}

func TestTransferLimitBecomesTypedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"errno":12,"target_file_nums":200,"target_file_nums_limit":100,"info":[]}`)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	err := client.TransferFiles(context.Background(), link, ShareContext{BDSToken: "t", ShareID: "1", ShareUK: "2"}, []int64{1}, "/BaiduDriveMover/task/b")
	var limitErr *TransferLimitError
	if !errors.As(err, &limitErr) || limitErr.Limit != 100 || limitErr.Target != 200 {
		t.Fatalf("unexpected limit error: %#v", err)
	}
}

func TestTransferFilesRejectsNonSuccessStatusWithZeroErrno(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
	}{
		{name: "client error", status: http.StatusBadRequest},
		{name: "redirect", status: http.StatusFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				fmt.Fprint(w, `{"errno":0,"info":[]}`)
			}))
			defer server.Close()

			client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			link, err := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
			if err != nil {
				t.Fatal(err)
			}
			err = client.TransferFiles(context.Background(), link, ShareContext{BDSToken: "t", ShareID: "1", ShareUK: "2"}, []int64{1}, "/BaiduDriveMover/task/b")
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", test.status)) {
				t.Fatalf("expected HTTP %d failure, got %v", test.status, err)
			}
		})
	}
}

func TestTransferFilesPreservesTypedTransientServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"errno":0,"info":[]}`)
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	link, err := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	if err != nil {
		t.Fatal(err)
	}
	err = client.TransferFiles(context.Background(), link, ShareContext{BDSToken: "t", ShareID: "1", ShareUK: "2"}, []int64{1}, "/BaiduDriveMover/task/b")
	var transientErr *TransientError
	if !errors.As(err, &transientErr) || transientErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("expected typed transient HTTP %d error, got %#v", http.StatusServiceUnavailable, err)
	}
}
