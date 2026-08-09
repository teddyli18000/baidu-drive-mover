package baidu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
)

type memorySink struct {
	mu    sync.Mutex
	dirs  map[string]manifest.Directory
	files map[string]manifest.File
}

func newMemorySink() *memorySink {
	return &memorySink{dirs: make(map[string]manifest.Directory), files: make(map[string]manifest.File)}
}

func (s *memorySink) UpsertManifestPage(_ context.Context, _ string, dirs []manifest.Directory, files []manifest.File) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, dir := range dirs {
		s.dirs[dir.LogicalPath] = dir
	}
	for _, file := range files {
		s.files[file.SourceID] = file
	}
	return nil
}

func fakeSharePage() string {
	return `<html><script>boot({"wrapper":{"loginstate":1,"bdstoken":"fake-token","shareid":12345,"share_uk":67890,"uk":777}});</script></html>`
}

func TestAccessSharePageExtractsNestedMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/s/1Synthetic" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, strings.ReplaceAll(fakeSharePage(), `\"`, `"`))
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	share, err := client.AccessSharePage(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	if share.BDSToken != "fake-token" || share.ShareID != "12345" || share.ShareUK != "67890" {
		t.Fatalf("unexpected share context: %+v", share)
	}
}

func TestVerifyPasswordCapturesSessionCookie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/share/verify" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.Form.Get("pwd") != "a1b2" {
			fmt.Fprint(w, `{"errno":-9}`)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "BDCLND", Value: "fake-randsk", Path: "/"})
		fmt.Fprint(w, `{"errno":0,"randsk":"fake-randsk"}`)
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	share := ShareContext{BDSToken: "t", ShareID: "1", ShareUK: "2"}
	if err := client.VerifyPassword(context.Background(), link, share, "a1b2"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.CookieString(), "BDCLND=fake-randsk") {
		t.Fatalf("verification cookie was not retained: %q", client.CookieString())
	}
}

func TestScanPreservesMixedTreeEmptyDirsAndLargeDirectory(t *testing.T) {
	tree := map[string][]shareListItem{
		"/": {
			{FsID: 1, ServerFilename: "root.txt", Path: "/root.txt", Size: 3},
			{FsID: 10, ServerFilename: "bulk", Path: "/bulk", IsDir: 1},
			{FsID: 11, ServerFilename: "empty", Path: "/empty", IsDir: 1},
			{FsID: 12, ServerFilename: "mixed", Path: "/mixed", IsDir: 1},
		},
		"/empty": {},
		"/mixed": {
			{FsID: 20, ServerFilename: "direct.txt", Path: "/mixed/direct.txt", Size: 5},
			{FsID: 21, ServerFilename: "nested", Path: "/mixed/nested", IsDir: 1},
		},
		"/mixed/nested": {{FsID: 22, ServerFilename: "deep.txt", Path: "/mixed/nested/deep.txt", Size: 7}},
	}
	bulk := make([]shareListItem, 0, 601)
	for i := 0; i < 601; i++ {
		bulk = append(bulk, shareListItem{FsID: int64(1000 + i), ServerFilename: fmt.Sprintf("f-%03d.bin", i), Path: fmt.Sprintf("/bulk/f-%03d.bin", i), Size: 1})
	}
	tree["/bulk"] = bulk

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/share/list" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		dir := r.Form.Get("dir")
		pageNumber, _ := strconv.Atoi(r.URL.Query().Get("page"))
		items := tree[dir]
		start := (pageNumber - 1) * shareListPageSize
		if start > len(items) {
			start = len(items)
		}
		end := start + shareListPageSize
		if end > len(items) {
			end = len(items)
		}
		_ = json.NewEncoder(w).Encode(shareListResponse{Errno: 0, List: items[start:end]})
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
	if len(sink.files) != 604 {
		t.Fatalf("files=%d want=604", len(sink.files))
	}
	for _, dir := range []string{"/bulk", "/empty", "/mixed", "/mixed/nested"} {
		if _, ok := sink.dirs[dir]; !ok {
			t.Fatalf("missing directory %q", dir)
		}
	}
	wantPaths := []string{"/root.txt", "/mixed/direct.txt", "/mixed/nested/deep.txt", "/bulk/f-600.bin"}
	seen := make(map[string]bool)
	for _, file := range sink.files {
		seen[file.LogicalPath] = true
	}
	for _, want := range wantPaths {
		if !seen[want] {
			t.Fatalf("missing logical file %q", want)
		}
	}
}

func TestScanRetriesTransientErrnoFour(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			fmt.Fprint(w, `{"errno":4,"list":[]}`)
			return
		}
		fmt.Fprint(w, `{"errno":0,"list":[]}`)
	}))
	defer server.Close()
	var sleeps []time.Duration
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithSleep(func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	if err := client.Scan(context.Background(), "task", link, ShareContext{BDSToken: "t"}, newMemorySink()); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || len(sleeps) != 2 {
		t.Fatalf("attempts=%d sleeps=%v", attempts, sleeps)
	}
}

func TestRelativeLogicalPathForSelectedSubpath(t *testing.T) {
	got, err := relativeLogicalPath("/folder/sub", "/folder/sub/child/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/child/file.txt" {
		t.Fatalf("got %q", got)
	}
	if _, err := relativeLogicalPath("/folder/sub", "/other/file.txt"); err == nil {
		t.Fatal("expected outside path rejection")
	}
}
