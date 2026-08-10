package baidu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccessSharePageRetries429UsingRetryAfter(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, "rate limited")
			return
		}
		fmt.Fprint(w, strings.ReplaceAll(fakeSharePage(), `\"`, `"`))
	}))
	defer server.Close()
	var sleeps []time.Duration
	client, err := NewClient("BDUSS=fake; STOKEN=fake",
		WithBaseURL(server.URL),
		WithSleep(func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	if _, err := client.AccessSharePage(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d want=3", attempts)
	}
	if len(sleeps) != 2 || sleeps[0] != time.Second || sleeps[1] != time.Second {
		t.Fatalf("Retry-After sleeps=%v", sleeps)
	}
}

func TestAccessSharePage503RetriesAreBounded(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "temporary outage")
	}))
	defer server.Close()
	var sleeps []time.Duration
	client, err := NewClient("BDUSS=fake; STOKEN=fake",
		WithBaseURL(server.URL),
		WithSleep(func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	_, err = client.AccessSharePage(context.Background(), link)
	if err == nil || !IsTransient(err) {
		t.Fatalf("expected bounded transient error, got %v", err)
	}
	if attempts != defaultMaxListRetries {
		t.Fatalf("attempts=%d want=%d", attempts, defaultMaxListRetries)
	}
	if len(sleeps) != defaultMaxListRetries-1 {
		t.Fatalf("sleeps=%v", sleeps)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleep[%d]=%v want=%v", i, sleeps[i], want[i])
		}
	}
}

func TestRetryAfterAboveCapFallsBackToExponentialDelay(t *testing.T) {
	err := &TransientError{Operation: "test", Status: 429, RetryAfter: 2 * time.Minute}
	if got := RetryDelay(err, 2); got != 2*time.Second {
		t.Fatalf("delay=%v want=2s", got)
	}
}

func TestRetryAfterHTTPDateIsParsedAndBounded(t *testing.T) {
	now := time.Date(2026, time.August, 10, 7, 0, 0, 0, time.UTC)
	if got := parseRetryAfter(now.Add(10*time.Second).Format(time.RFC1123), now); got != 10*time.Second {
		t.Fatalf("HTTP-date Retry-After=%v want=10s", got)
	}
	if got := parseRetryAfter(now.Add(time.Minute).Format(time.RFC1123), now); got != 0 {
		t.Fatalf("over-cap HTTP-date Retry-After=%v want=0", got)
	}
	if got := parseRetryAfter("not-a-date", now); got != 0 {
		t.Fatalf("invalid Retry-After=%v want=0", got)
	}
}

func TestRetryAfterIntegerOverflowFallsBackToExponentialDelay(t *testing.T) {
	delay := parseRetryAfter("9223372036854775807", time.Now())
	if delay != 0 {
		t.Fatalf("overflowing Retry-After parsed as %v", delay)
	}
	if got := RetryDelay(&TransientError{Operation: "test", Status: 429, RetryAfter: delay}, 0); got != 500*time.Millisecond {
		t.Fatalf("delay=%v want=500ms", got)
	}
}

func TestPCSListingsRetryTransientFailures(t *testing.T) {
	tests := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "staging",
			call: func(client *Client) error {
				_, err := client.ListStagingDirectory(context.Background(), "/BaiduDriveMover/task/b")
				return err
			},
		},
		{
			name: "cleanup",
			call: func(client *Client) error {
				_, err := client.ListStagingPathForCleanup(context.Background(), "/BaiduDriveMover/task/b")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if r.Method != http.MethodGet || r.URL.Query().Get("method") != "list" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
				}
				if attempts < 3 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fmt.Fprint(w, `{"error_code":0,"list":[]}`)
			}))
			defer server.Close()
			client, err := NewClient("BDUSS=fake; STOKEN=fake",
				WithPCSBaseURL(server.URL),
				WithSleep(func(context.Context, time.Duration) error { return nil }),
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := test.call(client); err != nil {
				t.Fatal(err)
			}
			if attempts != 3 {
				t.Fatalf("attempts=%d want=3", attempts)
			}
		})
	}
}

func TestReadRetryHelpersRejectMutationMethodsBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithPCSBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.doRead(context.Background(), http.MethodPost, "/share/transfer", nil, nil, "", 1024); err == nil {
		t.Fatal("pan read retry helper accepted a mutation endpoint")
	}
	if _, _, err := client.doPCSRead(context.Background(), http.MethodPost, "/rest/2.0/pcs/file", nil, nil, 1024); err == nil {
		t.Fatal("PCS read retry helper accepted a mutation method")
	}
	if calls != 0 {
		t.Fatalf("rejected mutation helpers made %d network calls", calls)
	}
}

func TestOversizedReadResponseIsPermanentAndNotRetried(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		fmt.Fprint(w, "response-too-large")
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake",
		WithBaseURL(server.URL),
		WithSleep(func(context.Context, time.Duration) error {
			t.Fatal("oversized response must not be retried")
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.doRead(context.Background(), http.MethodGet, "/oversized", nil, nil, "", 4)
	if err == nil || IsTransient(err) {
		t.Fatalf("expected permanent response-size error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d want=1", attempts)
	}
}

func TestTransferAndDeleteDoNotBlindRetryTransientFailures(t *testing.T) {
	tests := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "transfer",
			call: func(client *Client) error {
				link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
				return client.TransferFiles(context.Background(), link, ShareContext{BDSToken: "t", ShareID: "1", ShareUK: "2"}, []int64{1}, "/BaiduDriveMover/task/b")
			},
		},
		{
			name: "delete",
			call: func(client *Client) error {
				return client.DeleteStagingPath(context.Background(), "/BaiduDriveMover/task/b")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, "temporary outage")
			}))
			defer server.Close()
			client, err := NewClient("BDUSS=fake; STOKEN=fake",
				WithBaseURL(server.URL),
				WithPCSBaseURL(server.URL),
				WithSleep(func(context.Context, time.Duration) error {
					t.Fatal("mutation must not enter read retry sleep")
					return nil
				}),
			)
			if err != nil {
				t.Fatal(err)
			}
			err = test.call(client)
			if err == nil || !IsTransient(err) {
				t.Fatalf("expected one-shot transient error, got %v", err)
			}
			if attempts != 1 {
				t.Fatalf("attempts=%d want=1", attempts)
			}
		})
	}
}

func TestReadRetryCancellationStopsImmediately(t *testing.T) {
	attempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake",
		WithBaseURL(server.URL),
		WithSleep(func(ctx context.Context, _ time.Duration) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	_, err = client.AccessSharePage(ctx, link)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d want=1", attempts)
	}
}

func TestPasswordVerificationDoesNotBlindRetryTransientHTTPFailure(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "temporary outage")
	}))
	defer server.Close()
	client, err := NewClient("BDUSS=fake; STOKEN=fake", WithBaseURL(server.URL), WithSleep(func(context.Context, time.Duration) error {
		t.Fatal("password verification must not enter read retry sleep")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	link, _ := ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	err = client.VerifyPassword(context.Background(), link, ShareContext{BDSToken: "t", ShareID: "1", ShareUK: "2"}, "abcd")
	if err == nil || !IsTransient(err) {
		t.Fatalf("expected one-shot transient error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("password verification attempts=%d want=1", attempts)
	}
}
