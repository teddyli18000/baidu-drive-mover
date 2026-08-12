package baidu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestTransferFilesCopiesOwnedAccountSources(t *testing.T) {
	copyCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/share/list":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if got := r.Form.Get("dir"); got != "/" {
				t.Fatalf("share list dir=%q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []map[string]any{
				{"fs_id": 11, "server_filename": "a.bin", "path": "/owned/a.bin", "size": 1, "isdir": 0},
				{"fs_id": 22, "server_filename": "b.bin", "path": "/owned/b.bin", "size": 2, "isdir": 0},
			}})
		case "/api/filemanager":
			copyCalls++
			if r.Method != http.MethodPost || r.URL.Query().Get("opera") != "copy" || r.URL.Query().Get("async") != "0" {
				t.Fatalf("unexpected copy request %s %s", r.Method, r.URL.String())
			}
			if r.URL.Query().Get("app_id") != panAppID || r.URL.Query().Get("bdstoken") != "token" {
				t.Fatalf("unexpected copy query: %v", r.URL.Query())
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			var items []map[string]string
			if err := json.Unmarshal([]byte(r.Form.Get("filelist")), &items); err != nil {
				t.Fatal(err)
			}
			want := []map[string]string{
				{"path": "/owned/b.bin", "dest": "/BaiduDriveMover/task/b", "newname": "b.bin", "ondup": "fail"},
				{"path": "/owned/a.bin", "dest": "/BaiduDriveMover/task/b", "newname": "a.bin", "ondup": "fail"},
			}
			if !reflect.DeepEqual(items, want) {
				t.Fatalf("copy items=%v want=%v", items, want)
			}
			fmt.Fprint(w, `{"errno":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	err = client.TransferFiles(context.Background(), link, ShareContext{BDSToken: "token", ShareID: "123", ShareUK: "777", UK: "777"}, []int64{22, 11}, "/BaiduDriveMover/task/b")
	if err != nil {
		t.Fatal(err)
	}
	if copyCalls != 1 {
		t.Fatalf("copy calls=%d want=1", copyCalls)
	}
}

func TestOwnedAccountCopyRequiresEveryRequestedSource(t *testing.T) {
	copyCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/filemanager" {
			copyCalls++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []map[string]any{
			{"fs_id": 11, "server_filename": "a.bin", "path": "/owned/a.bin", "size": 1, "isdir": 0},
		}})
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	err := client.TransferFiles(context.Background(), link, ShareContext{BDSToken: "token", ShareID: "123", ShareUK: "777", UK: "777"}, []int64{22}, "/BaiduDriveMover/task/b")
	if err == nil {
		t.Fatal("missing owned source unexpectedly copied")
	}
	if copyCalls != 0 {
		t.Fatalf("missing resolution triggered %d copy calls", copyCalls)
	}
}

func TestOwnedAccountCopySplitsBeforeMutationLimit(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	client, _ := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	ids := make([]int64, ownCopyMaxItems+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	err := client.TransferFiles(context.Background(), link, ShareContext{BDSToken: "token", ShareID: "123", ShareUK: "777", UK: "777"}, ids, "/BaiduDriveMover/task/b")
	limit, ok := err.(*TransferLimitError)
	if !ok || limit.Limit != ownCopyMaxItems {
		t.Fatalf("expected owned-copy limit, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("oversized owned copy triggered %d network calls", calls)
	}
}

func TestClassifyOwnedCopyError(t *testing.T) {
	for _, test := range []struct {
		errno int
		want  error
	}{{-6, ErrAuthRequired}, {-30, ErrTransferConflict}, {8001, ErrVerificationRequired}} {
		if got := classifyOwnedCopyError(test.errno); got != test.want {
			t.Fatalf("errno %d classified as %v want %v", test.errno, got, test.want)
		}
	}
}
